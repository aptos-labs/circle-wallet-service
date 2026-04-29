-- Match ClaimNextQueuedForSender:
--   WHERE status = ? AND sender_address = ?
--   ORDER BY created_at ASC, id ASC
-- Without created_at in the index, large per-sender queues can require extra
-- scanning/filesort work while the claim transaction holds locks.
ALTER TABLE transactions ADD KEY idx_queue_sender_created (status, sender_address, created_at, id);
