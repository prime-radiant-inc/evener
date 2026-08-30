package tui

import (
	"fmt"
	"slices"
	"strings"
)

// authStatus is the TUI's view of one instance's credential state, decoded
// from appwire.AuthStatusResponse. Its vocabulary is the registry's, not the
// OAuth client's: ActiveSource is one of api_key, credential_headers, store,
// env:<VAR>, oauth, adc or none (spec §10), and AuthModes names the sign-in
// affordances the hub offers for this instance (spec §11.3).
type authStatus struct {
	Provider       string
	Supported      bool
	SignedIn       bool
	ActiveSource   string
	AuthModes      []string
	HasStoredOAuth bool
	Email          string
	StoredEmail    string
	AccountID      string
	WorkspaceID    string
	NeedsRefresh   bool
	NeedsLogin     bool
	Error          string
}

// authModeOffered reports whether the hub advertises one sign-in affordance
// for this instance: "oauth" is what /login needs, "apiKey" what key entry
// needs, and "none" marks an instance that wants no credential at all.
func authModeOffered(status authStatus, mode string) bool {
	return slices.Contains(status.AuthModes, mode)
}

// authSourceLabel puts the hub's activeSource into words. It is the TUI's
// half of the credentials pane's activeSourceLabel (cmd/evener-hub/frontend/
// src/panes/settings/sections/credentials/credentialLabels.ts) and covers the
// same seven values; env:<VAR> carries its variable name inside the string,
// so it is matched by prefix rather than by value.
func authSourceLabel(status authStatus) string {
	source := strings.TrimSpace(status.ActiveSource)
	if name, ok := strings.CutPrefix(source, "env:"); ok {
		return "environment variable (" + name + ")"
	}
	switch source {
	case "api_key":
		return "providers.toml"
	case "credential_headers":
		return "credential header"
	case "store":
		return "stored API key"
	case "oauth":
		switch {
		case status.NeedsLogin:
			return "OAuth expired"
		case status.NeedsRefresh:
			return "OAuth refreshable"
		}
		return "OAuth"
	case "adc":
		return "application default credentials"
	case "none", "":
		// Nothing resolved. An instance whose scheme never wants a credential
		// — auth none, or an optional bearer — is not missing one, and the
		// hub says which it is by offering the "none" mode.
		if authModeOffered(status, "none") {
			return "no credential required"
		}
		return "not configured"
	}
	// A source this build has no words for: show the hub's own value rather
	// than inventing one.
	return source
}

// authStatusEmail is the account the hub names for this instance, preferring
// the live record over the stored one.
func authStatusEmail(status authStatus) string {
	if email := strings.TrimSpace(status.Email); email != "" {
		return email
	}
	return strings.TrimSpace(status.StoredEmail)
}

// authStatusInstanceName is the instance a message is about; the hub echoes
// the name it normalized, so an unqualified /auth reports the instance it
// actually asked about.
func authStatusInstanceName(status authStatus) string {
	if name := strings.TrimSpace(status.Provider); name != "" {
		return name
	}
	return "the instance"
}

// formatAuthStatusSummary is /auth's answer for ANY instance: the hub reports
// on whichever one it was asked about, so there is no provider this renderer
// declines to render.
func formatAuthStatusSummary(status authStatus) string {
	name := strings.TrimSpace(status.Provider)
	if name == "" {
		return "Auth is not available until an instance is selected."
	}
	if !status.Supported {
		return fmt.Sprintf("Auth is not supported for instance %q.", name)
	}
	line := name + " auth: " + authSourceLabel(status)
	if email := authStatusEmail(status); email != "" {
		line += " (" + email + ")"
	}
	if detail := strings.TrimSpace(status.Error); detail != "" {
		line += ": " + detail
	}
	return line
}
