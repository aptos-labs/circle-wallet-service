package txn

import (
	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/api"
)

// TxnClient abstracts the Aptos client operations needed by Manager and Poller.
type TxnClient interface {
	// BuildOrderlessTransaction builds an orderless transaction given a payload to submit to the blockchain.
	// If maxGasAmount > 0, it overrides the client's default gas amount.
	BuildOrderlessTransaction(sender aptossdk.AccountAddress, payload aptossdk.TransactionPayload, maxGasAmount uint64) (*aptossdk.RawTransaction, uint64, error)
	// SubmitTransaction submits the full signed transaction to the blockchain
	SubmitTransaction(signed *aptossdk.SignedTransaction) (*api.SubmitTransactionResponse, error)
	// TransactionByHash retrieves the transaction status from onchain state (or mempool state)
	TransactionByHash(hash string) (*api.Transaction, error)
}
