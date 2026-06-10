package storage

import (
	"context"
	"time"

	"frappe-monitor/ent"
	entdbtarget "frappe-monitor/ent/dbtarget"
)

func entToDBTarget(r *ent.DBTarget) *DBTarget {
	return &DBTarget{
		ID:                  r.ID,
		ServerID:            r.ServerID,
		Name:                r.Name,
		Enabled:             r.Enabled,
		Engine:              string(r.Engine),
		LagThresholdSeconds: r.LagThresholdSeconds,
		ClientCommand:       r.ClientCommand,
		DefaultsFile:        r.DefaultsFile,
		Socket:              r.Socket,
		HeartbeatEnabled:    r.HeartbeatEnabled,
		HeartbeatQuery:      r.HeartbeatQuery,
		Status:              string(r.Status),
		LastCheckedAt:       r.LastCheckedAt,
		LagSeconds:          r.LagSeconds,
		HeartbeatLagSeconds: r.HeartbeatLagSeconds,
		IORunning:           r.IoRunning,
		SQLRunning:          r.SQLRunning,
		LastError:           r.LastError,
		CreatedAt:           r.CreatedAt,
		UpdatedAt:           r.UpdatedAt,
	}
}

func (s *EntStore) CreateDBTarget(ctx context.Context, in NewDBTarget) (*DBTarget, error) {
	b := s.client.DBTarget.Create().
		SetServerID(in.ServerID).
		SetName(in.Name).
		SetEnabled(in.Enabled).
		SetEngine(entdbtarget.Engine(firstNonEmpty(in.Engine, "mysql"))).
		SetClientCommand(in.ClientCommand).
		SetDefaultsFile(in.DefaultsFile).
		SetSocket(in.Socket).
		SetHeartbeatEnabled(in.HeartbeatEnabled).
		SetHeartbeatQuery(in.HeartbeatQuery)
	if in.LagThresholdSeconds > 0 {
		b = b.SetLagThresholdSeconds(in.LagThresholdSeconds)
	}
	row, err := b.Save(ctx)
	if err != nil {
		return nil, err
	}
	return entToDBTarget(row), nil
}

func (s *EntStore) GetDBTarget(ctx context.Context, id int) (*DBTarget, error) {
	row, err := s.client.DBTarget.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return entToDBTarget(row), nil
}

func (s *EntStore) ListDBTargets(ctx context.Context) ([]*DBTarget, error) {
	rows, err := s.client.DBTarget.Query().Order(ent.Asc(entdbtarget.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*DBTarget, 0, len(rows))
	for _, r := range rows {
		out = append(out, entToDBTarget(r))
	}
	return out, nil
}

func (s *EntStore) UpdateDBTarget(ctx context.Context, id int, in UpdateDBTarget) (*DBTarget, error) {
	u := s.client.DBTarget.UpdateOneID(id)
	if in.ServerID != nil {
		u = u.SetServerID(*in.ServerID)
	}
	if in.Name != nil {
		u = u.SetName(*in.Name)
	}
	if in.Enabled != nil {
		u = u.SetEnabled(*in.Enabled)
	}
	if in.Engine != nil {
		u = u.SetEngine(entdbtarget.Engine(*in.Engine))
	}
	if in.LagThresholdSeconds != nil {
		u = u.SetLagThresholdSeconds(*in.LagThresholdSeconds)
	}
	if in.ClientCommand != nil {
		u = u.SetClientCommand(*in.ClientCommand)
	}
	if in.DefaultsFile != nil {
		u = u.SetDefaultsFile(*in.DefaultsFile)
	}
	if in.Socket != nil {
		u = u.SetSocket(*in.Socket)
	}
	if in.HeartbeatEnabled != nil {
		u = u.SetHeartbeatEnabled(*in.HeartbeatEnabled)
	}
	if in.HeartbeatQuery != nil {
		u = u.SetHeartbeatQuery(*in.HeartbeatQuery)
	}
	row, err := u.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return entToDBTarget(row), nil
}

func (s *EntStore) DeleteDBTarget(ctx context.Context, id int) error {
	if err := s.client.DBTarget.DeleteOneID(id).Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// SetDBTargetStatus records the latest replication-check result.
func (s *EntStore) SetDBTargetStatus(ctx context.Context, id int, st DBTargetStatus) error {
	u := s.client.DBTarget.UpdateOneID(id).
		SetStatus(entdbtarget.Status(st.Status)).
		SetIoRunning(st.IORunning).
		SetSQLRunning(st.SQLRunning).
		SetLastError(st.LastError).
		SetLastCheckedAt(time.Now().UTC())
	if st.LagSeconds != nil {
		u = u.SetLagSeconds(*st.LagSeconds)
	} else {
		u = u.ClearLagSeconds()
	}
	if st.HeartbeatLagSeconds != nil {
		u = u.SetHeartbeatLagSeconds(*st.HeartbeatLagSeconds)
	} else {
		u = u.ClearHeartbeatLagSeconds()
	}
	if err := u.Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}
