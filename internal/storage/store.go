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
	Labels     map[string]string
}

type Store interface {
	CreateServer(ctx context.Context, in NewServer) (*Server, error)
	GetServer(ctx context.Context, id int) (*Server, error)
	ListServers(ctx context.Context) ([]*Server, error)
	SetServerStatus(ctx context.Context, id int, status string, lastErr string) error

	// Log cursors — Phase 3.
	GetLogCursor(ctx context.Context, serverID int, logPath string) (*LogCursor, error)
	UpsertLogCursor(ctx context.Context, c LogCursor) error

	Close() error
}
