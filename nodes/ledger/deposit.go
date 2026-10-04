package ledger

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// Money enters the ledger only through the custodian — in a real system the
// bank or clearing firm that holds the users' money; in the demo, whoever
// holds testnet/custody.key. A deposit is not signed by the user: it is the
// custodian's statement "this account received this amount", signed with the
// Ed25519 key registered in genesis (Params.CustodyKey), exactly like price
// quotes are signed by their sources. Each deposit carries the next custody
// sequence number, so a signed deposit can be applied once only.
//
//	{"deposit":{"chain":"…","seq":"7","to":"<address>","amount":"500.00"},"sig":"<base64>"}
//
// The custodian signs DepositMsg.Message():
//
//	tradedesk-deposit|<chain>|<seq>|<to>|<amount in cents>

// DepositTx is a deposit and the custodian's signature over it.
type DepositTx struct {
	Deposit DepositMsg `json:"deposit"`
	Sig     string     `json:"sig"`
}

// DepositMsg is what the custodian signs.
type DepositMsg struct {
	Chain  string `json:"chain"`
	Seq    string `json:"seq"`
	To     string `json:"to"`
	Amount string `json:"amount"`
}

const depositMarker = `{"deposit"`

var addressRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// IsDeposit reports whether a transaction is a custodian deposit.
func IsDeposit(raw []byte) bool {
	return len(raw) >= len(depositMarker) && string(raw[:len(depositMarker)]) == depositMarker
}

// Message is the exact byte string the custodian signs. The amount is in
// cents, so "500.00" and "500.0" cannot be two signatures for one deposit.
func (m DepositMsg) Message(amount Cents) []byte {
	return []byte("tradedesk-deposit|" + m.Chain + "|" + m.Seq + "|" + m.To + "|" + strconv.FormatInt(int64(amount), 10))
}

// SignDeposit returns a deposit transaction signed by the custodian's key.
func SignDeposit(key ed25519.PrivateKey, m DepositMsg) ([]byte, error) {
	amount, err := ParseCents(m.Amount)
	if err != nil {
		return nil, err
	}
	m.Amount = amount.String() // the canonical form ("250" -> "250.00")
	tx := DepositTx{Deposit: m, Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(key, m.Message(amount)))}
	return json.Marshal(tx)
}

type checkedDeposit struct {
	seq    uint64
	to     string
	amount Cents
}

// checkDeposit verifies a deposit against the state without applying it.
func (s *State) checkDeposit(raw []byte) (*checkedDeposit, error) {
	if len(raw) > MaxTxBytes {
		return nil, errors.New("transaction too large")
	}
	var tx DepositTx
	if err := strictUnmarshal(raw, &tx); err != nil {
		return nil, fmt.Errorf("malformed deposit: %v", err)
	}
	m := tx.Deposit
	seq, err := strconv.ParseUint(m.Seq, 10, 64)
	if err != nil || strconv.FormatUint(seq, 10) != m.Seq {
		return nil, fmt.Errorf("invalid deposit sequence %q", m.Seq)
	}
	amount, err := ParseCents(m.Amount)
	switch {
	case err == nil && amount.String() != m.Amount, !addressRe.MatchString(m.To):
		// one deposit, one encoding: "500.00", not "500" or "500.0"
		return nil, errors.New("deposit amount must be written like 500.00, and the address as 40 lowercase hex digits")
	case len(s.Params.CustodyKey) != ed25519.PublicKeySize:
		return nil, errors.New("this network has no custodian: deposits are disabled")
	case m.Chain != s.Params.ChainID:
		return nil, fmt.Errorf("wrong chain %q", m.Chain)
	case err != nil || amount <= 0 || amount > s.Params.MaxDeposit:
		return nil, fmt.Errorf("deposit must be between 0.01 and %s", s.Params.MaxDeposit)
	case s.Accounts[m.To] == nil:
		return nil, errors.New("deposit to an unknown account")
	}
	sig, err := strictBase64(tx.Sig)
	if err != nil || !ed25519.Verify(s.Params.CustodyKey, m.Message(amount), sig) {
		return nil, errors.New("deposit is not signed by the custodian registered in genesis")
	}
	return &checkedDeposit{seq: seq, to: m.To, amount: amount}, nil
}

// checkDepositTx is CheckTx for deposits: the "sender" is the custodian, and
// its sequence numbers are checked like an account's nonces.
func (s *State) checkDepositTx(raw []byte, pendingNonce func(string) (uint64, bool)) (string, uint64, error) {
	d, err := s.checkDeposit(raw)
	if err != nil {
		return "", 0, err
	}
	want := s.CustodySeq
	if n, ok := pendingNonce(CustodySender); ok && n > want {
		want = n
	}
	if d.seq != want {
		return "", 0, fmt.Errorf("wrong deposit sequence: expected %d, got %d", want, d.seq)
	}
	return CustodySender, d.seq, nil
}

// CustodySender is the mempool's name for the custodian's sequence.
const CustodySender = "custody"

func (s *State) applyDeposit(raw []byte) TxResult {
	d, err := s.checkDeposit(raw)
	if err != nil {
		return TxResult{Code: CodeInvalid, Log: err.Error()}
	}
	if d.seq != s.CustodySeq {
		return TxResult{Code: CodeInvalid, Log: fmt.Sprintf("wrong deposit sequence: expected %d, got %d", s.CustodySeq, d.seq)}
	}
	acc := s.Accounts[d.to]
	cash, err := addCents(acc.Cash, d.amount)
	if err != nil {
		return TxResult{Code: CodeInvalid, Log: err.Error()}
	}
	s.CustodySeq++
	acc.Cash = cash
	acc.record(s, &Record{ID: s.nextID(), Kind: "DEPOSIT", Status: "CONFIRMED", Amount: d.amount,
		Reason: fmt.Sprintf("bank transfer via the custodian (#%d)", d.seq)})
	return TxResult{Code: CodeOK, Log: "CONFIRMED deposit"}
}
