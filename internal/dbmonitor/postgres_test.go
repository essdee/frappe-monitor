package dbmonitor

import "testing"

func TestParsePostgresStatus(t *testing.T) {
	t.Run("streaming standby with lag", func(t *testing.T) {
		r := parsePostgresStatus("1|streaming|5\n")
		if !r.IsReplica {
			t.Fatal("expected IsReplica=true for in-recovery node")
		}
		if !r.IORunning {
			t.Fatal("expected IORunning=true when wal receiver is streaming")
		}
		if !r.SQLRunning {
			t.Fatal("expected SQLRunning=true on a standby")
		}
		if r.LagSeconds == nil || *r.LagSeconds != 5 {
			t.Fatalf("expected lag=5, got %v", r.LagSeconds)
		}
	})

	t.Run("standby with no receiver connected", func(t *testing.T) {
		r := parsePostgresStatus("1|none|120")
		if !r.IsReplica {
			t.Fatal("still a replica even with receiver down")
		}
		if r.IORunning {
			t.Fatal("IORunning must be false when receiver is not streaming")
		}
		if r.LagSeconds == nil || *r.LagSeconds != 120 {
			t.Fatalf("expected lag=120, got %v", r.LagSeconds)
		}
	})

	t.Run("primary is not a replica", func(t *testing.T) {
		r := parsePostgresStatus("0|none|-1")
		if r.IsReplica {
			t.Fatal("a primary (not in recovery) must report IsReplica=false")
		}
		if r.LagSeconds != nil {
			t.Fatal("primary should not report lag")
		}
	})

	t.Run("NULL lag (-1 sentinel) leaves lag unset", func(t *testing.T) {
		r := parsePostgresStatus("1|streaming|-1")
		if r.LagSeconds != nil {
			t.Fatalf("NULL lag must stay nil, got %v", *r.LagSeconds)
		}
	})

	t.Run("empty output is not a replica", func(t *testing.T) {
		if parsePostgresStatus("").IsReplica {
			t.Fatal("empty output must not be treated as a replica")
		}
	})

	t.Run("malformed row degrades safely", func(t *testing.T) {
		r := parsePostgresStatus("garbage")
		if r.IsReplica {
			t.Fatal("a row without the expected fields must not claim replica status")
		}
	})
}
