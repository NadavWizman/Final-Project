package quotes

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pb "nodes/oracle"
)

// Fetcher collects signed quotes from the oracle signers. Only the block
// proposer calls it; validators never go to the network to check a block.
type Fetcher interface {
	Fetch(ctx context.Context, tickers []string) []Quote
}

// GRPCFetcher asks every oracle signer for every ticker, in parallel.
type GRPCFetcher struct {
	Addrs   []string // oracle signer addresses
	Timeout time.Duration
	// MinQuotes is the quorum: once every ticker has this many quotes, the
	// fetch waits only Grace more for the slower sources, instead of
	// letting the slowest source set the block time. 0 waits for all.
	MinQuotes int
	Grace     time.Duration

	once  sync.Once
	conns []*grpc.ClientConn
}

func (f *GRPCFetcher) dial() {
	for _, a := range f.Addrs {
		c, err := grpc.NewClient(a, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err == nil {
			f.conns = append(f.conns, c)
		}
	}
}

// Fetch returns the quotes that arrive in time: all of them, or — once every
// ticker has MinQuotes — those that arrive within the grace period after
// that. A source that fails or is slow contributes nothing this block.
func (f *GRPCFetcher) Fetch(ctx context.Context, tickers []string) []Quote {
	f.once.Do(f.dial)
	timeout, grace := f.Timeout, f.Grace
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	if grace == 0 {
		grace = 150 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	total := len(f.conns) * len(tickers)
	results := make(chan *Quote, total) // buffered: late answers never block
	for _, conn := range f.conns {
		client := pb.NewOracleServiceClient(conn)
		for _, t := range tickers {
			go func(t string) {
				r, err := client.SignQuote(ctx, &pb.PriceRequest{Ticker: t})
				if err != nil {
					results <- nil
					return
				}
				results <- &Quote{Source: r.Source, Ticker: r.Ticker, Price: r.PriceCents, Time: r.Timestamp, Sig: r.Signature}
			}(t)
		}
	}

	var out []Quote
	perTicker := map[string]int{}
	var graceC <-chan time.Time
	for answered := 0; answered < total; answered++ {
		select {
		case q := <-results:
			if q == nil {
				continue
			}
			out = append(out, *q)
			perTicker[q.Ticker]++
			if graceC == nil && f.MinQuotes > 0 && enough(perTicker, tickers, f.MinQuotes) {
				graceC = time.After(grace)
			}
		case <-graceC:
			return out
		case <-ctx.Done():
			return out
		}
	}
	return out
}

func enough(perTicker map[string]int, tickers []string, min int) bool {
	for _, t := range tickers {
		if perTicker[t] < min {
			return false
		}
	}
	return true
}
