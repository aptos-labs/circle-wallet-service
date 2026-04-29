ALTER TABLE transactions
  DROP KEY idx_queue,
  ADD KEY idx_queue (status, sender_address, id);
