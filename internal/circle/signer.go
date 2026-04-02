package circle

import (
	"context"
	"encoding/hex"
	"fmt"

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

// SignTransaction signs a RawTransactionWithData using Circle's sign/transaction endpoint
// per the Aptos Signing APIs Tutorial.
//
// Steps:
// 1. BCS-serialize the RawTransactionWithData to hex
// 2. Send to Circle sign/transaction
// 3. Get partial signature back
// 4. Build AccountAuthenticator
func (s *Signer) SignTransaction(
	ctx context.Context,
	rawTxnWithData aptossdk.RawTransactionImpl,
	walletId string,
	pubKeyHex string,
) (*crypto.AccountAuthenticator, error) {
	// 1. BCS-serialize RawTransactionWithData to hex (per PDF tutorial step 4), this is specifically not a signing message
	// because Circle expects a serialized raw transaction (the whole data)
	rawTxnBytes, err := bcs.Serialize(rawTxnWithData)
	if err != nil {
		return nil, fmt.Errorf("BCS serialize: %w", err)
	}

	// Must start with 0x
	rawTxnHex := "0x" + hex.EncodeToString(rawTxnBytes)

	// 2. Encrypt entity secret (fresh per request)
	ciphertext, err := s.client.EncryptEntitySecret(ctx, s.entitySecret)
	if err != nil {
		return nil, fmt.Errorf("encrypt entity secret: %w", err)
	}

	// 3. Call Circle sign/transaction (per PDF tutorial step 5)
	signResp, err := s.client.SignTransaction(ctx, &SignTransactionForDeveloperRequest{
		WalletID:               walletId,
		RawTransaction:         rawTxnHex,
		EntitySecretCiphertext: ciphertext,
	})
	if err != nil {
		return nil, fmt.Errorf("circle sign/transaction: %w", err)
	}

	// 4. Decode signature
	sig := &crypto.Ed25519Signature{}
	err = sig.FromHex(signResp.Data.Signature)
	if err != nil {
		return nil, fmt.Errorf("bad signature: %w", err)
	}

	// 5. Decode public key
	pubKey := &crypto.Ed25519PublicKey{}
	err = pubKey.FromHex(pubKeyHex)
	if err != nil {
		return nil, fmt.Errorf("bad public key: %w", err)
	}

	// 6. Build AccountAuthenticator
	auth := &crypto.AccountAuthenticator{}
	err = auth.FromKeyAndSignature(pubKey, sig)
	if err != nil {
		return nil, fmt.Errorf("bad authenticator: %w", err)
	}
	return auth, nil
}
