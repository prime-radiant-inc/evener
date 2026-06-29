//go:build serffuzz

package invariant

import "fmt"

// Enabled is true in the fuzz build: invariants are live and `if
// invariant.Enabled { ... }` guards compile in.
const Enabled = true

// Hold panics when cond is false, reporting the formatted message. The fuzz
// targets build with -tags serffuzz, so a tripped invariant surfaces through the
// never-panic oracle at the point the logic first went wrong.
func Hold(cond bool, format string, args ...any) {
	if !cond {
		panic("invariant violated: " + fmt.Sprintf(format, args...))
	}
}
