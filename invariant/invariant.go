//go:build !serffuzz

// Package invariant provides build-tag-gated internal consistency assertions:
// cheap checks placed at the point a subsystem's logic could first go wrong, so
// the fuzzer trips them there rather than at some distant external surface.
//
// invariant.Hold(cond, format, args...) asserts that cond holds. In a normal
// build (this file) Hold is an empty, inlinable no-op: the Go compiler
// eliminates the call, its condition, and its message arguments entirely, so it
// carries zero runtime cost and cannot change behavior. (Verified by
// disassembly: a Hold call in a hot function compiles to no instructions — no
// boxing of the variadic args, no evaluation of the condition.)
//
// Built with -tags serffuzz (the build the fuzz targets use), a violated
// invariant panics with the formatted message, so the existing never-panic fuzz
// oracle reports it for free at the point the logic first went wrong.
//
// Contract: conditions and message arguments MUST be side-effect-free, because
// in a production build they are never evaluated. For an invariant whose
// CONDITION is itself expensive to compute (a full scan, say), guard it with
// `if invariant.Enabled { invariant.Hold(expensiveCheck(), ...) }` so the cost
// is compiled out in production along with the call.
package invariant

// Enabled reports whether invariants are live. It is an untyped constant, so
// `if invariant.Enabled { ... }` is dead-code-eliminated in a production build
// and compiled in under -tags serffuzz.
const Enabled = false

// Hold asserts cond is true. This is the production no-op form; see the package
// doc. Under -tags serffuzz it panics when cond is false.
func Hold(cond bool, format string, args ...any) {}
