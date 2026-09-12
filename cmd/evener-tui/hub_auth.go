package tui

import (
	"fmt"
	"strings"

	"primeradiant.com/evener/appwire"
)

// authProviderArg is the instance an /auth, /login or /logout names. An
// unqualified command sends nothing, so the hub's own normalizeAuthProvider
// picks the default — one definition of it, on the side that owns the
// registry. Naming "openai" here is what made /logout delete the platform
// API key stored under that name while reporting an OAuth sign-out.
func authProviderArg(args string) string {
	provider := strings.Fields(args)
	if len(provider) == 0 {
		return ""
	}
	return provider[0]
}

// hubAuthLoginBlockedReason reports why /login cannot start for name, or ""
// when it may proceed. authModes is the hub's own eligibility answer (spec
// §11.3): only an instance offering "oauth" has a flow the hub can start.
// The check needs a status for that exact instance; without one — including
// the unqualified form, where the hub picks the instance — the request goes
// to the hub, which answers for itself.
func (m hubModel) hubAuthLoginBlockedReason(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || !m.authStatusSeen || len(m.authStatus.AuthModes) == 0 {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(m.authStatus.Provider), name) {
		return ""
	}
	if authModeOffered(m.authStatus, "oauth") {
		return ""
	}
	return fmt.Sprintf("%s does not offer OAuth sign-in (auth modes: %s).", name, strings.Join(m.authStatus.AuthModes, ", "))
}

func authStatusFromAppWire(status appwire.AuthStatusResponse) authStatus {
	return authStatus{
		Provider:       status.Provider,
		Supported:      status.Supported,
		SignedIn:       status.SignedIn,
		ActiveSource:   status.ActiveSource,
		AuthModes:      status.AuthModes,
		HasStoredOAuth: status.HasStoredOAuth,
		Email:          status.Email,
		StoredEmail:    status.StoredEmail,
		AccountID:      status.AccountID,
		WorkspaceID:    status.WorkspaceID,
		NeedsRefresh:   status.NeedsRefresh,
		NeedsLogin:     status.NeedsLogin,
		Error:          status.Error,
	}
}
