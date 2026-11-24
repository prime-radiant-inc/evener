package githubcopilot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// scriptedGitHub serves the GitHub device, token, and account endpoints used by
// the service. Queues are consumed in request order; a blocked refresh lets a
// test hold one process inside the record lock while another starts.
type scriptedGitHub struct {
	mu             sync.Mutex
	deviceStarts   int
	userCalls      int
	pollCodes      []string
	refreshCodes   []string
	refreshStatus  []int
	refreshTokens  []string
	rotations      int
	exchangeCalls  int
	exchangeStatus []int
	omitRefresh    bool
	started        chan struct{}
	release        chan struct{}
}

func newScriptedGitHub(t *testing.T) (*scriptedGitHub, *httptest.Server) {
	t.Helper()
	gate := make(chan struct{})
	close(gate)
	gh := &scriptedGitHub{started: make(chan struct{}, 16), release: gate}
	server := httptest.NewServer(http.HandlerFunc(gh.serveHTTP))
	t.Cleanup(server.Close)
	return gh, server
}

func (g *scriptedGitHub) queuePollCodes(codes ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pollCodes = append(g.pollCodes, codes...)
}

func (g *scriptedGitHub) queueRefreshCodes(codes ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refreshCodes = append(g.refreshCodes, codes...)
}

func (g *scriptedGitHub) queueRefreshStatuses(statuses ...int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refreshStatus = append(g.refreshStatus, statuses...)
}

func (g *scriptedGitHub) queueExchangeStatuses(statuses ...int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.exchangeStatus = append(g.exchangeStatus, statuses...)
}

// blockRefresh parks every refresh request until the returned release runs.
// The release is idempotent so a failed test's cleanup can call it too.
func (g *scriptedGitHub) blockRefresh() func() {
	gate := make(chan struct{})
	g.mu.Lock()
	g.release = gate
	g.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

func (g *scriptedGitHub) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/login/device/code":
		_ = r.ParseForm()
		g.mu.Lock()
		g.deviceStarts++
		g.mu.Unlock()
		writeJSON(w, map[string]any{
			"device_code":      "device-1",
			"user_code":        "ABCD-1234",
			"verification_uri": "https://github.com/login/device",
			"expires_in":       900,
			"interval":         5,
		})
	case "/login/oauth/access_token":
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") == "refresh_token" {
			g.handleRefresh(w, r)
			return
		}
		g.mu.Lock()
		code := ""
		if len(g.pollCodes) > 0 {
			code = g.pollCodes[0]
			g.pollCodes = g.pollCodes[1:]
		}
		g.mu.Unlock()
		if code != "" {
			writeJSON(w, map[string]string{"error": code})
			return
		}
		writeJSON(w, map[string]any{"access_token": "login-access", "token_type": "bearer", "scope": "read:user"})
	case "/user":
		g.mu.Lock()
		g.userCalls++
		g.mu.Unlock()
		writeJSON(w, map[string]any{"id": 4242, "login": "octocat", "email": "octocat@example.com"})
	case copilotSessionPath:
		g.mu.Lock()
		g.exchangeCalls++
		n := g.exchangeCalls
		status := 0
		if len(g.exchangeStatus) > 0 {
			status = g.exchangeStatus[0]
			g.exchangeStatus = g.exchangeStatus[1:]
		}
		g.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			writeJSON(w, map[string]string{"message": "Bad credentials"})
			return
		}
		writeJSON(w, map[string]any{"token": fmt.Sprintf("copilot-%d", n), "expires_at": nowFunc().Add(30 * time.Minute).Unix()})
	default:
		http.NotFound(w, r)
	}
}

func (g *scriptedGitHub) handleRefresh(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.refreshTokens = append(g.refreshTokens, r.PostForm.Get("refresh_token"))
	code := ""
	if len(g.refreshCodes) > 0 {
		code = g.refreshCodes[0]
		g.refreshCodes = g.refreshCodes[1:]
	}
	status := 0
	if len(g.refreshStatus) > 0 {
		status = g.refreshStatus[0]
		g.refreshStatus = g.refreshStatus[1:]
	}
	gate := g.release
	g.mu.Unlock()
	select {
	case g.started <- struct{}{}:
	default:
	}
	<-gate
	if status != 0 {
		w.WriteHeader(status)
		writeJSON(w, map[string]string{"error": "server_error"})
		return
	}
	if code != "" {
		writeJSON(w, map[string]string{"error": code})
		return
	}
	g.mu.Lock()
	g.rotations++
	n := g.rotations
	omit := g.omitRefresh
	g.mu.Unlock()
	if omit {
		writeJSON(w, map[string]any{
			"access_token": fmt.Sprintf("access-%d", n),
			"token_type":   "bearer",
			"scope":        "read:user offline_access",
			"expires_in":   28800,
		})
		return
	}
	writeJSON(w, tokenResponse(fmt.Sprintf("access-%d", n), fmt.Sprintf("refresh-%d", n)))
}

