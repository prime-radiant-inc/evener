package pdfslice

import (
	"fmt"
	"strings"
	"testing"
)

// FuzzParsePageRange exercises ParsePageRange on arbitrary input strings and
// checks its structural invariants: blank inputs report provided=false, valid
// ranges satisfy 1 <= from <= to and to-from+1 <= MaxPDFPagesPerCall, parsing
// is deterministic, and every accepted range round-trips canonically.
func FuzzParsePageRange(f *testing.F) {
	for _, seed := range []string{
		"",
		"   \t\n ",
		"1",
		"3",
		"1-5",
		" 1-20 ",
		"1-21",
		"0",
		"-1",
		"5-3",
		"1,3,5",
		"1-",
		"-5",
		"1-2-3",
		"abc",
		"99999999999999999999999",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, spec string) {
		from, to, provided, err := ParsePageRange(spec)
		from2, to2, provided2, err2 := ParsePageRange(spec)
		if (err == nil) != (err2 == nil) || from != from2 || to != to2 || provided != provided2 {
			t.Fatalf("ParsePageRange(%q) non-deterministic: (%d,%d,%v,%v) vs (%d,%d,%v,%v)",
				spec, from, to, provided, err, from2, to2, provided2, err2)
		}
		if strings.TrimSpace(spec) == "" {
			if provided || err != nil || from != 0 || to != 0 {
				t.Fatalf("blank spec %q = (%d,%d,%v,%v), want (0,0,false,nil)", spec, from, to, provided, err)
			}
			return
		}
		if !provided {
			t.Fatalf("non-blank spec %q returned provided=false", spec)
		}
		if err != nil {
			return
		}
		if from < 1 || to < from || to-from+1 > MaxPDFPagesPerCall {
			t.Fatalf("ParsePageRange(%q) accepted invalid bounds %d-%d (cap %d)", spec, from, to, MaxPDFPagesPerCall)
		}
		canonical := fmt.Sprintf("%d-%d", from, to)
		rf, rt, rp, rerr := ParsePageRange(canonical)
		if rerr != nil || !rp || rf != from || rt != to {
			t.Fatalf("canonical round-trip %q = (%d,%d,%v,%v), want (%d,%d,true,nil)",
				canonical, rf, rt, rp, rerr, from, to)
		}
	})
}
