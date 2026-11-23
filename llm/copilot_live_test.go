package llm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/auth/githubcopilot"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/anthropic"
	"primeradiant.com/evener/llm/providers/chatcompletions"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/llm/registry"
)

// copilotLiveScenario is one reusable opt-in live leg: a wire protocol, the
// advertised endpoint that selects it, whether the request carries an image,
// an optional exact model pin, and whether this probe runs it. Task 9's
// shipped-provider smoke reuses it.
type copilotLiveScenario struct {
	Name     string
	Protocol string
	Endpoint string
	Images   bool
	ModelPin string
	Enabled  bool
}

// The GPT image leg failed at stream open with HTTP 400 "validating image item:
// image media type not supported"; that failure stays recorded. Image rows are
// re-enabled only when pinned to the exact live model that proved the path,
// claude-haiku-5.5. Task 5 enables images only for models that pass live, not
// globally.
var copilotLiveScenarios = []copilotLiveScenario{
	{Name: "chat", Protocol: registry.ProtocolOpenAIChat, Endpoint: "/chat/completions", Enabled: true},
	{Name: "chat-image", Protocol: registry.ProtocolOpenAIChat, Endpoint: "/chat/completions", Images: true, ModelPin: "claude-haiku-5.5", Enabled: true},
	{Name: "responses", Protocol: registry.ProtocolOpenAIResponses, Endpoint: "/responses", Enabled: true},
	{Name: "messages", Protocol: registry.ProtocolAnthropic, Endpoint: "/v1/messages", Enabled: true},
	{Name: "messages-image", Protocol: registry.ProtocolAnthropic, Endpoint: "/v1/messages", Images: true, ModelPin: "claude-haiku-5.5", Enabled: true},
}

// copilotLiveModel is the advertised roster facts the probe consumes.
type copilotLiveModel struct {
	ID                 string
	SupportedEndpoints []string
	PolicyState        string
	PickerEnabled      bool
	Family             string
	MaxOutputTokens    int
	MaxPromptTokens    int
	SupportsTools      bool
	SupportsVision     bool
	SupportsStreaming  bool
}

const copilotProbeStateDirEnv = "EVENER_GITHUB_COPILOT_PROBE_STATE_DIR"

// copilotProbeBootstrapDirEnv names the test-only directory holding Pi's
// identity token response (token.json) and current Copilot session
// response (session.json). The live probe converts them into its own
// scheme-bound record; the files themselves are never used as production
// credentials.
const copilotProbeBootstrapDirEnv = "EVENER_GITHUB_COPILOT_PROBE_BOOTSTRAP_DIR"

// copilotProbeStateDir returns the probe's state directory: the operator's
// session-scratch directory when EVENER_GITHUB_COPILOT_PROBE_STATE_DIR is set,
// else a throwaway t.TempDir(). A set directory is created owner-only so the
// stored record is not readable by other users; the probe never logs the path's
// record contents or tokens.
func copilotProbeStateDir(t *testing.T) string {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv(copilotProbeStateDirEnv))
	if dir == "" {
		return t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create probe state directory: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("restrict probe state directory: %v", err)
	}
	return dir
}

// copilotProbeCanReuseRecord reports whether stateDir holds a valid Copilot
// record that needs no fresh device login, letting probe reruns skip the device
// prompt. Missing, corrupt, refresh-rejected, and expired-refresh records
// return false so the probe logs in again.
func copilotProbeCanReuseRecord(stateDir, instance string, now time.Time) bool {
	record, err := githubcopilot.LoadAuth(stateDir, instance)
	return err == nil && !record.NeedsLogin(now)
}

type copilotProbeBootstrapToken struct {
	AccessToken string `json:"access_token"`
}

type copilotProbeBootstrapSession struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

// readCopilotProbeBootstrapRecord converts the scratch GitHub token response
// and Copilot session response into one validated scheme-bound record. It never
// returns or logs the token values.
func readCopilotProbeBootstrapRecord(dir, instance string, now time.Time) (githubcopilot.AuthRecord, error) {
	read := func(name string, out any) error {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("parse %s: %w", name, err)
		}
		return nil
	}
	var token copilotProbeBootstrapToken
	if err := read("token.json", &token); err != nil {
		return githubcopilot.AuthRecord{}, err
	}
	var session copilotProbeBootstrapSession
	if err := read("session.json", &session); err != nil {
		return githubcopilot.AuthRecord{}, err
	}
	if strings.TrimSpace(token.AccessToken) == "" || strings.TrimSpace(session.Token) == "" || session.ExpiresAt <= now.Unix() {
		return githubcopilot.AuthRecord{}, errors.New("scratch Copilot credentials are incomplete or expired")
	}
	record := githubcopilot.AuthRecord{
		Instance: instance, Scheme: githubcopilot.AuthScheme, Version: 1,
		AccessToken: token.AccessToken, TokenType: "bearer", ObtainedAt: now,
		CopilotToken: session.Token, CopilotExpiry: time.Unix(session.ExpiresAt, 0),
	}
	return record, nil
}

// bootstrapCopilotProbeRecord resolves the scratch record's account identity
// and persists the scheme-bound record into the probe state directory.
func bootstrapCopilotProbeRecord(ctx context.Context, t *testing.T, cfg githubcopilot.Config, stateDir, instance, bootstrapDir string) {
	t.Helper()
	record, err := readCopilotProbeBootstrapRecord(bootstrapDir, instance, time.Now())
	if err != nil {
		t.Fatalf("bootstrap scratch Copilot credentials: %v", err)
	}
	account, err := githubcopilot.FetchAccount(ctx, &http.Client{Timeout: cfg.HTTPTimeout}, cfg, record.AccessToken)
	if err != nil {
		t.Fatalf("bootstrap GitHub account lookup: %v", err)
	}
	record.AccountID, record.Email = account.ID, account.Email
	if err := githubcopilot.SaveAuth(stateDir, instance, record); err != nil {
		t.Fatalf("bootstrap probe record: %v", err)
	}
	t.Logf("bootstrapped a Copilot record for account %s from scratch credentials", account.ID)
}

