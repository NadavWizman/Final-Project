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

// Fetch returns whatever quotes arrive within the timeout; a source that
// fails or is slow simply contributes nothing this block.
func (f *GRPCFetcher) Fetch(ctx context.Context, tickers []string) []Quote {
	f.once.Do(f.dial)
	timeout := f.Timeout
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var mu sync.Mutex
	var out []Quote
	var wg sync.WaitGroup
	for _, conn := range f.conns {
		client := pb.NewOracleServiceClient(conn)
		for _, t := range tickers {
			wg.Add(1)
			go func(t string) {
				defer wg.Done()
				r, err := client.SignQuote(ctx, &pb.PriceRequest{Ticker: t})
				if err != nil {
					return
				}
				mu.Lock()
				out = append(out, Quote{Source: r.Source, Ticker: r.Ticker, Price: r.PriceCents, Time: r.Timestamp, Sig: r.Signature})
				mu.Unlock()
			}(t)
		}
	}
	wg.Wait()
	return out
}
