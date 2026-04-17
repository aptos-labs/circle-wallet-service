// Fee-payer (sponsored transaction) example for the Circle Wallet Service.
//
// Background
//
// On Aptos every transaction has to pay gas in APT. For a new user who has
// just created a wallet, this creates a chicken-and-egg problem: they can't
// do anything on chain until they first get APT. Aptos solves this with
// "fee-payer" (sponsored) transactions, where the SENDER of the transaction
// and the GAS PAYER are different accounts. The sender signs the transaction
// body; the fee payer signs a separate gas commitment. The chain accepts the
// transaction as long as both signatures are valid.
//
// The Circle Wallet Service exposes this via the optional "fee_payer" field
// on /v1/execute:
//
//	{
//	  "wallet_id": "user-wallet-id",       ← who's sending
//	  "address":   "0xuser...",
//	  "fee_payer": {
//	    "wallet_id": "sponsor-wallet-id",  ← who's paying gas
//	    "address":   "0xsponsor..."
//	  },
//	  ...
//	}
//
// Under the hood the service calls Circle twice — once to sign as the sender
// and once to sign as the fee payer — then assembles a FeePayerSignedTransaction
// and submits it. Both signatures are per-transaction; they don't commit the
// sponsor to anything future.
//
// What this example does
//
// 1. Query the USER's APT balance via /v1/query (before).
// 2. Submit a transfer FROM the user TO a recipient, with SPONSOR paying gas.
// 3. Poll /v1/transactions/{id} until the transfer confirms.
// 4. Query the USER's APT balance again (after).
//
// If fee-payer is working, the user's balance should drop by exactly the
// transfer amount (1 unit, as passed to 0x1::aptos_account::transfer) — the
// gas cost comes out of the sponsor's balance, not the user's. You should
// also see that the transfer succeeds even if the user started with only
// the transfer-amount-plus-one in their account (not enough to pay gas
// themselves).
//
// Usage:
//
//	export API_KEY=your-bearer-token
//	export USER_WALLET_ID=...
//	export USER_ADDRESS=0xuser...
//	export SPONSOR_WALLET_ID=...
//	export SPONSOR_ADDRESS=0xsponsor...
//	export RECIPIENT_ADDRESS=0xrecipient...   # optional; defaults to SPONSOR_ADDRESS
//	go run ./examples/fee_payer
//
// Both the user and the sponsor must be Circle wallets already registered
// with your Circle developer account. The sponsor must hold enough APT to
// cover gas.
package main
