package tokenauth

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"runtime"
	"strings"
	"sync"

	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// ClientVersion is reported in the User-Agent the OpenAI Codex backend
// expects; the evener binaries set it to the build version at startup.
var ClientVersion = "dev"

const (
	codexOriginator = "evener"
	codexLiteHeader = "x-openai-internal-codex-responses-lite"
)

// Codex is the oauth-openai-codex transport (spec §9.5). Apply reads the
// instance's OAuth record from <StateDir>/auth/<instance>.json through
// auth/openai (which refreshes and rewrites it) and sets the headers every
// Codex request carries, including ListModels; PrepareRequest adds the
// per-request headers and the client_metadata rule.
type Codex struct {
	// StateDir is the evener state directory; "" means authopenai.DefaultStateDir().
	// It must be the same state root the registry was loaded with: the registry's
	// oauth gate is a file-existence check under its own state root, so a mismatch
	// here would let Apply resolve credentials the registry never saw.
	StateDir string
	// Credentials is the token seam; nil means a shared authopenai.Service.
	Credentials func(ctx context.Context, stateDir, instance string) (authopenai.RuntimeCredentials, error)

	mu       sync.Mutex
	service  *authopenai.Service
	accounts map[string]string
}

// Apply implements llm.Authenticator.
func (c *Codex) Apply(ctx context.Context, req *http.Request, res registry.Resolved) error {
	if res.Credential.Source != "oauth" {
		return notSignedIn(res.Instance)
	}
	creds, err := c.credentials(ctx, res.Instance)
	if err != nil {
		if errors.Is(err, authopenai.ErrLoginRequired) {
			return &llm.ConfigurationError{Message: fmt.Sprintf("instance %q: %v (run `evener openai login --instance %s`)", res.Instance, err, res.Instance), Cause: err}
		}
		return fmt.Errorf("instance %q: codex credentials: %w", res.Instance, err)
	}
	if creds.Source != authopenai.AuthSourceOAuth {
		// res.Credential.Source above is the registry's own gate: a file-existence
		// check under ITS state root. It can disagree with what c.credentials just
		// resolved (a different StateDir, or a logout in between), and
		// ResolveRuntimeCredentials falls back to OPENAI_API_KEY whenever it finds
		// no stored record. The flag day rule is absolute: an env-sourced key must
		// never reach the Codex backend, even transitively, so re-check the source
		// actually returned rather than trusting the registry's gate alone.
		return notSignedIn(res.Instance)
	}
	req.Header.Set("Authorization", "Bearer "+creds.BearerToken)
	if id := c.accountID(res.Instance); id != "" {
		req.Header.Set("ChatGPT-Account-ID", id)
	}
	if req.Header.Get("originator") == "" {
		req.Header.Set("originator", codexOriginator)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", userAgent())
	}
	return nil
}

// notSignedIn reports that Apply has no oauth-sourced credential to send,
// whether because the registry's own gate failed or because c.credentials
// resolved something else (spec §9.5's flag day: never OPENAI_API_KEY).
func notSignedIn(instance string) error {
	return &llm.ConfigurationError{Message: fmt.Sprintf("instance %q is not signed in (run `evener openai login --instance %s`)", instance, instance)}
}

// PrepareRequest implements llm.RequestPreparer: the lite routing header
// (without it the backend hangs), the session and thread ids, and
// client_metadata = merge(body.metadata, req.ClientMetadata) when the row's
// metadata field is on, with metadata itself never sent (spec §9.5).
func (c *Codex) PrepareRequest(_ context.Context, httpReq *http.Request, body map[string]any, req llm.Request, res registry.Resolved) error {
	if registry.BoolValue(res.Caps.ResponsesLite) {
		httpReq.Header.Set(codexLiteHeader, "true")
	}
	if sid := strings.TrimSpace(req.SessionID); sid != "" {
		httpReq.Header.Set("session-id", sid)
	}
	if tid := strings.TrimSpace(req.ThreadID); tid != "" {
		httpReq.Header.Set("thread-id", tid)
		httpReq.Header.Set("x-client-request-id", tid)
	}
	if res.Caps.Fields["metadata"] {
		merged := map[string]string{}
		if m, ok := body["metadata"].(map[string]string); ok {
			maps.Copy(merged, m)
		}
		maps.Copy(merged, req.ClientMetadata)
		if len(merged) > 0 {
			body["client_metadata"] = merged
		}
	} else {
		// Off means neither field is sent, including a client_metadata a
		// provider option put in the body (spec §9.5).
		delete(body, "client_metadata")
	}
	delete(body, "metadata")
	return nil
}

// RequiresStreamingComplete reports that the Codex backend answers every
// request as a stream (spec §9.5).
func (*Codex) RequiresStreamingComplete() bool { return true }

func (c *Codex) stateDir() string {
	if c.StateDir != "" {
		return c.StateDir
	}
	return authopenai.DefaultStateDir()
}

func (c *Codex) credentials(ctx context.Context, instance string) (authopenai.RuntimeCredentials, error) {
	if c.Credentials != nil {
		return c.Credentials(ctx, c.stateDir(), instance)
	}
	c.mu.Lock()
	if c.service == nil {
		c.service = authopenai.NewService(authopenai.DefaultConfig(), nil)
	}
	service := c.service
	c.mu.Unlock()
	return service.ResolveRuntimeCredentials(ctx, c.stateDir(), instance)
}

// accountID reads the ChatGPT account id from the record (or its id token
// claims) once per instance; it is display metadata, so a missing or
// unreadable record yields "" rather than an error.
func (c *Codex) accountID(instance string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id, ok := c.accounts[instance]; ok {
		return id
	}
	id := ""
	if rec, err := authopenai.LoadAuth(c.stateDir(), instance); err == nil {
		id = rec.AccountID
		if id == "" {
			if claims, err := authopenai.ParseIDTokenClaims(rec.IDToken); err == nil {
				id = claims.AccountID
			}
		}
	}
	if c.accounts == nil {
		c.accounts = map[string]string{}
	}
	c.accounts[instance] = id
	return id
}

func userAgent() string {
	version := strings.TrimSpace(ClientVersion)
	if version == "" {
		version = "dev"
	}
	return fmt.Sprintf("%s/%s (%s %s)", codexOriginator, version, runtime.GOOS, runtime.GOARCH)
}
