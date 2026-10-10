package storage

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound          = errors.New("storage: not found")
	ErrDuplicateHostname = errors.New("storage: hostname already exists")
	ErrDuplicateName     = errors.New("storage: server name already exists")
)

// LogCursor records the remote file identity and exclusive byte offset last
// committed to Loki for a (server,log_path) stream.
type LogCursor struct {
	ServerID     int
	LogPath      string
	FileIdentity string
	ByteOffset   int64
	LastSeenAt   time.Time
}

type Server struct {
	ID           int
	Name         string
	Hostname     string
	SSHUser      string
	SSHPort      int
	SSHKeyPath   string
	BenchPaths   []string
	Labels       map[string]string
	Status       string // "unknown" | "reachable" | "unreachable"
	LastPingedAt *time.Time
	LastError    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type NewServer struct {
	Name       string
	Hostname   string
	SSHUser    string
	SSHPort    int
	SSHKeyPath string
	BenchPaths []string
	Labels     map[string]string
}

// UpdateServer carries optional fields. nil means "leave unchanged"; a
// pointer to the zero value means "explicit set to that value". Lets a
// PATCH handler accept partial input without ambiguity.
type UpdateServer struct {
	Name       *string
	Hostname   *string
	SSHUser    *string
	SSHPort    *int
	SSHKeyPath *string
	BenchPaths *[]string
	Labels     *map[string]string
}

// SystemSnapshot is the operator-friendly inventory of a server
// (OS, CPU, memory, disks, top processes). One row per server,
// upserted on capture.
type SystemSnapshot struct {
	ServerID   int
	CapturedAt time.Time
	Payload    []byte // JSON, schema-on-read; the SPA renders it.
	LastError  string
}

// AlertState mirrors ent/schema/alertstate.go. The alerts package's
// reconciler reads/writes these via the Store interface.
type AlertState struct {
	ID             int
	RuleName       string
	Fingerprint    string
	Labels         map[string]string
	Status         string // "firing" | "resolved"
	Value          float64
	FirstFiredAt   time.Time
	LastNotifiedAt time.Time
	UpdatedAt      time.Time
}

// AlertIncident is an immutable episode identity with mutable resolution
// metadata. Unlike AlertState, resolved incidents are retained for diagnosis.
type AlertIncident struct {
	ID             int
	RuleName       string
	Fingerprint    string
	Severity       string
	Labels         map[string]string
	Status         string
	Value          float64
	StartedAt      time.Time
	ResolvedAt     *time.Time
	LastNotifiedAt *time.Time
	UpdatedAt      time.Time
}

// AlertIncidentStore is optional so lightweight evaluator test stores and
// alternative Store implementations remain compatible. EntStore implements it.
type AlertIncidentStore interface {
	UpsertOpenAlertIncident(ctx context.Context, in AlertIncident) (*AlertIncident, error)
	ResolveOpenAlertIncident(ctx context.Context, ruleName, fingerprint string, value float64, resolvedAt time.Time) error
	ListAlertIncidents(ctx context.Context, limit int) ([]*AlertIncident, error)
}

type Store interface {
	CreateServer(ctx context.Context, in NewServer) (*Server, error)
	GetServer(ctx context.Context, id int) (*Server, error)
	ListServers(ctx context.Context) ([]*Server, error)
	UpdateServer(ctx context.Context, id int, in UpdateServer) (*Server, error)
	DeleteServer(ctx context.Context, id int) error
	SetServerStatus(ctx context.Context, id int, status string, lastErr string) error

	// Log cursors — Phase 3.
	GetLogCursor(ctx context.Context, serverID int, logPath string) (*LogCursor, error)
	UpsertLogCursor(ctx context.Context, c LogCursor) error

	// Alert state — Phase 6.
	ListAlertStatesByRule(ctx context.Context, ruleName string) ([]*AlertState, error)
	ListAlertStates(ctx context.Context) ([]*AlertState, error)
	UpsertAlertState(ctx context.Context, in AlertState) (*AlertState, error)
	DeleteAlertState(ctx context.Context, id int) error

	// System snapshot — operator-friendly inventory per server.
	GetSystemSnapshot(ctx context.Context, serverID int) (*SystemSnapshot, error)
	UpsertSystemSnapshot(ctx context.Context, in SystemSnapshot) error

	Close() error
}
