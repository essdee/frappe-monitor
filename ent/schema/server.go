package schema

import (
	"time"

	"entgo.io/ent"
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
	return nil
}