func (g *scriptedGitHub) refreshCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.refreshTokens)
}

func (g *scriptedGitHub) refreshTokenList() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.refreshTokens...)
}

func (g *scriptedGitHub) deviceStartCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.deviceStarts
}

func (g *scriptedGitHub) exchangeCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.exchangeCalls
}

func testRecord(now time.Time, accessTTL, refreshTTL time.Duration) AuthRecord {
	record := sampleRecord()
	record.AccessToken = "access-old"
	record.RefreshToken = "refresh-old"
	record.AccountID = "4242"
	record.Email = "octocat@example.com"
	record.ObtainedAt = now.Add(-2 * time.Hour)
	record.AccessExpiry = now.Add(accessTTL)
	record.RefreshExpiry = now.Add(refreshTTL)
	record.CopilotToken = "copilot-old"
	record.CopilotExpiry = now.Add(10 * time.Minute)
	return record
}

func newServiceForTest(server *httptest.Server) *Service {
	return NewService(newTestConfig(server), server.Client())
}

func TestLoginWithDevicePersistsAccountAndTokens(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	clock := swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	gh.queuePollCodes("authorization_pending")
	svc := newServiceForTest(server)
	var slept []time.Duration
	svc.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		*clock = clock.Add(d)
		return nil
	}
	stateDir := t.TempDir()
	var shown DeviceCode
	status, err := svc.LoginWithDevice(context.Background(), stateDir, "work", func(dc DeviceCode) { shown = dc })
	if err != nil {
		t.Fatalf("LoginWithDevice() error = %v", err)
	}
	if shown.DeviceCode != "device-1" || shown.UserCode != "ABCD-1234" {
		t.Fatalf("prompt device = %q user = %q", shown.DeviceCode, shown.UserCode)
	}
	if len(slept) != 1 || slept[0] != 5*time.Second {
		t.Fatalf("poll waits = %v, want one 5s wait", slept)
	}
	if !status.SignedIn || status.Source != AuthSourceOAuth || status.AccountID != "4242" || status.Email != "octocat@example.com" {
		t.Fatalf("status = %+v, want a signed-in oauth account", status)
	}
	record, err := LoadAuth(stateDir, "work")
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if record.Scheme != AuthScheme || record.Instance != "work" || record.AccountID != "4242" {
		t.Fatalf("record identity = scheme %q instance %q account %q", record.Scheme, record.Instance, record.AccountID)
	}
	if record.AccessToken != "login-access" || record.RefreshToken != "" || !record.RefreshExpiry.IsZero() || !record.AccessExpiry.IsZero() {
		t.Fatal("record did not carry the non-expiring GitHub access token")
	}
	if record.CopilotToken != "copilot-1" {
		t.Fatalf("copilot token = %q, want the exchanged session", record.CopilotToken)
	}
	if !record.CopilotExpiry.Equal(at.Add(5*time.Second + 30*time.Minute)) {
		t.Fatalf("copilot expiry = %v, want the exchanged session lifetime", record.CopilotExpiry)
	}
	if record.RefreshRejected {
		t.Fatal("fresh login saved a rejected refresh state")
	}
	if !status.Expiry.Equal(record.CopilotExpiry) {
		t.Fatalf("status expiry = %v, want the Copilot session expiry %v", status.Expiry, record.CopilotExpiry)
	}
	if gh.exchangeCount() != 1 {
		t.Fatalf("session exchanges = %d, want 1", gh.exchangeCount())
	}
	if gh.deviceStartCount() != 1 {
		t.Fatalf("device starts = %d, want 1", gh.deviceStartCount())
	}
}

// TestCompleteLoginPersistsAnAuthorizedTokenSet covers the completion a caller
// runs after it authorized a token set itself: the account and Copilot session
// are resolved and the record is saved, without starting a device flow.
func TestCompleteLoginPersistsAnAuthorizedTokenSet(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	svc := newServiceForTest(server)
	stateDir := t.TempDir()

	status, err := svc.CompleteLogin(context.Background(), stateDir, "work", TokenSet{
		AccessToken: "login-access", TokenType: "bearer", Scope: "read:user",
	})
	if err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}
	if !status.SignedIn || status.Source != AuthSourceOAuth || status.AccountID != "4242" || status.Email != "octocat@example.com" {
		t.Fatalf("status = %+v, want a signed-in oauth account", status)
	}
	if gh.deviceStartCount() != 0 {
		t.Fatalf("device starts = %d, want 0: completing an authorized token set must not start a flow", gh.deviceStartCount())
	}
	if gh.exchangeCount() != 1 {
		t.Fatalf("session exchanges = %d, want 1", gh.exchangeCount())
	}
	record, err := LoadAuth(stateDir, "work")
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if record.AccessToken != "login-access" {
		t.Fatal("record did not carry the authorized token set's access token")
	}
	if record.CopilotToken != "copilot-1" {
		t.Fatal("record did not carry the exchanged Copilot session")
	}
	if record.AccountID != "4242" || record.Email != "octocat@example.com" {
		t.Fatal("record did not carry the token set's account")
	}
	if !record.CopilotExpiry.Equal(at.Add(30 * time.Minute)) {
		t.Fatalf("copilot expiry = %v, want the exchanged session lifetime", record.CopilotExpiry)
	}
}

