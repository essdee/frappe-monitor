package storage

import (
	"context"
	"time"

	"frappe-monitor/ent"
	entcontrolaction "frappe-monitor/ent/controlaction"
)

func entToControlAction(r *ent.ControlAction) *ControlAction {
	return &ControlAction{
		ID:          r.ID,
		ServerID:    r.ServerID,
		Action:      r.Action,
		BenchPath:   r.BenchPath,
		Site:        r.Site,
		Command:     r.Command,
		RequestedBy: r.RequestedBy,
		Status:      string(r.Status),
		ExitOK:      r.ExitOk,
		Output:      r.Output,
		Error:       r.Error,
		DurationMs:  r.DurationMs,
		CreatedAt:   r.CreatedAt,
		FinishedAt:  r.FinishedAt,
	}
}

func (s *EntStore) CreateControlAction(ctx context.Context, in NewControlAction) (*ControlAction, error) {
	b := s.client.ControlAction.Create().
		SetServerID(in.ServerID).
		SetAction(in.Action).
		SetBenchPath(in.BenchPath).
		SetSite(in.Site).
		SetCommand(in.Command)
	if in.RequestedBy != "" {
		b = b.SetRequestedBy(in.RequestedBy)
	}
	row, err := b.Save(ctx)
	if err != nil {
		return nil, err
	}
	return entToControlAction(row), nil
}

func (s *EntStore) GetControlAction(ctx context.Context, id int) (*ControlAction, error) {
	row, err := s.client.ControlAction.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return entToControlAction(row), nil
}

func (s *EntStore) ListControlActions(ctx context.Context, f ListControlActions) ([]*ControlAction, error) {
	q := s.client.ControlAction.Query()
	if f.ServerID > 0 {
		q = q.Where(entcontrolaction.ServerID(f.ServerID))
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := q.Order(ent.Desc(entcontrolaction.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*ControlAction, 0, len(rows))
	for _, r := range rows {
		out = append(out, entToControlAction(r))
	}
	return out, nil
}

func (s *EntStore) MarkControlActionRunning(ctx context.Context, id int) error {
	if err := s.client.ControlAction.UpdateOneID(id).
		SetStatus(entcontrolaction.StatusRunning).Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (s *EntStore) FailStaleControlActions(ctx context.Context, reason string) (int, error) {
	return s.client.ControlAction.Update().
		Where(entcontrolaction.StatusIn(entcontrolaction.StatusPending, entcontrolaction.StatusRunning)).
		SetStatus(entcontrolaction.StatusFailed).
		SetExitOk(false).
		SetError(reason).
		SetFinishedAt(time.Now().UTC()).
		Save(ctx)
}

func (s *EntStore) FinishControlAction(ctx context.Context, id int, res ControlActionResult) (*ControlAction, error) {
	status := entcontrolaction.StatusSuccess
	if res.Status == "failed" {
		status = entcontrolaction.StatusFailed
	}
	row, err := s.client.ControlAction.UpdateOneID(id).
		SetStatus(status).
		SetExitOk(res.ExitOK).
		SetOutput(res.Output).
		SetError(res.Error).
		SetDurationMs(res.DurationMs).
		SetFinishedAt(time.Now().UTC()).
		Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return entToControlAction(row), nil
}
