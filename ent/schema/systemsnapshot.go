package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// SystemSnapshot stores a point-in-time inventory of a server: OS,
// kernel, CPU model, memory + swap totals, disk usage per mount, and
// top-N processes by CPU + memory. Captured on server creation and
// on-demand from the dashboard's "Refresh system details" button.
//
// One row per server (Unique edge); the row is upserted on capture
// so we never accumulate history. If you want time-series of system
// info (e.g. swap usage trended over weeks), that's a separate
// pipeline through VM — this table is "what does the box look like
// right now."
type SystemSnapshot struct {
	ent.Schema
}

func (SystemSnapshot) Fields() []ent.Field {
	return []ent.Field{
		field.Time("captured_at").Default(time.Now).UpdateDefault(time.Now),
		// Raw JSON payload from the bench-side script. Schema-on-read
		// — the frontend interprets it. Keeping this opaque means we
		// can extend the script (add containers, NICs, GPU, etc.)
		// without an ent migration each time.
		field.Bytes("payload"),
		// Last error string when capture fails. Empty on success.
		field.String("last_error").Optional(),
	}
}

func (SystemSnapshot) Edges() []ent.Edge {
	return []ent.Edge{
		// 1:1 with Server. Cascading delete from server.
		edge.From("server", Server.Type).
			Ref("system_snapshot").
			Unique().
			Required(),
	}
}
