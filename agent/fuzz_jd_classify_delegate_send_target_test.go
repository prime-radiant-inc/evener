//go:build serffuzz

package agent

import (
	"strings"
	"testing"
)

// FuzzJdClassifyDelegateSendTarget drives classifyDelegateSendTarget — the pure
// pre-dispatch decision lifted out of sendDelegateMessage — over arbitrary
// targets, messages, and routing contexts. Oracles (beyond never-panic):
//   - determinism: the same inputs yield the same (kind, reason);
//   - exactly one valid classification (the four documented kinds);
//   - prefix mutual exclusion: a job_-prefixed target never classifies as the
//     delegate-id path and vice-versa;
//   - callerAlias is only reachable with a caller route;
//   - every non-rejected kind carries an empty reason, and every rejected kind
//     carries a non-empty reason with a documented prefix.
func FuzzJdClassifyDelegateSendTarget(f *testing.F) {
	f.Add("dlg_1", "hi", "fail", 0, false, false)
	f.Add("job_2", "hi", "start", 0, false, false)
	f.Add("caller", "hi", "fail", 0, false, true)
	f.Add("caller", "hi", "fail", 0, true, true)
	f.Add("", "hi", "fail", 0, false, false)
	f.Add("main", "hi", "fail", -1, false, false)
	f.Add("bare", "", "bogus", 0, false, true)

	allowedPrefixes := []string{"invalid_request:", "target_not_found:", "internal:"}

	f.Fuzz(func(t *testing.T, target, message, onIdle string, blockTimeoutMS int, fromWatch, hasCallerRoute bool) {
		kind, reason := classifyDelegateSendTarget(target, message, onIdle, blockTimeoutMS, fromWatch, hasCallerRoute)
		if k2, r2 := classifyDelegateSendTarget(target, message, onIdle, blockTimeoutMS, fromWatch, hasCallerRoute); kind != k2 || reason != r2 {
			t.Fatalf("non-deterministic: (%v,%q) vs (%v,%q)", kind, reason, k2, r2)
		}

		switch kind {
		case delegateTargetRejected, delegateTargetCallerAlias, delegateTargetJobHandle, delegateTargetDelegateID:
		default:
			t.Fatalf("invalid kind %v", kind)
		}

		// Prefix mutual exclusion.
		if kind == delegateTargetJobHandle && !strings.HasPrefix(target, "job_") {
			t.Fatalf("jobHandle for non job_ target %q", target)
		}
		if kind == delegateTargetDelegateID && strings.HasPrefix(target, "job_") {
			t.Fatalf("delegateID for a job_ target %q", target)
		}

		// callerAlias only reachable with a caller route.
		if kind == delegateTargetCallerAlias && !hasCallerRoute {
			t.Fatalf("callerAlias without a caller route: target %q", target)
		}

		if kind == delegateTargetRejected {
			if reason == "" {
				t.Fatalf("rejected without a reason: target %q", target)
			}
			ok := false
			for _, p := range allowedPrefixes {
				if strings.HasPrefix(reason, p) {
					ok = true
					break
				}
			}
			if !ok {
				t.Fatalf("rejected reason lacks a documented prefix: %q", reason)
			}
		} else if reason != "" {
			t.Fatalf("non-rejected kind %v carried a reason %q", kind, reason)
		}
	})
}
