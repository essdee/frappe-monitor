package storage

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound          = errors.New("storage: not found")
	ErrDuplicateHostname = errors.New("storage: hostname already exists")
)

// LogCursor records the byte offset of the last log read for a given
// (server, log_path) pair. Used by the log-tailing pipeline to read
// only the bytes appended since the previous cycle.
type LogCursor struct {
	ServerID   int
	LogPath    string
	ByteOffset int64
	LastSeenAt time.Time
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

// DBTarget mirrors ent/schema/dbtarget.go — a database replica watched
// for replication health. Reached via its server's SSH.
type DBTarget struct {
	ID                  int
	ServerID            int
	Name                string
	Enabled             bool
	Engine              string // "mysql" | "postgres"
	LagThresholdSeconds int
	ClientCommand       string // remote client binary; empty = engine default
	DefaultsFile        string
	Socket              string
	HeartbeatEnabled    bool
	HeartbeatQuery      string
	// Last-check status.
	Status              string // unknown|healthy|lagging|broken|unreachable
	LastCheckedAt       *time.Time
	LagSeconds          *int64
	HeartbeatLagSeconds *float64
	IORunning           bool
	SQLRunning          bool
	LastError           string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type NewDBTarget struct {
	ServerID            int
	Name                string
	Enabled             bool
	Engine              string
	LagThresholdSeconds int
	ClientCommand       string
	DefaultsFile        string
	Socket              string
	HeartbeatEnabled    bool
	HeartbeatQuery      string
}

// UpdateDBTarget carries optional fields (nil = leave unchanged).
type UpdateDBTarget struct {
	ServerID            *int
	Name                *string
	Enabled             *bool
	Engine              *string
	LagThresholdSeconds *int
	ClientCommand       *string
	DefaultsFile        *string
	Socket              *string
	HeartbeatEnabled    *bool
	HeartbeatQuery      *string
}

// DBTargetStatus is one replication-check result, persisted by the
// dbmonitor service so the dashboard + alerts can read the latest state.
type DBTargetStatus struct {
	Status              string
	LagSeconds          *int64
	HeartbeatLagSeconds *float64
	IORunning           bool
	SQLRunning          bool
	LastError           string
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

	// DB replication targets — Phase 9 (admin-managed).
	CreateDBTarget(ctx context.Context, in NewDBTarget) (*DBTarget, error)
	GetDBTarget(ctx context.Context, id int) (*DBTarget, error)
	ListDBTargets(ctx context.Context) ([]*DBTarget, error)
	UpdateDBTarget(ctx context.Context, id int, in UpdateDBTarget) (*DBTarget, error)
	DeleteDBTarget(ctx context.Context, id int) error
	SetDBTargetStatus(ctx context.Context, id int, st DBTargetStatus) error

	Close() error
}
