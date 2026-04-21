-- Adds first-class diagnostic fields for terminal failures so API and webhook
-- consumers don't have to parse error_message. Populated by the submitter at
-- markPermanentFailure time; NULL for rows that never failed or failed before
-- this migration ran.
--
-- failure_kind is a short enum-like string identifying which stage rejected the
-- transaction: "simulation", "submit", "expired", "validation", "signing", etc.
-- vm_status is the Aptos VM's structured reason from /transactions/simulate
-- (e.g. "Move abort … code 1: EINSUFFICIENT_BALANCE"). Only populated when we
-- have a real VM response.
ALTER TABLE transactions
  ADD COLUMN failure_kind VARCHAR(32) NULL AFTER last_error,
  ADD COLUMN vm_status TEXT NULL AFTER failure_kind;
