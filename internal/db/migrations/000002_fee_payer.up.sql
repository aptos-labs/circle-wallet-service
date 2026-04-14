ALTER TABLE transactions
  ADD COLUMN fee_payer_wallet_id VARCHAR(128) NULL AFTER wallet_id,
  ADD COLUMN fee_payer_address VARCHAR(128) NULL AFTER fee_payer_wallet_id;
