package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"

	"frappe-monitor/ent"
	entalertincident "frappe-monitor/ent/alertincident"
	entalertstate "frappe-monitor/ent/alertstate"
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
	// WAL allows readers to proceed while the single SQLite writer commits.
	// A one-connection pool serialized dashboard reads behind slow writes and
	// became visible once alerts, cursors, and snapshots ran concurrently.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	if renamed, err := repairDuplicateServerNames(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("repair duplicate server names: %w", err)
	} else if renamed > 0 {
		slog.Warn("storage: renamed duplicate server labels before unique-index migration", "count", renamed)
	}
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := ent.NewClient(ent.Driver(drv))
	if err := client.Schema.Create(ctx); err != nil {
		_ = client.Close()
		_ = db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &EntStore{client: client, sqlDB: db}, nil
}

// repairDuplicateServerNames is a one-time compatibility migration for
// databases created before `servers.name` became the canonical unique
// telemetry label. The first row keeps its name; later duplicates receive a
// deterministic `-server-ID` suffix that cannot collide with any existing
// value. It runs before Ent creates the unique index.
func repairDuplicateServerNames(ctx context.Context, db *sql.DB) (int, error) {
	var exists int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='servers'`).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM servers ORDER BY id`)
	if err != nil {
		return 0, err
	}
	type row struct {
		id   int
		name string
	}
	var all []row
	used := map[string]struct{}{}
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.id, &item.name); err != nil {
			_ = rows.Close()
			return 0, err
		}
		all = append(all, item)
		used[item.name] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	seen := map[string]struct{}{}
	renamed := 0
	for _, item := range all {
		if _, duplicate := seen[item.name]; !duplicate {
			seen[item.name] = struct{}{}
			continue
		}
		candidate := fmt.Sprintf("%s-server-%d", item.name, item.id)
		for suffix := 2; ; suffix++ {
			if _, collision := used[candidate]; !collision {
				break
			}
			candidate = fmt.Sprintf("%s-server-%d-%d", item.name, item.id, suffix)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE servers SET name = ? WHERE id = ?`, candidate, item.id); err != nil {
			return 0, err
		}
		used[candidate] = struct{}{}
		renamed++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return renamed, nil
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
		if isUniqueFieldViolation(err, "name") {
			return nil, ErrDuplicateName
		}
		if isUniqueHostnameViolation(err) {
			return nil, ErrDuplicateHostname
		}
		return nil, err
	}
	return entToServer(created), nil
}

// isUniqueHostnameViolation detects the SQLite UNIQUE constraint failure on
// servers.hostname. ent does not expose typed errors per-column, so this
// tokenizes the driver error and matches the exact qualified column name.
func isUniqueHostnameViolation(err error) bool {
	return isUniqueFieldViolation(err, "hostname")
}

func isUniqueFieldViolation(err error, field string) bool {
	if !ent.IsConstraintError(err) {
		return false
	}
	want := "servers." + field
	for _, token := range strings.FieldsFunc(err.Error(), func(r rune) bool {
		return !(r == '.' || r == '_' || r >= 'a' && r <= 'z' ||
			r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) {
		if token == want {
			return true
		}
	}
	return false
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
		if isUniqueFieldViolation(err, "name") {
			return nil, ErrDuplicateName
		}
		return nil, err
	}
	return entToServer(row), nil
}

// DeleteServer removes the server. Schema edge cascades remove associated
// cursors and system snapshots. The evaluator separately filters/prunes alert
// state by the authoritative server registry because alerts store VM labels,
// not a relational server edge.
func (s *EntStore) DeleteServer(ctx context.Context, id int) error {
	err := s.client.Server.DeleteOneID(id).Exec(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrNotFound
		}
		return err
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
		ServerID:     serverID,
		LogPath:      row.LogPath,
		FileIdentity: row.FileIdentity,
		ByteOffset:   row.ByteOffset,
		LastSeenAt:   row.LastSeenAt,
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
			SetFileIdentity(c.FileIdentity).
			SetByteOffset(c.ByteOffset).
			SetServerID(c.ServerID).
			Save(ctx)
		return err
	}
	_, err = s.client.LogCursor.UpdateOneID(existing.ID).
		SetFileIdentity(c.FileIdentity).
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
	existing, err := s.client.SystemSnapshot.Query().
		Where(entsystemsnapshot.HasServerWith(entserver.ID(in.ServerID))).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return err
	}
	if ent.IsNotFound(err) {
		payload := in.Payload
		if payload == nil {
			// payload is NOT NULL in the schema, but the first capture can
			// legitimately fail before any good inventory exists. Store an
			// empty blob alongside last_error; GET intentionally omits invalid
			// or empty JSON until a successful refresh replaces it.
			payload = []byte{}
		}
		_, err = s.client.SystemSnapshot.Create().
			SetPayload(payload).
			SetLastError(in.LastError).
			SetServerID(in.ServerID).
			Save(ctx)
		return err
	}
	upd := s.client.SystemSnapshot.UpdateOneID(existing.ID).
		SetLastError(in.LastError)
	if len(in.Payload) > 0 {
		upd = upd.SetPayload(in.Payload)
	}
	_, err = upd.Save(ctx)
	return err
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

// UpsertOpenAlertIncident records the current value of an existing open
// episode or creates a new episode after a previous one was resolved.
func (s *EntStore) UpsertOpenAlertIncident(ctx context.Context, in AlertIncident) (*AlertIncident, error) {
	existing, err := s.client.AlertIncident.Query().
		Where(entalertincident.RuleName(in.RuleName)).
		Where(entalertincident.Fingerprint(in.Fingerprint)).
		Where(entalertincident.StatusEQ(entalertincident.StatusFiring)).
		Order(entalertincident.ByStartedAt(entsql.OrderDesc())).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if ent.IsNotFound(err) {
		create := s.client.AlertIncident.Create().
			SetRuleName(in.RuleName).
			SetFingerprint(in.Fingerprint).
			SetSeverity(firstNonEmpty(in.Severity, "warning")).
			SetStatus(entalertincident.StatusFiring).
			SetValue(in.Value)
		if in.Labels != nil {
			create.SetLabels(in.Labels)
		}
		if !in.StartedAt.IsZero() {
			create.SetStartedAt(in.StartedAt)
		}
		if in.LastNotifiedAt != nil && !in.LastNotifiedAt.IsZero() {
			create.SetLastNotifiedAt(*in.LastNotifiedAt)
		}
		row, err := create.Save(ctx)
		if err != nil {
			return nil, err
		}
		return entToAlertIncident(row), nil
	}

	update := s.client.AlertIncident.UpdateOneID(existing.ID).
		SetValue(in.Value).
		SetSeverity(firstNonEmpty(in.Severity, existing.Severity))
	if in.Labels != nil {
		update.SetLabels(in.Labels)
	}
	if in.LastNotifiedAt != nil && !in.LastNotifiedAt.IsZero() {
		update.SetLastNotifiedAt(*in.LastNotifiedAt)
	}
	row, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}
	return entToAlertIncident(row), nil
}

func (s *EntStore) ResolveOpenAlertIncident(ctx context.Context, ruleName, fingerprint string, value float64, resolvedAt time.Time) error {
	_, err := s.client.AlertIncident.Update().
		Where(entalertincident.RuleName(ruleName)).
		Where(entalertincident.Fingerprint(fingerprint)).
		Where(entalertincident.StatusEQ(entalertincident.StatusFiring)).
		SetStatus(entalertincident.StatusResolved).
		SetValue(value).
		SetResolvedAt(resolvedAt).
		Save(ctx)
	return err
}

func (s *EntStore) ListAlertIncidents(ctx context.Context, limit int) ([]*AlertIncident, error) {
	if limit <= 0 || limit > 1000 {
		limit = 250
	}
	rows, err := s.client.AlertIncident.Query().
		Order(entalertincident.ByStartedAt(entsql.OrderDesc())).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*AlertIncident, 0, len(rows))
	for _, row := range rows {
		out = append(out, entToAlertIncident(row))
	}
	return out, nil
}

func entToAlertIncident(e *ent.AlertIncident) *AlertIncident {
	labels := e.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	return &AlertIncident{
		ID: e.ID, RuleName: e.RuleName, Fingerprint: e.Fingerprint,
		Severity: e.Severity, Labels: labels, Status: string(e.Status), Value: e.Value,
		StartedAt: e.StartedAt, ResolvedAt: e.ResolvedAt,
		LastNotifiedAt: e.LastNotifiedAt, UpdatedAt: e.UpdatedAt,
	}
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
