package circle

import (
	"context"
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

// SignFeePayerTransaction signs a RawTransactionWithData using Circle's sign/message endpoint.
//
// Circle's sign/transaction doesn't support Aptos. sign/message rejects messages prefixed
// with sha3("APTOS::RawTransaction") (error 156025). The workaround: wrap as fee-payer
// RawTransactionWithData so the signing message has prefix sha3("APTOS::RawTransactionWithData"),
// which Circle allows.
func (s *Signer) SignTransaction(
	ctx context.Context,
	rawTxnWithData aptossdk.RawTransactionImpl,
	walletId string,
	pubKeyHex string,
) (*crypto.AccountAuthenticator, error) {
	// 1. Get the signing message: sha3("APTOS::RawTransactionWithData") || BCS(wrapper)
	signingMsg, err := bcs.Serialize(rawTxnWithData)
	if err != nil {
		return nil, fmt.Errorf("signing message: %w", err)
	}

	// 2. Encrypt entity secret (fresh per request)
	ciphertext, err := s.client.EncryptEntitySecret(ctx, s.entitySecret)
	if err != nil {
		return nil, fmt.Errorf("encrypt entity secret: %w", err)
	}

	// 3. Send full signing message to sign/message as hex
	hexMessage := aptossdk.BytesToHex(signingMsg)
	signResp, err := s.client.SignMessage(ctx, &SignTransactionForDeveloperRequest{
		WalletID:               walletId,
		RawTransaction:         hexMessage,
		EntitySecretCiphertext: ciphertext,
	})
	if err != nil {
		return nil, fmt.Errorf("circle sign/message: %w", err)
	}

	// 4. Decode signature
	sig := &crypto.Ed25519Signature{}
	err = sig.FromHex(signResp.Data.Signature)

	// 5. Decode public key
	pubKey := &crypto.Ed25519PublicKey{}
	err = pubKey.FromHex(pubKeyHex)
	if err != nil {
		return nil, fmt.Errorf("bad public key: %w", err)
	}

	// 6. Build FeePayerTransactionAuthenticator (same sig for sender+fee-payer, same wallet)
	senderAuth := &crypto.AccountAuthenticator{}
	err = senderAuth.FromKeyAndSignature(pubKey, sig)
	if err != nil {
		return nil, fmt.Errorf("bad authenticator: %w", err)
	}
	return senderAuth, nil
}
