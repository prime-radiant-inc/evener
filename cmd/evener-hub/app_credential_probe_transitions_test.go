package hub

// A credential probe is evidence about the access the instance had when it
// started. These are the supported access transitions that happen while it is
// in flight: the instance's endpoint or credential-selector edit, a stored-key
// write or clear, and the edits that do not change access. Every case runs the
// real InstanceEdit/API-key mutation path below a real local HTTP model-list
// boundary, so the outcome is the one the guided connector would receive.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// probeRequest is one model-list request the local boundary received. The
// Authorization header is what the regression needs: the replacement
// credential must be the one a fresh check dials, and the old one must not be
// reused.
type probeRequest struct {
	path          string
	authorization string
	// quotaProject is the request's x-goog-user-project, the header the GCP
	// authenticator derives from the resolved GOOGLE_VERTEX_PROJECT variable
	// for a user credential: the regression needs to see it present on the
	// held request and gone on the replacement's.
	quotaProject string
}

// probeBoundary is a local model-list endpoint. The first request is held
// until release when holdFirst is set, so a test can make the instance's
// access change while the answer is in flight.
type probeBoundary struct {
	server      *httptest.Server
	hold        atomic.Bool
	mu          sync.Mutex
	reqs        []probeRequest
	held        chan struct{}
	release     chan struct{}
	heldOnce    sync.Once
	releaseOnce sync.Once
	status      atomic.Int32
	// vertex serves the Vertex publisher-model listing shape instead of the
	// OpenAI one, for instances on the vertex transports.
	vertex bool
}

func newProbeBoundary(t *testing.T, hold bool) *probeBoundary {
	t.Helper()
	b := &probeBoundary{
		held:    make(chan struct{}),
		release: make(chan struct{}),
	}
	b.hold.Store(hold)
	b.status.Store(http.StatusOK)
	b.server = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(func() {
		// A failing assertion can leave the first request parked; unblock it so
		// the server can shut down instead of hanging the test binary.
		b.unleash()
		b.server.Close()
	})
	return b
}

