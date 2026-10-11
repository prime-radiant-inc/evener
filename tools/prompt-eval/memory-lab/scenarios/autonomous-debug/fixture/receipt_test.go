package receipts

import "testing"

func TestTotal(t *testing.T) {
	got, err := Total([]string{"apple,0.40,3", "bread,2.5,1", "milk,1.99,2"})
	if err != nil {
		t.Fatal(err)
	}
	if want := 120 + 250 + 398; got != want {
		t.Fatalf("Total = %s, want %s", FormatCents(got), FormatCents(want))
	}
}

func TestFormatCents(t *testing.T) {
	if got := FormatCents(1205); got != "$12.05" {
		t.Fatalf("FormatCents = %q", got)
	}
}
