package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AlertIncident is the durable operator-facing history of alert episodes.
// AlertState remains the evaluator's small reconciliation working set.
type AlertIncident struct {
	ent.Schema
}

func (AlertIncident) Fields() []ent.Field {
	return []ent.Field{
		field.String("rule_name").NotEmpty(),
		field.String("fingerprint").NotEmpty(),
		field.String("severity").Default("warning"),
		field.JSON("labels", map[string]string{}).Optional(),
		field.Enum("status").Values("firing", "resolved").Default("firing"),
		field.Float("value").Default(0),
		field.Time("started_at").Default(time.Now),
		field.Time("resolved_at").Optional().Nillable(),
		field.Time("last_notified_at").Optional().Nillable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (AlertIncident) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "started_at"),
		index.Fields("rule_name", "fingerprint", "status"),
	}
}
