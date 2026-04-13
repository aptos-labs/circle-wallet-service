-- account_sequences: next sequence number to use when building a transaction for each sender.
-- Invariant: next_sequence is always >= on-chain sequence_number for that account (reconciled
-- from the Aptos node before each submit). After a successful submit, next_sequence is advanced.
CREATE TABLE IF NOT EXISTS account_sequences (
  sender_address VARCHAR(128) NOT NULL PRIMARY KEY,
  next_sequence BIGINT UNSIGNED NOT NULL DEFAULT 0,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS transactions (
  id CHAR(36) NOT NULL PRIMARY KEY,
  sender_address VARCHAR(128) NOT NULL,
  wallet_id VARCHAR(128) NOT NULL,
  status VARCHAR(32) NOT NULL,
  sequence_number BIGINT UNSIGNED NULL,
  function_id TEXT NOT NULL,
  payload_json JSON NOT NULL,
  max_gas_amount BIGINT UNSIGNED NULL,
  idempotency_key VARCHAR(512) NULL,
  txn_hash VARCHAR(256) NULL,
  error_message TEXT NULL,
  webhook_url TEXT NULL,
  attempt_count INT NOT NULL DEFAULT 0,
  last_error TEXT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  expires_at TIMESTAMP NOT NULL,
  UNIQUE KEY uk_idempotency (idempotency_key),
  KEY idx_queue (status, sender_address, id),
  KEY idx_poller (status, txn_hash(128))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
