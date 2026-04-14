CREATE TABLE IF NOT EXISTS webhook_deliveries (
  id CHAR(36) NOT NULL PRIMARY KEY,
  transaction_id CHAR(36) NOT NULL,
  url TEXT NOT NULL,
  payload JSON NOT NULL,
  status VARCHAR(32) NOT NULL DEFAULT 'pending',
  attempts INT NOT NULL DEFAULT 0,
  last_attempt_at TIMESTAMP NULL,
  last_error TEXT NULL,
  next_retry_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  KEY idx_pending (status, next_retry_at),
  KEY idx_txn (transaction_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
