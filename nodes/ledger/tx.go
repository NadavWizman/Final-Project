package ledger

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// A transaction is signed in the user's browser (WebCrypto, ECDSA P-256 with
// SHA-256) and travels as an Envelope. The node verifies the signature over
// the exact Msg bytes it received — nothing is re-serialised, so the browser
// and Go never have to agree on a JSON encoding.
type Envelope struct {
	Msg    string `json:"msg"`              // the signed JSON message, verbatim
	Sig    string `json:"sig"`              // base64 of r‖s (64 bytes, IEEE P1363 — WebCrypto's format)
	PubKey string `json:"pubkey,omitempty"` // base64 of the 65-byte public key; only on "register"
}

// Msg is the content the user signs.
type Msg struct {
	Type     string    `json:"type"`  // register, deposit, order, level_add, level_cancel
	Chain    string    `json:"chain"` // must equal the chain id (no replay on another chain)
	From     string    `json:"from"`  // the sender's address
	Nonce    string    `json:"nonce"` // the account's next transaction number, as a decimal string
	Username string    `json:"username,omitempty"`
	Amount   string    `json:"amount,omitempty"`
	Order    *OrderMsg `json:"order,omitempty"`
	Level    *LevelMsg `json:"level,omitempty"`
	LevelID  string    `json:"level_id,omitempty"`
}

// OrderMsg describes a trade. Amounts are decimal strings, parsed exactly.
type OrderMsg struct {
	Kind       string `json:"kind"` // STOCK, CFD, CFD_CLOSE, OPTION, OPT_CLOSE, OPT_EXER
	Side       string `json:"side"` // BUY or SELL
	Ticker     string `json:"ticker"`
	Qty        string `json:"qty"`
	Limit      string `json:"limit,omitempty"`
	Leverage   string `json:"leverage,omitempty"`
	Position   string `json:"position,omitempty"` // CFD or option id, for close/exercise
	OptionType string `json:"option_type,omitempty"`
	Strike     string `json:"strike,omitempty"`
	Expiry     string `json:"expiry,omitempty"`
	SL         string `json:"sl,omitempty"`
	SLQty      string `json:"sl_qty,omitempty"`
	TP         string `json:"tp,omitempty"`
	TPQty      string `json:"tp_qty,omitempty"`
}

// LevelMsg adds a stop-loss or take-profit to a holding or a CFD.
type LevelMsg struct {
	Kind   string `json:"kind"`             // SL or TP
	Ticker string `json:"ticker,omitempty"` // for a stock holding
	CFD    string `json:"cfd,omitempty"`    // for a CFD position (its id)
	Price  string `json:"price"`
	Qty    string `json:"qty"`
}

// MaxTxBytes bounds a transaction so a block cannot be flooded.
const MaxTxBytes = 4096

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)

// Address derives an account address from its public key: the first 20
// bytes of SHA-256(pubkey), in hex.
func Address(pubKey []byte) string {
	h := sha256.Sum256(pubKey)
	return hex.EncodeToString(h[:20])
}

// decoded is a parsed, signature-checked transaction.
type decoded struct {
	msg    Msg
	nonce  uint64
	pubKey []byte
	hash   [32]byte
}

// decode parses an envelope, checks its signature and returns the message.
// It does not look at nonces or balances; s is used only to find the
// sender's registered public key.
func (s *State) decode(raw []byte) (*decoded, error) {
	if len(raw) > MaxTxBytes {
		return nil, errors.New("transaction too large")
	}
	var env Envelope
	if err := strictUnmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("malformed envelope: %v", err)
	}
	var m Msg
	if err := strictUnmarshal([]byte(env.Msg), &m); err != nil {
		return nil, fmt.Errorf("malformed message: %v", err)
	}
	if m.Chain != s.Params.ChainID {
		return nil, fmt.Errorf("wrong chain %q", m.Chain)
	}
	nonce, err := strconv.ParseUint(m.Nonce, 10, 64)
	if err != nil || strconv.FormatUint(nonce, 10) != m.Nonce {
		return nil, fmt.Errorf("invalid nonce %q", m.Nonce)
	}
	if err := checkShape(&m); err != nil {
		return nil, err
	}

	var pub []byte
	if m.Type == "register" {
		if pub, err = strictBase64(env.PubKey); err != nil {
			return nil, errors.New("invalid public key encoding")
		}
		if Address(pub) != m.From {
			return nil, errors.New("address does not match the public key")
		}
	} else {
		acc := s.Accounts[m.From]
		if acc == nil {
			return nil, errors.New("unknown account")
		}
		pub = acc.PubKey
	}

	sig, err := strictBase64(env.Sig)
	if err != nil {
		return nil, errors.New("invalid signature encoding")
	}
	if err := VerifyP1363(pub, []byte(env.Msg), sig); err != nil {
		return nil, err
	}
	return &decoded{msg: m, nonce: nonce, pubKey: pub, hash: sha256.Sum256(raw)}, nil
}