// copilotProbeLogin obtains a usable Copilot record in stateDir - reusing a
// live one, converting the scratch bootstrap credentials, or running the
// shipped device login - and then forces one shipped session renewal so the
// exchange runs rather than a cached session serving the gate. Tokens, device
// codes, and record contents never reach the log.
func copilotProbeLogin(ctx context.Context, t *testing.T, stateDir string) {
	t.Helper()
	const instance = "github-copilot"
	cfg := githubcopilot.DefaultConfig()
	if id := strings.TrimSpace(os.Getenv("EVENER_GITHUB_COPILOT_CLIENT_ID")); id != "" {
		cfg.ClientID = id
	}
	service := githubcopilot.NewService(cfg, nil)
	switch bootstrapDir := strings.TrimSpace(os.Getenv(copilotProbeBootstrapDirEnv)); {
	case copilotProbeCanReuseRecord(stateDir, instance, time.Now()):
		status, err := service.Status(stateDir, instance)
		if err != nil {
			t.Fatalf("status of stored record: %v", err)
		}
		t.Logf("reusing the stored Copilot record for account %s", status.AccountID)
	case bootstrapDir != "":
		bootstrapCopilotProbeRecord(ctx, t, cfg, stateDir, instance, bootstrapDir)
	default:
		t.Log("no reusable stored Copilot record; starting device login")
		status, err := service.LoginWithDevice(ctx, stateDir, instance, func(dc githubcopilot.DeviceCode) {
			// The device prompt is the only output a human needs; tokens and device
			// codes never reach the log.
			fmt.Fprintf(os.Stdout, "\nGitHub Copilot device login: open %s and enter code %s\n\n", dc.VerificationURL, dc.UserCode)
			t.Logf("GitHub Copilot device login: open %s and enter code %s", dc.VerificationURL, dc.UserCode)
		})
		if err != nil {
			t.Fatalf("device login: %v", err)
		}
		t.Logf("device login authorized for account %s", status.AccountID)
	}

	// Force one Copilot session renewal through the shipped service, without ever
	// printing the token. The record keeps its GitHub token, so this exercises
	// the session exchange rather than a fresh login.
	record, err := githubcopilot.LoadAuth(stateDir, instance)
	if err != nil {
		t.Fatalf("load record: %v", err)
	}
	record.CopilotExpiry = time.Now().Add(-time.Minute)
	if err := githubcopilot.SaveAuth(stateDir, instance, record); err != nil {
		t.Fatalf("expire Copilot session: %v", err)
	}
	creds, err := service.ResolveRuntimeCredentials(ctx, stateDir, instance)
	if err != nil {
		t.Fatalf("forced Copilot session renewal: %v", err)
	}
	if creds.BearerToken == "" {
		t.Fatal("forced session renewal returned no bearer token")
	}
	t.Log("forced Copilot session renewal succeeded")
}

// copilotLiveProbe drives the real Copilot service, listing path, and adapters
// against GitHub. It needs EVENER_GITHUB_COPILOT_E2E=1 plus a human completing
// the device prompt; every other test run skips it.
func TestGitHubCopilotLiveProbe(t *testing.T) {
	if os.Getenv("EVENER_GITHUB_COPILOT_E2E") != "1" {
		t.Skip("set EVENER_GITHUB_COPILOT_E2E=1 to run the live GitHub Copilot probe")
	}
	const instance = "github-copilot"
	stateDir := copilotProbeStateDir(t)
	baseURL := "https://api.githubcopilot.com"
	if override := strings.TrimSpace(os.Getenv("EVENER_GITHUB_COPILOT_BASE_URL")); override != "" {
		baseURL = override
	}

	// One budget for login, refresh, listing, and every scenario keeps the probe
	// inside `go test`'s default ten-minute timeout. Authorize promptly.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancelProbe()
	copilotProbeLogin(probeCtx, t, stateDir)

	recorder := &copilotHeaderRecorder{base: http.DefaultTransport}
	client := &http.Client{Transport: recorder, Timeout: 2 * time.Minute}
	restore := installCopilotProtocolClient(client)
	defer restore()

	// The raw /models fetch supplies the family, endpoint, and capability facts
	// the shipped ListModels parser does not carry; the real protocol listing
	// proves the same IDs come back through Evener's own model-listing path.
	roster, err := fetchCopilotRoster(probeCtx, client, baseURL, stateDir, instance)
	if err != nil {
		t.Fatalf("roster fetch: %v", err)
	}
	if len(roster) == 0 {
		t.Fatal("GitHub Copilot advertised no models")
	}
	listingRes := copilotResolved(baseURL, "", "", "", nil)
	listingCtx := llm.WithScopedAuthenticator(probeCtx, registry.AuthOAuthGitHubCopilot, tokenauth.ScopedCopilot(stateDir))
	listed, err := chatcompletions.DefaultProtocol.ListModels(listingCtx, listingRes)
	if err != nil {
		t.Fatalf("ListModels through the Chat Completions protocol: %v", err)
	}
	listedIDs := make([]string, 0, len(listed))
	for _, m := range listed {
		listedIDs = append(listedIDs, m.ID)
	}
	t.Logf("Copilot advertised %d models through the real Chat Completions listing", len(listed))

	runs := planCopilotLiveRuns(roster)
	if len(runs) == 0 {
		logCopilotRosterDiagnostics(t, roster)
		t.Fatal("no advertised family/protocol combination supports a tool loop")
	}
	requireCopilotRunsListed(t, runs, listedIDs)
	probeCtx = llm.WithScopedAuthenticator(probeCtx, registry.AuthOAuthGitHubCopilot, tokenauth.ScopedCopilot(stateDir))
	for _, run := range runs {
		t.Run(run.family+"/"+run.scenario.Name, func(t *testing.T) {
			if run.MissingPin {
				logCopilotRosterDiagnostics(t, roster)
				t.Fatalf("enabled pinned scenario %q wants live model %q, but no advertised model offered it with the required endpoint, vision, tools, and streaming", run.scenario.Name, run.scenario.ModelPin)
			}
			runCopilotLiveScenario(probeCtx, t, run, baseURL, recorder)
		})
	}
}

