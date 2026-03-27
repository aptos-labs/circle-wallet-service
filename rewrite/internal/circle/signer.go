package circle

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/bcs"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
)

type Signer struct {
	client       *Client
	entitySecret string
}

func NewSigner(client *Client, entitySecret string) *Signer {
	return &Signer{client: client, entitySecret: entitySecret}
}

// SignFeePayerTransaction signs a RawTransactionWithData using Circle's sign/transaction endpoint.
// walletID is the Circle wallet (both sender and fee-payer).
// pubKeyHex is the wallet's Ed25519 public key (hex, with or without 0x).
// feePayerAddr is the Aptos address of the wallet.
func (s *Signer) SignFeePayerTransaction(
	ctx context.Context,
	rawTxnWithData *aptossdk.RawTransactionWithData,
	walletID string,
	pubKeyHex string,
	feePayerAddr aptossdk.AccountAddress,
) (*aptossdk.SignedTransaction, error) {
	// 1. BCS-serialize RawTransactionWithData to hex
	rawTxnBytes, err := bcs.Serialize(rawTxnWithData)
	if err != nil {
		return nil, fmt.Errorf("BCS serialize raw txn: %w", err)
	}
	rawTxnHex := "0x" + hex.EncodeToString(rawTxnBytes)

	// 2. Encrypt entity secret (fresh per request for replay protection)
	ciphertext, err := s.client.EncryptEntitySecret(ctx, s.entitySecret)
	if err != nil {
		return nil, fmt.Errorf("encrypt entity secret: %w", err)
	}

	// 3. Call Circle sign/transaction
	signResp, err := s.client.SignTransaction(ctx, &SignTransactionRequest{
		WalletID:               walletID,
		RawTransaction:         rawTxnHex,
		EntitySecretCiphertext: ciphertext,
	})
	if err != nil {
		return nil, fmt.Errorf("circle sign/transaction: %w", err)
	}

	// 4. Decode partial signature
	sigHex := strings.TrimPrefix(signResp.Data.Signature, "0x")
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return nil, fmt.Errorf("decode signature hex: %w", err)
	}
	if len(sigBytes) != 64 {
		return nil, fmt.Errorf("unexpected signature length: %d (expected 64)", len(sigBytes))
	}

	// 5. Decode public key
	pubKeyHex = strings.TrimPrefix(pubKeyHex, "0x")
	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	if len(pubKeyBytes) != 32 {
		return nil, fmt.Errorf("unexpected public key length: %d (expected 32)", len(pubKeyBytes))
	}

	pubKey := crypto.Ed25519PublicKey{Inner: pubKeyBytes}
	var sigArr [64]byte
	copy(sigArr[:], sigBytes)

	// 6. Build FeePayerTransactionAuthenticator (same sig for sender+fee-payer, same wallet)
	auth := &crypto.AccountAuthenticator{
		Variant: crypto.AccountAuthenticatorEd25519,
		Auth: &crypto.Ed25519Authenticator{
			PubKey: &pubKey,
			Sig:    &crypto.Ed25519Signature{Inner: sigArr},
		},
	}

	inner, ok := rawTxnWithData.Inner.(*aptossdk.MultiAgentWithFeePayerRawTransactionWithData)
	if !ok {
		return nil, fmt.Errorf("unexpected RawTransactionWithData inner type")
	}

	return &aptossdk.SignedTransaction{
		Transaction: inner.RawTxn,
		Authenticator: &aptossdk.TransactionAuthenticator{
			Variant: aptossdk.TransactionAuthenticatorFeePayer,
			Auth: &aptossdk.FeePayerTransactionAuthenticator{
				Sender:                auth,
				FeePayer:              &feePayerAddr,
				FeePayerAuthenticator: auth,
			},
		},
	}, nil
}