func (b *probeBoundary) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	b.reqs = append(b.reqs, probeRequest{path: r.URL.Path, authorization: r.Header.Get("Authorization"), quotaProject: r.Header.Get("x-goog-user-project")})
	b.mu.Unlock()
	if b.hold.Load() {
		b.heldOnce.Do(func() { close(b.held) })
		<-b.release
	}
	w.Header().Set("Content-Type", "application/json")
	if b.status.Load() == http.StatusUnauthorized {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"fixture rejection"}}`))
		return
	}
	if b.vertex {
		// The publisher listing the vertex transports page through
		// (llm/providers/google/vertex_models.go): one GA text row is a live
		// listing a credential check can succeed on.
		_, _ = w.Write([]byte(`{"publisherModels":[{"name":"publishers/google/models/gemini-3.8-flash","launchStage":"GA"}]}`))
		return
	}
	_, _ = w.Write([]byte(`{"data":[{"id":"fixture-model","type":"model","display_name":"Fixture model"}],"has_more":false}`))
}

// newVertexProbeBoundary is a probeBoundary that answers with the Vertex
// publisher-listing shape, for an instance on a vertex transport.
func newVertexProbeBoundary(t *testing.T, hold bool) *probeBoundary {
	t.Helper()
	b := newProbeBoundary(t, hold)
	b.vertex = true
	return b
}

func (b *probeBoundary) requests() []probeRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]probeRequest(nil), b.reqs...)
}

func (b *probeBoundary) acknowledge(t *testing.T) {
	t.Helper()
	select {
	case <-b.held:
	case <-time.After(10 * time.Second):
		t.Fatal("the held probe never reached the local model-list boundary")
	}
}

func (b *probeBoundary) unleash() { b.releaseOnce.Do(func() { close(b.release) }) }

// accessProbeFixture is one real auth controller and the real instance editor
// over one providers.toml. The probe loader loads the file the hub just wrote,
// so a fresh check dials the edited access through the local boundary.
type accessProbeFixture struct {
	auth  *hubAuthController
	inst  *hubInstancesController
	toml  string
	creds string
	env   map[string]string
}

func newAccessProbeFixture(t *testing.T, toml string, env map[string]string) *accessProbeFixture {
	t.Helper()
	clearProviderKeysFromEnvironment(t)
	dir := t.TempDir()
	credsDir := filepath.Join(dir, "creds")
	if err := os.MkdirAll(credsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tomlPath := writeProvidersToml(t, dir, toml)
	auth := newTestAuthController(t, credsDir, t.TempDir(), tomlPath, env)
	credsPath := filepath.Join(credsDir, "credentials.toml")
	installRealProbeLoader(auth, credsPath, env)
	return &accessProbeFixture{
		auth:  auth,
		inst:  &hubInstancesController{reg: auth.reg, providersConfigPath: tomlPath, auth: auth},
		toml:  tomlPath,
		creds: credsPath,
		env:   env,
	}
}

// installRealProbeLoader makes the probe load the providers file the hub just
// wrote, through the real registry and HTTP client, with the fixture's
// environment map.
func installRealProbeLoader(auth *hubAuthController, credsPath string, env map[string]string) {
	auth.credentialTestLoader = func(path string, noUserLayer bool) (credentialProbeClient, error) {
		store, err := credentials.LoadStore(credsPath)
		if err != nil {
			return nil, err
		}
		opts := testProbeRegistryOptions(auth.stateDir, store, func(name string) (string, bool) {
			v, ok := env[name]
			return v, ok
		})
		if noUserLayer {
			opts = append(opts, registry.WithNoUserLayer())
		} else {
			opts = append(opts, registry.WithConfigPath(path))
		}
		r, err := registry.Load(opts...)
		if err != nil {
			return nil, err
		}
		return llm.NewClient(llm.WithRegistry(r)), nil
	}
}

type probeOutcome struct {
	resp appwire.AuthTestResponse
	err  error
}

func startProbe(c *hubAuthController, name string) <-chan probeOutcome {
	done := make(chan probeOutcome, 1)
	go func() {
		resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: name})
		done <- probeOutcome{resp: resp, err: err}
	}()
	return done
}

// joinProbe starts a second caller and waits until the shared in-flight call
// reports it joined, so a later release cannot race a fresh probe of its own.
func joinProbe(t *testing.T, c *hubAuthController, name string) <-chan probeOutcome {
	t.Helper()
	joined := make(chan struct{})
	var once sync.Once
	c.credentialTestJoined = func() { once.Do(func() { close(joined) }) }
	done := startProbe(c, name)
	select {
	case <-joined:
	case <-time.After(10 * time.Second):
		t.Fatal("the second caller did not join the pending probe")
	}
	return done
}

func waitProbe(t *testing.T, done <-chan probeOutcome) probeOutcome {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("probe did not settle")
		return probeOutcome{}
	}
}

func (f *accessProbeFixture) status(t *testing.T, name string) appwire.AuthStatusResponse {
	t.Helper()
	status, err := f.auth.Status(appwire.AuthStatusParams{Provider: name})
	if err != nil {
		t.Fatalf("Status(%s): %v", name, err)
	}
	return status
}

func (f *accessProbeFixture) edit(t *testing.T, params appwire.InstanceEditParams) {
	t.Helper()
	if err := f.inst.Edit(params); err != nil {
		t.Fatalf("Edit(%+v): %v", params, err)
	}
}

func (f *accessProbeFixture) freshCheck(t *testing.T, name string) appwire.AuthTestResponse {
	t.Helper()
	resp, err := f.auth.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: name})
	if err != nil {
		t.Fatalf("fresh TestCredentials: %v", err)
	}
	return resp
}

func keylessTOML(baseURL, extra string) string {
	return fmt.Sprintf("[providers.local]\nbase = \"openai-compatible\"\nbase_url = %q\nauth = \"none\"\n%s", baseURL, extra)
}

func headerTOML(baseURL, header string) string {
	return fmt.Sprintf("[providers.local]\nbase = \"openai-compatible\"\nbase_url = %q\n\n[providers.local.credential_headers]\nAuthorization = %q\n", baseURL, header)
}

// templatedHeaderTOML is one credential-header instance whose base URL is a
// template over a resolved variable: the ordinary supported shape whose
// literalization to the URL it already resolves to changes no request byte.
func templatedHeaderTOML(baseURL string) string {
	return fmt.Sprintf("[providers.local]\nbase = \"openai-compatible\"\nbase_url = \"{BASE_URL}\"\n\n[providers.local.vars]\n\"BASE_URL\" = %q\n\n[providers.local.credential_headers]\nAuthorization = \"Bearer $KEY_A\"\n", baseURL)
}

func headerEnv() map[string]string {
	return map[string]string{"KEY_A": "fixture-key-a", "KEY_B": "fixture-key-b"}
}

func optionalBearerTOML(baseURL string) string {
	return fmt.Sprintf("[providers.local]\nbase = \"openai-compatible\"\nbase_url = %q\nauth = \"optional-bearer\"\n", baseURL)
}

// implicitAnthropicEnv resolves the curated "anthropic" instance against the
// local boundary. No providers.toml entry is written, so the instance is
// implicit until an edit authors one.
func implicitAnthropicEnv(baseURL string) map[string]string {
	return map[string]string{
		"ANTHROPIC_API_KEY":  "fixture-implicit-key",
		"ANTHROPIC_BASE_URL": baseURL,
	}
}

// vertexUserADCTOML is one instance on the curated google-vertex base whose
// URL-template variables are pinned to the local boundary, so its resolved
// destination names the fixture and its model-list requests reach the
// scripted provider boundary.
func vertexUserADCTOML(host, project, location string) string {
	return fmt.Sprintf("[providers.local]\nbase = \"google-vertex\"\n\n[providers.local.vars]\n\"GOOGLE_VERTEX_HOST\" = %q\n\"GOOGLE_VERTEX_PROJECT\" = %q\n\"GOOGLE_VERTEX_LOCATION\" = %q\n", host, project, location)
}

// newADCTokenEndpoint is the scripted OAuth token endpoint: the real oauth2
// refresh flow POSTs here and mints a fixture access token. It is the token
// half of the scripted external boundary; the model-list half is the probe
// boundary.
func newADCTokenEndpoint(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fixture-adc-token","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// adcUserCredentialsFile writes a fixture authorized-user ADC file whose
// token_uri is the scripted endpoint. The registry's own token-endpoint gate
// (registry.CheckCredentialJSON) applies only to credential JSON stored for
// an instance; the ADC lookup path reads the file with the real Google
// library, which honors the file's token_uri, so this is the one route where
// the real lookup and refresh run against a local boundary.
func adcUserCredentialsFile(t *testing.T, tokenURI string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "application_default_credentials.json")
	raw := fmt.Sprintf(`{"type":"authorized_user","client_id":"fixture-client","client_secret":"fixture-secret","refresh_token":"fixture-refresh","token_uri":%q}`, tokenURI)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write ADC file: %v", err)
	}
	return path
}

// newVertexADCProbeFixture builds the access-probe fixture around one
// google-vertex instance resolved against the local boundary, with
// application-default credentials modeled by a fixture authorized-user file:
// the registry resolves the instance's credential source as adc through the
// fixture environment, and the real Google lookup that runs at request time
// reads the same file from the process environment, refreshing against the
// scripted endpoint. No ambient ADC is read: the process variable points at
// the fixture file for the duration of the test.
func newVertexADCProbeFixture(t *testing.T, b *probeBoundary) *accessProbeFixture {
	t.Helper()
	token := newADCTokenEndpoint(t)
	adcFile := adcUserCredentialsFile(t, token.URL+"/token")
	f := newAccessProbeFixture(t, vertexUserADCTOML(b.server.URL, "fixture-project", "global"), map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": adcFile})
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adcFile)
	return f
}

// TestCredentialProbeAccessTransitions is the invariant table: a probe accepts
// its outcome only if the access it read is still the access the instance has,
// and a refusal is the generic Conflict, not a verdict about the replacement.
func TestCredentialProbeAccessTransitions(t *testing.T) {
	t.Run("keyless endpoint edit refuses old success to original and joiner", func(t *testing.T) {
		b := newProbeBoundary(t, true)
		f := newAccessProbeFixture(t, keylessTOML(b.server.URL+"/a/v1", ""), nil)
		original := startProbe(f.auth, "local")
		b.acknowledge(t)

		f.edit(t, appwire.InstanceEditParams{Name: "local", BaseURL: b.server.URL + "/b/v1"})
		joined := joinProbe(t, f.auth, "local")
		b.unleash()

		got := waitProbe(t, original)
		assertGenericConflict(t, got.err)
		if got.resp.Status == appwire.AuthTestStatusSuccess {
			t.Fatal("the held check of endpoint A reported success for the edited endpoint B")
		}
		joinedGot := waitProbe(t, joined)
		assertGenericConflict(t, joinedGot.err)
		if joinedGot.resp.Status == appwire.AuthTestStatusSuccess {
			t.Fatal("the joined caller inherited a success about endpoint A for endpoint B")
		}

		// A genuinely fresh check is evidence about the edited access: it dials
		// B, and only after B's own answer may it succeed.
		fresh := f.freshCheck(t, "local")
		if fresh.Status != appwire.AuthTestStatusSuccess {
			t.Fatalf("fresh check status = %q (%q), want success", fresh.Status, fresh.Message)
		}
		paths := b.requests()
		if len(paths) != 2 || !strings.Contains(paths[1].path, "/b/") {
			t.Fatalf("probe requests = %+v, want the fresh check to dial endpoint B", paths)
		}
	})

	t.Run("keyless endpoint edit refuses old rejection and records nothing keyless", func(t *testing.T) {
		b := newProbeBoundary(t, true)
		b.status.Store(http.StatusUnauthorized)
		f := newAccessProbeFixture(t, keylessTOML(b.server.URL+"/a/v1", ""), nil)
		original := startProbe(f.auth, "local")
		b.acknowledge(t)

		f.edit(t, appwire.InstanceEditParams{Name: "local", BaseURL: b.server.URL + "/b/v1"})
		b.unleash()

		got := waitProbe(t, original)
		assertGenericConflict(t, got.err)
		if got.resp.Status == appwire.AuthTestStatusAuthRejected {
			t.Fatal("the old endpoint's rejection was published as the edited endpoint's verdict")
		}
		if errText := f.status(t, "local").Error; errText != "" {
			t.Fatalf("status error = %q: a keyless refusal created a credential-rejection record", errText)
		}
	})

	t.Run("keyless unchanged destination still succeeds", func(t *testing.T) {
		b := newProbeBoundary(t, false)
		f := newAccessProbeFixture(t, keylessTOML(b.server.URL+"/a/v1", ""), nil)
		resp := f.freshCheck(t, "local")
		if resp.Status != appwire.AuthTestStatusSuccess {
			t.Fatalf("unchanged keyless check = %q (%q), want success", resp.Status, resp.Message)
		}
		if errText := f.status(t, "local").Error; errText != "" {
			t.Fatalf("status error = %q, want none", errText)
		}
	})

	t.Run("same-name credential header edit refuses old success and keeps the public revision", func(t *testing.T) {
		b := newProbeBoundary(t, true)
		f := newAccessProbeFixture(t, headerTOML(b.server.URL+"/v1", "Bearer $KEY_A"), headerEnv())
		before := f.status(t, "local")
		if before.ConfigRevision == "" || before.ActiveSource != "credential_headers" {
			t.Fatalf("precondition: status = %+v, want a resolved credential_headers identity", before)
		}
		original := startProbe(f.auth, "local")
		b.acknowledge(t)

		f.edit(t, appwire.InstanceEditParams{Name: "local", CredentialHeader: "Authorization=Bearer $KEY_B"})
		after := f.status(t, "local")
		if after.ConfigRevision != before.ConfigRevision || after.ActiveSource != before.ActiveSource {
			t.Fatalf("precondition: the header-selector edit moved the public identity %+v -> %+v; this case must exercise an unchanged revision", before, after)
		}
		joined := joinProbe(t, f.auth, "local")
		b.unleash()

		got := waitProbe(t, original)
		assertGenericConflict(t, got.err)
		if got.resp.Status == appwire.AuthTestStatusSuccess {
			t.Fatal("the old key's success was accepted for the replacement key")
		}
		joinedGot := waitProbe(t, joined)
		assertGenericConflict(t, joinedGot.err)
		if joinedGot.resp.Status == appwire.AuthTestStatusSuccess {
			t.Fatal("the joined caller inherited the old key's success for the replacement key")
		}
		if errText := f.status(t, "local").Error; errText != "" {
			t.Fatalf("status error = %q: the old key's outcome poisoned the replacement", errText)
		}

		fresh := f.freshCheck(t, "local")
		if fresh.Status != appwire.AuthTestStatusSuccess {
			t.Fatalf("fresh check status = %q (%q), want success", fresh.Status, fresh.Message)
		}
		reqs := b.requests()
		if len(reqs) != 2 {
			t.Fatalf("probe requests = %+v, want exactly the old and fresh checks", reqs)
		}
		if reqs[0].authorization != "Bearer fixture-key-a" {
			t.Fatalf("old request Authorization = %q, want the held key A", reqs[0].authorization)
		}
		if reqs[1].authorization != "Bearer fixture-key-b" {
			t.Fatalf("fresh request Authorization = %q, want the replacement key B", reqs[1].authorization)
		}
	})

	t.Run("header edit publication does not let the probe settle before its fence", func(t *testing.T) {
		b := newProbeBoundary(t, true)
		f := newAccessProbeFixture(t, headerTOML(b.server.URL+"/v1", "Bearer $KEY_A"), headerEnv())
		before := f.status(t, "local")
		// The settlement-entry acknowledgment: it fires in
		// settleCredentialProbe immediately before the shared lock, so the
		// bounded observation below starts once the probe's outcome is back
		// and settlement is at credMu.
		settled := make(chan struct{})
		var settleOnce sync.Once
		f.auth.rejections.settlementEntered = func() { settleOnce.Do(func() { close(settled) }) }
		original := startProbe(f.auth, "local")
		b.acknowledge(t)

		published := make(chan struct{})
		finish := make(chan struct{})
		var publishOnce, finishOnce sync.Once
		allowFinish := func() { finishOnce.Do(func() { close(finish) }) }
		defer allowFinish()
		f.inst.afterAccessReload = func() {
			publishOnce.Do(func() { close(published) })
			<-finish
		}
		edited := make(chan error, 1)
		go func() {
			edited <- f.inst.Edit(appwire.InstanceEditParams{Name: "local", CredentialHeader: "Authorization=Bearer $KEY_B"})
		}()
		select {
		case <-published:
		case <-time.After(10 * time.Second):
			t.Fatal("the edit never published the replacement access")
		}
		// The replacement is current but the edit has not advanced the fence,
		// and the edit holds credMu exclusively while it is parked here.
		// Release the provider's answer and wait for the acknowledgment: the
		// probe's outcome is back and settlement has reached credMu.
		b.unleash()
		select {
		case <-settled:
		case <-time.After(10 * time.Second):
			t.Fatal("the probe's outcome never reached settlement")
		}
		// Bounded observation, in line with the repository's existing lock
		// test (TestInstances_MutationsWaitForAnInFlightCredentialWrite): no
		// outcome about key A is expected while the edit holds credMu between
		// its publication and its fence. An outcome inside the window is a
		// settlement that published without the lock; a silent window is the
		// repaired behavior. The window cannot distinguish blocked settlement
		// from an unlocked settlement delayed past it, and is not a proof of
		// blocking.
		select {
		case got := <-original:
			t.Fatalf("the probe settled between the edit's publication and its fence: resp=%+v err=%v", got.resp, got.err)
		case <-time.After(250 * time.Millisecond):
		}
		allowFinish()
		if err := <-edited; err != nil {
			t.Fatalf("Edit: %v", err)
		}
		after := f.status(t, "local")
		if after.ConfigRevision != before.ConfigRevision || after.ActiveSource != before.ActiveSource {
			t.Fatalf("precondition: the edit moved the public identity %+v -> %+v", before, after)
		}
		got := waitProbe(t, original)
		assertGenericConflict(t, got.err)
		if got.resp.Status == appwire.AuthTestStatusSuccess {
			t.Fatal("the old key's success was accepted for the replacement the edit published")
		}
	})

	t.Run("implicit equal effective access keeps a standing rejection and pending probe", func(t *testing.T) {
		b := newProbeBoundary(t, false)
		b.status.Store(http.StatusUnauthorized)
		f := newAccessProbeFixture(t, "", implicitAnthropicEnv(b.server.URL+"/v1"))
		inst, ok := f.auth.reg.Get().Instance("anthropic")
		if !ok || inst.Protocol == "" {
			t.Fatalf("precondition: implicit anthropic instance = %+v, present %v", inst, ok)
		}
		if resp := f.freshCheck(t, "anthropic"); resp.Status != appwire.AuthTestStatusAuthRejected {
			t.Fatalf("precondition: first check = %q (%q), want auth_rejected", resp.Status, resp.Message)
		}
		before := f.status(t, "anthropic")
		if before.Error == "" {
			t.Fatal("precondition: the first 401 was not recorded")
		}

		b.hold.Store(true)
		original := startProbe(f.auth, "anthropic")
		b.acknowledge(t)
		// Explicitly author the protocol the implicit instance already resolves to:
		// the record changes, the effective access does not.
		if err := f.inst.Edit(appwire.InstanceEditParams{Name: "anthropic", Protocol: inst.Protocol}); err != nil {
			t.Fatalf("Edit: %v", err)
		}
		after := f.status(t, "anthropic")
		if after.ConfigRevision == "" || after.ConfigRevision != before.ConfigRevision || after.ActiveSource != before.ActiveSource {
			t.Fatalf("precondition: equal effective access moved the public identity %+v -> %+v", before, after)
		}
		if after.Error == "" {
			t.Fatal("an edit that re-authored equal effective access dropped the standing rejection")
		}

		b.unleash()
		got := waitProbe(t, original)
		if got.err != nil {
			t.Fatalf("equal effective access refused a pending probe: %v", got.err)
		}
		if got.resp.Status != appwire.AuthTestStatusAuthRejected {
			t.Fatalf("probe status = %q (%q), want the standing rejection's own answer", got.resp.Status, got.resp.Message)
		}
		if f.status(t, "anthropic").Error == "" {
			t.Fatal("the standing rejection disappeared after the probe settled")
		}
	})

	t.Run("keyless optional-bearer access replaced by a stored key refuses old keyless success", func(t *testing.T) {
		b := newProbeBoundary(t, true)
		f := newAccessProbeFixture(t, optionalBearerTOML(b.server.URL+"/v1"), nil)
		if source := f.status(t, "local").ActiveSource; source != "none" {
			t.Fatalf("precondition: active source = %q, want none", source)
		}
		original := startProbe(f.auth, "local")
		b.acknowledge(t)

		if _, err := f.auth.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "local", Value: "fixture-stored"}); err != nil {
			t.Fatalf("ApiKeySet: %v", err)
		}
		if source := f.status(t, "local").ActiveSource; source != "store" {
			t.Fatalf("precondition: active source = %q after the write, want store", source)
		}
		b.unleash()
		got := waitProbe(t, original)
		assertGenericConflict(t, got.err)
		if got.resp.Status == appwire.AuthTestStatusSuccess {
			t.Fatal("the old keyless success was accepted after the access gained a stored key")
		}
		if fresh := f.freshCheck(t, "local"); fresh.Status != appwire.AuthTestStatusSuccess {
			t.Fatalf("fresh check = %q (%q), want success", fresh.Status, fresh.Message)
		}
		reqs := b.requests()
		if last := reqs[len(reqs)-1]; last.authorization != "Bearer fixture-stored" {
			t.Fatalf("fresh request Authorization = %q, want the stored key", last.authorization)
		}
	})

	t.Run("keyless auth-none to authored header refuses old keyless success", func(t *testing.T) {
		b := newProbeBoundary(t, true)
		f := newAccessProbeFixture(t, keylessTOML(b.server.URL+"/v1", ""), headerEnv())
		original := startProbe(f.auth, "local")
		b.acknowledge(t)

		f.edit(t, appwire.InstanceEditParams{Name: "local", CredentialHeader: "Authorization=Bearer $KEY_A"})
		if source := f.status(t, "local").ActiveSource; source != "credential_headers" {
			t.Fatalf("precondition: active source = %q after the edit, want credential_headers", source)
		}
		b.unleash()
		got := waitProbe(t, original)
		assertGenericConflict(t, got.err)
		if got.resp.Status == appwire.AuthTestStatusSuccess {
			t.Fatal("the old keyless success was accepted after the access gained a credential header")
		}
		if fresh := f.freshCheck(t, "local"); fresh.Status != appwire.AuthTestStatusSuccess {
			t.Fatalf("fresh check = %q (%q), want success", fresh.Status, fresh.Message)
		}
		reqs := b.requests()
		if last := reqs[len(reqs)-1]; last.authorization != "Bearer fixture-key-a" {
			t.Fatalf("fresh request Authorization = %q, want the authored header", last.authorization)
		}
	})

	t.Run("failed-before-application edit keeps the standing rejection and pending probe", func(t *testing.T) {
		b := newProbeBoundary(t, false)
		b.status.Store(http.StatusUnauthorized)
		f := newAccessProbeFixture(t, headerTOML(b.server.URL+"/v1", "Bearer $KEY_A"), headerEnv())
		if resp := f.freshCheck(t, "local"); resp.Status != appwire.AuthTestStatusAuthRejected {
			t.Fatalf("precondition: first check = %q (%q), want auth_rejected", resp.Status, resp.Message)
		}
		if f.status(t, "local").Error == "" {
			t.Fatal("precondition: the first 401 was not recorded")
		}

		b.hold.Store(true)
		original := startProbe(f.auth, "local")
		b.acknowledge(t)
		err := f.inst.Edit(appwire.InstanceEditParams{Name: "local", CredentialHeader: "Authorization=fixture-literal-secret"})
		if err == nil {
			t.Fatal("the invalid credential header edit succeeded")
		}
		if f.status(t, "local").Error == "" {
			t.Fatal("a refused-before-write edit dropped the standing rejection")
		}
		b.unleash()
		got := waitProbe(t, original)
		if got.err != nil {
			t.Fatalf("a refused-before-write edit refused the pending probe: %v", got.err)
		}
		if got.resp.Status != appwire.AuthTestStatusAuthRejected {
			t.Fatalf("probe status = %q (%q), want the standing rejection's answer", got.resp.Status, got.resp.Message)
		}
		if f.status(t, "local").Error == "" {
			t.Fatal("the standing rejection disappeared after the probe settled")
		}
	})

	t.Run("standing access edit whose restore fails advances the fence", func(t *testing.T) {
		b := newProbeBoundary(t, false)
		b.status.Store(http.StatusUnauthorized)
		clearProviderKeysFromEnvironment(t)
		dir := t.TempDir()
		credsDir := filepath.Join(dir, "creds")
		if err := os.MkdirAll(credsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		stateDir := t.TempDir()
		tomlPath := writeProvidersToml(t, dir, headerTOML(b.server.URL+"/v1", "Bearer $KEY_A"))
		var breakOnce sync.Once
		ctl := newTestInstancesController(t, tomlPath, credsDir, stateDir, headerEnv(), instancesControllerHooks{
			load: func(load int) error {
				if load != 2 {
					return nil
				}
				breakOnce.Do(func() {
					if err := os.Remove(tomlPath); err != nil {
						t.Errorf("remove providers.toml: %v", err)
					}
					if err := os.Mkdir(tomlPath, 0o700); err != nil {
						t.Errorf("block providers.toml: %v", err)
					}
				})
				return errors.New("providers config could not be read")
			},
		})
		installRealProbeLoader(ctl.auth, filepath.Join(credsDir, "credentials.toml"), headerEnv())
		if resp, err := ctl.auth.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "local"}); err != nil || resp.Status != appwire.AuthTestStatusAuthRejected {
			t.Fatalf("precondition: first check = %+v (%v), want auth_rejected", resp, err)
		}

		b.hold.Store(true)
		original := startProbe(ctl.auth, "local")
		b.acknowledge(t)
		err := ctl.Edit(appwire.InstanceEditParams{Name: "local", CredentialHeader: "Authorization=Bearer $KEY_B"})
		if err == nil {
			t.Fatal("the edit whose reload and restore both fail succeeded")
		}
		if !errors.Is(err, hubcore.ErrWriteApplied) {
			t.Fatalf("edit error = %v, want a standing write", err)
		}
		b.unleash()
		got := waitProbe(t, original)
		assertGenericConflict(t, got.err)
		ctl.auth.rejections.mu.Lock()
		_, kept := ctl.auth.rejections.byName["local"]
		ctl.auth.rejections.mu.Unlock()
		if kept {
			t.Fatal("a standing access replacement kept the rejection of the replaced access")
		}
	})

	t.Run("same-name credential header edit refuses old rejection and checks the replacement", func(t *testing.T) {
		b := newProbeBoundary(t, true)
		b.status.Store(http.StatusUnauthorized)
		f := newAccessProbeFixture(t, headerTOML(b.server.URL+"/v1", "Bearer $KEY_A"), headerEnv())
		original := startProbe(f.auth, "local")
		b.acknowledge(t)

		f.edit(t, appwire.InstanceEditParams{Name: "local", CredentialHeader: "Authorization=Bearer $KEY_B"})
		joined := joinProbe(t, f.auth, "local")
		b.unleash()

		got := waitProbe(t, original)
		assertGenericConflict(t, got.err)
		if got.resp.Status == appwire.AuthTestStatusAuthRejected {
			t.Fatal("the old key's rejection was published as the replacement's verdict")
		}
		joinedGot := waitProbe(t, joined)
		assertGenericConflict(t, joinedGot.err)
		if joinedGot.resp.Status == appwire.AuthTestStatusAuthRejected {
			t.Fatal("the joined caller inherited the old key's rejection for the replacement")
		}
		if errText := f.status(t, "local").Error; errText != "" {
			t.Fatalf("status error = %q: the old key's rejection was recorded against the replacement", errText)
		}

		// The replacement's own answer is what may be recorded now.
		fresh := f.freshCheck(t, "local")
		if fresh.Status != appwire.AuthTestStatusAuthRejected {
			t.Fatalf("fresh check status = %q (%q), want the replacement's own rejection", fresh.Status, fresh.Message)
		}
		if errText := f.status(t, "local").Error; errText == "" {
			t.Fatal("a genuinely rejected replacement credential was not recorded")
		}
	})

	t.Run("unchanged credential records its own rejection", func(t *testing.T) {
		b := newProbeBoundary(t, false)
		b.status.Store(http.StatusUnauthorized)
		f := newAccessProbeFixture(t, headerTOML(b.server.URL+"/v1", "Bearer $KEY_A"), headerEnv())
		resp := f.freshCheck(t, "local")
		if resp.Status != appwire.AuthTestStatusAuthRejected {
			t.Fatalf("check status = %q (%q), want auth_rejected", resp.Status, resp.Message)
		}
		if errText := f.status(t, "local").Error; errText == "" {
			t.Fatal("a genuinely rejected unchanged credential was not recorded")
		}
	})

	t.Run("no-effective-access edit keeps the probe current", func(t *testing.T) {
		b := newProbeBoundary(t, true)
		f := newAccessProbeFixture(t, headerTOML(b.server.URL+"/v1", "Bearer $KEY_A"), headerEnv())
		original := startProbe(f.auth, "local")
		b.acknowledge(t)

		f.edit(t, appwire.InstanceEditParams{Name: "local", CredentialHeader: "Authorization=Bearer $KEY_A"})
		b.unleash()

		got := waitProbe(t, original)
		if got.err != nil {
			t.Fatalf("an edit that re-authored the same access refused the probe: %v", got.err)
		}
		if got.resp.Status != appwire.AuthTestStatusSuccess {
			t.Fatalf("status = %q (%q), want success", got.resp.Status, got.resp.Message)
		}
	})

	t.Run("model-only change keeps the probe current", func(t *testing.T) {
		b := newProbeBoundary(t, true)
		toml := headerTOML(b.server.URL+"/v1", "Bearer $KEY_A") + "\n[providers.local.models.\"house-model\"]\n"
		f := newAccessProbeFixture(t, toml, headerEnv())
		original := startProbe(f.auth, "local")
		b.acknowledge(t)

		if err := f.inst.SetModelDisabled(appwire.InstanceSetModelDisabledParams{Name: "local", Model: "house-model", Disabled: true}); err != nil {
			t.Fatalf("SetModelDisabled: %v", err)
		}
		b.unleash()

		got := waitProbe(t, original)
		if got.err != nil {
			t.Fatalf("a model-only change refused the probe: %v", got.err)
		}
		if got.resp.Status != appwire.AuthTestStatusSuccess {
			t.Fatalf("status = %q (%q), want success", got.resp.Status, got.resp.Message)
		}
	})

	t.Run("unrelated instance access edit keeps the probe current", func(t *testing.T) {
		b := newProbeBoundary(t, true)
		toml := headerTOML(b.server.URL+"/v1", "Bearer $KEY_A") +
			"\n[providers.other]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\n\n[providers.other.credential_headers]\nAuthorization = \"Bearer $KEY_A\"\n"
		f := newAccessProbeFixture(t, toml, headerEnv())
		original := startProbe(f.auth, "local")
		b.acknowledge(t)

		f.edit(t, appwire.InstanceEditParams{Name: "other", CredentialHeader: "Authorization=Bearer $KEY_B"})
		b.unleash()

		got := waitProbe(t, original)
		if got.err != nil {
			t.Fatalf("an unrelated instance's edit refused this probe: %v", got.err)
		}
		if got.resp.Status != appwire.AuthTestStatusSuccess {
			t.Fatalf("status = %q (%q), want success", got.resp.Status, got.resp.Message)
		}
	})

	t.Run("failed edit that rolls back keeps the probe current", func(t *testing.T) {
		b := newProbeBoundary(t, true)
		// A standalone instance with no base resolves from base_url alone, so
		// clearing base_url is a write that cannot load and is rolled back.
		toml := fmt.Sprintf("[providers.local]\nprotocol = \"openai-chat\"\nbase_url = %q\nauth = \"none\"\n", b.server.URL+"/a/v1")
		f := newAccessProbeFixture(t, toml, nil)
		original := startProbe(f.auth, "local")
		b.acknowledge(t)

		if err := f.inst.Edit(appwire.InstanceEditParams{Name: "local", ClearBaseURL: true}); err == nil {
			t.Fatal("the unloadable edit succeeded")
		}
		b.unleash()

		got := waitProbe(t, original)
		if got.err != nil {
			t.Fatalf("a rolled-back edit refused the probe: %v", got.err)
		}
		if got.resp.Status != appwire.AuthTestStatusSuccess {
			t.Fatalf("status = %q (%q), want success", got.resp.Status, got.resp.Message)
		}
	})

	t.Run("stored key replacement refuses the probe that dialed the old key", func(t *testing.T) {
		b := newProbeBoundary(t, true)
		f := newAccessProbeFixture(t, fmt.Sprintf("[providers.local]\nbase = \"openai-compatible\"\nbase_url = %q\n", b.server.URL+"/v1"), nil)
		if err := f.auth.creds.Set("local", "fixture-stored-a"); err != nil {
			t.Fatal(err)
		}
		if err := f.auth.reloadRegistry(); err != nil {
			t.Fatalf("reload: %v", err)
		}
		original := startProbe(f.auth, "local")
		b.acknowledge(t)

		if _, err := f.auth.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "local", Value: "fixture-stored-b"}); err != nil {
			t.Fatalf("ApiKeySet: %v", err)
		}
		b.unleash()

		got := waitProbe(t, original)
		assertGenericConflict(t, got.err)
		if got.resp.Status == appwire.AuthTestStatusSuccess {
			t.Fatal("the old stored key's success was accepted after a replacement")
		}
	})

	t.Run("stored key removal refuses the probe that dialed it", func(t *testing.T) {
		b := newProbeBoundary(t, true)
		f := newAccessProbeFixture(t, fmt.Sprintf("[providers.local]\nbase = \"openai-compatible\"\nbase_url = %q\n", b.server.URL+"/v1"), nil)
		if err := f.auth.creds.Set("local", "fixture-stored-a"); err != nil {
			t.Fatal(err)
		}
		if err := f.auth.reloadRegistry(); err != nil {
			t.Fatalf("reload: %v", err)
		}
		original := startProbe(f.auth, "local")
		b.acknowledge(t)

		if _, err := f.auth.ApiKeyClear(appwire.AuthApiKeyClearParams{Provider: "local"}); err != nil {
			t.Fatalf("ApiKeyClear: %v", err)
		}
		b.unleash()

		got := waitProbe(t, original)
		assertGenericConflict(t, got.err)
		if got.resp.Status == appwire.AuthTestStatusSuccess {
			t.Fatal("the removed key's success was accepted as the current access")
		}
	})

	t.Run("vertex url literalization changes the auth header and refuses the old success", func(t *testing.T) {
		b := newVertexProbeBoundary(t, true)
		f := newVertexADCProbeFixture(t, b)
		res, err := f.auth.reg.Get().ResolveInstancePresence("local")
		if err != nil {
			t.Fatalf("precondition: resolve: %v", err)
		}
		resolvedURL := res.Transport.BaseURL
		if !strings.Contains(resolvedURL, "/projects/fixture-project/locations/global") {
			t.Fatalf("precondition: resolved base URL = %q, want the fixture project and location in the URL", resolvedURL)
		}
		if project := res.Transport.Vars["GOOGLE_VERTEX_PROJECT"]; project != "fixture-project" {
			t.Fatalf("precondition: resolved project var = %q, want the fixture project the authenticator reads", project)
		}
		before := f.status(t, "local")
		if before.ConfigRevision == "" || before.ActiveSource != "adc" {
			t.Fatalf("precondition: status = %+v, want a resolved ADC identity", before)
		}

		original := startProbe(f.auth, "local")
		b.acknowledge(t)
		held := b.requests()
		if len(held) != 1 || held[0].authorization != "Bearer fixture-adc-token" || held[0].quotaProject != "fixture-project" {
			t.Fatalf("held request = %+v, want the fixture ADC token and the quota-project header the resolved project variable carries", held)
		}

		// Supported edit: author the exact literal URL the instance already
		// resolves to. Every resolved destination and the public identity stay
		// equal; the resolved transport vars the template carried do not, and
		// for a user credential they are what puts the quota-project header on
		// the request.
		f.edit(t, appwire.InstanceEditParams{Name: "local", BaseURL: resolvedURL})
		after := f.status(t, "local")
		if after.ConfigRevision != before.ConfigRevision || after.ActiveSource != before.ActiveSource {
			t.Fatalf("precondition: the literalization moved the public identity %+v -> %+v; this case must exercise an unchanged revision", before, after)
		}
		joined := joinProbe(t, f.auth, "local")
		b.unleash()

		got := waitProbe(t, original)
		assertGenericConflict(t, got.err)
		if got.resp.Status == appwire.AuthTestStatusSuccess {
			t.Fatal("the old success was accepted for a replacement whose requests drop the quota-project header")
		}
		joinedGot := waitProbe(t, joined)
		assertGenericConflict(t, joinedGot.err)
		if joinedGot.resp.Status == appwire.AuthTestStatusSuccess {
			t.Fatal("the joined caller inherited the old success for the replacement")
		}

		// The replacement's own answer is what may succeed now: same
		// destination, same token, and the authentication header the
		// resolved project variable used to carry is gone.
		fresh := f.freshCheck(t, "local")
		if fresh.Status != appwire.AuthTestStatusSuccess {
			t.Fatalf("fresh check status = %q (%q), want success", fresh.Status, fresh.Message)
		}
		reqs := b.requests()
		if len(reqs) != 2 {
			t.Fatalf("probe requests = %+v, want exactly the held and fresh checks", reqs)
		}
		if reqs[1].authorization != "Bearer fixture-adc-token" {
			t.Fatalf("fresh request Authorization = %q, want the same fixture ADC token", reqs[1].authorization)
		}
		if reqs[1].quotaProject != "" {
			t.Fatalf("fresh request x-goog-user-project = %q, want the header the template's project variable carried to be gone", reqs[1].quotaProject)
		}
	})

	t.Run("vertex template re-authoring keeps the probe current", func(t *testing.T) {
		b := newVertexProbeBoundary(t, true)
		f := newVertexADCProbeFixture(t, b)
		before := f.status(t, "local")
		original := startProbe(f.auth, "local")
		b.acknowledge(t)

		// Re-author the inherited template string itself (the curated
		// google-vertex base URL): the authored record gains a base_url field,
		// while every resolved destination and the resolved vars are the ones
		// the probe already dialed, so the effective access is unchanged and
		// the probe must stay current.
		const inheritedTemplate = "{GOOGLE_VERTEX_HOST}/v1/projects/{GOOGLE_VERTEX_PROJECT}/locations/{GOOGLE_VERTEX_LOCATION}"
		f.edit(t, appwire.InstanceEditParams{Name: "local", BaseURL: inheritedTemplate})
		after := f.status(t, "local")
		if after.ConfigRevision != before.ConfigRevision || after.ActiveSource != before.ActiveSource {
			t.Fatalf("precondition: re-authoring the inherited template moved the public identity %+v -> %+v", before, after)
		}
		b.unleash()

		got := waitProbe(t, original)
		if got.err != nil {
			t.Fatalf("an edit that re-authored the template the instance already resolves from refused the probe: %v", got.err)
		}
		if got.resp.Status != appwire.AuthTestStatusSuccess {
			t.Fatalf("status = %q (%q), want success", got.resp.Status, got.resp.Message)
		}
		reqs := b.requests()
		if len(reqs) != 1 || reqs[0].quotaProject != "fixture-project" {
			t.Fatalf("probe requests = %+v, want the one held check still carrying the quota-project header", reqs)
		}
	})

	t.Run("literal url edit that changes no request keeps the probe and rejection", func(t *testing.T) {
		b := newProbeBoundary(t, false)
		b.status.Store(http.StatusUnauthorized)
		f := newAccessProbeFixture(t, templatedHeaderTOML(b.server.URL+"/v1"), headerEnv())
		if resp := f.freshCheck(t, "local"); resp.Status != appwire.AuthTestStatusAuthRejected {
			t.Fatalf("precondition: first check = %q (%q), want auth_rejected", resp.Status, resp.Message)
		}
		before := f.status(t, "local")
		if before.Error == "" {
			t.Fatal("precondition: the first 401 was not recorded")
		}
		if before.ConfigRevision == "" || before.ActiveSource != "credential_headers" {
			t.Fatalf("precondition: status = %+v, want a resolved credential_headers identity", before)
		}

		b.hold.Store(true)
		original := startProbe(f.auth, "local")
		b.acknowledge(t)
		held := b.requests()
		if len(held) != 2 || held[1].authorization != "Bearer fixture-key-a" {
			t.Fatalf("precondition: held request = %+v, want the authored key A on it", held)
		}

		// Supported edit: author the exact literal URL the {BASE_URL} template
		// already resolves to. The resolved destination, the credential
		// source, the authored header and the request bytes are all unchanged;
		// only the template's variable provenance disappears from the
		// resolution, and no authentication scheme reads it.
		res, err := f.auth.reg.Get().ResolveInstancePresence("local")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		f.edit(t, appwire.InstanceEditParams{Name: "local", BaseURL: res.Transport.BaseURL})
		after := f.status(t, "local")
		if after.ConfigRevision != before.ConfigRevision || after.ActiveSource != before.ActiveSource {
			t.Fatalf("precondition: the edit moved the public identity %+v -> %+v; this case must exercise an unchanged revision", before, after)
		}
		if after.Error == "" {
			t.Fatal("an edit that changed no request dropped the standing rejection")
		}
		b.unleash()

		got := waitProbe(t, original)
		if got.err != nil {
			t.Fatalf("an edit that changed no request refused the pending probe: %v", got.err)
		}
		if got.resp.Status != appwire.AuthTestStatusAuthRejected {
			t.Fatalf("probe status = %q (%q), want the standing rejection's own answer", got.resp.Status, got.resp.Message)
		}
		if f.status(t, "local").Error == "" {
			t.Fatal("the standing rejection disappeared after the probe settled")
		}

		// The replacement's own request is identical to the one the held probe
		// made: same path, same Authorization, no changed byte.
		fresh := f.freshCheck(t, "local")
		if fresh.Status != appwire.AuthTestStatusAuthRejected {
			t.Fatalf("fresh check = %q (%q), want the same rejection", fresh.Status, fresh.Message)
		}
		reqs := b.requests()
		if len(reqs) != 3 {
			t.Fatalf("probe requests = %+v, want the precondition check, the held check, and the fresh check", reqs)
		}
		if reqs[1].path != reqs[2].path || reqs[1].authorization != reqs[2].authorization || reqs[1].authorization != "Bearer fixture-key-a" {
			t.Fatalf("requests = %+v, want identical path and Authorization before and after the edit", reqs)
		}
	})
}
