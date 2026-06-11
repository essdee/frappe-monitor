package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// PatchLog records which one-time data patches have already run, so each patch
// executes exactly once across restarts — the same idea as Frappe's patch log.
// Schema DDL is handled by ent's auto-migration; this table tracks DATA
// migrations (patches) and is consulted by storage.RunMigrations.
type PatchLog struct {
	ent.Schema
}

func (PatchLog) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").Unique().NotEmpty(),
		field.Time("applied_at").Default(time.Now).Immutable(),
	}
}
