package storage

import (
	"context"
	"database/sql"
	"fmt"
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
	if err := s.client.Close(); err != nil {
		return err
	}
	return s.sqlDB.Close()
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
		return nil, err
	}
	return entToServer(created), nil
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
	if e.LastPingedAt != nil {
		t := *e.LastPingedAt
		out.LastPingedAt = &t
	}
	return out
}
