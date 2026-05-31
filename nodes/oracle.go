package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type OracleData struct {
	Ticker         string `json:"ticker"`
	ExecutionPrice string `json:"execution_price"`
	Timestamp      string `json:"timestamp"`
	Error          string `json:"error"`
}

func FetchPrice(oracleURL, ticker string) (*OracleData, error) {
	resp, err := http.Get(fmt.Sprintf("%s/price/%s", oracleURL, ticker))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data OracleData
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if data.Error != "" {
		return nil, fmt.Errorf("oracle error: %s", data.Error)
	}
	return &data, nil
}