// VerifyP1363 checks an ECDSA P-256 / SHA-256 signature in r‖s form over msg.
func VerifyP1363(pubKey, msg, sig []byte) error {
	pk, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pubKey)
	if err != nil {
		return errors.New("invalid public key")
	}
	if len(sig) != 64 {
		return errors.New("signature must be 64 bytes (r‖s)")
	}
	r := new(big.Int).SetBytes(sig[:32])
	sv := new(big.Int).SetBytes(sig[32:])
	// (r, s) and (r, n−s) are both valid; only the low-S form is accepted, so
	// a signed transaction has exactly one encoding (and one hash).
	if sv.Cmp(halfOrder) > 0 {
		return errors.New("signature is not in canonical low-S form")
	}
	digest := sha256.Sum256(msg)
	if !ecdsa.Verify(pk, digest[:], r, sv) {
		return errors.New("signature verification failed")
	}
	return nil
}

var halfOrder = new(big.Int).Rsh(elliptic.P256().Params().N, 1)

// SignP1363 signs msg with ECDSA P-256 / SHA-256 and returns r‖s in the
// canonical low-S form the chain accepts. (WebCrypto in the browser signs the
// same way and normalises s likewise.)
func SignP1363(key *ecdsa.PrivateKey, msg []byte) []byte {
	digest := sha256.Sum256(msg)
	r, sv, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		panic(err)
	}
	if sv.Cmp(halfOrder) > 0 {
		sv.Sub(key.Params().N, sv)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	sv.FillBytes(sig[32:])
	return sig
}

// strictBase64 accepts only canonical standard base64 (no line breaks, no
// non-zero padding bits), so a value has a single encoding.
func strictBase64(s string) ([]byte, error) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, errors.New("line break in base64")
	}
	return base64.StdEncoding.Strict().DecodeString(s)
}

var (
	tickerRe = regexp.MustCompile(`^[A-Z][A-Z.]{0,9}$`)
	idRe     = regexp.MustCompile(`^[0-9]{1,19}$`)
	keyRe    = regexp.MustCompile(`^[a-z_]+$`)

	msgTypes   = map[string]bool{"register": true, "deposit": true, "order": true, "level_add": true, "level_cancel": true}
	orderKinds = map[string]bool{"STOCK": true, "CFD": true, "CFD_CLOSE": true, "OPTION": true, "OPT_CLOSE": true, "OPT_EXER": true}
)

// checkShape refuses a message whose enumerated fields are outside their
// sets. Such a message is malformed: it never reaches the mempool, and its
// text is never stored in anyone's history.
func checkShape(m *Msg) error {
	if !msgTypes[m.Type] {
		return fmt.Errorf("unknown transaction type")
	}
	if o := m.Order; o != nil {
		switch {
		case !orderKinds[o.Kind]:
			return errors.New("unknown order kind")
		case o.Side != "BUY" && o.Side != "SELL":
			return errors.New("side must be BUY or SELL")
		case !tickerRe.MatchString(o.Ticker):
			return errors.New("malformed ticker")
		case o.OptionType != "" && o.OptionType != "CALL" && o.OptionType != "PUT":
			return errors.New("option type must be CALL or PUT")
		case o.Position != "" && !idRe.MatchString(o.Position):
			return errors.New("malformed position id")
		}
	}
	if l := m.Level; l != nil {
		if l.Kind != "SL" && l.Kind != "TP" {
			return errors.New("level kind must be SL or TP")
		}
		if l.Ticker != "" && !tickerRe.MatchString(l.Ticker) {
			return errors.New("malformed ticker")
		}
		if l.CFD != "" && !idRe.MatchString(l.CFD) {
			return errors.New("malformed position id")
		}
	}
	if m.LevelID != "" && !idRe.MatchString(m.LevelID) {
		return errors.New("malformed level id")
	}
	return nil
}

// strictUnmarshal decodes JSON refusing unknown fields, trailing data,
// duplicate keys and keys that differ from the field names only in case
// (Go's decoder would accept "TYPE" for "type", and the last of two "type"
// keys) — so the bytes the user signed have exactly one meaning.
func strictUnmarshal(data []byte, v any) error {
	if err := checkKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

// checkKeys walks one JSON value and rejects duplicate or non-lower-case keys.
func checkKeys(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := k.(string)
			if !keyRe.MatchString(key) || seen[key] {
				return fmt.Errorf("invalid or duplicate key %q", key)
			}
			seen[key] = true
			if err := checkKeys(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token() // '}'
		return err
	case json.Delim('['):
		for dec.More() {
			if err := checkKeys(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token() // ']'
		return err
	}
	return nil
}
