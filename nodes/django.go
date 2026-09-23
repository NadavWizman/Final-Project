package main

import (
	"encoding/json"
	"fmt"
	"io"
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

// Orders fetches every order visible to this node.
func (d *DjangoClient) Orders() ([]Order, error) {
	var orders []Order
	err := d.getJSON("/orders/", &orders)
	return orders, err
}