// TestGitHubCopilotShippedLiveGate drives the shipped Copilot provider end to
// end for the setup surface: the real registry record, the shipped scoped
// authenticator, model selection through llm.Client's production listing, and
// the production request path. Every enabled copilotLiveScenario is mandatory,
// including both exact claude-haiku-5.5 image legs. Without
// EVENER_GITHUB_COPILOT_E2E=1 it skips, so default runs stay deterministic and
// network-free.
func TestGitHubCopilotShippedLiveGate(t *testing.T) {
	if os.Getenv("EVENER_GITHUB_COPILOT_E2E") != "1" {
		t.Skip("set EVENER_GITHUB_COPILOT_E2E=1 to run the shipped GitHub Copilot live gate")
	}
	const instance = "github-copilot"
	stateDir := copilotProbeStateDir(t)
	gateCtx, cancelGate := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancelGate()
	copilotProbeLogin(gateCtx, t, stateDir)

	baseURL := "https://api.githubcopilot.com"
	if override := strings.TrimSpace(os.Getenv("EVENER_GITHUB_COPILOT_BASE_URL")); override != "" {
		baseURL = override
	}
	reg, err := registry.Load(
		registry.WithStateRoot(stateDir),
		registry.WithNoUserLayer(),
		registry.WithOffline(true),
		registry.WithoutCache(),
		registry.WithEnv(func(key string) (string, bool) {
			if key == "GITHUB_COPILOT_BASE_URL" && strings.TrimSpace(os.Getenv("EVENER_GITHUB_COPILOT_BASE_URL")) != "" {
				return baseURL, true
			}
			return "", false
		}),
	)
	if err != nil {
		t.Fatalf("load the real registry: %v", err)
	}
	if _, ok := reg.Instance(instance); !ok {
		t.Fatalf("the real registry does not list %q although its OAuth record is ready (state root %s)", instance, reg.StateRoot())
	}

	// The shipped client, not a hand-built row: requests resolve through the
	// registry record, and the shipped authenticator supplies the credential.
	recorder := &copilotHeaderRecorder{base: http.DefaultTransport}
	restore := installCopilotProtocolClient(&http.Client{Transport: recorder, Timeout: 2 * time.Minute})
	defer restore()
	client := llm.NewClient(llm.WithRegistry(reg), llm.WithClientStateDir(""))
	gateCtx = llm.WithScopedAuthenticator(gateCtx, registry.AuthOAuthGitHubCopilot, tokenauth.ScopedCopilot(stateDir))

	listing, err := client.Models(gateCtx, instance)
	if err != nil {
		t.Fatalf("live model listing through the shipped client: %v", err)
	}
	if !listing.Live || !listing.Usable {
		t.Fatalf("live model listing through the shipped client: live=%v usable=%v models=%d", listing.Live, listing.Usable, len(listing.Models))
	}
	t.Logf("the shipped listing offered %d visible models", len(listing.Models))

	// The raw roster carries the family, endpoint, and vision facts the shipped
	// listing parser does not publish; the shipped listing above is what proved
	// those models are the ones this account actually offers.
	roster, err := fetchCopilotRoster(gateCtx, &http.Client{Transport: recorder, Timeout: 2 * time.Minute}, baseURL, stateDir, instance)
	if err != nil {
		t.Fatalf("roster fetch: %v", err)
	}
	if len(roster) == 0 {
		t.Fatal("GitHub Copilot advertised no models")
	}
	runs := planCopilotLiveRuns(roster)
	for _, run := range runs {
		if run.MissingPin {
			logCopilotRosterDiagnostics(t, roster)
			t.Fatalf("enabled pinned scenario %q wants live model %q, but no advertised model offered it with the required endpoint, vision, tools, and streaming", run.scenario.Name, run.scenario.ModelPin)
		}
	}
	for _, scenario := range copilotLiveScenarios {
		if !scenario.Enabled {
			continue
		}
		if !copilotLiveScenarioRan(runs, scenario) {
			logCopilotRosterDiagnostics(t, roster)
			t.Fatalf("enabled scenario %q is not covered by the live roster; every enabled scenario is mandatory", scenario.Name)
		}
	}
	listedIDs := make([]string, 0, len(listing.Models))
	for _, row := range listing.Models {
		listedIDs = append(listedIDs, row.ModelID)
	}
	requireCopilotRunsListed(t, runs, listedIDs)
	for _, run := range runs {
		t.Run(run.family+"/"+run.scenario.Name, func(t *testing.T) {
			runCopilotShippedScenario(gateCtx, t, client, run, recorder)
		})
	}
}

// runCopilotShippedScenario drives one scenario through the shipped client. The
// registry picks the production-preferred route the account advertises for the
// model; when the scenario's approved route is the model's other advertised
// endpoint, the row is retargeted to it while the registry's transport and the
// shipped authenticator stay in charge of the request.
func runCopilotShippedScenario(parent context.Context, t *testing.T, client *llm.Client, run copilotLiveRun, recorder *copilotHeaderRecorder) {
	t.Helper()
	resolved, err := client.Resolve("github-copilot/" + run.model.ID)
	if err != nil {
		t.Fatalf("the shipped registry does not resolve live model %q: %v", run.model.ID, err)
	}
	if resolved.Transport.Auth != registry.AuthOAuthGitHubCopilot {
		t.Fatalf("resolved transport auth = %q, want %q", resolved.Transport.Auth, registry.AuthOAuthGitHubCopilot)
	}
	requireCopilotLiveToolLoop(t, run.scenario, run.family, run.model.ID, recorder, func(req llm.Request) *llm.Response {
		if resolved.Protocol == run.scenario.Protocol {
			return streamShippedCopilotOnce(parent, t, client, req)
		}
		route := resolved
		route.Protocol = run.scenario.Protocol
		route.Model.Protocol = run.scenario.Protocol
		route.Transport.Endpoint = run.scenario.Endpoint
		route.Transport.StreamEndpoint = run.scenario.Endpoint
		return streamCopilotOnce(parent, t, route, req)
	})
}

