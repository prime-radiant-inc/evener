//go:build browserguard

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/rendezvous"
)

// oauthFixture scripts only the external OAuth issuer and the external model
// endpoint. Device codes, polling, token exchange, persistence, registry
// reload, AppWire, the browser UI and the daemon all stay production code.
type oauthFixture struct {
	mu         sync.Mutex
	scenario   string
	deviceHits int
	polls      map[string]int
	exchanges  int
	refreshes  int
	models     int
	access     string
	providerAt time.Time
	// Late-effect handshake for cancellation and destination change. The
	// first poll for those flows is observed by the issuer and held until the
	// driver reports the user's intent; it then answers with a real success so
	// the retired operation runs its real save/refusal and its late UI effects
	// have to stay retired.
	// Settlement is NOT signalled here: the issuer answering is not production
	// receiving the tokens. The browser observes the real device-poll RPC reply,
	// which production writes only after DevicePoll has exchanged, verified and
	// saved or refused.
	firstPollObserved chan struct{}
	releaseLate       chan struct{}
	observeOnce       sync.Once
	releaseOnce       sync.Once
}

func newOAuthFixture(scenario string) *oauthFixture {
	return &oauthFixture{
		scenario:          scenario,
		polls:             map[string]int{},
		access:            "oauth-fixture-access-not-a-secret",
		firstPollObserved: make(chan struct{}),
		releaseLate:       make(chan struct{}),
	}
}

func (f *oauthFixture) releaseLateSuccess() {
	f.releaseOnce.Do(func() { close(f.releaseLate) })
}

