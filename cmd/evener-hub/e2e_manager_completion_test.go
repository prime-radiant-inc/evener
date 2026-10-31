package hub

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestManagerAcceptanceCompletionEvidence(t *testing.T) {
	t.Parallel()
	const watchID = "watch-acceptance"
	for _, note := range []string{"", "WATCH-NOTICE-ALTERNATE", "通知"} {
		t.Run(note, func(t *testing.T) {
			completed := appwire.SessionWatch{State: appwire.SessionWatchStateEnded, Watch: appwire.EvenerWatchInfo{ID: watchID, EndReason: "completed", Note: note}}
			for _, tc := range []struct {
				name  string
				watch appwire.SessionWatch
				want  bool
			}{
				{"completed", completed, true},
				{"armed", appwire.SessionWatch{State: appwire.SessionWatchStateArmed, Watch: appwire.EvenerWatchInfo{ID: watchID, Active: true}}, false},
				{"restart", appwire.SessionWatch{State: appwire.SessionWatchStateEnded, Watch: appwire.EvenerWatchInfo{ID: watchID, EndReason: "restart"}}, false},
				{"other-watch", appwire.SessionWatch{State: appwire.SessionWatchStateEnded, Watch: appwire.EvenerWatchInfo{ID: "watch-other", EndReason: "completed"}}, false},
				{"unknown", appwire.SessionWatch{State: appwire.SessionWatchStateUnknown, Watch: appwire.EvenerWatchInfo{ID: watchID, EndReason: "completed"}}, false},
				{"still-active", appwire.SessionWatch{State: appwire.SessionWatchStateEnded, Watch: appwire.EvenerWatchInfo{ID: watchID, EndReason: "completed", Active: true}}, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					response := appwire.SessionWatchesResponse{Watches: []appwire.SessionWatch{tc.watch}}
					if got := acceptanceWatchCompleted(response, watchID); got != tc.want {
						t.Fatalf("completion=%t, want %t for %+v", got, tc.want, tc.watch)
					}
				})
			}
		})
	}
}
