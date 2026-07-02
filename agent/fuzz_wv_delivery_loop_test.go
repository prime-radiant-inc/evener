//go:build serffuzz

package agent

import (
	"testing"

	"primeradiant.com/serf/agent/events"
)

// FuzzWvDeliveryLoop drives validateWatchDeliveryLoop — the already-pure
// structural feedback-loop guard (spec §6.1) — over fuzzed watch configs.
//
// Oracles (beyond never-panic):
//   - determinism: the same config yields the same verdict;
//   - total: the verdict is either nil or a non-empty typed error;
//   - a cross-session receiver never trips the guard;
//   - a non-self delivery (send to a target other than the caller) never trips it;
//   - the guard trips exactly when a self-delivery would replay a self-generated
//     event kind (wildcard / assistant.tool / communicate) back into its own
//     session.
func FuzzWvDeliveryLoop(f *testing.F) {
	f.Add("", true, false, true, false, "")
	f.Add("sess_recv", false, true, true, true, "caller")
	f.Add("", false, true, false, false, "caller")
	f.Add("", false, false, false, false, "dlg_1")
	f.Add("", true, false, false, false, "caller")

	f.Fuzz(func(t *testing.T, receiverSessionID string, wildcard, toolEnd, communicate, hasSend bool, sendTo string) {
		cfg := &watchConfig{
			receiverSessionID: receiverSessionID,
			wildcardEvents:    wildcard,
			eventKinds:        map[events.EventKind]bool{},
		}
		if toolEnd {
			cfg.eventKinds[events.EventToolCallEnd] = true
		}
		if communicate {
			cfg.eventKinds[events.EventCommunicate] = true
		}
		if hasSend {
			cfg.send = &watchSendArgs{To: sendTo}
		}

		err := validateWatchDeliveryLoop(cfg)
		if err2 := validateWatchDeliveryLoop(cfg); (err == nil) != (err2 == nil) {
			t.Fatalf("non-deterministic: %v vs %v", err, err2)
		}
		if err != nil && err.Error() == "" {
			t.Fatalf("rejection must carry a non-empty message")
		}

		selfDelivery := !hasSend || sendTo == runtimeMessageAliasCaller
		selfGenerated := wildcard || toolEnd || communicate
		wantError := receiverSessionID == "" && selfDelivery && selfGenerated
		if (err != nil) != wantError {
			t.Fatalf("guard verdict mismatch: err=%v want=%v (recv=%q selfDelivery=%v selfGen=%v)",
				err, wantError, receiverSessionID, selfDelivery, selfGenerated)
		}
		if receiverSessionID != "" && err != nil {
			t.Fatalf("cross-session receiver must never trip the guard: %v", err)
		}
		if hasSend && sendTo != runtimeMessageAliasCaller && err != nil {
			t.Fatalf("non-self delivery must never trip the guard: %v", err)
		}
	})
}
