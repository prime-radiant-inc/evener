package repair

import (
	"math"
	"testing"
)

// matchingBranchCount is the exactly-one check an Example must pass: the
// object an Example describes must satisfy one branch, not zero or several.
func TestMatchingBranchCount(t *testing.T) {
	branches := []any{
		map[string]any{"required": []any{"a"}},
		map[string]any{"required": []any{"a", "b"}},
	}
	if got := matchingBranchCount(branches, map[string]bool{"a": true}); got != 1 {
		t.Fatalf("matchingBranchCount({a}) = %d, want 1", got)
	}
	if got := matchingBranchCount(branches, map[string]bool{"a": true, "b": true}); got != 2 {
		t.Fatalf("matchingBranchCount({a,b}) = %d, want 2 (over-match)", got)
	}
	// A boolean true branch matches any instance; false matches none.
	withBool := []any{
		map[string]any{"required": []any{"a"}},
		true,
		false,
	}
	if got := matchingBranchCount(withBool, map[string]bool{"a": true}); got != 2 {
		t.Fatalf("matchingBranchCount with a boolean true branch = %d, want 2", got)
	}
}

// rationalMultiple decides exact decimal divisibility: a near-miss is not a
// multiple, and a value with no finite common decimal form is undecided.
func TestRationalMultiple(t *testing.T) {
	for _, tc := range []struct {
		name        string
		v, m        float64
		want        bool
		wantDecided bool
	}{
		{name: "exact", v: 7, m: 0.7, want: true, wantDecided: true},
		{name: "fractional exact", v: 1.4, m: 0.7, want: true, wantDecided: true},
		{name: "near miss", v: 1.0000000005, m: 1, want: false, wantDecided: true},
		{name: "large fractional", v: 1000000000000.5, m: 1, want: false, wantDecided: true},
		{name: "huge integral", v: 1e20, m: 1e19, want: true, wantDecided: true},
		{name: "huge non-multiple", v: 3e19, m: 7, want: false, wantDecided: true},
		{name: "not a multiple", v: 1, m: 0.7, want: false, wantDecided: true},
		{name: "tiny multiple", v: 1e-9, m: 1e-9, want: true, wantDecided: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, decided := rationalMultiple(tc.v, tc.m)
			if decided != tc.wantDecided {
				t.Fatalf("rationalMultiple(%v, %v) decided = %v, want %v", tc.v, tc.m, decided, tc.wantDecided)
			}
			if decided && got != tc.want {
				t.Fatalf("rationalMultiple(%v, %v) = %v, want %v", tc.v, tc.m, got, tc.want)
			}
		})
	}
}

// Seventeenth review, Medium 2: propertyNames constrains the property NAME, so
// a combinator nested under it must not resolve a value-scoped holder.
func TestEnclosingOneOfDeclinesPropertyNamesPath(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":          "object",
				"propertyNames": map[string]any{"oneOf": []any{map[string]any{"required": []any{"x"}}}},
			},
		},
	}
	if _, _, _, _, _, ok := enclosingOneOf(params, "properties/opts/propertyNames/oneOf/0/required", "opts/k"); ok {
		t.Fatalf("propertyNames path must not resolve a value-scoped holder")
	}
}

// snapMultiple's contract, stated independently of how the rounding is
// computed: with upward=false it returns the greatest multiple of m that is
// <= candidate, and with upward=true the least multiple of m that is >=
// candidate; ok is false when the decimal forms cannot be formed. The negative
// cases matter because an implementation that truncated toward zero would
// return -1 for -1.5 where the contract requires the floor, -2.
func TestSnapMultipleSemantics(t *testing.T) {
	for _, tc := range []struct {
		name      string
		candidate float64
		m         float64
		upward    bool
		want      float64
	}{
		{name: "negative floors", candidate: -1.5, m: 1, upward: false, want: -2},
		{name: "negative half floors", candidate: -0.5, m: 1, upward: false, want: -1},
		{name: "negative two and a half floors", candidate: -2.5, m: 1, upward: false, want: -3},
		{name: "positive snaps down", candidate: 1.5, m: 1, upward: false, want: 1},
		{name: "on grid is unchanged below", candidate: 2, m: 1, upward: false, want: 2},
		{name: "negative ceils", candidate: -1.5, m: 1, upward: true, want: -1},
		{name: "positive ceils", candidate: 1.5, m: 1, upward: true, want: 2},
		{name: "on grid is unchanged above", candidate: 2, m: 1, upward: true, want: 2},
		{name: "fractional multiple floors", candidate: 1.0, m: 0.7, upward: false, want: 0.7},
		{name: "fractional multiple ceils", candidate: 1.0, m: 0.7, upward: true, want: 1.4},
		{name: "exact fractional multiple", candidate: 1.4, m: 0.7, upward: false, want: 1.4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := snapMultiple(tc.candidate, tc.m, tc.upward)
			if !ok {
				t.Fatalf("snapMultiple(%v, %v, upward=%v) ok = false, want true", tc.candidate, tc.m, tc.upward)
			}
			if math.Abs(got-tc.want) > 1e-12 {
				t.Fatalf("snapMultiple(%v, %v, upward=%v) = %v, want %v", tc.candidate, tc.m, tc.upward, got, tc.want)
			}
			// The contract also fixes the direction of the snap, independent
			// of the chosen multiple.
			if !tc.upward && got > tc.candidate+1e-12 {
				t.Fatalf("downward snap %v is above candidate %v", got, tc.candidate)
			}
			if tc.upward && got < tc.candidate-1e-12 {
				t.Fatalf("upward snap %v is below candidate %v", got, tc.candidate)
			}
		})
	}
}
