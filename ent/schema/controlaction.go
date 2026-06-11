package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// ControlAction is the audit record for one run of an allowlisted control-panel
// command (bench migrate/update/clear-cache, service restart, site-config edit)
// against a monitored server. Every run — who, when, the exact command, its
// exit status, and captured output — is persisted so the operator always has a
// full, immutable history of what was done to production.
type ControlAction struct {
	ent.Schema
}

func (ControlAction) Fields() []ent.Field {
	return []ent.Field{
		field.Int("server_id"), // FK to the server the command ran against

		// The allowlist key (e.g. "bench.migrate"). Free-form commands are
		// never stored here — only keys the server-side registry recognises.
		field.String("action").NotEmpty(),
		// Optional scoping for bench/site-level actions.
		field.String("bench_path").Optional(),
		field.String("site").Optional(),
		// The exact shell command that was executed, kept verbatim for audit.
		field.String("command").Optional(),
		// Best-effort identity of who triggered it. The app is single-password
		// today, so this defaults to "operator"; kept for future per-user auth.
		field.String("requested_by").Default("operator"),

		field.Enum("status").
			Values("pending", "running", "success", "failed").
			Default("pending"),
		field.Bool("exit_ok").Default(false),
		// Combined stdout/stderr, truncated by the service before storing.
		field.Text("output").Optional(),
		field.String("error").Optional(),
		field.Int("duration_ms").Default(0).NonNegative(),

		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("finished_at").Optional().Nillable(),
	}
}

func (ControlAction) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("server", Server.Type).
			Ref("control_actions").
			Field("server_id").
			Unique().
			Required(),
	}
}