// streamShippedCopilotOnce drives one production llm.Client stream and returns
// its accumulated response. In-band stream errors are failures: the gate proves
// live behavior, not a fallback.
func streamShippedCopilotOnce(parent context.Context, t *testing.T, client *llm.Client, req llm.Request) *llm.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	stream, err := client.Stream(ctx, req)
	if err != nil {
		t.Fatalf("shipped client stream open: %v", err)
	}
	defer stream.Close()
	acc := llm.NewStreamAccumulator()
	var streamErr error
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventError && ev.Err != nil {
			streamErr = ev.Err
		}
		acc.Process(ev)
	}
	if streamErr != nil {
		t.Fatalf("shipped client stream error: %v", streamErr)
	}
	resp := acc.Response()
	if resp == nil {
		t.Fatal("shipped client stream produced no final response")
	}
	return resp
}

// logCopilotRosterDiagnostics prints the non-secret roster facts that explain
// why a scenario found no model. Tokens, headers, records, and bodies are never
// logged.
func logCopilotRosterDiagnostics(t *testing.T, roster []copilotLiveModel) {
	t.Helper()
	for _, model := range roster {
		t.Logf("live roster model %q: endpoints=%v policy=%q picker=%v family=%q tools=%v streaming=%v vision=%v",
			model.ID, model.SupportedEndpoints, model.PolicyState, model.PickerEnabled, model.Family,
			model.SupportsTools, model.SupportsStreaming, model.SupportsVision)
	}
}

// TestCopilotProbeStateDirUsesPrivateEnvDirectory pins the opt-in persistent
// state directory: the probe uses that exact path, creates it owner-only, and
// keeps using it on a rerun instead of minting a new temp directory.
func TestCopilotProbeStateDirUsesPrivateEnvDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "scratch", "copilot-probe-auth")
	t.Setenv(copilotProbeStateDirEnv, dir)
	if got := copilotProbeStateDir(t); got != dir {
		t.Fatalf("state dir = %q, want %q", got, dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("state dir mode = %o, want 700", perm)
		}
	}
	if got := copilotProbeStateDir(t); got != dir {
		t.Fatalf("second call = %q, want the persistent directory %q", got, dir)
	}
}

// TestCopilotProbeReusesOnlyRefreshableRecord pins the skip-login decision:
// only a valid, not-login-required record is reused; missing, corrupt,
// refresh-rejected, and expired-refresh records start a fresh device login.
func TestCopilotProbeReusesOnlyRefreshableRecord(t *testing.T) {
	const instance = "github-copilot"
	now := time.Now()
	dir := t.TempDir()
	if copilotProbeCanReuseRecord(dir, instance, now) {
		t.Fatal("missing record reported reusable")
	}
	record := githubcopilot.AuthRecord{
		Instance: instance, Scheme: githubcopilot.AuthScheme, Version: 1,
		AccessToken: "probe-access", RefreshToken: "probe-refresh",
		TokenType: "Bearer", Scope: "read:user",
		ObtainedAt: now.Add(-time.Hour), AccessExpiry: now.Add(-time.Minute),
		RefreshExpiry: now.Add(24 * time.Hour), AccountID: "probe-account",
		CopilotToken: "probe-copilot", CopilotExpiry: now.Add(10 * time.Minute),
	}
	if err := githubcopilot.SaveAuth(dir, instance, record); err != nil {
		t.Fatalf("save valid record: %v", err)
	}
	if !copilotProbeCanReuseRecord(dir, instance, now) {
		t.Fatal("valid refreshable record reported not reusable")
	}
	rejected := record
	rejected.RefreshRejected = true
	if err := githubcopilot.SaveAuth(dir, instance, rejected); err != nil {
		t.Fatalf("save refresh-rejected record: %v", err)
	}
	if copilotProbeCanReuseRecord(dir, instance, now) {
		t.Fatal("refresh-rejected record reported reusable")
	}
	expired := record
	expired.RefreshExpiry = now.Add(-time.Minute)
	if err := githubcopilot.SaveAuth(dir, instance, expired); err != nil {
		t.Fatalf("save expired-refresh record: %v", err)
	}
	if copilotProbeCanReuseRecord(dir, instance, now) {
		t.Fatal("expired-refresh record reported reusable")
	}
	if err := os.WriteFile(githubcopilot.AuthFilePath(dir, instance), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt record: %v", err)
	}
	if copilotProbeCanReuseRecord(dir, instance, now) {
		t.Fatal("corrupt record reported reusable")
	}
}

// TestCopilotProbeBootstrapReadsScratchRecord pins the test-only bootstrap: the
// Pi-identity GitHub token response and Copilot session response become one
// scheme-bound record without either token being parsed from anywhere else.
func TestCopilotProbeBootstrapReadsScratchRecord(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 22, 7, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token.json"),
		[]byte(`{"access_token":"github-bootstrap","token_type":"bearer","scope":""}`), 0o600); err != nil {
		t.Fatalf("write token.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"),
		[]byte(fmt.Sprintf(`{"token":"copilot-bootstrap","expires_at":%d}`, now.Add(30*time.Minute).Unix())), 0o600); err != nil {
		t.Fatalf("write session.json: %v", err)
	}
	record, err := readCopilotProbeBootstrapRecord(dir, "github-copilot", now)
	if err != nil {
		t.Fatalf("readCopilotProbeBootstrapRecord() error = %v", err)
	}
	if record.AccessToken != "github-bootstrap" || record.CopilotToken != "copilot-bootstrap" {
		t.Fatal("bootstrap record did not carry the scratch GitHub and Copilot tokens")
	}
	if record.Scheme != githubcopilot.AuthScheme || record.Instance != "github-copilot" || record.Version != 1 {
		t.Fatalf("bootstrap record identity = scheme %q instance %q version %d", record.Scheme, record.Instance, record.Version)
	}
	if !record.CopilotExpiry.Equal(now.Add(30*time.Minute)) || !record.ObtainedAt.Equal(now) {
		t.Fatalf("bootstrap times = session %v obtained %v", record.CopilotExpiry, record.ObtainedAt)
	}
	if record.RefreshToken != "" || record.RefreshRejected {
		t.Fatal("bootstrap record invented GitHub refresh state")
	}
}

