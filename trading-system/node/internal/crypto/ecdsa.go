// Package crypto wraps Go's stdlib ECDSA (P-256) in the exact shape the
// trading system expects: DER-encoded public keys, ASN.1 signatures, and
// SHA-256 message digests.
//
// Keeping this in one place means the Django signer and the Go verifier
// agree on every byte. If you change anything here you MUST mirror it in
// django_app/orders/signer.py.
package crypto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// LoadPrivateKeyPEM reads a PEM-encoded PKCS8 ECDSA P-256 private key.
func LoadPrivateKeyPEM(path string) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM block found in key file")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse pkcs8: %w", err)
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("key is not an ECDSA key")
	}
	if ec.Curve != elliptic.P256() {
		return nil, errors.New("only P-256 is supported")
	}
	return ec, nil
}

// MarshalPublicKeyDER returns the SubjectPublicKeyInfo DER encoding.
// This is what we transmit inside SignedOrderTx.pub_key.
func MarshalPublicKeyDER(pub *ecdsa.PublicKey) ([]byte, error) {
	return x509.MarshalPKIXPublicKey(pub)
}

// ParsePublicKeyDER is the inverse.
func ParsePublicKeyDER(der []byte) (*ecdsa.PublicKey, error) {
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse pkix: %w", err)
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("key is not ECDSA")
	}
	if ec.Curve != elliptic.P256() {
		return nil, errors.New("only P-256 is supported")
	}
	return ec, nil
}

// Sign returns an ASN.1 DER-encoded ECDSA signature over sha256(msg).
func Sign(priv *ecdsa.PrivateKey, msg []byte) ([]byte, error) {
	digest := sha256.Sum256(msg)
	return ecdsa.SignASN1(rand.Reader, priv, digest[:])
}

// Verify checks an ASN.1 DER signature against sha256(msg).
func Verify(pub *ecdsa.PublicKey, msg, sig []byte) bool {
	digest := sha256.Sum256(msg)
	return ecdsa.VerifyASN1(pub, digest[:], sig)
}
