package agent

import (
	"fmt"
	"testing"
	"time"
)

// FU2's measure-first deliverable: the delegate-tree controller's claim scans
// walk c.durable repeatedly per claim (subtreeMembersLocked re-scans every
// durable record once per tree level, memberIDsLeafFirstLocked compares by
// walking parent chains, and the shared-task-store check scans the durable
// set again), all under c.mu. Whether that cost justifies maintained
// indexes depends on what a claim actually costs at realistic worst-case
// trees, so these benchmarks measure the two claim entry points at the
// retained-terminal cap:
//
//   - ClaimRuntimeReclamation, the spawn-admission path: one claim per
//     delegate spawn once the retained cap is saturated.
//   - ClaimIdleRuntimeRelease, the per-finalize grace path: one claim per
//     delegate that finishes with retained scratch.
//
// Shapes: "flat" (every terminal a depth-1 child of the live root — the
// fan-out shape), "chain" (each terminal parented to the previous one —
// the fixed-point worst case; measured at 512 and 128 for the depth curve),
// and "mixed" (running mid-roots with terminal children — the realistic
// restored-children fanout). The live root and mids are never claimable;
// only the terminal leaves enter the candidates loop.
//
// Each iteration claims and then aborts: abort restores residency, so every
// iteration performs the full scan over the same tree.

func benchSeedReclaimTree(b *testing.B, shape string, count int) *delegateTreeController {
	b.Helper()
	c, _ := newDelegateControllerTestHarness(b, 8, 4)
	seedDelegateControllerRunning(b, c, "dlg_root", "")
	endedAt := time.Unix(10, 0).UTC()
	switch shape {
	case "flat":
		for i := range count {
			seedDelegateReclaimRuntime(b, c, fmt.Sprintf("dlg_t%05d", i), "dlg_root", endedAt, true, true)
		}
	case "chain":
		parent := "dlg_root"
		for i := range count {
			id := fmt.Sprintf("dlg_t%05d", i)
			seedDelegateReclaimRuntime(b, c, id, parent, endedAt, true, true)
			parent = id
		}
	case "mixed":
		mids := count / 32
		for m := range mids {
			mid := fmt.Sprintf("dlg_mid%04d", m)
			seedDelegateControllerRunning(b, c, mid, "dlg_root")
			for i := range 32 {
				seedDelegateReclaimRuntime(b, c, fmt.Sprintf("dlg_t%05d", m*32+i), mid, endedAt, true, true)
			}
		}
	default:
		b.Fatalf("unknown benchmark tree shape %q", shape)
	}
	// One slot short of the resident count, so an admission claim for one
	// more delegate must free two entries and the candidates loop always runs.
	c.maxRetainedTerminal = count - 1
	return c
}

func benchClaimRuntimeReclamation(b *testing.B, shape string, count int) {
	c := benchSeedReclaimTree(b, shape, count)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		claim, err := c.ClaimRuntimeReclamation(1)
		if err != nil {
			b.Fatalf("ClaimRuntimeReclamation: %v", err)
		}
		if claim == nil {
			b.Fatalf("ClaimRuntimeReclamation returned no claim at a saturated cap")
		}
		if err := c.AbortRuntimeReclamation(claim); err != nil {
			b.Fatalf("AbortRuntimeReclamation: %v", err)
		}
	}
}

func benchClaimIdleRuntimeRelease(b *testing.B, shape string, count int, target string) {
	c := benchSeedReclaimTree(b, shape, count)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		claim, _, err := c.ClaimIdleRuntimeRelease(target)
		if err != nil {
			b.Fatalf("ClaimIdleRuntimeRelease(%s): %v", target, err)
		}
		if claim == nil {
			b.Fatalf("ClaimIdleRuntimeRelease(%s) returned no claim", target)
		}
		if err := c.AbortRuntimeReclamation(claim); err != nil {
			b.Fatalf("AbortRuntimeReclamation: %v", err)
		}
	}
}

func BenchmarkClaimRuntimeReclamationFlat2048(b *testing.B) {
	benchClaimRuntimeReclamation(b, "flat", 2048)
}

func BenchmarkClaimRuntimeReclamationMixed2048(b *testing.B) {
	benchClaimRuntimeReclamation(b, "mixed", 2048)
}

func BenchmarkClaimRuntimeReclamationChain128(b *testing.B) {
	benchClaimRuntimeReclamation(b, "chain", 128)
}

func BenchmarkClaimRuntimeReclamationChain512(b *testing.B) {
	benchClaimRuntimeReclamation(b, "chain", 512)
}

func BenchmarkClaimIdleRuntimeReleaseFlat2048(b *testing.B) {
	benchClaimIdleRuntimeRelease(b, "flat", 2048, "dlg_t00000")
}

func BenchmarkClaimIdleRuntimeReleaseChain512Leaf(b *testing.B) {
	benchClaimIdleRuntimeRelease(b, "chain", 512, fmt.Sprintf("dlg_t%05d", 511))
}

func BenchmarkClaimIdleRuntimeReleaseChain512Head(b *testing.B) {
	benchClaimIdleRuntimeRelease(b, "chain", 512, "dlg_t00000")
}
