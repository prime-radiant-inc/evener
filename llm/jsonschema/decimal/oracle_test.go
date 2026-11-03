package decimal_test

import (
	"fmt"
	"math/big"
	"math/rand"
	"testing"

	"primeradiant.com/evener/llm/jsonschema/decimal"
)

type oracleValue struct {
	text   string
	rat    *big.Rat
	number decimal.Number
}

// The reference starts with an integer mantissa and bounded integer exponent.
// It never parses Number.String or uses decimal.Parse to construct its Rat.
func reference(m, e int) *big.Rat {
	p := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(e))), nil)
	r := new(big.Rat).SetInt64(int64(m))
	if e < 0 {
		return r.Quo(r, new(big.Rat).SetInt(p))
	}
	return r.Mul(r, new(big.Rat).SetInt(p))
}
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func TestDecimalSmallOracle(t *testing.T) {
	var values []oracleValue
	for m := -125; m <= 125; m++ {
		for e := -6; e <= 6; e++ {
			text := fmt.Sprintf("%de%d", m, e)
			values = append(values, oracleValue{text, reference(m, e), parse(t, text)})
		}
	}
	check := func(a, b oracleValue) {
		t.Helper()
		if got, want := a.number.Cmp(b.number), a.rat.Cmp(b.rat); got != want {
			t.Fatalf("compare %s %s got %d want %d", a.text, b.text, got, want)
		}
		if a.number.Equal(b.number) != (a.rat.Cmp(b.rat) == 0) {
			t.Fatalf("equal %s %s", a.text, b.text)
		}
		want := false
		if b.rat.Sign() > 0 {
			want = new(big.Rat).Quo(a.rat, b.rat).IsInt()
		}
		if got := a.number.MultipleOf(b.number); got != want {
			t.Fatalf("multiple %s %s got %v want %v", a.text, b.text, got, want)
		}
	}
	var boundaries []oracleValue
	for _, m := range []int{-125, -25, -14, -7, -5, -2, -1, 0, 1, 2, 3, 5, 7, 10, 14, 25, 125} {
		for _, e := range []int{-6, 0, 6} {
			text := fmt.Sprintf("%de%d", m, e)
			boundaries = append(boundaries, oracleValue{text, reference(m, e), parse(t, text)})
		}
	}
	zero := oracleValue{"0", new(big.Rat), decimal.Number{}}
	for _, a := range values {
		if a.number.Sign() != a.rat.Sign() || a.number.IsInteger() != a.rat.IsInt() {
			t.Fatal("classification", a.text)
		}
		if !parse(t, a.number.String()).Equal(a.number) {
			t.Fatal("roundtrip", a.text)
		}
		check(zero, a) // includes every positive divisor and all nonpositive divisors
		for _, b := range boundaries {
			check(a, b)
			check(b, a)
		}
	}
	random := rand.New(rand.NewSource(20261103))
	for range 20000 {
		check(values[random.Intn(len(values))], values[random.Intn(len(values))])
	}
}
