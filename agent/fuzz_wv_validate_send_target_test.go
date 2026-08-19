//go:build evenerfuzz

package agent

import "testing"

// FuzzWvValidateSendTarget drives the pure install-time validation decision
// tree without touching a store.
//
// Oracles (beyond never-panic):
//   - determinism: the same target yields the same verdict;
//   - the caller alias validates;
//   - every other target yields a non-empty error.
func FuzzWvValidateSendTarget(f *testing.F) {
	f.Add(runtimeMessageAliasCaller)
	f.Add(runtimeMessageAliasWatched)
	f.Add("")
	f.Add("job_9")
	f.Add("dlg_1")
	f.Add("not-prefixed")

	f.Fuzz(func(t *testing.T, target string) {
		err := validateWatchSendDeliveryTarget(target, watchArgs{})
		err2 := validateWatchSendDeliveryTarget(target, watchArgs{})
		if (err == nil) != (err2 == nil) || (err != nil && err.Error() != err2.Error()) {
			t.Fatalf("non-deterministic: %v vs %v", err, err2)
		}
		if err != nil && err.Error() == "" {
			t.Fatalf("rejection must carry a non-empty message for %q", target)
		}

		if target == runtimeMessageAliasCaller {
			if err != nil {
				t.Fatalf("alias %q must validate: %v", target, err)
			}
		} else if err == nil {
			t.Fatalf("non-alias target %q must be rejected", target)
		}
	})
}
