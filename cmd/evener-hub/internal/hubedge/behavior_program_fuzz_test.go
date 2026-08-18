package hubedge

import "testing"

// FuzzHubEdgeBehaviorProgram replays one deterministic behavioral contract selected by the
// fuzz input. The seed corpus covers every production branch; mutation varies
// ordering and repetition without relying on network, wall clock, or host state.
func FuzzHubEdgeBehaviorProgram(f *testing.F) {
	checks := []func(*testing.T){
		checkLoadOrCreateAuthToken_RandomError,
		checkLoadOrCreateAuthToken_RenameErrorRemovesTemporaryFile,
		checkLoadOrCreateAuthToken_PersistsAndReloads,
		checkAuthGuard_AllowsExemptRoutes,
		checkAuthGuard_RejectsMissingToken,
		checkAuthGuard_AcceptsCookie,
		checkAuthGuard_AcceptsBearer,
		checkAuthGuard_RejectsWrongToken,
		checkAuthGuard_EmptyTokenBypassesAuth,
		checkHandleAuth_ValidatesAndSetsCookie,
		checkHandleAuth_RejectsWrongToken,
		checkHandleAuth_HonorsNextParam,
		checkHandleAuth_RejectsExternalNext,
		checkAuthURLFor,
		checkLoadOrCreateAuthToken_EmptyRoot,
		checkLoadOrCreateAuthToken_MkdirAllError,
		checkLoadOrCreateAuthToken_ReadFileError,
		checkLoadOrCreateAuthToken_WriteFileError,
		checkAuthGuard_ReturnsHTMLForBrowser,
		checkAuthGuard_AcceptsQueryTokenOnAnyGET,
		checkAuthGuard_RejectsWrongQueryToken,
		checkAuthGuard_IgnoresQueryTokenOnPOST,
		checkAuthGuard_401IsNoStore,
		checkAuthGuard_RefreshesCookieOnAuthenticatedRequest,
		checkAuthGuard_NoCookieRefreshForBearer,
		checkAuthGuard_ReturnsPlainForAPI,
		checkAuthGuard_ReloadSurvivesAnotherSameHostHub,
		checkCookieName_DistinctPerToken,
	}
	for i := range checks {
		f.Add(uint8(i))
	}
	f.Fuzz(func(t *testing.T, selector uint8) {
		checks[int(selector)%len(checks)](t)
	})
}
