// Package procgroup is the one implementation of the process-group
// primitives for spawned commands: put a child in its own group and signal
// the whole tree. agent/execenv's command runtime and the config value
// evaluator (internal/valueexpr) both use it, so a fix to group-kill
// semantics lands in one place.
package procgroup