// TestPrepareLoginDefersTheSaveToTheLockedWrite pins the split the hub uses to
// keep network requests out of its credential lock: preparation resolves the
// record without touching disk, and the locked save writes it.
func TestPrepareLoginDefersTheSaveToTheLockedWrite(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	svc := newServiceForTest(server)
	stateDir := t.TempDir()

	record, err := svc.PrepareLogin(context.Background(), "work", TokenSet{
		AccessToken: "login-access", TokenType: "bearer", Scope: "read:user",
	})
	if err != nil {
		t.Fatalf("PrepareLogin() error = %v", err)
	}
	if record.Instance != "work" || record.AccessToken != "login-access" {
		t.Fatal("prepared record did not carry the authorized token set")
	}
	if record.CopilotToken != "copilot-1" || record.AccountID != "4242" {
		t.Fatal("prepared record did not carry the exchanged session and account")
	}
	if _, err := LoadAuth(stateDir, "work"); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("LoadAuth() after preparation = %v, want ErrAuthNotFound: preparation must not save", err)
	}

	status, err := svc.SavePreparedLogin(stateDir, "work", record)
	if err != nil {
		t.Fatalf("SavePreparedLogin() error = %v", err)
	}
	if !status.SignedIn || status.AccountID != "4242" {
		t.Fatalf("status = %+v, want the saved record", status)
	}
	stored, err := LoadAuth(stateDir, "work")
	if err != nil {
		t.Fatalf("LoadAuth() after save: %v", err)
	}
	if stored.CopilotToken != "copilot-1" || stored.AccountID != "4242" {
		t.Fatal("saved record did not carry the prepared session and account")
	}
	if gh.exchangeCount() != 1 {
		t.Fatalf("session exchanges = %d, want 1", gh.exchangeCount())
	}
}

func TestLoginWithDeviceStopsAtCodeExpiry(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	clock := swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	gh.queuePollCodes("authorization_pending")
	svc := newServiceForTest(server)
	svc.sleep = func(ctx context.Context, d time.Duration) error {
		*clock = clock.Add(20 * time.Minute)
		return nil
	}
	stateDir := t.TempDir()
	if _, err := svc.LoginWithDevice(context.Background(), stateDir, "work", nil); !errors.Is(err, ErrDeviceExpired) {
		t.Fatalf("LoginWithDevice() error = %v, want ErrDeviceExpired", err)
	}
	if _, err := LoadAuth(stateDir, "work"); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("LoadAuth() after expired login error = %v, want ErrAuthNotFound", err)
	}
}

func TestLoginWithDeviceCarriesSlowDownInterval(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	clock := swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	gh.queuePollCodes("authorization_pending", "slow_down", "authorization_pending")
	svc := newServiceForTest(server)
	var slept []time.Duration
	svc.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		*clock = clock.Add(d)
		return nil
	}
	if _, err := svc.LoginWithDevice(context.Background(), t.TempDir(), "work", nil); err != nil {
		t.Fatalf("LoginWithDevice() error = %v", err)
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 10 * time.Second}
	if !slices.Equal(slept, want) {
		t.Fatalf("poll waits = %v, want %v", slept, want)
	}
}

func TestLoginWithDeviceReportsDenial(t *testing.T) {
	gh, server := newScriptedGitHub(t)
	gh.queuePollCodes("access_denied")
	svc := newServiceForTest(server)
	if _, err := svc.LoginWithDevice(context.Background(), t.TempDir(), "work", nil); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("LoginWithDevice() error = %v, want ErrAccessDenied", err)
	}
}

func TestStatusReportsSignedOut(t *testing.T) {
	status, err := NewService(DefaultConfig(), nil).Status(t.TempDir(), "work")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.SignedIn || status.Source != AuthSourceSignedOut {
		t.Fatalf("status = %+v, want signed out", status)
	}
}

