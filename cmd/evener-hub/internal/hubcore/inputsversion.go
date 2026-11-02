package hubcore

import "sync/atomic"

// InputsVersion is a monotonically increasing counter bumped whenever an input
// to the tree changes (past-index content delta, roster membership/state delta,
// archive/favorite writes, attention poke). The navigation source revision
// reads it.
type InputsVersion struct{ v atomic.Uint64 }

func (iv *InputsVersion) Bump()        { iv.v.Add(1) }
func (iv *InputsVersion) Load() uint64 { return iv.v.Load() }
