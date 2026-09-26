package agent

import (
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
