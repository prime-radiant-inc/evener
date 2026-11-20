package hub

import (
	"testing"
	"testing/synctest"
)

// The three fan-out tests whose negative assertions need a quiescence check
// rather than a fixed wait. Their bodies live in app_host_admin_test.go, which
// deliberately does not import testing/synctest: the fixed-sleep audit exempts
// a whole file that imports it.
func TestHostAdminFanOutLeavesReconnectToTheSupervisorWhileOffline(t *testing.T) {
	synctest.Test(t, testHostAdminFanOutLeavesReconnectToTheSupervisorWhileOffline)
}

func TestHostAdminAttachWakesBackoffSleepingFanOut(t *testing.T) {
	synctest.Test(t, testHostAdminAttachWakesBackoffSleepingFanOut)
}

func TestHostAdminFanOutExitClearSparesReplacementWake(t *testing.T) {
	synctest.Test(t, testHostAdminFanOutExitClearSparesReplacementWake)
}

// waitQuiescent blocks until every goroutine in the synctest bubble is durably
// blocked. It lives here, with the synctest import, because a test file that
// imports testing/synctest is exempt from the fixed-sleep audit whole; keeping
// the import out of app_host_admin_test.go keeps its other sleeps counted.
func waitQuiescent() { synctest.Wait() }
