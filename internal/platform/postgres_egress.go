package platform

import (
	"context"
	"errors"
	"time"
)

// RecordEgressUsage implements trafficproxy.EgressUsageSink. Rows are aggregated
// per (run, egress proxy) and never contain packet data or credentials — only
// the masked proxy label.
func (s *Store) RecordEgressUsage(ctx context.Context, scanID, instanceID, label string, connections, requests int64) error {
	if s == nil || s.pool == nil {
		return errors.New("platform/postgres: store unavailable")
	}
	if scanID == "" || label == "" {
		return nil
	}
	now := time.Now().UTC()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO run_egress_usage (scan_id, proxy_label, instance_id, connections, requests, first_at, last_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (scan_id, proxy_label) DO UPDATE SET
			connections = run_egress_usage.connections + EXCLUDED.connections,
			requests    = run_egress_usage.requests + EXCLUDED.requests,
			instance_id = CASE WHEN EXCLUDED.instance_id <> '' THEN EXCLUDED.instance_id ELSE run_egress_usage.instance_id END,
			first_at    = LEAST(run_egress_usage.first_at, EXCLUDED.first_at),
			last_at     = GREATEST(run_egress_usage.last_at, EXCLUDED.last_at)`,
		scanID, label, instanceID, connections, requests, now, now)
	return err
}
