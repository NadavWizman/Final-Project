package ledger

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

// custody gives a ledger a custodian and returns its key.
func custody(s *State) ed25519.PrivateKey {
	pub, priv, _ := ed25519.GenerateKey(nil)
	s.Params.CustodyKey = pub
	return priv
}

func deposit(t *testing.T, key ed25519.PrivateKey, seq, to, amount string) []byte {
	t.Helper()
	raw, err := SignDeposit(key, DepositMsg{Chain: chain, Seq: seq, To: to, Amount: amount})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Money enters only through the custodian's signature, once per sequence
// number. A user cannot credit himself, and a deposit cannot be replayed.
func TestDepositsNeedTheCustodian(t *testing.T) {
	s, u, acc := setup(t)
	key := custody(s)
	_, other, _ := ed25519.GenerateKey(nil)

	res := block(s, t0, nil, deposit(t, key, "0", u.addr, "500.00"))
	if res[0].Code != CodeOK || acc.Cash != 1_000_000+50_000 || s.CustodySeq != 1 {
		t.Fatalf("custodian deposit: %+v cash %s", res[0], acc.Cash)
	}
	if r := last(acc); r.Kind != "DEPOSIT" || r.Amount != 50_000 {
		t.Fatalf("deposit record %+v", r)
	}

	cases := map[string][]byte{
		"replayed deposit":          deposit(t, key, "0", u.addr, "500.00"),
		"skipped sequence":          deposit(t, key, "5", u.addr, "500.00"),
		"signed by another key":     deposit(t, other, "1", u.addr, "500.00"),
		"unknown account":           deposit(t, key, "1", strings.Repeat("ab", 20), "500.00"),
		"zero amount":               deposit(t, key, "1", u.addr, "0"),
		"negative amount":           []byte(`{"deposit":{"chain":"` + chain + `","seq":"1","to":"` + u.addr + `","amount":"-5"},"sig":"AAAA"}`),
		"above the per-tx cap":      deposit(t, key, "1", u.addr, "1000000.01"),
		"non-canonical sequence":    deposit(t, key, "01", u.addr, "5.00"),
		"non-canonical amount":      []byte(strings.Replace(string(deposit(t, key, "1", u.addr, "500")), `"500.00"`, `"500"`, 1)),
		"upper-case address":        deposit(t, key, "1", strings.ToUpper(u.addr), "5.00"),
		"amount changed in transit": []byte(strings.Replace(string(deposit(t, key, "1", u.addr, "5.00")), `"5.00"`, `"5000.00"`, 1)),
	}
	for name, raw := range cases {
		if _, _, err := s.CheckTx(raw, noPending); err == nil {
			t.Errorf("%s passed CheckTx", name)
		}
		if res := block(s, t0, nil, raw); res[0].Code != CodeInvalid {
			t.Errorf("%s was applied: %+v", name, res[0])
		}
	}
	if acc.Cash != 1_050_000 || s.CustodySeq != 1 {
		t.Fatalf("a refused deposit changed the state: cash %s seq %d", acc.Cash, s.CustodySeq)
	}

	// a user-signed "deposit" is not a transaction type at all
	raw := u.sign(t, Msg{Type: "deposit"}, false)
	u.nonce--
	if _, _, err := s.CheckTx(raw, noPending); err == nil {
		t.Fatal("user-signed deposit accepted")
	}

	// without a custodian in genesis, deposits are disabled
	s2, u2, _ := setup(t)
	if _, _, err := s2.CheckTx(deposit(t, key, "0", u2.addr, "5.00"), noPending); err == nil {
		t.Fatal("deposit accepted on a network with no custodian")
	}
}

// The registration grant is capped in total by genesis.
func TestRegistrationGrantsAreCapped(t *testing.T) {
	s := newLedger()
	s.Params.FaucetTotal = 2_500_000 // two and a half grants
	var cash []Cents
	for i, name := range []string{"one", "two", "three", "four"} {
		u := newUser(t)
		mustOK(t, block(s, t0+int64(i), nil, u.register(t, name)))
		cash = append(cash, s.Accounts[u.addr].Cash)
	}
	if cash[0] != 1_000_000 || cash[1] != 1_000_000 || cash[2] != 500_000 || cash[3] != 0 || s.FaucetPaid != 2_500_000 {
		t.Fatalf("grants %v, paid %s", cash, s.FaucetPaid)
	}
}

// The lecturer's scenario: all the cash as margin on a 100x long and a 100x
// short of one stock, then a 5 % gap up. The short's loss ($25,000) exceeds
// its margin ($5,000) by $20,000. That shortfall is a debt, so closing the
// winning long cannot leave the user with more than he started with — in
// either order (the long closed in the gap block, before the rules run, or
// after the short has been liquidated).
func TestGapShortfallIsADebt(t *testing.T) {
	for _, closeWinnerFirst := range []bool{false, true} {
		s, u, acc := setup(t)
		s.Params.MaxLeverage = 100 // the scenario as asked (the genesis default is 20)
		start := acc.Cash          // $10,000
		mustOK(t, block(s, t0, p(10000),
			u.order(t, OrderMsg{Kind: "CFD", Side: "BUY", Ticker: "AAPL", Qty: "5000", Leverage: "100"}),
			u.order(t, OrderMsg{Kind: "CFD", Side: "SELL", Ticker: "AAPL", Qty: "5000", Leverage: "100"})))
		if acc.Cash != 0 {
			t.Fatalf("not all cash posted as margin: %s", acc.Cash)
		}
		long, short := acc.CFDs[0], acc.CFDs[1]
		closeLong := func() []byte {
			return u.order(t, OrderMsg{Kind: "CFD_CLOSE", Side: "SELL", Ticker: "AAPL", Qty: "5000", Position: itoa(long.ID)})
		}
		if closeWinnerFirst {
			mustOK(t, block(s, t0+1, p(10500), closeLong()))
		} else {
			block(s, t0+1, p(10500)) // the short is liquidated: cash −$20,000
			if acc.Cash != -2_000_000 || last(acc).Kind != "LIQUIDATION" {
				t.Fatalf("shortfall not charged: cash %s, %+v", acc.Cash, last(acc))
			}
			// with a debt nothing new can be bought or opened
			mustOK(t, block(s, t0+2, p(10500), u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "0.0001"})))
			if r := last(acc); r.Status != "REJECTED" {
				t.Fatalf("bought with a negative balance: %+v", r)
			}
			mustOK(t, block(s, t0+3, p(10500), closeLong()))
		}
		if long.Open || short.Open {
			t.Fatalf("positions still open (winner first=%v): long %v short %v", closeWinnerFirst, long.Open, short.Open)
		}
		if acc.Cash != start {
			t.Fatalf("winner first=%v: cash %s -> %s, want exactly %s (a 5 %% move is zero-sum for a hedged pair)",
				closeWinnerFirst, start, acc.Cash, start)
		}
	}
}

