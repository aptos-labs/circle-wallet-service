-- Reference SQL for the transactions table.
-- The actual migration is embedded in Go at internal/store/migrations.go.

CREATE TABLE IF NOT EXISTS transactions (
    id              TEXT PRIMARY KEY,
    operation_type  TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    txn_hash        TEXT,
    nonce           INTEGER NOT NULL DEFAULT 0,
    sender_address  TEXT NOT NULL,
    attempt         INTEGER NOT NULL DEFAULT 0,
    max_retries     INTEGER NOT NULL DEFAULT 3,
    request_payload TEXT NOT NULL DEFAULT '{}',
    error_message   TEXT,
    created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at      DATETIME NOT NULL,
    retry_after     DATETIME
);

CREATE INDEX IF NOT EXISTS idx_transactions_status ON transactions(status);
CREATE INDEX IF NOT EXISTS idx_transactions_status_attempt ON transactions(status, attempt);
CREATE INDEX IF NOT EXISTS idx_transactions_txn_hash ON transactions(txn_hash);
CREATE INDEX IF NOT EXISTS idx_transactions_created_at ON transactions(created_at);
