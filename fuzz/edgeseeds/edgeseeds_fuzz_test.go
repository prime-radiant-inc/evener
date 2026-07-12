package edgeseeds

import (
	"bytes"
	"testing"
)

// FuzzSeedRegeneration exercises every seed family and verifies that callers
// can mutate returned seed data without changing a later regeneration.
func FuzzSeedRegeneration(f *testing.F) {
	for _, seed := range [][]byte{{0}, {1}, {2}, {3}, []byte("yaml-dos")} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input []byte) {
		if bytes.Equal(input, []byte("yaml-dos")) {
			assertYAMLDoSRegenerates(t)
			return
		}

		selector := byte(0)
		if len(input) > 0 {
			selector = input[0]
		}
		index := byte(0)
		if len(input) > 1 {
			index = input[1]
		}
		switch selector % 4 {
		case 0:
			assertByteSeedsRegenerate(t, JSON, index)
		case 1:
			assertByteSeedsRegenerate(t, TOML, index)
		case 2:
			assertStringSeedsRegenerate(t, FrontmatterYAML, index)
		case 3:
			want := TOMLFeatureDoc()
			got := TOMLFeatureDoc()
			mutateBytes(want)
			if bytes.Equal(want, got) {
				t.Fatal("TOML feature document mutation had no effect")
			}
		}
	})
}

func assertByteSeedsRegenerate(t *testing.T, generate func() [][]byte, index byte) {
	t.Helper()
	want := generate()
	got := generate()
	if len(want) == 0 || len(got) != len(want) {
		t.Fatalf("invalid regenerated corpus lengths: %d and %d", len(want), len(got))
	}
	i := int(index) % len(want)
	mutateBytes(want[i])
	if bytes.Equal(want[i], got[i]) {
		t.Fatal("seed mutation had no effect")
	}
}

func assertStringSeedsRegenerate(t *testing.T, generate func() []string, index byte) {
	t.Helper()
	want := generate()
	got := generate()
	if len(want) == 0 || len(got) != len(want) {
		t.Fatalf("invalid regenerated corpus lengths: %d and %d", len(want), len(got))
	}
	i := int(index) % len(want)
	want[i] += "mutation"
	if want[i] == got[i] {
		t.Fatal("seed mutation had no effect")
	}
}

func assertYAMLDoSRegenerates(t *testing.T) {
	t.Helper()
	want := YAMLDoS()
	got := YAMLDoS()
	if len(want) == 0 || len(got) != len(want) {
		t.Fatalf("invalid regenerated corpus lengths: %d and %d", len(want), len(got))
	}
	for i := range want {
		if want[i].Name == "" || len(want[i].YAML) == 0 || want[i].ErrSubstr == "" {
			t.Fatalf("incomplete YAML DoS case at index %d", i)
		}
	}
	mutateBytes(want[0].YAML)
	if bytes.Equal(want[0].YAML, got[0].YAML) {
		t.Fatal("YAML DoS seed mutation had no effect")
	}
}

func mutateBytes(b []byte) {
	if len(b) > 0 {
		b[0] ^= 0xff
	}
}
