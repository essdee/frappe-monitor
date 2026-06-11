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
	entalertstate "frappe-monitor/ent/alertstate"
	entcontrolaction "frappe-monitor/ent/controlaction"
	entdbtarget "frappe-monitor/ent/dbtarget"
	entlogcursor "frappe-monitor/ent/logcursor"
	entserver "frappe-monitor/ent/server"
	entsystemsnapshot "frappe-monitor/ent/systemsnapshot"
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
	if in.BenchPaths != nil {
		b = b.SetBenchPaths(in.BenchPaths)
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

// UpdateServer applies partial changes. Pointer fields that are nil are
// left untouched on the row. Returns ErrNotFound if id does not exist;
// ErrDuplicateHostname if a hostname rename would collide.
func (s *EntStore) UpdateServer(ctx context.Context, id int, in UpdateServer) (*Server, error) {
	upd := s.client.Server.UpdateOneID(id)
	if in.Name != nil {
		upd = upd.SetName(*in.Name)
	}
	if in.Hostname != nil {
		upd = upd.SetHostname(*in.Hostname)
	}
	if in.SSHUser != nil {
		upd = upd.SetSSHUser(*in.SSHUser)
	}
	if in.SSHPort != nil {
		upd = upd.SetSSHPort(*in.SSHPort)
	}
	if in.SSHKeyPath != nil {
		upd = upd.SetSSHKeyPath(*in.SSHKeyPath)
	}
	if in.Labels != nil {
		upd = upd.SetLabels(*in.Labels)
	}
	if in.BenchPaths != nil {
		upd = upd.SetBenchPaths(*in.BenchPaths)
	}
	row, err := upd.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		if isUniqueHostnameViolation(err) {
			return nil, ErrDuplicateHostname
		}
		return nil, err
	}
	return entToServer(row), nil
}

// DeleteServer removes the server and its child rows (log cursors and
// system snapshot) in a single transaction. The generated foreign keys
// are ON DELETE NO ACTION, so with foreign_keys=on a plain server
// delete fails the moment any child row exists — we delete the children
// explicitly first. Alert states (Phase 6) are not edge-tied; they age
// out on the next reconciliation cycle when their series disappears.
func (s *EntStore) DeleteServer(ctx context.Context, id int) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	if _, err := tx.LogCursor.Delete().
		Where(entlogcursor.HasServerWith(entserver.ID(id))).Exec(ctx); err != nil {
		return fmt.Errorf("delete log cursors: %w", err)
	}
	if _, err := tx.SystemSnapshot.Delete().
		Where(entsystemsnapshot.HasServerWith(entserver.ID(id))).Exec(ctx); err != nil {
		return fmt.Errorf("delete system snapshot: %w", err)
	}
	if _, err := tx.DBTarget.Delete().
		Where(entdbtarget.HasServerWith(entserver.ID(id))).Exec(ctx); err != nil {
		return fmt.Errorf("delete db targets: %w", err)
	}
	if _, err := tx.ControlAction.Delete().
		Where(entcontrolaction.HasServerWith(entserver.ID(id))).Exec(ctx); err != nil {
		return fmt.Errorf("delete control actions: %w", err)
	}
	if err := tx.Server.DeleteOneID(id).Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrNotFound
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete: %w", err)
	}
	return nil
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

// GetLogCursor returns the cursor for (serverID, logPath). ErrNotFound
// when no cursor exists yet — callers treat that as offset=0.
func (s *EntStore) GetLogCursor(ctx context.Context, serverID int, logPath string) (*LogCursor, error) {
	row, err := s.client.LogCursor.Query().
		Where(entlogcursor.LogPath(logPath)).
		Where(entlogcursor.HasServerWith(entserver.ID(serverID))).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	out := &LogCursor{
		ServerID:   serverID,
		LogPath:    row.LogPath,
		ByteOffset: row.ByteOffset,
		LastSeenAt: row.LastSeenAt,
	}
	return out, nil
}

// UpsertLogCursor inserts or updates the cursor for (c.ServerID,
// c.LogPath). LastSeenAt is set to time.Now() automatically (the
// schema's UpdateDefault), so callers can leave it zero.
func (s *EntStore) UpsertLogCursor(ctx context.Context, c LogCursor) error {
	existing, err := s.client.LogCursor.Query().
		Where(entlogcursor.LogPath(c.LogPath)).
		Where(entlogcursor.HasServerWith(entserver.ID(c.ServerID))).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return err
	}
	if ent.IsNotFound(err) {
		_, err = s.client.LogCursor.Create().
			SetLogPath(c.LogPath).
			SetByteOffset(c.ByteOffset).
			SetServerID(c.ServerID).
			Save(ctx)
		return err
	}
	_, err = s.client.LogCursor.UpdateOneID(existing.ID).
		SetByteOffset(c.ByteOffset).
		SetLastSeenAt(time.Now().UTC()).
		Save(ctx)
	return err
}

// --- System snapshot ----------------------------------------------------

// GetSystemSnapshot returns the most recent inventory for a server,
// or ErrNotFound if none has been captured yet.
func (s *EntStore) GetSystemSnapshot(ctx context.Context, serverID int) (*SystemSnapshot, error) {
	row, err := s.client.SystemSnapshot.Query().
		Where(entsystemsnapshot.HasServerWith(entserver.ID(serverID))).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &SystemSnapshot{
		ServerID:   serverID,
		CapturedAt: row.CapturedAt,
		Payload:    row.Payload,
		LastError:  row.LastError,
	}, nil
}

