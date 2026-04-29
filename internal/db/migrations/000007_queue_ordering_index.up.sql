-- Match ClaimNextQueuedForSender:
--   WHERE status = ? AND sender_address = ?
--   ORDER BY created_at ASC, id ASC
-- Replaces the older idx_queue(status, sender_address, id) so we avoid a
-- redundant overlapping index and the write amplification that comes with it.
ALTER TABLE transactions
  DROP KEY idx_queue,
  ADD KEY idx_queue (status, sender_address, created_at, id);