func TestCopilotProbeBootstrapRejectsExpiredOrIncompleteFiles(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 22, 7, 0, 0, 0, time.UTC)
	cases := map[string]map[string]string{
		"expired session": {
			"token.json":   `{"access_token":"github-bootstrap"}`,
			"session.json": fmt.Sprintf(`{"token":"copilot-bootstrap","expires_at":%d}`, now.Add(-time.Minute).Unix()),
		},
		"missing github token": {
			"token.json":   `{}`,
			"session.json": fmt.Sprintf(`{"token":"copilot-bootstrap","expires_at":%d}`, now.Add(30*time.Minute).Unix()),
		},
		"missing session token": {
			"token.json":   `{"access_token":"github-bootstrap"}`,
			"session.json": fmt.Sprintf(`{"expires_at":%d}`, now.Add(30*time.Minute).Unix()),
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for file, body := range files {
				if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600); err != nil {
					t.Fatalf("write %s: %v", file, err)
				}
			}
			if _, err := readCopilotProbeBootstrapRecord(dir, "github-copilot", now); err == nil {
				t.Fatal("bootstrap accepted invalid scratch credentials")
			}
		})
	}
}

// TestPlanCopilotLiveRunsFallsBackToChatCompletionsWithoutEndpointMetadata pins
// the approved fallback: a tool/stream-capable model that advertises no
// supported_endpoints still selects the Chat Completions failsafe rather than
// producing zero live runs.
func TestPlanCopilotLiveRunsFallsBackToChatCompletionsWithoutEndpointMetadata(t *testing.T) {
	t.Parallel()
	model := copilotLiveModel{
		ID:                "gpt-no-endpoint-metadata",
		Family:            "gpt",
		PickerEnabled:     true,
		SupportsTools:     true,
		SupportsStreaming: true,
	}
	runs := planCopilotLiveRuns([]copilotLiveModel{model})
	if len(runs) == 0 {
		t.Fatal("planner returned no run for a tool/stream-capable model with no endpoint metadata")
	}
	chat := false
	for _, run := range runs {
		if run.scenario.Name == "chat" && run.scenario.Endpoint == "/chat/completions" && run.model.ID == model.ID {
			chat = true
		}
	}
	if !chat {
		t.Fatalf("planner did not select /chat/completions for %s: %+v", model.ID, runs)
	}
}

// TestPlanCopilotLiveRunsSkipsModelsHiddenFromThePicker proves the plan covers
// the models the account is offered rather than internal roster entries that
// reject completions. A non-picker model of the same family appears first, so
// selecting it instead of the picker-enabled model fails the test.
func TestPlanCopilotLiveRunsSkipsModelsHiddenFromThePicker(t *testing.T) {
	t.Parallel()
	roster := []copilotLiveModel{
		{
			ID:                 "internal-model",
			Family:             "gpt",
			SupportedEndpoints: []string{"/chat/completions"},
			SupportsTools:      true,
			SupportsStreaming:  true,
		},
		{
			ID:                 "picker-model",
			Family:             "gpt",
			PickerEnabled:      true,
			SupportedEndpoints: []string{"/chat/completions"},
			SupportsTools:      true,
			SupportsStreaming:  true,
		},
	}
	runs := planCopilotLiveRuns(roster)
	chat := false
	for _, run := range runs {
		if run.model.ID == "internal-model" {
			t.Fatalf("planner selected picker-hidden model %q", run.model.ID)
		}
		if run.scenario.Name == "chat" && run.model.ID == "picker-model" {
			chat = true
		}
	}
	if !chat {
		t.Fatalf("planner did not select the picker-enabled model: %+v", runs)
	}
}

// TestPlanCopilotLiveRunsPrefersResponsesOverChatForOrdinaryText proves the
// ordinary text/tool run follows the production route preference rather than
// exercising every advertised secondary endpoint. A model offering both
// Responses and Chat must produce exactly one ordinary run, on Responses.
func TestPlanCopilotLiveRunsPrefersResponsesOverChatForOrdinaryText(t *testing.T) {
	t.Parallel()
	roster := []copilotLiveModel{
		{
			ID:                 "dual-endpoint-model",
			Family:             "gpt",
			PickerEnabled:      true,
			SupportedEndpoints: []string{"/chat/completions", "/responses"},
			SupportsTools:      true,
			SupportsStreaming:  true,
		},
	}
	runs := planCopilotLiveRuns(roster)
	var ordinary []copilotLiveRun
	for _, run := range runs {
		if !run.scenario.Images {
			ordinary = append(ordinary, run)
		}
	}
	if len(ordinary) != 1 {
		t.Fatalf("ordinary text runs = %+v, want exactly one", ordinary)
	}
	if ordinary[0].scenario.Endpoint != "/responses" {
		t.Fatalf("ordinary text route = %q, want /responses", ordinary[0].scenario.Endpoint)
	}
}

// TestPlanCopilotLiveRunsPinsImagesToExactModel proves an image scenario runs
// only against its exact pinned model even when another vision-capable model of
// the same family appears first in the roster.
func TestPlanCopilotLiveRunsPinsImagesToExactModel(t *testing.T) {
	t.Parallel()
	roster := []copilotLiveModel{
		{
			ID:                 "claude-sonnet-4.5",
			Family:             "claude",
			PickerEnabled:      true,
			SupportedEndpoints: []string{"/chat/completions", "/v1/messages"},
			SupportsTools:      true,
			SupportsStreaming:  true,
			SupportsVision:     true,
		},
		{
			ID:                 "claude-haiku-5.5",
			Family:             "claude",
			PickerEnabled:      true,
			SupportedEndpoints: []string{"/chat/completions", "/v1/messages"},
			SupportsTools:      true,
			SupportsStreaming:  true,
			SupportsVision:     true,
		},
	}
	runs := planCopilotLiveRuns(roster)
	for _, name := range []string{"chat-image", "messages-image"} {
		var got []string
		for _, run := range runs {
			if run.scenario.Name == name && !run.MissingPin {
				got = append(got, run.model.ID)
			}
		}
		if !slices.Equal(got, []string{"claude-haiku-5.5"}) {
			t.Fatalf("%s models = %v, want exactly [claude-haiku-5.5]", name, got)
		}
	}
}

