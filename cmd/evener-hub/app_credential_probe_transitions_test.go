package hub

// A credential probe is evidence about the access the instance had when it
// started. These are the supported access transitions that happen while it is
// in flight: the instance's endpoint or credential-selector edit, a stored-key
// write or clear, and the edits that do not change access. Every case runs the
// real InstanceEdit/API-key mutation path below a real local HTTP model-list
// boundary, so the outcome is the one the guided connector would receive.

import (
	"context"
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
	hold        bool
	mu          sync.Mutex
	reqs        []probeRequest
	started     chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
	status      atomic.Int32
}

func newProbeBoundary(t *testing.T, hold bool) *probeBoundary {
	t.Helper()
	b := &probeBoundary{
		hold:    hold,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
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
	first := len(b.reqs) == 1
	b.mu.Unlock()
	if first {
		b.once.Do(func() { close(b.started) })
		if b.hold {
			<-b.release
		}
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
	case <-b.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the probe never reached the local model-list boundary")
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
	return &accessProbeFixture{
		auth:  auth,
		inst:  &hubInstancesController{reg: auth.reg, providersConfigPath: tomlPath, auth: auth},
		toml:  tomlPath,
		creds: credsPath,
		env:   env,
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
		joined := startProbe(f.auth, "local")
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
		joined := startProbe(f.auth, "local")
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

	t.Run("same-name credential header edit refuses old rejection and checks the replacement", func(t *testing.T) {
		b := newProbeBoundary(t, true)
		b.status.Store(http.StatusUnauthorized)
		f := newAccessProbeFixture(t, headerTOML(b.server.URL+"/v1", "Bearer $KEY_A"), headerEnv())
		original := startProbe(f.auth, "local")
		b.acknowledge(t)

		f.edit(t, appwire.InstanceEditParams{Name: "local", CredentialHeader: "Authorization=Bearer $KEY_B"})
		b.unleash()

		got := waitProbe(t, original)
		assertGenericConflict(t, got.err)
		if got.resp.Status == appwire.AuthTestStatusAuthRejected {
			t.Fatal("the old key's rejection was published as the replacement's verdict")
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
