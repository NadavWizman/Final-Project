package ledger

// Option pricing must give the same cents on every node, so it uses a simple
// closed-form model in integer arithmetic instead of market quotes (which
// differ between sources and are not signed):
//
//	premium = intrinsic value + time value
//	time value at the money ≈ 0.4 × S × σ × √T          (the Black–Scholes ATM approximation)
//	time value falls linearly to 0 as |S − K| grows to 2.5 × S × σ × √T
//
// S = verified underlying price, K = strike, σ = annual volatility (genesis
// parameter), T = time to expiry in years. The result is at least 1 cent.

const secondsPerYear = 365 * 24 * 3600

// OptionPremium returns the per-share premium of a call or put.
func OptionPremium(call bool, s, k Cents, secondsLeft, volBps int64) Cents {
	iv := intrinsic(call, s, k)
	if secondsLeft <= 0 {
		return iv
	}
	// √(T in years) × 10^6, from integer square root
	sqrtT := isqrt(secondsLeft * 1_000_000_000_000 / secondsPerYear)
	// one standard deviation of the price move, in cents: S × σ × √T
	band, err := mulDiv(int64(s), volBps*sqrtT, 10_000*1_000_000)
	if err != nil || band <= 0 {
		return max(iv, 1)
	}
	atm := band * 4 / 10
	dist := int64(s - k)
	if dist < 0 {
		dist = -dist
	}
	width := band * 25 / 10
	tv := int64(0)
	if dist < width {
		tv, _ = mulDiv(atm, width-dist, width)
	}
	return max(iv+Cents(tv), 1)
}

// intrinsic is what exercising one share's worth of the option is worth now.
func intrinsic(call bool, s, k Cents) Cents {
	v := s - k
	if !call {
		v = k - s
	}
	return max(v, 0)
}
