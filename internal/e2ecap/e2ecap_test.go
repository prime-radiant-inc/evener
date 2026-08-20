package e2ecap

import "testing"

// On a normal development host both probes pass. The skip paths are
// inherently environmental; they are exercised only on hosts where the
// capability is genuinely missing.
func TestRequireLoopbackBind(t *testing.T) {
	RequireLoopbackBind(t)
}

func TestRequireProcessInspect(t *testing.T) {
	RequireProcessInspect(t)
}
