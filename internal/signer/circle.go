package signer

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
)

// CircleSigner signs Aptos transactions via Circle Programmable Wallets.
//
// Circle's sign/transaction endpoint does not support Aptos, and sign/message
// rejects hex-encoded messages prefixed with sha3("APTOS::RawTransaction") (error 156025).
//
// The workaround: wrap transactions as fee-payer RawTransactionWithData (sender=fee-payer=same
// wallet). The signing message then has prefix sha3("APTOS::RawTransactionWithData"), which
// Circle allows through sign/message.
//
// NOTE: Circle may also block the RawTransactionWithData prefix in some API versions.
// If error 156025 recurs, Circle's prefix check has been tightened and a new approach
// is needed (e.g., coordinating with Circle to whitelist the prefix).
type CircleSigner struct {
	client       *CircleClient
	walletID     string
	entitySecret string // raw 32-byte hex; encrypted fresh per request
	pubKey       *crypto.Ed25519PublicKey
	address      aptossdk.AccountAddress
}

// NewCircleSigner creates a signer backed by a Circle Programmable Wallet.
func NewCircleSigner(
	client *CircleClient,
	walletID string,
	entitySecretHex string,
	pubKeyHex string,
	addressHex string,
) (*CircleSigner, error) {
	pubKey := &crypto.Ed25519PublicKey{}
	if err := pubKey.FromHex(pubKeyHex); err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}

	var addr aptossdk.AccountAddress
	if err := addr.ParseStringRelaxed(addressHex); err != nil {
		return nil, fmt.Errorf("parse address: %w", err)
	}

	return &CircleSigner{
		client:       client,
		walletID:     walletID,
		entitySecret: entitySecretHex,
		pubKey:       pubKey,
		address:      addr,
	}, nil
}

func (s *CircleSigner) Sign(ctx context.Context, message []byte) (*crypto.AccountAuthenticator, error) {
	ciphertext, err := s.client.EncryptEntitySecret(ctx, s.entitySecret)
	if err != nil {
		return nil, fmt.Errorf("encrypt entity secret: %w", err)
	}

	// Strip the 32-byte SHA3 domain prefix from the signing message.
	// Circle's Aptos sign/message expects BCS bytes without the prefix.
	const prehashLen = 32
	if len(message) <= prehashLen {
		return nil, fmt.Errorf("signing message too short: %d bytes", len(message))
	}
	bcsBytes := message[prehashLen:]
	hexMessage := "0x" + hex.EncodeToString(bcsBytes)

	resp, err := s.client.SignMessage(ctx, &SignMessageRequest{
		WalletID:               s.walletID,
		Message:                hexMessage,
		EntitySecretCiphertext: ciphertext,
		EncodedByHex:           true,
	})
	if err != nil {
		return nil, fmt.Errorf("circle sign message: %w", err)
	}

	return buildEd25519Auth(s.pubKey, resp.Data.Signature)
}

// SignTransaction signs a raw transaction via Circle's sign/message endpoint using
// a fee-payer wrapper to bypass Circle's prefix restriction.
//
// Circle's sign/message rejects messages whose prefix matches sha3("APTOS::RawTransaction")
// (error 156025). By wrapping as a fee-payer RawTransactionWithData, the signing message gets
// prefix sha3("APTOS::RawTransactionWithData"), which Circle allows.
func (s *CircleSigner) SignTransaction(ctx context.Context, rawTxn *aptossdk.RawTransaction) (*aptossdk.SignedTransaction, error) {
	// 1. Wrap as fee-payer: sender = fee payer = s.address
	rawTxnWithData := &aptossdk.RawTransactionWithData{
		Variant: aptossdk.MultiAgentWithFeePayerRawTransactionWithDataVariant,
		Inner: &aptossdk.MultiAgentWithFeePayerRawTransactionWithData{
			RawTxn:           rawTxn,
			SecondarySigners: []aptossdk.AccountAddress{},
			FeePayer:         &s.address,
		},
	}

	// 2. Get the full signing message: sha3("APTOS::RawTransactionWithData") || BCS(wrapper)
	signingMsg, err := rawTxnWithData.SigningMessage()
	if err != nil {
		return nil, fmt.Errorf("signing message: %w", err)
	}

	// 3. Encrypt entity secret (fresh per request)
	ciphertext, err := s.client.EncryptEntitySecret(ctx, s.entitySecret)
	if err != nil {
		return nil, fmt.Errorf("encrypt entity secret: %w", err)
	}

	// 4. Send the full signing message to sign/message as hex.
	hexMessage := "0x" + hex.EncodeToString(signingMsg)
	resp, err := s.client.SignMessage(ctx, &SignMessageRequest{
		WalletID:               s.walletID,
		Message:                hexMessage,
		EntitySecretCiphertext: ciphertext,
		EncodedByHex:           true,
	})
	if err != nil {
		return nil, fmt.Errorf("circle sign message: %w", err)
	}

	// 5. Decode signature, build authenticator
	auth, err := buildEd25519Auth(s.pubKey, resp.Data.Signature)
	if err != nil {
		return nil, fmt.Errorf("build authenticator: %w", err)
	}

	slog.Debug("circle sign/message (fee-payer) success",
		"wallet_id", s.walletID,
		"signing_msg_len", len(signingMsg),
	)

	// 6. Build signed transaction with fee-payer authenticator.
	// Same signature for both sender and fee-payer (same key signs the same message).
	signedTxn, ok := rawTxnWithData.ToFeePayerSignedTransaction(
		auth,
		auth,
		[]crypto.AccountAuthenticator{},
	)
	if !ok {
		return nil, fmt.Errorf("failed to build fee-payer signed transaction")
	}
	return signedTxn, nil
}

// buildEd25519Auth decodes a hex-encoded Ed25519 signature and pairs it with a public key
// to produce an AccountAuthenticator.
func buildEd25519Auth(pubKey *crypto.Ed25519PublicKey, sigHex string) (*crypto.AccountAuthenticator, error) {
	sigHex = strings.TrimPrefix(sigHex, "0x")
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return nil, fmt.Errorf("decode signature hex: %w", err)
	}
	if len(sigBytes) != 64 {
		return nil, fmt.Errorf("unexpected signature length: %d (expected 64)", len(sigBytes))
	}

	sig := &crypto.Ed25519Signature{}
	copy(sig.Inner[:], sigBytes)

	return &crypto.AccountAuthenticator{
		Variant: crypto.AccountAuthenticatorEd25519,
		Auth: &crypto.Ed25519Authenticator{
			PubKey: pubKey,
			Sig:    sig,
		},
	}, nil
}

func (s *CircleSigner) PublicKey() crypto.PublicKey {
	return s.pubKey
}

func (s *CircleSigner) Address() aptossdk.AccountAddress {
	return s.address
}