func TestStatusTracksRefreshAndLoginNeeds(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	stateDir := t.TempDir()
	svc := NewService(DefaultConfig(), nil)

	record := testRecord(at, 10*time.Minute, 24*time.Hour)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	status, err := svc.Status(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.SignedIn || status.Source != AuthSourceOAuth || status.AccountID != "4242" || status.NeedsRefresh || status.NeedsLogin {
		t.Fatalf("fresh status = %+v", status)
	}

	record.AccessExpiry = at.Add(2 * time.Minute)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	status, err = svc.Status(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.NeedsRefresh || status.NeedsLogin {
		t.Fatalf("expired access token status = %+v, want refresh but not login", status)
	}

	record.RefreshExpiry = at.Add(-time.Minute)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	status, err = svc.Status(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.NeedsLogin {
		t.Fatalf("expired refresh token status = %+v, want needs login", status)
	}

	record.RefreshExpiry = at.Add(24 * time.Hour)
	record.RefreshRejected = true
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	status, err = svc.Status(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.NeedsLogin {
		t.Fatalf("rejected refresh token status = %+v, want needs login", status)
	}
}

func TestResolveUsesFreshAccessTokenWithoutNetwork(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	stateDir := t.TempDir()
	record := testRecord(at, 10*time.Minute, 24*time.Hour)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	credentials, err := newServiceForTest(server).ResolveRuntimeCredentials(context.Background(), stateDir, record.Instance)
	if err != nil {
		t.Fatalf("ResolveRuntimeCredentials() error = %v", err)
	}
	if credentials.BearerToken != "copilot-old" || credentials.AccountID != "4242" {
		t.Fatal("runtime credentials did not carry the stored Copilot session token and account")
	}
	if !credentials.Expiry.Equal(record.CopilotExpiry) {
		t.Fatalf("expiry = %v, want %v", credentials.Expiry, record.CopilotExpiry)
	}
	if gh.refreshCount() != 0 {
		t.Fatalf("refresh requests = %d, want 0 for a fresh token", gh.refreshCount())
	}
	if gh.exchangeCount() != 0 {
		t.Fatalf("session exchanges = %d, want 0 for a fresh session", gh.exchangeCount())
	}
}

func TestResolveRefreshesFiveMinutesBeforeExpiry(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	stateDir := t.TempDir()
	record := testRecord(at, 4*time.Minute, 24*time.Hour)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	credentials, err := newServiceForTest(server).ResolveRuntimeCredentials(context.Background(), stateDir, record.Instance)
	if err != nil {
		t.Fatalf("ResolveRuntimeCredentials() error = %v", err)
	}
	if credentials.BearerToken != "copilot-old" || credentials.AccountID != "4242" {
		t.Fatal("runtime credentials did not carry the current Copilot session and account")
	}
	if !credentials.Expiry.Equal(record.CopilotExpiry) {
		t.Fatalf("expiry = %v, want %v", credentials.Expiry, record.CopilotExpiry)
	}
	if got := gh.refreshTokenList(); len(got) != 1 {
		t.Fatalf("refresh requests = %d, want the one stored-token refresh", len(got))
	} else if got[0] != "refresh-old" {
		t.Fatal("refresh did not redeem the stored refresh token")
	}
	loaded, err := LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if loaded.AccessToken != "access-1" || loaded.RefreshToken != "refresh-1" {
		t.Fatal("refreshed record did not carry the rotated pair")
	}
	if loaded.CopilotToken != "copilot-old" {
		t.Fatal("a GitHub token refresh unexpectedly replaced the Copilot session")
	}
	if gh.exchangeCount() != 0 {
		t.Fatalf("session exchanges = %d, want 0 when the session is fresh", gh.exchangeCount())
	}
	if !loaded.ObtainedAt.Equal(at) {
		t.Fatalf("obtained at = %v, want %v", loaded.ObtainedAt, at)
	}
	if loaded.AccountID != "4242" || loaded.RefreshRejected {
		t.Fatalf("refreshed record identity = %q, rejected = %v", loaded.AccountID, loaded.RefreshRejected)
	}
}

// TestResolvePersistsRotatedGitHubTokenBeforeTransientSessionExchange proves a
// successful GitHub refresh is not stranded when the following Copilot session
// exchange fails transiently: the rotated pair is persisted and the still-valid
// stored session answers the call. The retry must redeem the new refresh token
// and succeed without a fresh login.
func TestResolvePersistsRotatedGitHubTokenBeforeTransientSessionExchange(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	gh.queueExchangeStatuses(http.StatusBadGateway)
	stateDir := t.TempDir()
	record := testRecord(at, -time.Minute, 24*time.Hour)
	record.CopilotExpiry = at.Add(4 * time.Minute)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	svc := newServiceForTest(server)
	credentials, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, record.Instance)
	if err != nil {
		t.Fatalf("first ResolveRuntimeCredentials() error = %v, want the still-valid stored session", err)
	}
	if credentials.BearerToken != "copilot-old" {
		t.Fatalf("first bearer = %q, want the stored session while it has not expired", credentials.BearerToken)
	}
	loaded, err := LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() after transient failure error = %v", err)
	}
	if loaded.AccessToken != "access-1" || loaded.RefreshToken != "refresh-1" {
		t.Fatal("successful GitHub rotation was not persisted before the failed exchange")
	}
	if gh.refreshCount() != 1 {
		t.Fatalf("refresh requests after first failure = %d, want 1", gh.refreshCount())
	}
	credentials, err = svc.ResolveRuntimeCredentials(context.Background(), stateDir, record.Instance)
	if err != nil {
		t.Fatalf("second ResolveRuntimeCredentials() error = %v", err)
	}
	if credentials.BearerToken != "copilot-2" {
		t.Fatalf("retry bearer = %q, want the second exchange's session", credentials.BearerToken)
	}
	if gh.refreshCount() != 1 {
		t.Fatalf("retry refresh requests = %d, want no second GitHub refresh", gh.refreshCount())
	}
	if got := gh.refreshTokenList(); len(got) != 1 || got[0] != "refresh-old" {
		t.Fatalf("refresh tokens redeemed = %v, want only the original before rotation", got)
	}
	loaded, err = LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() after retry error = %v", err)
	}
	if loaded.AccessToken != "access-1" || loaded.RefreshToken != "refresh-1" || loaded.CopilotToken != "copilot-2" {
		t.Fatal("retry did not keep the rotated GitHub pair and the renewed session")
	}
}

// TestResolveUsesValidSessionWhenRenewalExchangeFailsTransiently proves the
// five-minute window is an early-renewal margin, not an expiry: a transient
// session exchange failure keeps answering with the stored token while it is
// still valid, and the next resolve retries the renewal.
func TestResolveUsesValidSessionWhenRenewalExchangeFailsTransiently(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	gh.queueExchangeStatuses(http.StatusBadGateway)
	stateDir := t.TempDir()
	record := testRecord(at, 10*time.Minute, 24*time.Hour)
	record.AccessExpiry = time.Time{}
	record.RefreshToken = ""
	record.RefreshExpiry = time.Time{}
	record.CopilotExpiry = at.Add(4 * time.Minute)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	svc := newServiceForTest(server)

	credentials, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, record.Instance)
	if err != nil {
		t.Fatalf("ResolveRuntimeCredentials() error = %v, want the still-valid stored session", err)
	}
	if credentials.BearerToken != record.CopilotToken || !credentials.Expiry.Equal(record.CopilotExpiry) {
		t.Fatalf("runtime credentials = %q expiring %v, want the stored session %q expiring %v",
			credentials.BearerToken, credentials.Expiry, record.CopilotToken, record.CopilotExpiry)
	}
	loaded, err := LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() after fallback error = %v", err)
	}
	if loaded.CopilotToken != record.CopilotToken || !loaded.CopilotExpiry.Equal(record.CopilotExpiry) {
		t.Fatal("a failed renewal changed the stored session, want the still-valid generation kept")
	}
	if gh.exchangeCount() != 1 {
		t.Fatalf("session exchanges = %d, want 1", gh.exchangeCount())
	}

	credentials, err = svc.ResolveRuntimeCredentials(context.Background(), stateDir, record.Instance)
	if err != nil {
		t.Fatalf("retry ResolveRuntimeCredentials() error = %v", err)
	}
	if credentials.BearerToken != "copilot-2" {
		t.Fatalf("retry bearer = %q, want the renewed session", credentials.BearerToken)
	}
	if gh.exchangeCount() != 2 {
		t.Fatalf("session exchanges after retry = %d, want the renewal to be retried", gh.exchangeCount())
	}
}

