package agent

import (
	"strconv"
	"strings"
	"testing"

	toolpkg "primeradiant.com/evener/agent/internal/tool"
)

// TestSessionRegisteredToolsDocumentParameters audits the registration seam:
// every tool a session actually registers must document every parameter,
// including tools defined outside the Def* constructors (compact_context
// today) and any future inline-schema tool. The constructor-level gate in
// agent/internal/tool cannot see these, so a tool registered here but
// omitted there would otherwise ship bare parameters with both gates green.
func TestSessionRegisteredToolsDocumentParameters(t *testing.T) {
	s := newTestSession(t)
	defs := s.reg.Definitions()
	if len(defs) < 20 {
		t.Fatalf("session registered only %d tools; the production path should register the full builtin set", len(defs))
	}
	for _, def := range defs {
		if missing := toolpkg.UndocumentedProperties(def); len(missing) > 0 {
			t.Errorf("%s parameters carry no description: %s", def.Name, strings.Join(missing, ", "))
		}
	}
}

// TestDefListDirLimitProseMatchesEnforcedDefault pins list_dir's advertised
// default limit to the constant the handler enforces (roborev round 2 caught
// the prose saying 500 where the code says 1000): a model sizing an explicit
// limit against the advertised default must not work from a wrong cap.
func TestDefListDirLimitProseMatchesEnforcedDefault(t *testing.T) {
	def := toolpkg.DefListDir()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("list_dir properties = %T, want map[string]any", def.Parameters["properties"])
	}
	limit, ok := props["limit"].(map[string]any)
	if !ok {
		t.Fatalf("list_dir missing limit property; got properties: %v", props)
	}
	desc, _ := limit["description"].(string)
	if !strings.Contains(desc, strconv.Itoa(defaultListDirLimit)) {
		t.Errorf("list_dir limit description should state the enforced default of %d, got: %q", defaultListDirLimit, desc)
	}
}
