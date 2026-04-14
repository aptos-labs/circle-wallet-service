ALTER TABLE webhook_deliveries
  ADD CONSTRAINT fk_webhook_txn
  FOREIGN KEY (transaction_id) REFERENCES transactions(id)
  ON DELETE CASCADE;