// TestPlanCopilotLiveRunsMarksEnabledPinnedScenarioWithNoMatchingModel proves a
// near-miss ID and a different model never satisfy the pin, and that each
// enabled pinned scenario yields a MissingPin run instead of silently
// disappearing from the plan.
func TestPlanCopilotLiveRunsMarksEnabledPinnedScenarioWithNoMatchingModel(t *testing.T) {
	t.Parallel()
	roster := []copilotLiveModel{
		{
			ID:                 "claude-haiku-5.5-preview",
			Family:             "claude",
			PickerEnabled:      true,
			SupportedEndpoints: []string{"/chat/completions", "/v1/messages"},
			SupportsTools:      true,
			SupportsStreaming:  true,
			SupportsVision:     true,
		},
		{
			ID:                 "claude-sonnet-4.5",
			Family:             "claude",
			PickerEnabled:      true,
			SupportedEndpoints: []string{"/chat/completions", "/v1/messages"},
			SupportsTools:      true,
			SupportsStreaming:  true,
			SupportsVision:     true,
		},
	}
	runs := planCopilotLiveRuns(roster)
	for _, name := range []string{"chat-image", "messages-image"} {
		found := false
		for _, run := range runs {
			if run.scenario.Name != name {
				continue
			}
			found = true
			if !run.MissingPin {
				t.Fatalf("%s ran against %q instead of failing on the absent pinned model", name, run.model.ID)
			}
			if run.scenario.ModelPin != "claude-haiku-5.5" {
				t.Fatalf("%s pin = %q, want claude-haiku-5.5", name, run.scenario.ModelPin)
			}
		}
		if !found {
			t.Fatalf("planner silently dropped enabled pinned scenario %s", name)
		}
	}
}

// installCopilotProtocolClient points every real protocol at the recording
// client so the probe can prove the headers the adapters sent.
func installCopilotProtocolClient(client *http.Client) func() {
	oldChat, oldResponses, oldAnthropic := chatcompletions.DefaultProtocol.Client, responses.DefaultProtocol.Client, anthropic.DefaultProtocol.Client
	chatcompletions.DefaultProtocol.Client = client
	responses.DefaultProtocol.Client = client
	anthropic.DefaultProtocol.Client = client
	return func() {
		chatcompletions.DefaultProtocol.Client = oldChat
		responses.DefaultProtocol.Client = oldResponses
		anthropic.DefaultProtocol.Client = oldAnthropic
	}
}

