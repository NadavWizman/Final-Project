package ledger

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"
)

// Every amount in the ledger is an integer, so that every node computes exactly
// the same result. Floating point is never used for money.
//
//	Cents — money and prices, in US cents      ("190.25" → 19025)
//	Qty   — share quantities, in 1/10000 share ("2.5"    → 25000)
type (
	Cents int64
	Qty   int64
)

// QtyScale is the number of Qty units in one share.
const QtyScale = 10_000

// Upper bounds for parsed input. Arithmetic on stored values goes through the
// overflow-checked helpers below, so these only keep inputs sensible.
const (
	maxCents = 1_000_000_000_000 // $10 billion
	maxQty   = 10_000_000 * QtyScale
)

// ParseCents parses a decimal string with at most 2 decimal places, e.g. "190.25".
func ParseCents(s string) (Cents, error) {
	v, err := parseFixed(s, 2, maxCents)
	return Cents(v), err
}

// ParseQty parses a decimal share quantity with at most 4 decimal places.
func ParseQty(s string) (Qty, error) {
	v, err := parseFixed(s, 4, maxQty)
	return Qty(v), err
}

// parseFixed parses a plain positive decimal ("12", "12.5", "0.0001") into an
// integer scaled by 10^places. Signs, exponents, spaces and extra decimal
// places are refused, so one string has exactly one meaning on every node.
func parseFixed(s string, places int, max int64) (int64, error) {
	if s == "" || len(s) > 24 {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" || (hasDot && frac == "") || len(frac) > places {
		return 0, fmt.Errorf("invalid amount %q (at most %d decimal places)", s, places)
	}
	for _, part := range []string{whole, frac} {
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("invalid amount %q", s)
			}
		}
	}
	frac += strings.Repeat("0", places-len(frac))
	v, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil || v > max {
		return 0, fmt.Errorf("amount %q out of range", s)
	}
	return v, nil
}

// String renders cents as a decimal with 2 places ("190.25", "-3.05").
func (c Cents) String() string { return formatFixed(int64(c), 2) }

// String renders a quantity with 4 decimal places ("2.5000").
func (q Qty) String() string { return formatFixed(int64(q), 4) }

func formatFixed(v int64, places int) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	scale := int64(1)
	for i := 0; i < places; i++ {
		scale *= 10
	}
	return fmt.Sprintf("%s%d.%0*d", sign, v/scale, places, v%scale)
}

// ErrOverflow is returned when a computed amount does not fit in int64.
var ErrOverflow = errors.New("amount too large")

// mulDiv computes ⌊a × b / d⌋ for a, b ≥ 0 and d > 0 with a 128-bit
// intermediate, reporting overflow instead of wrapping around.
func mulDiv(a, b, d int64) (int64, error) {
	if a < 0 || b < 0 || d <= 0 {
		return 0, fmt.Errorf("mulDiv(%d, %d, %d): negative operand", a, b, d)
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi >= uint64(d) {
		return 0, ErrOverflow
	}
	q, _ := bits.Div64(hi, lo, uint64(d))
	if q > math.MaxInt64 {
		return 0, ErrOverflow
	}
	return int64(q), nil
}

// mulDivUp is mulDiv rounded up.
func mulDivUp(a, b, d int64) (int64, error) {
	q, err := mulDiv(a, b, d)
	if err != nil {
		return 0, err
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if _, rem := bits.Div64(hi, lo, uint64(d)); rem != 0 {
		q++
	}
	return q, nil
}

// cost is what buying qty at price p debits (rounded up) and proceeds what
// selling it credits (rounded down), so rounding can never create money.
func cost(q Qty, p Cents) (Cents, error) {
	v, err := mulDivUp(int64(q), int64(p), QtyScale)
	return Cents(v), err
}

func proceeds(q Qty, p Cents) (Cents, error) {
	v, err := mulDiv(int64(q), int64(p), QtyScale)
	return Cents(v), err
}

// addCents adds with an overflow check.
func addCents(a, b Cents) (Cents, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, ErrOverflow
	}
	return a + b, nil
}

// isqrt returns ⌊√n⌋ for n ≥ 0 using integer Newton iteration.
func isqrt(n int64) int64 {
	if n < 2 {
		return n
	}
	x := n
	y := (x + 1) / 2
	for y < x {
		x = y
		y = (x + n/x) / 2
	}
	return x
}
