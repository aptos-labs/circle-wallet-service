-- Composite index to back the poller's paginated sweep. The poller now pages
-- through rows using ListByStatusPaged, which issues:
--   WHERE status = ?
--     AND (updated_at > ? OR (updated_at = ? AND id > ?))
--   ORDER BY updated_at ASC, id ASC
--   LIMIT ?
-- The existing idx_queue (status, sender_address, id) is useless for this
-- ordering. idx_status_updated matches the filter+order exactly so pagination
-- is an index range scan rather than a filesort over all rows in the status.
ALTER TABLE transactions ADD KEY idx_status_updated (status, updated_at, id);
