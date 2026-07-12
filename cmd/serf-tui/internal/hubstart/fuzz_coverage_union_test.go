//go:build serffuzz

package hubstart

import "testing"

func init() {
	fuzzCoverageUnion = func(t *testing.T) {
		TestAuthTokenFilePathWithoutHome(t)
		TestBearerTransport_EmptyTokenPassesThrough(t)
		TestCheckHubEnvironment(t)
		TestClassifyStartHubError(t)
		TestEnvDefault(t)
		TestHTTPClientWithBearer(t)
		TestHubAddressNormalization(t)
		TestHubRPCURL(t)
		TestLooksLikeBindFailure(t)
		TestResolveAuthToken(t)
		TestResolveAuthTokenMissingWarns(t)
		TestStartHubClientDistinguishesStartupFailures(t)
		TestStartHubClientDoesNotAutoStartIncompatibleOrStaleHub(t)
		TestStartHubClientDoesNotAutoStartRemoteHub(t)
		TestStartHubClientHonorsNoAutoStartForLocalHub(t)
		TestStartHubClientPassesStateDirAndLogFileToLocalHub(t)
		TestStartHubClientReloadsAuthTokenAfterAutoStart(t)
		TestStartHubClientReportsMissingHubBinary(t)
		TestStartHubClientReportsUnhealthyAutoStartedHub(t)
		TestStartHubClientWritesStartupDiagnosticsToLogFile(t)
		TestStartLocalHubReportsImmediateExitOutput(t)
		TestStartupErrorScreenAllKinds(t)
		TestStartupErrorScreenNamesFailureKind(t)
		TestStartupError_DetailFallsBackToWrappedErr(t)
		TestStartupError_ErrorMessagesPerKind(t)
		TestStateHomeForSerfStateDir(t)

	}
}
