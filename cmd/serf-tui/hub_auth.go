package main

import (
	"strings"

	"primeradiant.com/serf/appwire"
)

func authProviderArg(args string) string {
	provider := strings.Fields(args)
	if len(provider) == 0 {
		return "openai"
	}
	return provider[0]
}

func authStatusFromAppWire(status appwire.AuthStatusResponse) authStatus {
	return authStatus{
		Provider:       status.Provider,
		Supported:      status.Supported,
		SignedIn:       status.SignedIn,
		ActiveSource:   status.ActiveSource,
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