// TestResolveSessionRenewalFailureWithExpiredSessionReturnsError proves the
// fallback stops at the session's actual expiry: a transient exchange failure
// with an expired stored token stays an ordinary error the caller can retry.
func TestResolveSessionRenewalFailureWithExpiredSessionReturnsError(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	gh.queueExchangeStatuses(http.StatusBadGateway)
	stateDir := t.TempDir()
	record := testRecord(at, 10*time.Minute, 24*time.Hour)
	record.AccessExpiry = time.Time{}
	record.RefreshToken = ""
	record.RefreshExpiry = time.Time{}
	record.CopilotExpiry = at.Add(-time.Minute)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	svc := newServiceForTest(server)

	credentials, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, record.Instance)
	if err == nil {
		t.Fatalf("ResolveRuntimeCredentials() = %+v, want an error for an expired session", credentials)
	}
	if errors.Is(err, ErrLoginRequired) {
		t.Fatalf("transient exchange failure was classified as login required: %v", err)
	}
	if gh.exchangeCount() != 1 {
		t.Fatalf("session exchanges = %d, want 1", gh.exchangeCount())
	}
}

// TestResolveSessionRenewalFailureWithCancelledContextReturnsError proves a
// cancelled caller is never answered from the stored session: the fallback
// serves only an active request.
func TestResolveSessionRenewalFailureWithCancelledContextReturnsError(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	_, server := newScriptedGitHub(t)
	stateDir := t.TempDir()
	record := testRecord(at, 10*time.Minute, 24*time.Hour)
	record.AccessExpiry = time.Time{}
	record.RefreshToken = ""
	record.RefreshExpiry = time.Time{}
	record.CopilotExpiry = at.Add(4 * time.Minute)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	svc := newServiceForTest(server)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	credentials, err := svc.ResolveRuntimeCredentials(ctx, stateDir, record.Instance)
	if err == nil {
		t.Fatalf("ResolveRuntimeCredentials() = %+v, want the cancelled exchange", credentials)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveRuntimeCredentials() error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrLoginRequired) {
		t.Fatalf("cancellation was classified as login required: %v", err)
	}
}

