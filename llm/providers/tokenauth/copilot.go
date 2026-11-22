package tokenauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/auth/githubcopilot"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// copilotAPIVersion is the GitHub API version the Copilot backend accepts.
const copilotAPIVersion = "2026-08-01"

var (
	_ llm.Authenticator     = (*Copilot)(nil)
	_ llm.AuthScopeProvider = (*Copilot)(nil)
	_ llm.RequestPreparer   = (*Copilot)(nil)
)

// Copilot is the oauth-github-copilot transport. Apply resolves the instance's
// record through auth/githubcopilot (which refreshes it under the record lock)
// and sets the headers every Copilot request carries, including ListModels;
// PrepareRequest adds the per-request compatibility headers. The record is the
// only credential source: no ambient GitHub token is ever consulted.
type Copilot struct {
	// StateDir is the evener state directory; "" means userdirs.DefaultStateRoot().
	// It must be the state root the registry resolved, so a request can only use
	// the record the launch was admitted with. A Copilot value must never be
	// mutated after registration: concurrent launches bind ScopedCopilot instead.
	StateDir string
}

// Apply implements llm.Authenticator.
func (c *Copilot) Apply(ctx context.Context, req *http.Request, res registry.Resolved) error {
	creds, err := c.credentials(ctx, res.Instance)
	if err != nil {
		if errors.Is(err, githubcopilot.ErrLoginRequired) ||
			errors.Is(err, githubcopilot.ErrAuthNotFound) ||
			errors.Is(err, githubcopilot.ErrAuthCorrupt) {
			return copilotSignIn(res.Instance, err)
		}
		return fmt.Errorf("instance %q: copilot credentials: %w", res.Instance, err)
	}
	req.Header.Set("Authorization", "Bearer "+creds.BearerToken)
	githubcopilot.SetCopilotCompatibilityHeaders(req.Header)
	req.Header.Set("User-Agent", copilotUserAgent())
	req.Header.Set("X-GitHub-Api-Version", copilotAPIVersion)
	return nil
}

// AuthScope implements llm.AuthScopeProvider: the stable account claim of the
// instance's record. Copilot has no workspace claim, so the second value is
// always empty.
func (c *Copilot) AuthScope(_ context.Context, res registry.Resolved) (string, string, error) {
	stateDir, err := c.stateDir()
	if err != nil {
		return "", "", err
	}
	record, err := githubcopilot.LoadAuth(stateDir, res.Instance)
	if err != nil {
		return "", "", err
	}
	return record.AccountID, "", nil
}

// AuthGeneration implements the llm client's authRotation check: the digest
// changes when the record becomes a new generation (refresh token or
// obtained-at), letting the client retry one 401 after another process rotated
// the record. The digest names a generation without carrying a token.
func (c *Copilot) AuthGeneration(_ context.Context, res registry.Resolved) (string, error) {
	stateDir, err := c.stateDir()
	if err != nil {
		return "", err
	}
	record, err := githubcopilot.LoadAuth(stateDir, res.Instance)
	if err != nil {
		return "", err
	}
	generation := record.RefreshToken + "\x00" + record.ObtainedAt.UTC().Format(time.RFC3339Nano)
	sum := sha256.Sum256([]byte(generation))
	return hex.EncodeToString(sum[:]), nil
}

// PrepareRequest implements llm.RequestPreparer: the spec's validated
// compatibility headers and nothing that spoofs another client. The body is
// left untouched.
func (c *Copilot) PrepareRequest(_ context.Context, httpReq *http.Request, _ map[string]any, req llm.Request, _ registry.Resolved) error {
	httpReq.Header.Set("Openai-Intent", "conversation-edits")
	httpReq.Header.Set("X-Interaction-Type", "conversation-agent")
	if id := strings.TrimSpace(req.SessionID); id != "" {
		httpReq.Header.Set("X-Interaction-Id", id)
	} else if id := strings.TrimSpace(req.ThreadID); id != "" {
		httpReq.Header.Set("X-Interaction-Id", id)
	}
	if initiator := copilotInitiator(req.Messages); initiator != "" {
		httpReq.Header.Set("X-Initiator", initiator)
	}
	if copilotRequestHasImage(req.Messages) {
		httpReq.Header.Set("Copilot-Vision-Request", "true")
	} else {
		httpReq.Header.Del("Copilot-Vision-Request")
	}
	return nil
}

// RequiresStreamingComplete reports that Copilot answers complete requests
// directly; it is not a stream-only transport.
func (*Copilot) RequiresStreamingComplete() bool { return false }

// copilotInitiator reports whether the latest conversational input is the
// user's message or a tool-driven step. System and developer messages are not
// conversational input, so they are skipped.
func copilotInitiator(messages []llm.Message) string {
	for _, message := range slices.Backward(messages) {
		switch message.Role {
		case llm.RoleUser:
			return "user"
		case llm.RoleAssistant, llm.RoleTool:
			return "agent"
		}
	}
	return ""
}

// copilotRequestHasImage reports whether any message carries an image, either
// as an image part or as a tool result's image payload.
func copilotRequestHasImage(messages []llm.Message) bool {
	for _, message := range messages {
		for _, part := range message.Content {
			switch part.Kind {
			case llm.ContentImage:
				if part.Image != nil {
					return true
				}
			case llm.ContentToolResult:
				if part.ToolResult != nil && len(part.ToolResult.ImageData) > 0 {
					return true
				}
			}
		}
	}
	return false
}

func (c *Copilot) stateDir() (string, error) {
	if c.StateDir != "" {
		return c.StateDir, nil
	}
	return userdirs.DefaultStateRoot()
}

func (c *Copilot) credentials(ctx context.Context, instance string) (githubcopilot.RuntimeCredentials, error) {
	stateDir, err := c.stateDir()
	if err != nil {
		return githubcopilot.RuntimeCredentials{}, err
	}
	return githubcopilot.NewService(githubcopilot.DefaultConfig(), nil).ResolveRuntimeCredentials(ctx, stateDir, instance)
}

// copilotSignIn is the typed sign-in-required error the CLI and hub render.
func copilotSignIn(instance string, cause error) error {
	return &llm.ConfigurationError{
		Message:          fmt.Sprintf("instance %q: %v (run `evener github-copilot login --instance %s`)", instance, cause, instance),
		Cause:            errors.Join(llm.ErrSignInRequired, cause),
		ProviderInstance: instance,
	}
}

// ScopedCopilot returns a Copilot bound to stateDir that shares nothing mutable
// with the registered shared instance.
func ScopedCopilot(stateDir string) *Copilot {
	return &Copilot{StateDir: stateDir}
}

func copilotUserAgent() string {
	version := strings.TrimSpace(ClientVersion)
	if version == "" {
		version = "dev"
	}
	return "evener/" + version
}
