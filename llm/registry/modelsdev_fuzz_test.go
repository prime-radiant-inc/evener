package registry

import (
	"os"
	"reflect"
	"testing"
)

// FuzzFromModelsDev drives the converter over mutated models.dev JSON.
// Oracles: never panics; a parse error is returned as an error, not a nil
// slice with no error; conversion is deterministic; every provider id is
// unique and sorted; every model row's ID equals its map key.
func FuzzFromModelsDev(f *testing.F) {
	seed, err := os.ReadFile("testdata/models.dev.sample.json")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"x":{"id":"x","models":{"m":{"id":"m","modalities":{"output":["text"]},"interleaved":true}}}}`))
	f.Add([]byte(`{"x":{"id":"x","api":"https://${A}/v1","env":["A","X_API_KEY"],"models":{}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		a, errA := FromModelsDev(data)
		b, errB := FromModelsDev(data)
		if (errA == nil) != (errB == nil) || !reflect.DeepEqual(a, b) {
			t.Fatal("nondeterministic")
		}
		if errA != nil {
			return
		}
		seen := map[string]bool{}
		for i, p := range a {
			if seen[p.ID] {
				t.Fatalf("duplicate provider %q", p.ID)
			}
			seen[p.ID] = true
			if i > 0 && a[i-1].ID >= p.ID {
				t.Fatalf("unsorted at %d", i)
			}
			for k, m := range p.Models {
				if m.ID != k {
					t.Fatalf("row key %q != id %q", k, m.ID)
				}
			}
		}
	})
}
