package appwire

import "testing"

// TestMethodCatalogWellFormed guards the catalog's internal invariants: the
// generated protocol doc and the router cross-checks both rely on these.
func TestMethodCatalogWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i, m := range Methods {
		if m.Name == "" {
			t.Errorf("Methods[%d] has empty Name", i)
		}
		if seen[m.Name] {
			t.Errorf("Methods has duplicate entry for %q", m.Name)
		}
		seen[m.Name] = true
		if m.Params == nil {
			t.Errorf("method %q has nil Params", m.Name)
		}
		if m.Result == nil {
			t.Errorf("method %q has nil Result", m.Name)
		}
		switch m.Scope {
		case ScopeBoth, ScopeHub, ScopeDaemon, ScopeConnection, ScopeUnimplemented:
		default:
			t.Errorf("method %q has invalid scope %q", m.Name, m.Scope)
		}
		if m.Summary == "" {
			t.Errorf("method %q has empty Summary", m.Name)
		}
	}
}

func TestNotificationCatalogWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i, n := range Notifications {
		if n.Name == "" {
			t.Errorf("Notifications[%d] has empty Name", i)
		}
		if seen[n.Name] {
			t.Errorf("Notifications has duplicate entry for %q", n.Name)
		}
		seen[n.Name] = true
		if n.Summary == "" {
			t.Errorf("notification %q has empty Summary", n.Name)
		}
	}
}

// TestConnectionAndReservedMethodsCataloged pins the special cases the router
// cross-checks deliberately skip, so a future change can't silently drop them.
func TestConnectionAndReservedMethodsCataloged(t *testing.T) {
	scope := map[string]MethodScope{}
	for _, m := range Methods {
		scope[m.Name] = m.Scope
	}
	for name, want := range map[string]MethodScope{
		MethodInitialize:          ScopeConnection,
		MethodPing:                ScopeConnection,
		MethodThreadTurnsList:     ScopeUnimplemented,
		MethodThreadTurnItemsList: ScopeUnimplemented,
	} {
		if got, ok := scope[name]; !ok {
			t.Errorf("method %q missing from catalog", name)
		} else if got != want {
			t.Errorf("method %q scope = %q, want %q", name, got, want)
		}
	}
}