// UpsertSystemSnapshot writes the latest inventory for a server,
// creating the row if absent or replacing the payload if present.
// Captured_at is bumped automatically by the ent UpdateDefault.
//
// Empty Payload is treated as "leave existing payload alone" — the
// failure path (refresh-system handler, on SSH or JSON-parse error)
// passes only LastError so a transient failure doesn't wipe the last
// good snapshot. To explicitly clear payload, callers must pass a
// non-nil zero-byte slice (no current caller does).
func (s *EntStore) UpsertSystemSnapshot(ctx context.Context, in SystemSnapshot) error {
	// Run the read-modify-write in a transaction. With SetMaxOpenConns(1)
	// the tx pins the single connection, so a concurrent upsert for the
	// same server (e.g. the create-time bootstrap racing a dashboard
	// "refresh system details") serializes behind it instead of both
	// seeing NotFound and racing two Creates into a UNIQUE violation.
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	existing, err := tx.SystemSnapshot.Query().
		Where(entsystemsnapshot.HasServerWith(entserver.ID(in.ServerID))).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return err
	}
	if ent.IsNotFound(err) {
		if _, err = tx.SystemSnapshot.Create().
			SetPayload(in.Payload).
			SetLastError(in.LastError).
			SetServerID(in.ServerID).
			Save(ctx); err != nil {
			return err
		}
		return tx.Commit()
	}
	upd := tx.SystemSnapshot.UpdateOneID(existing.ID).
		SetLastError(in.LastError)
	if len(in.Payload) > 0 {
		upd = upd.SetPayload(in.Payload)
	}
	if _, err = upd.Save(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

// --- Alert state (Phase 6) -----------------------------------------------

// ListAlertStatesByRule returns every state row currently associated
// with a given rule name. The reconciler uses this to compute which
// (rule, fingerprint) pairs were firing last cycle and are no longer
// firing — those become "resolved" notifications and get deleted.
func (s *EntStore) ListAlertStatesByRule(ctx context.Context, ruleName string) ([]*AlertState, error) {
	rows, err := s.client.AlertState.Query().
		Where(entalertstate.RuleName(ruleName)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*AlertState, 0, len(rows))
	for _, r := range rows {
		out = append(out, entToAlertState(r))
	}
	return out, nil
}

// ListAlertStates returns every state row in the database. Used by
// the dashboard's GET /api/v1/alerts endpoint to render a single
// "what is firing right now" view across all rules.
func (s *EntStore) ListAlertStates(ctx context.Context) ([]*AlertState, error) {
	rows, err := s.client.AlertState.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*AlertState, 0, len(rows))
	for _, r := range rows {
		out = append(out, entToAlertState(r))
	}
	return out, nil
}

// UpsertAlertState creates a row for a (rule, fingerprint) pair if
// none exists, or updates the existing row's value/status/notified_at
// otherwise. Returns the upserted row so the caller can compare the
// returned LastNotifiedAt against its own clock.
func (s *EntStore) UpsertAlertState(ctx context.Context, in AlertState) (*AlertState, error) {
	existing, err := s.client.AlertState.Query().
		Where(entalertstate.RuleName(in.RuleName)).
		Where(entalertstate.Fingerprint(in.Fingerprint)).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if ent.IsNotFound(err) {
		create := s.client.AlertState.Create().
			SetRuleName(in.RuleName).
			SetFingerprint(in.Fingerprint).
			SetValue(in.Value).
			SetStatus(entalertstate.Status(firstNonEmpty(in.Status, "firing")))
		if in.Labels != nil {
			create = create.SetLabels(in.Labels)
		}
		if !in.LastNotifiedAt.IsZero() {
			create = create.SetLastNotifiedAt(in.LastNotifiedAt)
		}
		row, err := create.Save(ctx)
		if err != nil {
			return nil, err
		}
		return entToAlertState(row), nil
	}
	upd := s.client.AlertState.UpdateOneID(existing.ID).
		SetValue(in.Value)
	if in.Status != "" {
		upd = upd.SetStatus(entalertstate.Status(in.Status))
	}
	if in.Labels != nil {
		upd = upd.SetLabels(in.Labels)
	}
	if !in.LastNotifiedAt.IsZero() {
		upd = upd.SetLastNotifiedAt(in.LastNotifiedAt)
	}
	row, err := upd.Save(ctx)
	if err != nil {
		return nil, err
	}
	return entToAlertState(row), nil
}

func (s *EntStore) DeleteAlertState(ctx context.Context, id int) error {
	err := s.client.AlertState.DeleteOneID(id).Exec(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func entToAlertState(e *ent.AlertState) *AlertState {
	out := &AlertState{
		ID:             e.ID,
		RuleName:       e.RuleName,
		Fingerprint:    e.Fingerprint,
		Labels:         e.Labels,
		Status:         string(e.Status),
		Value:          e.Value,
		FirstFiredAt:   e.FirstFiredAt,
		LastNotifiedAt: e.LastNotifiedAt,
		UpdatedAt:      e.UpdatedAt,
	}
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func entToServer(e *ent.Server) *Server {
	out := &Server{
		ID:         e.ID,
		Name:       e.Name,
		Hostname:   e.Hostname,
		SSHUser:    e.SSHUser,
		SSHPort:    e.SSHPort,
		SSHKeyPath: e.SSHKeyPath,
		BenchPaths: e.BenchPaths,
		Labels:     e.Labels,
		Status:     string(e.Status),
		LastError:  e.LastError,
		CreatedAt:  e.CreatedAt,
		UpdatedAt:  e.UpdatedAt,
	}
	// Normalize labels and bench_paths to non-nil empty values so
	// callers never need to nil-check before reading.
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	if out.BenchPaths == nil {
		out.BenchPaths = []string{}
	}
	if e.LastPingedAt != nil {
		t := *e.LastPingedAt
		out.LastPingedAt = &t
	}
	return out
}
