package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// DBTarget is a database replica to watch for master/slave replication
// health. The monitor SSHes into the target's server (reusing that
// server's SSH credentials) and runs the mysql client there — no new DB
// port is exposed. Managed entirely from the dashboard (admin UI), like
// Server.
type DBTarget struct {
	ent.Schema
}

func (DBTarget) Fields() []ent.Field {
	return []ent.Field{
		// FK to the server reached via SSH (exposed as a field so reads
		// don't need to load the server edge). Bound to the `server` edge.
		field.Int("server_id"),

		// Config (admin-editable).
		field.String("name").NotEmpty(),
		field.Bool("enabled").Default(true),
		// Replication-lag alert threshold for this target, in seconds.
		field.Int("lag_threshold_seconds").Default(30).Positive(),
		// mysql client invocation on the remote host.
		field.String("mysql_command").Default("mysql"),
		// my.cnf-style file holding the DB credentials (a user with
		// REPLICATION CLIENT), so no DB password is stored here.
		field.String("defaults_file").Optional(),
		// Optional --socket to reach a local mysqld.
		field.String("socket").Optional(),
		// Heartbeat-table lag check (accurate even when replication is
		// idle, unlike Seconds_Behind_Master). The query must return one
		// numeric column = current lag in seconds.
		field.Bool("heartbeat_enabled").Default(false),
		field.String("heartbeat_query").Optional(),

		// Last-check status (written by the dbmonitor service).
		field.Enum("status").
			Values("unknown", "healthy", "lagging", "broken", "unreachable").
			Default("unknown"),
		field.Time("last_checked_at").Optional().Nillable(),
		// Replication lag in seconds (Seconds_Behind_Master). Nillable so
		// "unknown / NULL (replica stopped)" is distinct from 0.
		field.Int64("lag_seconds").Optional().Nillable(),
		// Heartbeat-derived lag in seconds (when heartbeat_enabled).
		field.Float("heartbeat_lag_seconds").Optional().Nillable(),
		field.Bool("io_running").Default(false),
		field.Bool("sql_running").Default(false),
		field.String("last_error").Optional(),

		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (DBTarget) Edges() []ent.Edge {
	return []ent.Edge{
		// Each DB target is reached via one registered server's SSH.
		// Cascading delete from server.
		edge.From("server", Server.Type).
			Ref("db_targets").
			Field("server_id").
			Unique().
			Required(),
	}
}
