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

	Close() error
}
