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
	b.reqs = append(b.reqs, probeRequest{path: r.URL.Path, authorization: r.Header.Get("Authorization")})
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
	_, _ = w.Write([]byte(`{"data":[{"id":"fixture-model","type":"model","display_name":"Fixture model"}],"has_more":false}`))
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
		// The replacement is current but the edit has not advanced the fence.
		// The probe must not settle here: its answer is about key A.
		b.unleash()
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
}
