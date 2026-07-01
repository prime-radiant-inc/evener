package envvars

import "testing"

// TestAll returns a copy of the full registry; mutating it must not affect the
// package's backing slice.
func TestAll(t *testing.T) {
	all := All()
	if len(all) != len(allVars) {
		t.Fatalf("All() len = %d, want %d", len(all), len(allVars))
	}
	if len(all) == 0 {
		t.Fatal("All() returned no vars")
	}
	all[0] = Var{Name: "MUTATED"}
	if allVars[0].Name == "MUTATED" {
		t.Error("All() aliased the backing slice; mutation leaked")
	}
}

// TestByVisibility filters the registry and returns only matching vars; an
// unused visibility yields nothing.
func TestByVisibility(t *testing.T) {
	for _, vis := range []Visibility{Public, Internal, Inherited, Tooling} {
		got := ByVisibility(vis)
		if len(got) == 0 {
			t.Errorf("ByVisibility(%q) returned nothing", vis)
		}
		for _, v := range got {
			if v.Visibility != vis {
				t.Errorf("ByVisibility(%q) included %q with visibility %q", vis, v.Name, v.Visibility)
			}
		}
	}

	// Every registered var must appear under exactly one of the four buckets.
	var total int
	for _, vis := range []Visibility{Public, Internal, Inherited, Tooling} {
		total += len(ByVisibility(vis))
	}
	if total != len(allVars) {
		t.Errorf("visibility buckets sum to %d, want %d", total, len(allVars))
	}

	if got := ByVisibility("nonexistent"); got != nil {
		t.Errorf("ByVisibility(unknown) = %v, want nil", got)
	}
}

func TestFind(t *testing.T) {
	v, ok := Find("SERF_MODEL")
	if !ok {
		t.Fatal("Find(SERF_MODEL) not found")
	}
	if v.Name != "SERF_MODEL" || v != SERFModel {
		t.Errorf("Find(SERF_MODEL) = %+v, want %+v", v, SERFModel)
	}

	if got, ok := Find("DOES_NOT_EXIST"); ok || got != (Var{}) {
		t.Errorf("Find(missing) = %+v, %v; want zero, false", got, ok)
	}
}