// stallingExchangeTransport answers every request only when its context ends.
// It makes a session exchange consume the request context's whole deadline or
// cancellation with no wall-clock sleep.
type stallingExchangeTransport struct{}

func (stallingExchangeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

// TestResolveValidSessionFallbackFitsInsideCallerDeadline proves the
// early-renewal exchange is bounded by its own budget rather than the caller's
// deadline: a stalled exchange that spends the opportunistic budget must leave
// an active caller long enough to answer with the still-valid stored session.
func TestResolveValidSessionFallbackFitsInsideCallerDeadline(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	stateDir := t.TempDir()
	record := testRecord(at, 10*time.Minute, 24*time.Hour)
	record.AccessExpiry = time.Time{}
	record.RefreshToken = ""
	record.RefreshExpiry = time.Time{}
	record.CopilotExpiry = at.Add(4 * time.Minute)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	svc := NewService(DefaultConfig(), &http.Client{Transport: stallingExchangeTransport{}})

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		start := time.Now()
		credentials, err := svc.ResolveRuntimeCredentials(ctx, stateDir, record.Instance)
		if err != nil {
			t.Fatalf("ResolveRuntimeCredentials() error = %v, want the still-valid stored session before the 8s caller deadline", err)
		}
		if credentials.BearerToken != record.CopilotToken || !credentials.Expiry.Equal(record.CopilotExpiry) {
			t.Fatalf("runtime credentials = %q expiring %v, want the stored session %q expiring %v",
				credentials.BearerToken, credentials.Expiry, record.CopilotToken, record.CopilotExpiry)
		}
		if elapsed := time.Since(start); elapsed != 5*time.Second {
			t.Fatalf("resolve took %v, want the exchange bounded at 5s while the stored session stays valid", elapsed)
		}
	})
}

// TestResolveExpiredSessionRenewalSpendsCallerDeadline proves the bound covers
// only the opportunistic early-renewal exchange: once the stored session is
// actually expired, renewal is required and may spend the caller's deadline
// before it fails.
func TestResolveExpiredSessionRenewalSpendsCallerDeadline(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	stateDir := t.TempDir()
	record := testRecord(at, 10*time.Minute, 24*time.Hour)
	record.AccessExpiry = time.Time{}
	record.RefreshToken = ""
	record.RefreshExpiry = time.Time{}
	record.CopilotExpiry = at.Add(-time.Minute)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	svc := NewService(DefaultConfig(), &http.Client{Transport: stallingExchangeTransport{}})

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		start := time.Now()
		credentials, err := svc.ResolveRuntimeCredentials(ctx, stateDir, record.Instance)
		if err == nil {
			t.Fatalf("ResolveRuntimeCredentials() = %+v, want an error for an expired session", credentials)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ResolveRuntimeCredentials() error = %v, want the caller deadline", err)
		}
		if elapsed := time.Since(start); elapsed != 8*time.Second {
			t.Fatalf("resolve took %v, want the caller's 8s deadline for a required renewal", elapsed)
		}
	})
}

// TestResolvePreservesRefreshTokenWhenResponseOmitsRotation proves a refresh
// response without refresh_token or refresh_token_expires_in keeps the stored
// pair, so a later refresh still redeems the original token.
func TestResolvePreservesRefreshTokenWhenResponseOmitsRotation(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	clock := swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	gh.omitRefresh = true
	stateDir := t.TempDir()
	record := testRecord(at, -time.Minute, 24*time.Hour)
	record.CopilotExpiry = at.Add(30 * time.Hour)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	svc := newServiceForTest(server)
	if _, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, record.Instance); err != nil {
		t.Fatalf("first ResolveRuntimeCredentials() error = %v", err)
	}
	loaded, err := LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() after first refresh error = %v", err)
	}
	if loaded.AccessToken != "access-1" {
		t.Fatalf("access token = %q, want the refreshed token", loaded.AccessToken)
	}
	if loaded.RefreshToken != record.RefreshToken || !loaded.RefreshExpiry.Equal(record.RefreshExpiry) {
		t.Fatal("refresh response without rotation erased the stored refresh pair")
	}
	*clock = at.Add(9 * time.Hour)
	if _, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, record.Instance); err != nil {
		t.Fatalf("second ResolveRuntimeCredentials() error = %v", err)
	}
	got := gh.refreshTokenList()
	if len(got) != 2 || got[0] != "refresh-old" || got[1] != "refresh-old" {
		t.Fatalf("refresh tokens redeemed = %v, want the original token twice", got)
	}
}

func TestResolveWithoutRecordReportsLoginRequired(t *testing.T) {
	_, server := newScriptedGitHub(t)
	_, err := newServiceForTest(server).ResolveRuntimeCredentials(context.Background(), t.TempDir(), "work")
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("ResolveRuntimeCredentials() error = %v, want ErrLoginRequired", err)
	}
}

