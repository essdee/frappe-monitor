package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type Server struct {
	ent.Schema
}

func (Server) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("hostname").NotEmpty().Unique(),
		field.String("ssh_user").Default("monitor"),
		field.Int("ssh_port").Default(22).Positive(),
		field.String("ssh_key_path").NotEmpty(),
		// Optional list of explicit bench paths on this server. When
		// empty, the bench-side collector falls back to scanning
		// /home/*/frappe-bench, /home/*/bench-* and /opt/bench/* —
		// fine for one-bench hosts, but per-bench-customized hosts
		// (and multi-bench hosts) can supply absolute paths here to
		// short-circuit discovery and ensure every bench is included.
		field.JSON("bench_paths", []string{}).Optional(),
		field.JSON("labels", map[string]string{}).Optional(),
		field.Enum("status").
			Values("unknown", "reachable", "unreachable").
			Default("unknown"),
		field.Time("last_pinged_at").Optional().Nillable(),
		field.String("last_error").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Server) Edges() []ent.Edge {
	return []ent.Edge{
		// One server -> many log cursors. The reverse end lives on
		// LogCursor (Required + Unique → cascading delete falls out).
		edge.To("log_cursors", LogCursor.Type),
		// One server -> one system snapshot (Unique). Cascading
		// delete from server.
		edge.To("system_snapshot", SystemSnapshot.Type).Unique(),
	}
}