func (f *oauthFixture) issuer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/accounts/deviceauth/usercode", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.scenario == "oauth-redirect" {
			http.NotFound(w, r)
			return
		}
		f.deviceHits++
		code := fmt.Sprintf("ONBOARD-CODE-%d", f.deviceHits)
		if f.scenario == "oauth-cancel" && f.deviceHits == 1 {
			code = "ONBOARD-CANCEL"
		}
		if f.scenario == "oauth-endpoint-change" {
			code = "ONBOARD-CHANGE"
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"device_auth_id":"fixture-device-%d","user_code":%q,"interval":"1"}`, f.deviceHits, code)
	})
	mux.HandleFunc("/api/accounts/deviceauth/token", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserCode string `json:"user_code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.UserCode, "CANCEL") || strings.Contains(req.UserCode, "CHANGE") {
			f.observeOnce.Do(func() { close(f.firstPollObserved) })
			<-f.releaseLate
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"authorization_code":"fixture-late-authorization-code","code_challenge":"fixture-challenge","code_verifier":"fixture-verifier"}`)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.polls[req.UserCode]++
		if f.polls[req.UserCode] == 1 {
			http.Error(w, "pending", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"authorization_code":"fixture-authorization-code","code_challenge":"fixture-challenge","code_verifier":"fixture-verifier"}`)
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			f.exchanges++
		case "refresh_token":
			f.refreshes++
		default:
			http.Error(w, "unexpected grant", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"oauth-fixture-refresh-not-a-secret","id_token":%q,"token_type":"Bearer","scope":"openid profile email","expires_in":3600}`,
			f.access, hubAuthTestJWT(t, map[string]any{"email": "onboarding-oauth@example.invalid", "sub": "fixture-account"}))
	})
	mux.HandleFunc("/oauth/authorize", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "fixture authorization page")
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// cleanupOAuthFixture releases a poll the scripted issuer is holding before it
// closes the fixture's servers. httptest.Server.Close waits for active
// handlers, and the held poll waits for this release, so the release must run
// first. t.Cleanup runs LIFO: registering it after the servers' creation puts
// it ahead of their own Close cleanups.
func cleanupOAuthFixture(f *oauthFixture, servers ...*httptest.Server) {
	f.releaseLateSuccess()
	for _, server := range servers {
		server.Close()
	}
}

// TestOnboardingOAuthBrowserFixtureCleanupWithHeldPoll proves the fixture
// cleanup finishes while the issuer still holds a device poll. With the release
// registered after the servers (the order testOnboardingOAuthAt uses), cleanup
// releases the handler before Close waits on it.
func TestOnboardingOAuthBrowserFixtureCleanupWithHeldPoll(t *testing.T) {
	fixture := newOAuthFixture("oauth-cancel")
	issuer := fixture.issuer(t)
	// Registered after the issuer's own Close cleanup, so it runs first and a
	// failure below cannot leave the held handler deadlocking teardown.
	t.Cleanup(fixture.releaseLateSuccess)
	provider := fixture.provider(t)
	polled := make(chan error, 1)
	go func() {
		response, err := http.Post(
			issuer.URL+"/api/accounts/deviceauth/token",
			"application/json",
			strings.NewReader(`{"user_code":"ONBOARD-CANCEL"}`),
		)
		if err == nil {
			response.Body.Close()
		}
		polled <- err
	}()
	select {
	case <-fixture.firstPollObserved:
	case err := <-polled:
		t.Fatalf("the poll was not held: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("issuer never observed the held poll")
	}
	done := make(chan struct{})
	go func() {
		cleanupOAuthFixture(fixture, provider, issuer)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("fixture cleanup blocked on the held poll")
	}
	select {
	case err := <-polled:
		if err != nil {
			t.Fatalf("held poll: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("held poll handler did not return after cleanup released it")
	}
}

func (f *oauthFixture) provider(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/unavailable") {
			http.Error(w, "fixture local discovery unavailable", http.StatusServiceUnavailable)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/models") {
			t.Errorf("unexpected provider route %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); got != f.access {
			t.Errorf("model list carried access %q, want the OAuth record's pointer", got)
		}
		f.models++
		f.providerAt = time.Now()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[{"slug":"gpt-fixture-first"},{"slug":"gpt-fixture-chosen"}]}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func (f *oauthFixture) counts() (device, exchanges, refreshes, models int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deviceHits, f.exchanges, f.refreshes, f.models
}

// TestOnboardingOAuthBrowser drives both production OAuth continuations
// (device polling and browser redirect), explicit cancellation with no late
// completion, and a real instance endpoint edit refused while sign-in is
// pending. The only scripted services are the issuer and the model endpoint.
func TestOnboardingOAuthBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome and production frontend")
	}
	var cases []struct {
		scenario string
		view     onboardingView
	}
	for _, scenario := range []string{"oauth-device", "oauth-redirect"} {
		for _, view := range onboardingViews() {
			cases = append(cases, struct {
				scenario string
				view     onboardingView
			}{scenario, view})
		}
	}
	// Cancellation and an endpoint move need the late-effect handshake, not the
	// full presentation matrix: one representative desktop layout each.
	cases = append(cases,
		struct {
			scenario string
			view     onboardingView
		}{"oauth-cancel", onboardingViews()[0]},
		struct {
			scenario string
			view     onboardingView
		}{"oauth-endpoint-change", onboardingViews()[0]},
	)
	for _, c := range cases {
		scenario, view := c.scenario, c.view
		t.Run(scenario+"-"+view.name, func(t *testing.T) {
			testOnboardingOAuthAt(t, scenario, view)
		})
	}
}

func testOnboardingOAuthAt(t *testing.T, scenario string, view onboardingView) {
	t.Helper()
	guard := newOnboardingGuardEnv(t)
	fixture := newOAuthFixture(scenario)
	issuer := fixture.issuer(t)
	provider := fixture.provider(t)
	// LIFO cleanup: register after the servers so a held late poll is released
	// before their own Close cleanups wait on it.
	t.Cleanup(func() { cleanupOAuthFixture(fixture, provider, issuer) })
	config := fmt.Sprintf("[providers.\"openai-codex\"]\nbase_url = %q\ndefault_model = \"gpt-fixture-chosen\"\n", provider.URL+"/v1")
	if err := os.WriteFile(os.Getenv("EVENER_PROVIDERS_CONFIG"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLLAMA_BASE_URL", provider.URL+"/unavailable")
	t.Setenv("EVENER_OFFLINE", "1")
	hub := newOnboardingGuardHub(t, guard, func(c *hubAuthController) {
		c.cfg = openai.Config{IssuerBaseURL: issuer.URL}
		c.client = issuer.Client()
	})
	if inst, ok := hub.reg.Get().Instance("openai-codex"); !ok || inst.DefaultModel != "gpt-fixture-chosen" {
		t.Fatalf("fixture instance default = %q ok=%v, want the configured non-first default", inst.DefaultModel, ok)
	}
	// The checkpoint is the fixture channel to the real Go owners. It
	// asserts the pre-Start absence of sessions/generations and, at the
	// named stage, performs a real instance edit or verifies what a
	// cancelled/changed flow left behind: saved access for the unchanged
	// cancellation destination, nothing for the changed one.
	var stageMu sync.Mutex
	stages := map[string]bool{}
	checkpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stage := r.URL.Query().Get("stage")
		entries, err := rendezvous.List(guard.run)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		device, exchanges, _, models := fixture.counts()
		if len(entries) != 0 {
			http.Error(w, "session before Start", http.StatusInternalServerError)
			return
		}
		switch stage {
		case "oauth-pending":
			stageMu.Lock()
			stages[stage] = true
			stageMu.Unlock()
		case "oauth-poll-observed":
			select {
			case <-fixture.firstPollObserved:
				stageMu.Lock()
				stages[stage] = true
				stageMu.Unlock()
			default:
				http.Error(w, "issuer has not observed the poll yet", http.StatusInternalServerError)
				return
			}
		case "oauth-cancelled-intent", "oauth-changed-intent":
			if exchanges != 0 || models != 0 {
				http.Error(w, fmt.Sprintf("late effect before intent was settled: exchanges=%d models=%d", exchanges, models), http.StatusInternalServerError)
				return
			}
			stageMu.Lock()
			stages[stage] = true
			stageMu.Unlock()
		case "oauth-release-late-success":
			fixture.releaseLateSuccess()
			stageMu.Lock()
			stages[stage] = true
			stageMu.Unlock()
		case "oauth-late-settled":
			// The browser reports this only after it has observed the retired
			// operation's real device-poll RPC reply. Production writes that
			// reply after DevicePoll has received the tokens and run its
			// persistence/refusal under the credential lock, so the effect
			// checks below run at the real completion boundary. No recovery
			// flow may have started yet, or its exchange would stand in for
			// the retired one.
			if device != 1 {
				http.Error(w, fmt.Sprintf("device starts=%d at settlement, want only the retired flow's", device), http.StatusInternalServerError)
				return
			}
			if exchanges != 1 {
				http.Error(w, fmt.Sprintf("retired poll exchanges=%d, want its in-flight exchange completed once", exchanges), http.StatusInternalServerError)
				return
			}
			stageMu.Lock()
			stages[stage] = true
			stageMu.Unlock()
		case "oauth-cancelled":
			if exchanges != 1 {
				http.Error(w, fmt.Sprintf("released late poll exchanges=%d, want the in-flight request completed once", exchanges), http.StatusInternalServerError)
				return
			}
			// Cancel retires the dialog's ownership, not an already-running
			// server-side sign-in: for an unchanged destination the authorized
			// operation may save access. Assert the record is this scripted
			// exchange rather than its absence. No model-request count belongs
			// here; a later catalog refresh is allowed and its caller is not
			// established by a count.
			record, err := openai.LoadAuth(hub.state, "openai-codex")
			if err != nil {
				http.Error(w, fmt.Sprintf("cancelled flow saved no usable OAuth record: %v", err), http.StatusInternalServerError)
				return
			}
			if record.Provider != "openai-codex" || record.AccessToken != fixture.access || record.Email != "onboarding-oauth@example.invalid" {
				http.Error(w, "cancelled flow saved a record that does not match the scripted exchange", http.StatusInternalServerError)
				return
			}
			stageMu.Lock()
			stages[stage] = true
			stageMu.Unlock()
		case "oauth-endpoint-change":
			if err := editOnboardingEndpoint(r.Context(), hub, guard, provider.URL+"/moved/v1"); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		case "oauth-endpoint-changed":
			if exchanges != 1 {
				http.Error(w, fmt.Sprintf("released late poll exchanges=%d, want the in-flight request completed once", exchanges), http.StatusInternalServerError)
				return
			}
			if models != 0 {
				http.Error(w, fmt.Sprintf("changed flow checked models: models=%d", models), http.StatusInternalServerError)
				return
			}
			if _, err := openai.LoadAuth(hub.state, "openai-codex"); !errors.Is(err, openai.ErrAuthNotFound) {
				http.Error(w, fmt.Sprintf("changed flow persisted access: %v", err), http.StatusInternalServerError)
				return
			}
			stageMu.Lock()
			stages[stage] = true
			stageMu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"checks": models, "sessions": len(entries), "generations": 0})
	}))
	defer checkpoint.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	args := []string{
		"scripts/onboardingguard/run.mjs",
		"--url", hubedge.AuthURLFor(hub.server.URL, guard.token),
		"--artifact-dir", guard.root,
		"--work", guard.work,
		"--checkpoint", checkpoint.URL,
		"--scenario", scenario,
		"--adopted-model", "openai-codex/gpt-fixture-chosen",
		"--width", strconv.Itoa(view.width),
		"--height", strconv.Itoa(view.height),
		"--theme", view.theme,
	}
	if guard.recordVideo != "" {
		args = append(args, "--record-video", guard.recordVideo)
	}
	if guard.evidenceDir != "" {
		args = append(args, "--evidence-dir", guard.evidenceDir)
	}
	cmd := exec.CommandContext(ctx, "node", args...)
	cmd.Dir = filepath.Join(guard.repo, "cmd", "evener-hub", "frontend")
	cmd.Cancel = func() error { return onboardingGuardCancel(cmd.Process) }
	cmd.WaitDelay = 15 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser %s: %v\n%s", scenario, err, out)
	}
	t.Logf("browser: %s", out)

	switch scenario {
	case "oauth-device", "oauth-redirect":
		record, err := openai.LoadAuth(hub.state, "openai-codex")
		if err != nil {
			t.Fatalf("saved OAuth record: %v", err)
		}
		if record.AccessToken != fixture.access || record.Email != "onboarding-oauth@example.invalid" {
			t.Fatalf("OAuth record = access %q email %q, want the fixture exchange result", record.AccessToken, record.Email)
		}
		_, exchanges, _, models := fixture.counts()
		if exchanges != 1 || models == 0 {
			t.Fatalf("exchanges=%d model lists=%d, want one real token exchange and model-list access", exchanges, models)
		}
	case "oauth-cancel":
		device, exchanges, _, models := fixture.counts()
		if device != 2 || exchanges != 2 {
			t.Fatalf("device starts=%d exchanges=%d, want the late cancelled exchange plus the recovering flow", device, exchanges)
		}
		if models == 0 {
			t.Fatal("recovering sign-in never checked the model list")
		}
		record, err := openai.LoadAuth(hub.state, "openai-codex")
		if err != nil || record.AccessToken != fixture.access {
			// Both authorized exchanges carry the same fixture access, so this
			// checks the saved destination's record, not which flow wrote last.
			t.Fatalf("saved OAuth record = %v err=%v, want the fixture exchange result", record, err)
		}
	case "oauth-endpoint-change":
		_, exchanges, _, models := fixture.counts()
		if exchanges != 1 || models != 0 {
			t.Fatalf("changed flow late effect: exchanges=%d models=%d, want the token exchange attempted but no model check", exchanges, models)
		}
		if _, err := openai.LoadAuth(hub.state, "openai-codex"); !errors.Is(err, openai.ErrAuthNotFound) {
			t.Fatalf("changed flow persisted access: %v", err)
		}
	}
	stageMu.Lock()
	defer stageMu.Unlock()
	for _, required := range requiredStages(scenario) {
		if !stages[required] {
			t.Errorf("checkpoint stage %q never observed", required)
		}
	}
}

// onboardingGuardCancel is the signal boundary the Go owner uses to stop the
// browser driver. SIGTERM is the handshake the runner's own entry point
// installs and the sibling browser test already uses; the shared browser
// lifecycle also handles SIGINT, so this is consistency with the runner's
// documented stop path, not a leak fix.
func onboardingGuardCancel(process *os.Process) error {
	return process.Signal(syscall.SIGTERM)
}

// TestOnboardingEvidenceGuardCancellationStopsBrowser proves the cancellation
// boundary above at the driver's own lifetime: a context cancellation sends the
// runner the signal it owns, and the runner stops the Chrome it spawned
// detached before it exits. This asserts the captured browser identity is gone
// rather than trusting the exit code alone.
func TestOnboardingEvidenceGuardCancellationStopsBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	repo := skillGuardRepoRoot(t)
	// This page satisfies nothing the journey waits for, so the driver stays in
	// flight with its browser up until the cancellation below.
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<!doctype html><title>cancellation fixture</title>")
	}))
	defer page.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "scripts/onboardingguard/run.mjs",
		"--url", page.URL,
		"--artifact-dir", t.TempDir(),
		"--work", t.TempDir(),
		"--scenario", "auto",
	)
	cmd.Dir = filepath.Join(repo, "cmd", "evener-hub", "frontend")
	cmd.Cancel = func() error { return onboardingGuardCancel(cmd.Process) }
	cmd.WaitDelay = 15 * time.Second
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start driver: %v", err)
	}
	browsers, err := waitForOnboardingGuardBrowsers(cmd.Process.Pid, 30*time.Second)
	if err != nil {
		cancel()
		_ = cmd.Wait()
		t.Fatalf("inspect driver browser: %v: %s", err, stderr.String())
	}
	if len(browsers) == 0 {
		cancel()
		_ = cmd.Wait()
		t.Fatalf("driver never launched its detached browser: %s", stderr.String())
	}
	t.Logf("driver browsers running: %v", browsers)
	defer func() {
		// The fallback may signal only an identity the current process table
		// still shows as this run's browser. A snapshot that cannot be read
		// authorizes no signal at all.
		snapshot, err := onboardingGuardProcessSnapshot()
		for _, browser := range onboardingGuardBrowsersToSignal(snapshot, err, browsers) {
			_ = syscall.Kill(-browser.pgid, syscall.SIGKILL)
		}
	}()

	// The captured identity must describe a live process now. Without this,
	// "gone after cancellation" below could pass on an identity that never
	// matched.
	snapshot, err := onboardingGuardProcessSnapshot()
	if err != nil {
		cancel()
		_ = cmd.Wait()
		t.Fatalf("inspect running browser: %v", err)
	}
	if live := onboardingGuardBrowsersOwned(snapshot, browsers); len(live) != len(browsers) {
		cancel()
		_ = cmd.Wait()
		t.Fatalf("captured browser %v is not owned by the live process table: %v", browsers, live)
	}

	// Deliver the configured context signal through the same CommandContext
	// path the OAuth journey uses.
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatalf("driver exited 0 after cancellation, want the signal's failure status")
	}
	deadline := time.Now().Add(10 * time.Second)
	var (
		alive   []onboardingGuardBrowser
		lastErr error
	)
	for {
		snapshot, err := onboardingGuardProcessSnapshot()
		if err != nil {
			lastErr = err
		} else {
			lastErr = nil
			if onboardingGuardBrowsersGroupGone(snapshot, nil, browsers) {
				return
			}
			alive = onboardingGuardBrowsersGroupAlive(snapshot, browsers)
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				t.Fatalf("process inspection failed; browser cleanup is inconclusive: %v", lastErr)
			}
			t.Fatalf("cancelled driver left browser process group running: %v: %s", alive, stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// onboardingGuardBrowser is the captured identity of a detached Chrome the
// driver launched: the pid and process group it ran as, plus the profile
// argument that names this run. Numeric group existence alone is not
// ownership, because a group can exit and its id be reused.
type onboardingGuardBrowser struct {
	pid     int
	pgid    int
	profile string
}

func (b onboardingGuardBrowser) String() string {
	return fmt.Sprintf("pid=%d pgid=%d profile=%s", b.pid, b.pgid, b.profile)
}

// onboardingGuardProcessSnapshot reads the process table columns the identity
// checks need.
func onboardingGuardProcessSnapshot() (string, error) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,pgid=,command=").Output()
	return string(out), err
}

func onboardingGuardProfileArg(command string) string {
	const prefix = "--user-data-dir="
	for field := range strings.FieldsSeq(command) {
		if profile, ok := strings.CutPrefix(field, prefix); ok {
			return profile
		}
	}
	return ""
}

// parseOnboardingGuardBrowsers returns the direct children of parent that are
// Chrome launched for a skillguard profile, with their captured identity.
func parseOnboardingGuardBrowsers(snapshot string, parent int) []onboardingGuardBrowser {
	var browsers []onboardingGuardBrowser
	for line := range strings.SplitSeq(snapshot, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid != parent {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		pgid, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		profile := onboardingGuardProfileArg(strings.Join(fields[3:], " "))
		if !strings.Contains(profile, "skillguard-chrome-") {
			continue
		}
		browsers = append(browsers, onboardingGuardBrowser{pid: pid, pgid: pgid, profile: profile})
	}
	return browsers
}

// onboardingGuardBrowserAlive reports whether the captured browser still owns
// a process row: the same pid, the same process group, and the same profile
// argument. A reused numeric id with another pid or argv is not ownership.
func onboardingGuardBrowserAlive(snapshot string, browser onboardingGuardBrowser) bool {
	for line := range strings.SplitSeq(snapshot, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		pgid, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		if pid != browser.pid || pgid != browser.pgid {
			continue
		}
		if onboardingGuardProfileArg(strings.Join(fields[3:], " ")) == browser.profile {
			return true
		}
	}
	return false
}

func onboardingGuardBrowsersOwned(snapshot string, browsers []onboardingGuardBrowser) []onboardingGuardBrowser {
	var owned []onboardingGuardBrowser
	for _, browser := range browsers {
		if onboardingGuardBrowserAlive(snapshot, browser) {
			owned = append(owned, browser)
		}
	}
	return owned
}

// onboardingGuardBrowsersGroupGone is the post-cancellation cleanup-success
// predicate. Success needs the whole captured process group empty, because an
// owned renderer or utility child can outlive the leader the capture names.
// An unreadable process table is inconclusive and cannot prove cleanup, and an
// empty capture has nothing left to clean.
func onboardingGuardBrowsersGroupGone(snapshot string, snapshotErr error, browsers []onboardingGuardBrowser) bool {
	if snapshotErr != nil {
		return false
	}
	return len(onboardingGuardBrowsersGroupAlive(snapshot, browsers)) == 0
}

// onboardingGuardBrowsersGroupAlive returns the captured browsers whose
// process group still holds at least one member, whether or not the captured
// leader itself is one of them.
func onboardingGuardBrowsersGroupAlive(snapshot string, browsers []onboardingGuardBrowser) []onboardingGuardBrowser {
	var alive []onboardingGuardBrowser
	for _, browser := range browsers {
		if onboardingGuardBrowserGroupAlive(snapshot, browser) {
			alive = append(alive, browser)
		}
	}
	return alive
}

// onboardingGuardBrowserGroupAlive reports whether any process still belongs
// to the captured browser's process group.
func onboardingGuardBrowserGroupAlive(snapshot string, browser onboardingGuardBrowser) bool {
	for line := range strings.SplitSeq(snapshot, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pgid, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		if pgid == browser.pgid {
			return true
		}
	}
	return false
}

// onboardingGuardBrowsersToSignal returns the captured identities a
// destructive fallback may signal. An unreadable process table is
// inconclusive: it proves nothing about ownership, so it authorizes no signal.
func onboardingGuardBrowsersToSignal(snapshot string, snapshotErr error, browsers []onboardingGuardBrowser) []onboardingGuardBrowser {
	if snapshotErr != nil {
		return nil
	}
	return onboardingGuardBrowsersOwned(snapshot, browsers)
}

func waitForOnboardingGuardBrowsers(parent int, timeout time.Duration) ([]onboardingGuardBrowser, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		snapshot, err := onboardingGuardProcessSnapshot()
		if err != nil {
			lastErr = err
		} else {
			lastErr = nil
			if browsers := parseOnboardingGuardBrowsers(snapshot, parent); len(browsers) > 0 {
				return browsers, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, lastErr
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TestOnboardingEvidenceBrowserIdentityAcceptsOwnedProcess pins the positive
// case: the exact captured pid, group and profile argument is this run's
// browser.
func TestOnboardingEvidenceBrowserIdentityAcceptsOwnedProcess(t *testing.T) {
	snapshot := "123 1 456 /usr/bin/google-chrome --user-data-dir=/tmp/skillguard-chrome-owned\n"
	browsers := parseOnboardingGuardBrowsers(snapshot, 1)
	if len(browsers) != 1 {
		t.Fatalf("captured browsers = %v, want one", browsers)
	}
	if !onboardingGuardBrowserAlive(snapshot, browsers[0]) {
		t.Fatalf("captured browser %v not recognized in its own snapshot", browsers[0])
	}
	if got := onboardingGuardBrowsersToSignal(snapshot, nil, browsers); len(got) != 1 {
		t.Fatalf("signalable browsers = %v, want the captured one", got)
	}
}

// TestOnboardingEvidenceBrowserIdentityRejectsReusedGroup feeds a snapshot
// where the captured group id now belongs to an unrelated pid and argv. It
// must not count as ownership, whether checking liveness or authorizing a
// fallback signal.
func TestOnboardingEvidenceBrowserIdentityRejectsReusedGroup(t *testing.T) {
	captured := onboardingGuardBrowser{pid: 123, pgid: 456, profile: "/tmp/skillguard-chrome-owned"}
	reused := "999 1 456 /usr/bin/unrelated --flag\n"
	if onboardingGuardBrowserAlive(reused, captured) {
		t.Fatalf("reused group 456 with unrelated pid/argv counted as %v", captured)
	}
	if got := onboardingGuardBrowsersToSignal(reused, nil, []onboardingGuardBrowser{captured}); len(got) != 0 {
		t.Fatalf("fallback would signal reused group: %v", got)
	}
}

// TestOnboardingEvidenceBrowserIdentityRejectsInspectionError proves an
// unreadable process table is inconclusive and authorizes no destructive
// fallback.
func TestOnboardingEvidenceBrowserIdentityRejectsInspectionError(t *testing.T) {
	captured := onboardingGuardBrowser{pid: 123, pgid: 456, profile: "/tmp/skillguard-chrome-owned"}
	if got := onboardingGuardBrowsersToSignal("", errors.New("ps unavailable"), []onboardingGuardBrowser{captured}); len(got) != 0 {
		t.Fatalf("inspection error authorized a signal: %v", got)
	}
}

// TestOnboardingEvidenceBrowserCleanupRejectsLeaderGoneChildSurvives proves the
// post-cancellation success check needs the whole captured process group empty:
// a renderer that outlives its leader still belongs to the browser this run
// started, so the leader's disappearance is not cleanup.
func TestOnboardingEvidenceBrowserCleanupRejectsLeaderGoneChildSurvives(t *testing.T) {
	captured := []onboardingGuardBrowser{{pid: 123, pgid: 123, profile: "/tmp/skillguard-chrome-owned"}}
	survivor := "124 1 123 /usr/bin/google-chrome --type=renderer --user-data-dir=/tmp/skillguard-chrome-owned\n"
	if onboardingGuardBrowsersGroupGone(survivor, nil, captured) {
		t.Fatalf("leader gone but owned group member survived; accepted cleanup for %v", captured)
	}
	if got := onboardingGuardBrowsersToSignal(survivor, nil, captured); len(got) != 0 {
		t.Fatalf("surviving owned child authorized a signal: %v", got)
	}
}

// TestOnboardingEvidenceBrowserCleanupAcceptsEmptyGroup pins cleanup success:
// once no member of the captured group remains, and an empty capture trivially
// has nothing to clean, the predicate reports success. An unreadable snapshot
// is inconclusive and must not report success.
func TestOnboardingEvidenceBrowserCleanupAcceptsEmptyGroup(t *testing.T) {
	captured := []onboardingGuardBrowser{{pid: 123, pgid: 123, profile: "/tmp/skillguard-chrome-owned"}}
	unrelated := "999 1 999 /usr/bin/unrelated --flag\n"
	if !onboardingGuardBrowsersGroupGone(unrelated, nil, captured) {
		t.Fatalf("empty captured group still reported not gone for %v", captured)
	}
	if !onboardingGuardBrowsersGroupGone(unrelated, nil, nil) {
		t.Fatal("empty capture reported not gone")
	}
	if onboardingGuardBrowsersGroupGone("", errors.New("ps unavailable"), captured) {
		t.Fatal("unreadable snapshot reported cleanup success")
	}
}

func requiredStages(scenario string) []string {
	switch scenario {
	case "oauth-cancel":
		return []string{"oauth-pending", "oauth-poll-observed", "oauth-cancelled-intent", "oauth-release-late-success", "oauth-late-settled", "oauth-cancelled"}
	case "oauth-endpoint-change":
		return []string{"oauth-pending", "oauth-poll-observed", "oauth-changed-intent", "oauth-release-late-success", "oauth-late-settled", "oauth-endpoint-changed"}
	case "oauth-device":
		return []string{"oauth-pending"}
	}
	return nil
}

// editOnboardingEndpoint performs the real AppWire instance edit the browser
// sees: a second client re-points the instance while sign-in is pending, the
// hub rewrites providers.toml and reloads, and its auth/updated broadcast
// refreshes the browser's listing.
func editOnboardingEndpoint(ctx context.Context, hub *onboardingGuardHub, guard *onboardingGuardEnv, baseURL string) error {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+guard.token)
	transport, err := appwire.DialWebSocketWithHeaders(ctx, "ws"+strings.TrimPrefix(hub.server.URL, "http")+"/rpc", http.DefaultClient, header)
	if err != nil {
		return fmt.Errorf("dial rpc: %w", err)
	}
	defer transport.Close()
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	var out appwire.InstanceListResponse
	return client.Request(ctx, appwire.MethodEvenerInstanceEdit, appwire.InstanceEditParams{
		Name:    "openai-codex",
		BaseURL: baseURL,
	}, &out)
}