func TestResolveBadRefreshTokenMarksRecordForLogin(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	gh.queueRefreshCodes("bad_refresh_token")
	stateDir := t.TempDir()
	record := testRecord(at, -time.Minute, 24*time.Hour)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	svc := newServiceForTest(server)
	if _, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, record.Instance); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("ResolveRuntimeCredentials() error = %v, want ErrLoginRequired", err)
	}
	loaded, err := LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if !loaded.RefreshRejected {
		t.Fatal("rejected refresh token was not recorded on the record")
	}
	if loaded.AccessToken != record.AccessToken || loaded.RefreshToken != record.RefreshToken {
		t.Fatal("rejection changed the refused token pair, want it retained for diagnosis")
	}
	status, err := svc.Status(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.SignedIn || !status.NeedsLogin {
		t.Fatalf("status = %+v, want signed in but needing login", status)
	}
}

func TestResolveTransientRefreshFailureLeavesRecordUsable(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	gh.queueRefreshStatuses(http.StatusBadGateway)
	stateDir := t.TempDir()
	record := testRecord(at, -time.Minute, 24*time.Hour)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	svc := newServiceForTest(server)
	_, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, record.Instance)
	if err == nil {
		t.Fatal("ResolveRuntimeCredentials() error = nil, want a transient failure")
	}
	if errors.Is(err, ErrBadRefreshToken) || errors.Is(err, ErrLoginRequired) {
		t.Fatalf("transient failure was classified as permanent: %v", err)
	}
	loaded, err := LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if loaded.RefreshRejected || loaded.RefreshToken != record.RefreshToken {
		t.Fatal("transient failure changed the stored record")
	}
	credentials, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, record.Instance)
	if err != nil {
		t.Fatalf("second ResolveRuntimeCredentials() error = %v", err)
	}
	if credentials.BearerToken != "copilot-old" {
		t.Fatal("retry after a transient failure did not use the stored session token")
	}
}

// TestResolveRenewsCopilotSessionFiveMinutesEarly pins session renewal for the
// current non-expiring GitHub token: the same GitHub token is exchanged again,
// no GitHub refresh happens, and the replacement session is persisted.
func TestResolveRenewsCopilotSessionFiveMinutesEarly(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	stateDir := t.TempDir()
	record := testRecord(at, 10*time.Minute, 24*time.Hour)
	record.AccessExpiry = time.Time{}
	record.RefreshToken = ""
	record.RefreshExpiry = time.Time{}
	record.CopilotExpiry = at.Add(4 * time.Minute)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	credentials, err := newServiceForTest(server).ResolveRuntimeCredentials(context.Background(), stateDir, record.Instance)
	if err != nil {
		t.Fatalf("ResolveRuntimeCredentials() error = %v", err)
	}
	if credentials.BearerToken != "copilot-1" {
		t.Fatalf("runtime bearer = %q, want the renewed Copilot session", credentials.BearerToken)
	}
	if !credentials.Expiry.Equal(at.Add(30 * time.Minute)) {
		t.Fatalf("runtime expiry = %v, want the renewed session expiry", credentials.Expiry)
	}
	if gh.exchangeCount() != 1 {
		t.Fatalf("session exchanges = %d, want 1", gh.exchangeCount())
	}
	if gh.refreshCount() != 0 {
		t.Fatalf("GitHub refresh requests = %d, want 0 for a non-expiring token", gh.refreshCount())
	}
	loaded, err := LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if loaded.CopilotToken != "copilot-1" || !loaded.CopilotExpiry.Equal(at.Add(30*time.Minute)) {
		t.Fatal("renewed session was not persisted")
	}
	if !loaded.ObtainedAt.Equal(at) {
		t.Fatalf("obtained at = %v, want %v", loaded.ObtainedAt, at)
	}
}

// TestResolveRejectedSessionExchangeMarksLoginRequired proves a permanent
// exchange rejection keeps the record for diagnosis and returns the typed
// login-required error.
func TestResolveRejectedSessionExchangeMarksLoginRequired(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	gh.queueExchangeStatuses(http.StatusUnauthorized)
	stateDir := t.TempDir()
	record := testRecord(at, 10*time.Minute, 24*time.Hour)
	record.AccessExpiry = time.Time{}
	record.RefreshToken = ""
	record.RefreshExpiry = time.Time{}
	record.CopilotExpiry = at.Add(4 * time.Minute)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	svc := newServiceForTest(server)
	_, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, record.Instance)
	if !errors.Is(err, ErrLoginRequired) || !errors.Is(err, ErrCopilotTokenRejected) {
		t.Fatalf("ResolveRuntimeCredentials() error = %v, want login required with the rejection cause", err)
	}
	loaded, err := LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if !loaded.RefreshRejected {
		t.Fatal("rejected session exchange did not mark the record for login")
	}
	if loaded.CopilotToken != record.CopilotToken || loaded.AccessToken != record.AccessToken {
		t.Fatal("rejection changed the refused generation, want it retained for diagnosis")
	}
	status, err := svc.Status(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.SignedIn || !status.NeedsLogin {
		t.Fatalf("status = %+v, want signed in but needing login", status)
	}
}

