// Package circle provides a client for the Circle Programmable Wallets API,
// specifically the operations needed for Aptos fee-payer transaction signing.
//
// Three main components:
//   - [Client] — low-level HTTP client: wallet lookup, RSA key fetch, entity
//     secret encryption, and the sign/transaction endpoint.
//   - [Signer] — higher-level helper that BCS-serializes a RawTransactionWithData,
//     encrypts the entity secret, calls sign/transaction, and assembles the
//     resulting Ed25519 AccountAuthenticator.
//   - [PublicKeyCache] — thread-safe, lazy-loading cache that resolves wallet
//     public keys from Circle (using singleflight to coalesce concurrent lookups).
package circle
