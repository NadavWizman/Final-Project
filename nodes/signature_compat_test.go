package main

import "testing"

// Fixture produced by backend/trading/crypto_utils.py (sign_order over the
// canonical message). Guards the Python ⇄ Go signature contract.
const (
	pyPublicKey = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE0OfTDbmnjnCSWvRCIaOnPm+6DTfQ
zQDQVpIOfcSJ9XFzMpJ/tD21G7YKyyZHmSpROxp7Cq2TUTJH5jan9w+RBg==
-----END PUBLIC KEY-----`
	pySignedMessage = `{"leverage":"","limit_price":"410.1000","nonce":"go-compat-1","option_contract_type":"","option_expiry":"","option_strike":"","order_type":"BUY","position_id":"","quantity":"2.5000","stock":"BRK.B","trade_type":"STOCK"}`
	pySignature     = "MEQCIGDFukZl7OFVeOn6IYkf47t3B1BOUQ2k2LhLiawtWRheAiACAIX+F9zpYz/DHUomvjA3G0IHVhg8zD6N3GpQBLqQPw=="
)

func TestVerifiesSignatureProducedByDjango(t *testing.T) {
	if err := verifyECDSA(pyPublicKey, pySignedMessage, pySignature); err != nil {
		t.Fatalf("Django-produced signature rejected: %v", err)
	}
}

func TestRejectsAlteredSignedMessage(t *testing.T) {
	altered := pySignedMessage[:len(pySignedMessage)-2] + "x}"
	if err := verifyECDSA(pyPublicKey, altered, pySignature); err == nil {
		t.Fatal("signature accepted over an altered message")
	}
}
