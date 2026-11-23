-- atlas:txmode none

-- PERF-M9: partial indexes for OutputEvents, LastHarnessStatus/Error/End, and an unfiltered
-- LatestDrill index replacing drill_reports_last_pass (LatestDrill reads failing reports too).
CREATE INDEX CONCURRENTLY events_output_events ON events (room_id, run_id, (payload->'output'->>'id'), seq)
  WHERE type = 'message' AND origin = 'client' AND payload->'output'->>'id' IS NOT NULL;

CREATE INDEX CONCURRENTLY events_harness_state ON events (room_id, run_id, (payload->>'kind'), seq DESC)
  WHERE type = 'state_changed' AND payload->>'kind' IN ('harness_status', 'harness_error', 'harness_end');

CREATE INDEX CONCURRENTLY drill_reports_latest ON drill_reports (version, config_hash, at DESC, drill_id DESC);

DROP INDEX CONCURRENTLY IF EXISTS drill_reports_last_pass;
