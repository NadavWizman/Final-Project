package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// Order — order as served by the Django API
type Order struct {
	ID        int    `json:"id"`
	Stock     string `json:"stock"`
	Status    string `json:"status"`
	OrderType string `json:"order_type"`
	TradeType string `json:"trade_type"`
	Quantity  string `json:"quantity"`
	Nonce     string `json:"nonce"`      // prevents replay attacks
	Signature string `json:"signature"`  // user's ECDSA signature
	PublicKey string `json:"public_key"` // PEM public key for verification
	// SignedMessage is the exact canonical message the signature covers, as
	// produced by Django. Nodes verify these bytes; they never rebuild them.
	SignedMessage string `json:"signed_message"`
	// BlockHash is the consensus block the order settled under. Django stores
	// it only after verifying a quorum of signed votes for it.
	BlockHash string `json:"block_hash"`
	// LimitPrice is empty for market orders.
	LimitPrice string    `json:"limit_price"`
	CreatedAt  time.Time `json:"created_at"`
}

// DjangoClient talks to the Django API with this node's credentials. Every
// request has a timeout, so a hung backend cannot stall a node forever.
type DjangoClient struct {
	baseURL  string
	user     string
	password string
	http     *http.Client
}

func NewDjangoClient(cfg Config) *DjangoClient {
	return &DjangoClient{
		baseURL:  cfg.DjangoURL,
		user:     cfg.NodeName,
		password: cfg.NodePass,
		http:     &http.Client{Timeout: 10 * time.Second},
	}
}

// getJSON performs an authenticated GET and decodes a 200 response into out.
func (d *DjangoClient) getJSON(path string, out any) error {
	req, err := http.NewRequest("GET", d.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(d.user, d.password)
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("django GET %s: %d %s", path, resp.StatusCode, body)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Order fetches one order as Django currently holds it.
func (d *DjangoClient) Order(id int) (*Order, error) {
	var o Order
	if err := d.getJSON(fmt.Sprintf("/orders/%d/", id), &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// SubmittedOrders fetches the orders awaiting consensus, oldest first.
func (d *DjangoClient) SubmittedOrders() ([]Order, error) {
	var orders []Order
	err := d.getJSON("/orders/?status=SUBMITTED", &orders)
	return orders, err
}

// postJSON performs an authenticated POST and returns the status and body.
func (d *DjangoClient) postJSON(path string, payload any) (int, []byte, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", d.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(d.user, d.password)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, respBody, nil
}

// RejectOrder tells Django that consensus failed, so the order stops being
// re-proposed on every cycle and shows as REJECTED to the user.
func (d *DjangoClient) RejectOrder(orderID int, reason string) bool {
	code, body, err := d.postJSON(fmt.Sprintf("/orders/%d/reject_order/", orderID),
		map[string]string{"reason": reason})
	if err != nil {
		log.Printf("[Leader] Reject request failed: %v", err)
		return false
	}
	if code != http.StatusOK {
		log.Printf("[Leader] Reject refused by Django (%d): %s", code, body)
		return false
	}
	return true
}

// ExecuteOrder asks Django to settle the order, presenting the quorum of
// signed votes as proof of consensus. It returns Django's HTTP status; err is
// set when the outcome is unknown (network failure, timeout).
func (d *DjangoClient) ExecuteOrder(orderID int, oracle *OracleData, blockHash string, votes []Vote) (int, error) {
	code, body, err := d.postJSON(fmt.Sprintf("/orders/%d/execute_order/", orderID), map[string]any{
		"execution_price": oracle.ExecutionPrice,
		"timestamp":       oracle.Timestamp,
		"block_hash":      blockHash,
		"votes":           votes,
	})
	if err != nil {
		return 0, err
	}
	fmt.Printf("[Leader] Django response (%d): %s\n", code, body)
	return code, nil
}
