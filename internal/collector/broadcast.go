package collector

import (
	"frappe-monitor/internal/metrics"
	"frappe-monitor/internal/realtime"
)

// emitMetrics pushes a fresh metrics snapshot to the per-server room so
// open detail charts can append the new collection's point without an
// HTTP round-trip. No-op when Broadcaster is nil (tests).
func (p *Pipeline) emitMetrics(serverID int, serverName string, m metrics.ServerMetrics, benches []metrics.BenchMetrics, sites []metrics.SiteMetrics) {
	if p.Broadcaster == nil {
		return
	}
	p.Broadcaster.Broadcast(realtime.Event{
		Type:  realtime.TypeMetrics,
		Topic: realtime.TopicServer(serverID),
		Data:  buildMetricsData(serverID, serverName, m, benches, sites),
	})
}

// buildMetricsData shapes one collection cycle into a self-describing,
// snake_case JSON payload the dashboard appends to its charts. CPU and
// network are raw cumulative counters (the client computes the rate from
// the previous pushed sample, matching the PromQL rate() of the historical
// load); everything else is a direct gauge.
func buildMetricsData(serverID int, serverName string, m metrics.ServerMetrics, benches []metrics.BenchMetrics, sites []metrics.SiteMetrics) map[string]any {
	disks := make([]map[string]any, 0, len(m.Disks))
	for _, d := range m.Disks {
		disks = append(disks, map[string]any{
			"mount": d.Mount, "used_bytes": d.UsedBytes, "total_bytes": d.TotalBytes,
		})
	}
	nets := make([]map[string]any, 0, len(m.Net))
	for _, n := range m.Net {
		nets = append(nets, map[string]any{
			"name": n.Name, "rx_bytes": n.RxBytes, "tx_bytes": n.TxBytes,
		})
	}
	benchList := make([]map[string]any, 0, len(benches))
	for _, b := range benches {
		rq := make(map[string]int64, len(b.RedisQueues))
		for _, q := range b.RedisQueues {
			rq[q.Name] = q.Depth
		}
		benchList = append(benchList, map[string]any{
			"bench":              b.Bench,
			"apps_count":         b.AppsCount,
			"supervisor_running": b.SupervisorRun,
			"supervisor_total":   b.SupervisorTotal,
			"frappe_version":     b.FrappeVersion,
			"redis_queues":       rq,
		})
	}
	siteList := make([]map[string]any, 0, len(sites))
	for _, s := range sites {
		siteList = append(siteList, map[string]any{
			"bench":            s.Bench,
			"site":             s.Site,
			"http_status_code": s.HTTPStatusCode,
			"http_response_ms": s.HTTPResponseMs,
			"is_healthy":       s.IsHealthy,
		})
	}
	return map[string]any{
		"server_id":    serverID,
		"server":       serverName,
		"timestamp_ms": m.Timestamp.UnixMilli(),
		"server_metrics": map[string]any{
			"cpu": map[string]int64{
				"user": m.CPUUser, "nice": m.CPUNice, "system": m.CPUSystem, "idle": m.CPUIdle,
				"iowait": m.CPUIOWait, "irq": m.CPUIRQ, "softirq": m.CPUSoftIRQ, "steal": m.CPUSteal,
			},
			"mem": map[string]int64{
				"total_bytes":     m.MemTotalKB * 1024,
				"available_bytes": m.MemAvailableKB * 1024,
			},
			"load":           map[string]float64{"1m": m.Load1, "5m": m.Load5, "15m": m.Load15},
			"uptime_seconds": m.UptimeSeconds,
			"disks":          disks,
			"net":            nets,
		},
		"benches": benchList,
		"sites":   siteList,
	}
}
