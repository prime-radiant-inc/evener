//go:build !linux

package numerictest

import "testing"

// Other platforms retain subprocess isolation, the deadline, and the same
// portable runtime CPU/allocation budgets. Unsafe pre-fix RED is Linux-only.
func limitChild(t *testing.T) { t.Helper() }
