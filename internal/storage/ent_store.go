package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"

	"frappe-monitor/ent"
	entserver "frappe-monitor/ent/server"
)

type EntStore struct {
	client *ent.Client
	sqlDB  *sql.DB
}

// OpenEntStore opens SQLite with recommended PRAGMAs via DSN and returns a Store.
// dsn example: "file:./data/monitor.db?_pragma=journal_mode(wal)&_pragma=foreign_keys(1)&..."
func OpenEntStore(ctx context.Context, dsn string) (*EntStore, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite: one writer. WAL still allows concurrent readers via separate conn later.
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := ent.NewClient(ent.Driver(drv))
	if err := client.Schema.Create(ctx); err != nil {
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &EntStore{client: client, sqlDB: db}, nil
}

func (s *EntStore) Close() error {
	// Both layers must be closed; errors.Join surfaces both if either fails.
	return errors.Join(s.client.Close(), s.sqlDB.Close())
}

func (s *EntStore) CreateServer(ctx context.Context, in NewServer) (*Server, error) {
	b := s.client.Server.Create().
		SetName(in.Name).
		SetHostname(in.Hostname).
		SetSSHUser(in.SSHUser).
		SetSSHPort(in.SSHPort).
		SetSSHKeyPath(in.SSHKeyPath)
	if in.Labels != nil {
		b = b.SetLabels(in.Labels)
	}
	created, err := b.Save(ctx)
	if err != nil {
		if isUniqueHostnameViolation(err) {
			return nil, ErrDuplicateHostname
		}
		return nil, err
	}
	return entToServer(created), nil
}

// isUniqueHostnameViolation detects the SQLite UNIQUE constraint failure on
// servers.hostname. ent does not expose typed errors per-column, so this
// matches on the error string. The combination of ent.IsConstraintError +
// substring "hostname" is specific enough since no other column shares that
// substring in our schema.
func isUniqueHostnameViolation(err error) bool {
	if !ent.IsConstraintError(err) {
		return false
	}
	return strings.Contains(err.Error(), "hostname")
}

func (s *EntStore) GetServer(ctx context.Context, id int) (*Server, error) {
	e, err := s.client.Server.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return entToServer(e), nil
}

func (s *EntStore) ListServers(ctx context.Context) ([]*Server, error) {
	rows, err := s.client.Server.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*Server, 0, len(rows))
	for _, r := range rows {
		out = append(out, entToServer(r))
	}
	return out, nil
}

func (s *EntStore) SetServerStatus(ctx context.Context, id int, status, lastErr string) error {
	now := time.Now().UTC()
	_, err := s.client.Server.UpdateOneID(id).
		SetStatus(entserver.Status(status)).
		SetLastPingedAt(now).
		SetLastError(lastErr).
		Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func entToServer(e *ent.Server) *Server {
	out := &Server{
		ID:         e.ID,
		Name:       e.Name,
		Hostname:   e.Hostname,
		SSHUser:    e.SSHUser,
		SSHPort:    e.SSHPort,
		SSHKeyPath: e.SSHKeyPath,
		Labels:     e.Labels,
		Status:     string(e.Status),
		LastError:  e.LastError,
		CreatedAt:  e.CreatedAt,
		UpdatedAt:  e.UpdatedAt,
	}
	// Normalize labels to a non-nil empty map so callers never need to
	// nil-check before reading or writing.
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	if e.LastPingedAt != nil {
		t := *e.LastPingedAt
		out.LastPingedAt = &t
	}
	return out
}
