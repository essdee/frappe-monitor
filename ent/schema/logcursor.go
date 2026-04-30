package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// LogCursor tracks the byte offset of the last log read for a given
// (server, log_path) pair. The collector uses this to incrementally
// tail log files via `tail -c +<offset>` over SSH without re-reading
// previously-pushed bytes on every cycle.
type LogCursor struct {
	ent.Schema
}

func (LogCursor) Fields() []ent.Field {
	return []ent.Field{
		field.String("log_path").NotEmpty(),
		field.Int64("byte_offset").Default(0).Min(0),
		field.Time("last_seen_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (LogCursor) Edges() []ent.Edge {
	return []ent.Edge{
		// One server -> many log cursors. Cascading delete from server.
		edge.From("server", Server.Type).
			Ref("log_cursors").
			Unique().
			Required(),
	}
}

func (LogCursor) Indexes() []ent.Index {
	return []ent.Index{
		// Composite uniqueness: at most one cursor per (server, log_path).
		index.Fields("log_path").Edges("server").Unique(),
	}
}