func TestResolveUsesNewerRecordGeneration(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	stateDir := t.TempDir()
	older := testRecord(at, -time.Minute, 24*time.Hour)
	older.ObtainedAt = at.Add(-2 * time.Hour)
	if err := SaveAuth(stateDir, older.Instance, older); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	newer := testRecord(at, -time.Minute, 24*time.Hour)
	newer.ObtainedAt = at.Add(-time.Hour)
	newer.RefreshToken = "refresh-new"
	if err := SaveAuth(stateDir, newer.Instance, newer); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	if _, err := newServiceForTest(server).ResolveRuntimeCredentials(context.Background(), stateDir, newer.Instance); err != nil {
		t.Fatalf("ResolveRuntimeCredentials() error = %v", err)
	}
	if got := gh.refreshTokenList(); len(got) != 1 {
		t.Fatalf("refresh requests = %d, want one", len(got))
	} else if got[0] != "refresh-new" {
		t.Fatal("refresh did not redeem the newer record's token")
	}
}

func TestFreshLoginClearsRefreshRejection(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	stateDir := t.TempDir()
	record := testRecord(at, -time.Minute, 24*time.Hour)
	record.RefreshRejected = true
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	status, err := newServiceForTest(server).LoginWithDevice(context.Background(), stateDir, record.Instance, nil)
	if err != nil {
		t.Fatalf("LoginWithDevice() error = %v", err)
	}
	if status.NeedsLogin {
		t.Fatalf("status after fresh login = %+v, want no login need", status)
	}
	loaded, err := LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if loaded.RefreshRejected || loaded.AccessToken != "login-access" {
		t.Fatal("fresh login did not clear the rejection and replace the tokens")
	}
	if gh.refreshCount() != 0 {
		t.Fatalf("fresh login refreshed %d times, want 0", gh.refreshCount())
	}
}

func TestLogoutRemovesRecordAndIsIdempotent(t *testing.T) {
	stateDir := t.TempDir()
	record := testRecord(time.Now(), -time.Minute, 24*time.Hour)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	svc := NewService(DefaultConfig(), nil)
	existed, err := svc.Logout(stateDir, record.Instance)
	if err != nil || !existed {
		t.Fatalf("Logout() = %v, %v; want true, nil", existed, err)
	}
	if _, err := LoadAuth(stateDir, record.Instance); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("LoadAuth() after Logout() error = %v, want ErrAuthNotFound", err)
	}
	existed, err = svc.Logout(stateDir, record.Instance)
	if err != nil || existed {
		t.Fatalf("second Logout() = %v, %v; want false, nil", existed, err)
	}
}

func TestLogoutRemovesCorruptRecord(t *testing.T) {
	stateDir := t.TempDir()
	path := AuthFilePath(stateDir, "work")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	existed, err := NewService(DefaultConfig(), nil).Logout(stateDir, "work")
	if err != nil || !existed {
		t.Fatalf("Logout() = %v, %v; want true, nil", existed, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt record still exists after Logout(), Stat error = %v", err)
	}
}

func TestRecordRejectionIgnoresNewerGeneration(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	stateDir := t.TempDir()
	refused := testRecord(at, -time.Minute, 24*time.Hour)
	if err := SaveAuth(stateDir, refused.Instance, refused); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	newer := testRecord(at, 10*time.Minute, 24*time.Hour)
	newer.ObtainedAt = at.Add(-time.Hour)
	newer.RefreshToken = "refresh-newer"
	if err := SaveAuth(stateDir, newer.Instance, newer); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	err := new(Service).saveRejected(stateDir, newer.Instance, refused, refused, ErrBadRefreshToken)
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("saveRejected() error = %v, want ErrLoginRequired", err)
	}
	loaded, err := LoadAuth(stateDir, newer.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if loaded.RefreshRejected {
		t.Fatal("a refused older generation marked the newer record rejected")
	}
	if loaded.RefreshToken != "refresh-newer" {
		t.Fatal("rejection overwrote the newer record")
	}
}

func TestRecordRejectionDoesNotResurrectDeletedRecord(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	stateDir := t.TempDir()
	refused := testRecord(at, -time.Minute, 24*time.Hour)
	if err := SaveAuth(stateDir, refused.Instance, refused); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	if err := DeleteAuth(stateDir, refused.Instance); err != nil {
		t.Fatalf("DeleteAuth() error = %v", err)
	}
	err := new(Service).saveRejected(stateDir, refused.Instance, refused, refused, ErrBadRefreshToken)
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("saveRejected() error = %v, want ErrLoginRequired", err)
	}
	if _, err := LoadAuth(stateDir, refused.Instance); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("LoadAuth() error = %v, want the record to stay deleted", err)
	}
}
