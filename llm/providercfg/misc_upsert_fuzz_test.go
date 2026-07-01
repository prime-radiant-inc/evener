package providercfg

import (
	"reflect"
	"sort"
	"testing"
)

// miscBuildConfig assembles a Config whose instance names are drawn from the
// fuzzed name list (duplicates included, so the replace-vs-append branches of
// Upsert both get exercised).
func miscBuildConfig(def string, names []string) Config {
	insts := make([]InstanceConfig, 0, len(names))
	for i, n := range names {
		insts = append(insts, InstanceConfig{Name: n, Type: Type("t"), BaseURL: "u", Quirks: names[i%len(names)]})
	}
	return Config{Default: def, Instances: insts}
}

// FuzzMiscProviderCfgUpsert drives Config.Upsert — the pure insert-or-replace
// that returns a new name-sorted Config without mutating the receiver — over
// fuzzed existing-instance sets and a fuzzed instance to upsert.
//
// Oracles:
//   - never panics (floor);
//   - receiver immutability: the input Config's Instances slice is unchanged
//     (contents) after Upsert;
//   - sorted invariant: the returned Instances are sorted by Name;
//   - presence + replacement: exactly one instance named inst.Name exists in the
//     result and it equals inst (Upsert replaces, never duplicates);
//   - count invariant: the result has the same length as the input when the name
//     already existed, else exactly one more;
//   - idempotence (metamorphic): upserting the same instance twice yields a
//     Config deeply equal to upserting it once — a strong round-trip property;
//   - determinism: two Upsert calls agree.
func FuzzMiscProviderCfgUpsert(f *testing.F) {
	f.Add("def", "a,b,c", "b", "kimi")
	f.Add("", "z,z,a", "z", "")
	f.Add("d", "", "new", "glm")
	f.Add("x", "m", "n", "openrouter")

	f.Fuzz(func(t *testing.T, def, csv, newName, newQuirks string) {
		names := splitMiscCSV(csv)
		if len(names) == 0 {
			names = []string{""}
		}
		cfg := miscBuildConfig(def, names)

		// Snapshot the receiver's instances to prove immutability.
		before := make([]InstanceConfig, len(cfg.Instances))
		copy(before, cfg.Instances)

		// A fuzzed Config may carry duplicate instance names; Upsert replaces every
		// match 1:1, so the expected post-count is the pre-count of matches (or 1
		// when the name was absent and a single instance is appended).
		preMatches := 0
		for _, in := range cfg.Instances {
			if in.Name == newName {
				preMatches++
			}
		}
		existed := preMatches > 0

		inst := InstanceConfig{Name: newName, Type: Type("upserted"), BaseURL: "b", Quirks: newQuirks}
		out := cfg.Upsert(inst)

		// Receiver immutability.
		if !reflect.DeepEqual(cfg.Instances, before) {
			t.Fatalf("Upsert mutated the receiver:\n before=%#v\n after =%#v", before, cfg.Instances)
		}
		// Default is carried through.
		if out.Default != cfg.Default {
			t.Fatalf("Upsert changed Default: %q -> %q", cfg.Default, out.Default)
		}

		// Sorted-by-name invariant.
		if !sort.SliceIsSorted(out.Instances, func(i, j int) bool { return out.Instances[i].Name < out.Instances[j].Name }) {
			t.Fatalf("Upsert result not sorted by Name: %#v", out.Instances)
		}

		// Exactly-one-and-equals presence.
		matches := 0
		for _, in := range out.Instances {
			if in.Name == newName {
				matches++
				if !reflect.DeepEqual(in, inst) {
					t.Fatalf("Upsert did not replace instance %q with the new value:\n got=%#v\n want=%#v", newName, in, inst)
				}
			}
		}
		wantMatches := 1
		if existed {
			wantMatches = preMatches
		}
		if matches != wantMatches {
			t.Fatalf("Upsert produced %d instances named %q, want %d", matches, newName, wantMatches)
		}

		// Count invariant.
		wantLen := len(cfg.Instances) + 1
		if existed {
			wantLen = len(cfg.Instances)
		}
		if len(out.Instances) != wantLen {
			t.Fatalf("Upsert length = %d, want %d (existed=%v)", len(out.Instances), wantLen, existed)
		}

		// Idempotence: a second Upsert of the same inst changes nothing.
		twice := out.Upsert(inst)
		if !reflect.DeepEqual(out, twice) {
			t.Fatalf("Upsert not idempotent:\n once =%#v\n twice=%#v", out, twice)
		}

		// Determinism.
		outB := cfg.Upsert(inst)
		if !reflect.DeepEqual(out, outB) {
			t.Fatalf("Upsert nondeterministic:\n a=%#v\n b=%#v", out, outB)
		}
	})
}

// splitMiscCSV splits a comma-separated list into fields without collapsing
// empties, so duplicate and empty instance names are preserved for the fuzzer.
func splitMiscCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