func fetchCopilotRoster(ctx context.Context, client *http.Client, baseURL, stateDir, instance string) ([]copilotLiveModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if err := tokenauth.ScopedCopilot(stateDir).Apply(ctx, req, copilotResolved(baseURL, "", "", "", nil)); err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data []struct {
			ID                 string   `json:"id"`
			ModelPickerEnabled bool     `json:"model_picker_enabled"`
			SupportedEndpoints []string `json:"supported_endpoints"`
			Policy             struct {
				State string `json:"state"`
			} `json:"policy"`
			Capabilities struct {
				Family string `json:"family"`
				Limits struct {
					MaxOutputTokens int `json:"max_output_tokens"`
					MaxPromptTokens int `json:"max_prompt_tokens"`
				} `json:"limits"`
				Supports struct {
					ToolCalls bool `json:"tool_calls"`
					Vision    bool `json:"vision"`
					Streaming bool `json:"streaming"`
				} `json:"supports"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	roster := make([]copilotLiveModel, 0, len(payload.Data))
	for _, row := range payload.Data {
		if strings.TrimSpace(row.ID) == "" {
			continue
		}
		roster = append(roster, copilotLiveModel{
			ID:                 row.ID,
			SupportedEndpoints: row.SupportedEndpoints,
			PolicyState:        row.Policy.State,
			PickerEnabled:      row.ModelPickerEnabled,
			Family:             row.Capabilities.Family,
			MaxOutputTokens:    row.Capabilities.Limits.MaxOutputTokens,
			MaxPromptTokens:    row.Capabilities.Limits.MaxPromptTokens,
			SupportsTools:      row.Capabilities.Supports.ToolCalls,
			SupportsVision:     row.Capabilities.Supports.Vision,
			SupportsStreaming:  row.Capabilities.Supports.Streaming,
		})
	}
	return roster, nil
}

type copilotLiveRun struct {
	scenario copilotLiveScenario
	family   string
	model    copilotLiveModel
	// MissingPin marks an enabled pinned scenario that no advertised model
	// satisfied. The live probe must fail it with roster diagnostics, never
	// silently skip it.
	MissingPin bool
}

// copilotLivePinMissingFamily names the probe run that reports an enabled
// pinned scenario no advertised model satisfied.
const copilotLivePinMissingFamily = "pin-missing"

// copilotProductionRoutePreference is the approved production route order:
// Messages first (Claude), otherwise Responses, then Chat. The probe exercises
// only the preferred route for ordinary text/tool scenarios; advertised
// secondary endpoints are not separate live targets.
var copilotProductionRoutePreference = []string{"/v1/messages", "/responses", "/chat/completions"}

// planCopilotLiveRuns picks one production route per family for ordinary
// text/tool scenarios, avoiding repeated protocols for every model in a family.
// Only picker-enabled models are advertised to customers; internal roster
// entries return HTTP 400 for completions and are not probe targets. An enabled
// scenario with an exact model pin runs only on that model; when none matches,
// the plan carries a MissingPin run so the live probe fails loudly with
// diagnostics.
func planCopilotLiveRuns(roster []copilotLiveModel) []copilotLiveRun {
	seenFamily := map[string]bool{}
	var runs []copilotLiveRun
	for _, endpoint := range copilotProductionRoutePreference {
		scenario, ok := copilotOrdinaryScenario(endpoint)
		if !ok {
			continue
		}
		for _, model := range roster {
			if strings.EqualFold(model.PolicyState, "disabled") || !model.PickerEnabled || !model.SupportsTools || !model.SupportsStreaming {
				continue
			}
			family := copilotLiveFamily(model.Family)
			if seenFamily[family] || !copilotLiveScenarioMatches(scenario, model) {
				continue
			}
			seenFamily[family] = true
			runs = append(runs, copilotLiveRun{scenario: scenario, family: family, model: model})
		}
	}
	for _, scenario := range copilotLiveScenarios {
		if !scenario.Enabled || scenario.ModelPin == "" {
			continue
		}
		for _, model := range roster {
			if strings.EqualFold(model.PolicyState, "disabled") || !model.PickerEnabled || !model.SupportsTools || !model.SupportsStreaming {
				continue
			}
			if !copilotLiveScenarioMatches(scenario, model) {
				continue
			}
			runs = append(runs, copilotLiveRun{scenario: scenario, family: copilotLiveFamily(model.Family), model: model})
			break
		}
	}
	for _, scenario := range copilotLiveScenarios {
		if !scenario.Enabled || scenario.ModelPin == "" || copilotLiveScenarioRan(runs, scenario) {
			continue
		}
		runs = append(runs, copilotLiveRun{scenario: scenario, family: copilotLivePinMissingFamily, MissingPin: true})
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].family != runs[j].family {
			return runs[i].family < runs[j].family
		}
		return runs[i].scenario.Name < runs[j].scenario.Name
	})
	return runs
}

// copilotOrdinaryScenario returns the enabled text/tool scenario for one
// production route endpoint.
func copilotOrdinaryScenario(endpoint string) (copilotLiveScenario, bool) {
	for _, scenario := range copilotLiveScenarios {
		if scenario.Enabled && !scenario.Images && scenario.Endpoint == endpoint {
			return scenario, true
		}
	}
	return copilotLiveScenario{}, false
}

// copilotLiveScenarioMatches applies the scenario's exact pin and capability
// requirements to one advertised model.
func copilotLiveScenarioMatches(scenario copilotLiveScenario, model copilotLiveModel) bool {
	if scenario.ModelPin != "" && model.ID != scenario.ModelPin {
		return false
	}
	if !copilotAdvertisesEndpoint(model, scenario.Endpoint) {
		return false
	}
	return !scenario.Images || model.SupportsVision
}

func copilotLiveScenarioRan(runs []copilotLiveRun, scenario copilotLiveScenario) bool {
	for _, run := range runs {
		if run.scenario.Name == scenario.Name {
			return true
		}
	}
	return false
}

// requireCopilotRunsListed proves the live model listing offers every model a
// selected run will drive. The raw roster also carries rows the listing
// deliberately hides (unsupported, non-picker, embedding), so the contract is
// one-way: a run the probe plans must be visible, not every roster row.
func requireCopilotRunsListed(t *testing.T, runs []copilotLiveRun, listedIDs []string) {
	t.Helper()
	listed := make(map[string]bool, len(listedIDs))
	for _, id := range listedIDs {
		listed[id] = true
	}
	for _, run := range runs {
		if run.MissingPin {
			continue
		}
		if !listed[run.model.ID] {
			t.Fatalf("scenario %q selects live model %q, which the live listing did not offer", run.scenario.Name, run.model.ID)
		}
	}
}

// copilotAdvertisesEndpoint applies the approved roster-metadata fallback: a
// model that omits supported_endpoints entirely is treated as Chat
// Completions-capable, matching the OpenCode reference. Explicit endpoint
// metadata is always authoritative.
func copilotAdvertisesEndpoint(model copilotLiveModel, endpoint string) bool {
	if len(model.SupportedEndpoints) == 0 {
		return endpoint == "/chat/completions"
	}
	return slices.Contains(model.SupportedEndpoints, endpoint)
}

func copilotLiveFamily(family string) string {
	family = strings.ToLower(strings.TrimSpace(family))
	switch {
	case family == "":
		return "unknown"
	case strings.HasPrefix(family, "claude"):
		return "claude"
	case strings.HasPrefix(family, "gpt"):
		return "gpt"
	case strings.HasPrefix(family, "gemini"):
		return "gemini"
	}
	return family
}

const copilotLiveToolName = "get_secret_code"

func runCopilotLiveScenario(parent context.Context, t *testing.T, run copilotLiveRun, baseURL string, recorder *copilotHeaderRecorder) {
	t.Helper()
	if !run.scenario.Enabled {
		t.Skipf("%s is not enabled yet", run.scenario.Name)
	}
	row := copilotResolved(baseURL, run.model.ID, run.model.ID, run.scenario.Protocol, &run.model)
	requireCopilotLiveToolLoop(t, run.scenario, run.family, run.model.ID, recorder, func(req llm.Request) *llm.Response {
		return streamCopilotOnce(parent, t, row, req)
	})
}

// requireCopilotLiveToolLoop proves one scenario's live tool round and its
// continuation through whatever shipped transport `stream` drives.
func requireCopilotLiveToolLoop(t *testing.T, scenario copilotLiveScenario, family, modelID string, recorder *copilotHeaderRecorder, stream func(llm.Request) *llm.Response) {
	t.Helper()
	tool := llm.ToolDefinition{
		Name:        copilotLiveToolName,
		Description: "Returns the secret code for this probe. Call it once and use its result.",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
	history := []llm.Message{llm.User("Call the get_secret_code tool once, then tell me the exact code it returned.")}
	if scenario.Images {
		history[0].Content = append(history[0].Content, llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{Data: livePNG(t), MediaType: "image/png"}})
	}
	interactionID := "copilot-live-" + strings.NewReplacer("/", "-", " ", "-").Replace(family+"-"+scenario.Name)

	before := len(recorder.snapshot())
	first := stream(llm.Request{
		Model: modelID, Provider: "github-copilot", Messages: history,
		Tools: []llm.ToolDefinition{tool}, MaxTokens: new(1024),
		SessionID: interactionID, ThreadID: interactionID,
	})
	calls := first.ToolCalls()
	var call *llm.ToolCallData
	for i := range calls {
		if calls[i].Name == copilotLiveToolName {
			call = &calls[i]
			break
		}
	}
	if call == nil {
		t.Fatalf("no streamed %s tool call: %v", copilotLiveToolName, toolCallNames(calls))
	}
	history = append(history, first.Message, llm.ToolResultNamed(call.ID, call.Name, "SECRET-CODE-7", false))
	second := stream(llm.Request{
		Model: modelID, Provider: "github-copilot", Messages: history,
		Tools: []llm.ToolDefinition{tool}, MaxTokens: new(1024),
		SessionID: interactionID, ThreadID: interactionID,
	})
	if strings.TrimSpace(second.Text()) == "" {
		t.Fatal("final response after the tool result was empty")
	}

	sent := recorder.snapshot()[before:]
	if len(sent) != 2 {
		t.Fatalf("HTTP requests = %d (%v), want 2: the tool round and the continuation", len(sent), sent)
	}
	if sent[0].Initiator != "user" {
		t.Fatalf("first request X-Initiator = %q, want user", sent[0].Initiator)
	}
	if sent[1].Initiator != "agent" {
		t.Fatalf("tool-result request X-Initiator = %q, want agent", sent[1].Initiator)
	}
	for _, req := range sent {
		if req.InteractionID != interactionID {
			t.Fatalf("X-Interaction-Id = %q, want %q", req.InteractionID, interactionID)
		}
		if !req.HadAuthorization {
			t.Fatal("request carried no Authorization header")
		}
		if !strings.HasPrefix(req.UserAgent, "evener/") {
			t.Fatalf("User-Agent = %q, want Evener's own agent on model requests", req.UserAgent)
		}
		if req.EditorVersion != githubcopilot.EditorVersion ||
			req.EditorPluginVersion != githubcopilot.EditorPluginVersion ||
			req.IntegrationID != githubcopilot.IntegrationID {
			t.Fatalf("compatibility headers = editor %q plugin %q integration %q",
				req.EditorVersion, req.EditorPluginVersion, req.IntegrationID)
		}
	}
	if scenario.Images {
		for _, req := range sent {
			if req.Vision != "true" {
				t.Fatalf("image request Copilot-Vision-Request = %q, want true", req.Vision)
			}
		}
	}
	t.Logf("%s/%s model=%s text=%d bytes", family, scenario.Name, modelID, len(second.Text()))
}

// streamCopilotOnce drives one real protocol stream and returns its accumulated
// response. In-band stream errors are failures: the probe proves live behavior,
// not a fallback.
func streamCopilotOnce(parent context.Context, t *testing.T, row registry.Resolved, req llm.Request) *llm.Response {
	t.Helper()
	protocol, ok := llm.ProtocolFor(row.Protocol)
	if !ok {
		t.Fatalf("protocol %q is not registered", row.Protocol)
	}
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	stream, err := protocol.Stream(ctx, req, row)
	if err != nil {
		t.Fatalf("stream open: %v", err)
	}
	defer stream.Close()
	acc := llm.NewStreamAccumulator()
	var streamErr error
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventError && ev.Err != nil {
			streamErr = ev.Err
		}
		acc.Process(ev)
	}
	if streamErr != nil {
		t.Fatalf("stream error: %v", streamErr)
	}
	resp := acc.Response()
	if resp == nil {
		t.Fatal("stream produced no final response")
	}
	return resp
}

func copilotResolved(baseURL, modelID, wireID, protocol string, model *copilotLiveModel) registry.Resolved {
	endpoint := map[string]string{
		registry.ProtocolOpenAIChat:      "/chat/completions",
		registry.ProtocolOpenAIResponses: "/responses",
		registry.ProtocolAnthropic:       "/v1/messages",
	}[protocol]
	caps := registry.Caps{Tools: new(true), Fields: registry.Baseline(protocol)}
	if model != nil {
		caps.ContextWindow = new(model.MaxPromptTokens)
		caps.MaxOutputTokens = new(model.MaxOutputTokens)
	}
	return registry.Resolved{
		Instance:   "github-copilot",
		ProviderID: "github-copilot",
		Protocol:   protocol,
		ModelID:    modelID,
		WireID:     wireID,
		Transport: registry.Transport{
			Auth:                registry.AuthOAuthGitHubCopilot,
			BaseURL:             baseURL,
			Endpoint:            endpoint,
			StreamEndpoint:      endpoint,
			ModelsEndpoint:      "/models",
			CountTokensEndpoint: registry.EndpointUnsupported,
		},
		Caps: caps,
	}
}

type copilotSentRequest struct {
	Path                string
	Initiator           string
	InteractionID       string
	Vision              string
	UserAgent           string
	EditorVersion       string
	EditorPluginVersion string
	IntegrationID       string
	HadAuthorization    bool
}

type copilotHeaderRecorder struct {
	base http.RoundTripper
	mu   sync.Mutex
	sent []copilotSentRequest
}

func (r *copilotHeaderRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.sent = append(r.sent, copilotSentRequest{
		Path:                req.URL.Path,
		Initiator:           req.Header.Get("X-Initiator"),
		InteractionID:       req.Header.Get("X-Interaction-Id"),
		Vision:              req.Header.Get("Copilot-Vision-Request"),
		UserAgent:           req.Header.Get("User-Agent"),
		EditorVersion:       req.Header.Get("Editor-Version"),
		EditorPluginVersion: req.Header.Get("Editor-Plugin-Version"),
		IntegrationID:       req.Header.Get("Copilot-Integration-Id"),
		HadAuthorization:    req.Header.Get("Authorization") != "",
	})
	r.mu.Unlock()
	return r.base.RoundTrip(req)
}

func (r *copilotHeaderRecorder) snapshot() []copilotSentRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]copilotSentRequest(nil), r.sent...)
}

func toolCallNames(calls []llm.ToolCallData) []string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		names = append(names, call.Name)
	}
	return names
}

// livePNG renders a small real PNG so the image leg is not a placeholder URL.
func livePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			img.Set(x, y, color.RGBA{R: 200, G: 30, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode probe image: %v", err)
	}
	return buf.Bytes()
}
