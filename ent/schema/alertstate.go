package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AlertState tracks the firing/resolved status of one (rule_name,
// fingerprint) pair. Fingerprint is a stable hash of the alert's
// distinguishing labels (e.g. server+bench+site for site_unhealthy).
//
// Phase 6 alerts evaluator queries VM, builds (rule, fingerprint)
// keys for each firing series, and reconciles against rows here:
//   - new firing series → insert with status="firing", notify
//   - already firing, cooldown elapsed → notify again (re-page)
//   - no longer firing → status="resolved", notify recovery, delete
type AlertState struct {
	ent.Schema
}

func (AlertState) Fields() []ent.Field {
	return []ent.Field{
		field.String("rule_name").NotEmpty(),
		field.String("fingerprint").NotEmpty(),
		// JSON of {label: value} from the firing series. Stored so the
		// recovery message can name the same target as the firing one.
		field.JSON("labels", map[string]string{}).Optional(),
		field.Enum("status").
			Values("firing", "resolved").
			Default("firing"),
		// Most recent payload value from VM (e.g. 0.94 for disk usage).
		field.Float("value").Default(0),
		field.Time("first_fired_at").Default(time.Now).Immutable(),
		field.Time("last_notified_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (AlertState) Indexes() []ent.Index {
	return []ent.Index{
		// One row per (rule, fingerprint). Reconciliation upserts on this.
		index.Fields("rule_name", "fingerprint").Unique(),
	}
}
