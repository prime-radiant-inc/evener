package appwire

import "testing"

func TestThreadNameSetInCatalog(t *testing.T) {
	found := false
	for _, m := range Methods {
		if m.Name == MethodEvenerThreadNameSet {
			found = true
			if m.Scope != ScopeBoth {
				t.Fatalf("rename must be ScopeBoth, got %v", m.Scope)
			}
		}
	}
	if !found {
		t.Fatal("evener/thread/name/set missing from the catalog")
	}
	var caps ThreadCapabilities
	caps.Rename = true // must compile
}
