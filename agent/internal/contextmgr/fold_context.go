package contextmgr

import (
	"context"

	"primeradiant.com/evener/agent/schema"
)

type compactionTurnCallbackKey struct{}

// WithCompactionTurnCallback returns a context whose callback handles each
// checkpoint or summary created during that operation. Manager.OnCompactionTurn
// remains the fallback for callers that do not install an operation callback.
func WithCompactionTurnCallback(ctx context.Context, callback func(schema.Turn)) context.Context {
	return context.WithValue(ctx, compactionTurnCallbackKey{}, callback)
}

type postFoldInjectionCallbackKey struct{}

// WithPostFoldInjectionCallback returns a context whose callback receives the
// net turn-count delta a Strategy appends to history AFTER its own fold layers
// run — OODAStrategy's orientation message. Those turns
// land after whatever the fold preserved, so a caller measuring the overall
// before/after turn-count delta across the whole ManageContext call needs this
// reported separately to avoid under-shrinking the N4 in-flight-turn boundary
// by exactly this amount, the same class of bug compactionEmitFunc's
// runPreCompactHook-injected count already corrects for (issue #634).
//
// A strategy that does not self-inject after its fold has nothing to report
// and needs not call reportPostFoldInjection at all; a caller that does not
// install this callback (e.g. a test driving ManageContext directly) is
// unaffected — reportPostFoldInjection silently no-ops.
func WithPostFoldInjectionCallback(ctx context.Context, callback func(int)) context.Context {
	return context.WithValue(ctx, postFoldInjectionCallbackKey{}, callback)
}

// reportPostFoldInjection is the strategy-side counterpart of
// WithPostFoldInjectionCallback: report n turns net-appended after this
// strategy's own fold layers. n may be negative (e.g. a self-injecting
// strategy that nets out more markers removed than added) or zero (steady
// state: an old marker was replaced by a new one of the same count) — zero is
// a no-op, since the caller's own before/after delta already accounts for it
// correctly.
func reportPostFoldInjection(ctx context.Context, n int) {
	if n == 0 {
		return
	}
	if callback, ok := ctx.Value(postFoldInjectionCallbackKey{}).(func(int)); ok {
		callback(n)
	}
}

type baselineQueryKey struct{}

// WithBaselineQuery returns a context whose query function reports the N4
// in-flight-turn boundary (turnHistoryBaseline), translated into the CURRENT
// history array's indexing as of the moment it's called: adjusted for every
// fold layer's shrinkage seen so far via EventContextCompaction, but not yet
// adjusted for any strategy-level append/removal a Strategy is about to
// perform on its own.
//
// A self-injecting Strategy that replaces an existing marker turn
// (ooda) uses this to tell whether the
// marker it's about to remove sits before or after the boundary: removing a
// pre-boundary marker shifts every in-flight turn left by one, and the
// marker's own re-append (always at the end) does not restore that —
// WithPostFoldInjectionCallback's plain net-delta count can't see this, since
// the removal and the append cancel out numerically to zero (issue #634).
//
// ok is false when the caller installed no tracking (e.g. a test driving
// ManageContext directly) — a strategy that gets ok=false should not attempt
// a positional correction, matching WithPostFoldInjectionCallback's fallback
// of doing nothing when nothing is installed.
func WithBaselineQuery(ctx context.Context, query func() (int, bool)) context.Context {
	return context.WithValue(ctx, baselineQueryKey{}, query)
}

// currentBaseline is the strategy-side counterpart of WithBaselineQuery.
func currentBaseline(ctx context.Context) (int, bool) {
	if query, ok := ctx.Value(baselineQueryKey{}).(func() (int, bool)); ok {
		return query()
	}
	return 0, false
}

type compactionMetaKey struct{}

// WithCompactionMeta returns a context carrying the compaction metadata for
// ONE fold operation. The session's fold staging installs it per call, so
// concurrent fold publishers never share — or write — Manager.Meta: the
// shared field would race across publishers, slice fields included.
// Manager.Meta remains only as the fallback for direct single-threaded
// callers (tests driving layers without a fold context).
func WithCompactionMeta(ctx context.Context, meta CompactionMeta) context.Context {
	return context.WithValue(ctx, compactionMetaKey{}, meta)
}

// metaFor resolves the compaction metadata for one operation: the per-call
// context value when installed (see WithCompactionMeta), else the Manager's
// fallback field.
func (cm *Manager) metaFor(ctx context.Context) *CompactionMeta {
	if meta, ok := ctx.Value(compactionMetaKey{}).(CompactionMeta); ok {
		return &meta
	}
	return &cm.Meta
}
