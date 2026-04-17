// High-throughput usage example for the Circle Wallet Service.
//
// The server processes transactions FIFO per sender but in parallel across
// senders. Using M wallets therefore gives ~M× throughput.
//
// Usage:
//
//	export API_KEY=your-bearer-token
//	export WALLETS='[{"wallet_id":"w1","address":"0xabc..."},{"wallet_id":"w2","address":"0xdef..."}]'
//	go run ./examples/high_throughput
package main
