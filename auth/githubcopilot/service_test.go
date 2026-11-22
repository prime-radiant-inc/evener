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
	"time"
)

// scriptedGitHub serves the GitHub device, token, and account endpoints used by
// the service. Queues are consumed in request order; a blocked refresh lets a
// test hold one process inside the record lock while another starts.
type scriptedGitHub struct {
	mu            sync.Mutex
	deviceStarts  int
	userCalls     int
	pollCodes     []string
	refreshCodes  []string
	refreshStatus []int
	refreshTokens []string
	rotations     int
	started       chan struct{}
	release       chan struct{}
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
		writeJSON(w, tokenResponse("login-access", "login-refresh"))
	case "/user":
		g.mu.Lock()
		g.userCalls++
		g.mu.Unlock()
		writeJSON(w, map[string]any{"id": 4242, "login": "octocat", "email": "octocat@example.com"})
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
	g.mu.Unlock()
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

func testRecord(now time.Time, accessTTL, refreshTTL time.Duration) AuthRecord {
	record := sampleRecord()
	record.AccessToken = "access-old"
	record.RefreshToken = "refresh-old"
	record.AccountID = "4242"
	record.Email = "octocat@example.com"
	record.ObtainedAt = now.Add(-2 * time.Hour)
	record.AccessExpiry = now.Add(accessTTL)
	record.RefreshExpiry = now.Add(refreshTTL)
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
		*clock = (*clock).Add(d)
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
	if record.AccessToken != "login-access" || record.RefreshToken != "login-refresh" {
		t.Fatal("record did not carry the login token pair")
	}
	if !record.AccessExpiry.Equal(at.Add(5*time.Second + 8*time.Hour)) {
		t.Fatalf("access expiry = %v, want %v", record.AccessExpiry, at.Add(5*time.Second+8*time.Hour))
	}
	if !record.RefreshExpiry.Equal(at.Add(5*time.Second + 15897600*time.Second)) {
		t.Fatalf("refresh expiry = %v, want the issued refresh lifetime", record.RefreshExpiry)
	}
	if record.RefreshRejected {
		t.Fatal("fresh login saved a rejected refresh state")
	}
	if gh.deviceStartCount() != 1 {
		t.Fatalf("device starts = %d, want 1", gh.deviceStartCount())
	}
}

func TestLoginWithDeviceStopsAtCodeExpiry(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	clock := swapNow(t, at)
	gh, server := newScriptedGitHub(t)
	gh.queuePollCodes("authorization_pending")
	svc := newServiceForTest(server)
	svc.sleep = func(ctx context.Context, d time.Duration) error {
		*clock = (*clock).Add(20 * time.Minute)
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
		*clock = (*clock).Add(d)
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
	if credentials.BearerToken != "access-old" || credentials.AccountID != "4242" {
		t.Fatal("runtime credentials did not carry the stored access token and account")
	}
	if !credentials.Expiry.Equal(record.AccessExpiry) {
		t.Fatalf("expiry = %v, want %v", credentials.Expiry, record.AccessExpiry)
	}
	if gh.refreshCount() != 0 {
		t.Fatalf("refresh requests = %d, want 0 for a fresh token", gh.refreshCount())
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
	if credentials.BearerToken != "access-1" || credentials.AccountID != "4242" {
		t.Fatal("runtime credentials did not carry the refreshed token and account")
	}
	if !credentials.Expiry.Equal(at.Add(8 * time.Hour)) {
		t.Fatalf("expiry = %v, want %v", credentials.Expiry, at.Add(8*time.Hour))
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
	if !loaded.ObtainedAt.Equal(at) {
		t.Fatalf("obtained at = %v, want %v", loaded.ObtainedAt, at)
	}
	if loaded.AccountID != "4242" || loaded.RefreshRejected {
		t.Fatalf("refreshed record identity = %q, rejected = %v", loaded.AccountID, loaded.RefreshRejected)
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
	if credentials.BearerToken != "access-1" {
		t.Fatal("retry after a transient failure did not use the refreshed token")
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
	err := new(Service).recordRejection(stateDir, newer.Instance, refused)
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("recordRejection() error = %v, want ErrLoginRequired", err)
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
	err := new(Service).recordRejection(stateDir, refused.Instance, refused)
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("recordRejection() error = %v, want ErrLoginRequired", err)
	}
	if _, err := LoadAuth(stateDir, refused.Instance); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("LoadAuth() error = %v, want the record to stay deleted", err)
	}
}
