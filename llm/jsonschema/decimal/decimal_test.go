package decimal_test

import (
	"testing"

	"primeradiant.com/evener/llm/jsonschema/decimal"
)

func parse(t *testing.T, s string) decimal.Number {
	t.Helper()
	n, err := decimal.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDecimalExtremeIdentity(t *testing.T) {
	for _, s := range []string{"", "01", "+1", ".1", "1.", "1e", "NaN", "Infinity", "-01", "1e+", "1e-", " 1", "1 ", "1/2", "0x1"} {
		if _, err := decimal.Parse(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	for _, s := range []string{"0", "-0", "0.0", "1e+0000", "1.20e-2"} {
		parse(t, s)
	}
	for _, tc := range []struct {
		a, b  string
		equal bool
	}{
		{"10e999999999999999999999", "1e1000000000000000000000", true},
		{"0e-999999999999999999999", "-0e999999999999999999999", true},
		{"1.20e-2", "12e-3", true},
		{"1e-999999999999999999999", "0", false},
	} {
		a, b := parse(t, tc.a), parse(t, tc.b)
		if a.Equal(b) != tc.equal || (a.Cmp(b) == 0) != tc.equal {
			t.Fatal(tc)
		}
		if tc.equal && a.String() != b.String() {
			t.Fatal("noncanonical identity", tc)
		}
		if round := parse(t, a.String()); !round.Equal(a) {
			t.Fatal("round trip", tc)
		}
	}
	for _, tc := range []struct {
		a, b     string
		multiple bool
	}{
		{"3e999999999999999999999", "7e999999999999999999998", false},
		{"6e999999999999999999999", "3e999999999999999999998", true},
		{"1e-999999999999999999999", "2e-1000000000000000000000", true},
		{"2e-1000000000000000000000", "1e-999999999999999999999", false},
	} {
		a, b := parse(t, tc.a), parse(t, tc.b)
		beforeA, beforeB := a.String(), b.String()
		for range 3 {
			if a.MultipleOf(b) != tc.multiple {
				t.Fatal(tc)
			}
		}
		if a.String() != beforeA || b.String() != beforeB {
			t.Fatal("mutated operand", tc)
		}
	}
	a := parse(t, "1e-999999999999999999999")
	zero := decimal.Number{}
	if a.Cmp(zero) <= 0 || a.IsInteger() || a.Equal(zero) || a.Sign() != 1 {
		t.Fatal(a)
	}
	if !zero.IsInteger() || zero.Sign() != 0 || zero.String() != "0" {
		t.Fatal(zero)
	}
	for _, tc := range []struct {
		a, b string
		cmp  int
	}{
		{"12e999999999999999999999", "119e999999999999999999998", 1},
		{"-12e999999999999999999999", "-119e999999999999999999998", -1},
		{"1e999999999999999999999", "9e999999999999999999998", 1},
		{"-1e-999999999999999999999", "0", -1},
		{"1.0001", "1", 1}, {"1", "1.0001", -1},
	} {
		if got := parse(t, tc.a).Cmp(parse(t, tc.b)); got != tc.cmp {
			t.Fatal(tc, got)
		}
	}
}