// A debt is repaid by the next credit: here a custodian deposit.
func TestDebtIsRepaidByTheNextCredit(t *testing.T) {
	s, u, acc := setup(t)
	key := custody(s)
	s.Params.MaxLeverage = 100
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "CFD", Side: "BUY", Ticker: "AAPL", Qty: "10000", Leverage: "100"})))
	block(s, t0+1, p(9000)) // −10 %: loss $100,000 on a $10,000 margin
	if acc.Cash != -9_000_000 {
		t.Fatalf("debt %s", acc.Cash)
	}
	mustOK(t, block(s, t0+2, nil, deposit(t, key, "0", u.addr, "100000.00")))
	if acc.Cash != 1_000_000 {
		t.Fatalf("deposit did not repay the debt first: %s", acc.Cash)
	}
}

// Every field of the state is committed to by the root: changing any one of
// them — including the custody sequence, the grants paid and the usernames —
// changes the root that goes into the block header.
func TestRootCoversEveryField(t *testing.T) {
	s, _, acc := setup(t)
	base := s.Root()
	for name, change := range map[string]func(){
		"custody sequence": func() { s.CustodySeq++ },
		"grants paid":      func() { s.FaucetPaid++ },
		"usernames":        func() { s.Usernames["mallory"] = acc.Address },
		"an account":       func() { acc.Cash++ },
		"next id":          func() { s.NextID++ },
		"a price":          func() { s.LastPrices["AAPL"]++ },
	} {
		c := s.Clone()
		s2 := s
		s = c
		acc = s.Accounts[acc.Address]
		change()
		if s.Root() == base {
			t.Errorf("changing %s does not change the root", name)
		}
		s = s2
		acc = s.Accounts[acc.Address]
	}
}

// Two accounts (a long in one, a short in the other) cannot use a gap either
// at the genesis leverage cap of 20x: liquidation fires at a 4 % move, so
// even a 5 % gap between two priced blocks leaves no loss past the margin,
// and the pair ends with no more than it started with.
func TestTwoAccountGapAtDefaultLeverage(t *testing.T) {
	s := newLedger()
	a, b := newUser(t), newUser(t)
	mustOK(t, block(s, t0, nil, a.register(t, "longside"), b.register(t, "shortside")))
	start := s.Accounts[a.addr].Cash + s.Accounts[b.addr].Cash
	mustOK(t, block(s, t0+1, p(10000), b.order(t, OrderMsg{Kind: "CFD", Side: "SELL", Ticker: "AAPL", Qty: "100", Leverage: "100"})))
	if r := last(s.Accounts[b.addr]); r.Status != "REJECTED" {
		t.Fatalf("100x accepted under the default genesis: %+v", r)
	}
	mustOK(t, block(s, t0+1, p(10000),
		a.order(t, OrderMsg{Kind: "CFD", Side: "BUY", Ticker: "AAPL", Qty: "2000", Leverage: "20"}),
		b.order(t, OrderMsg{Kind: "CFD", Side: "SELL", Ticker: "AAPL", Qty: "2000", Leverage: "20"})))
	block(s, t0+2, p(10500)) // +5 %: the short is liquidated with exactly its margin lost
	mustOK(t, block(s, t0+3, p(10500), a.order(t, OrderMsg{Kind: "CFD_CLOSE", Side: "SELL", Ticker: "AAPL", Qty: "2000",
		Position: itoa(s.Accounts[a.addr].CFDs[0].ID)})))
	end := s.Accounts[a.addr].Cash + s.Accounts[b.addr].Cash
	if s.Accounts[b.addr].Cash < 0 || end > start {
		t.Fatalf("two accounts gained from a 5%% gap: %s -> %s (short side %s)", start, end, s.Accounts[b.addr].Cash)
	}
}
