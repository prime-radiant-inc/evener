package hubcore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/internal/valueexpr"
	"primeradiant.com/evener/llm/registry"
)

// hermeticLoader is cmdutil.LoadRegistry with the network and the catalog
// cache taken away, so a test observes only the user layer and the env it
// sets itself.
func hermeticLoader(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
	return cmdutil.LoadRegistry(append(extra, registry.WithOffline(true), registry.WithoutCache())...)
}

// fakeCredentialSource stands in for the credentials store the hub always
// wires (cmdutil.LoadRegistry): present but empty by default, so a test
// exercises the production paths where a store miss must fall through.
type fakeCredentialSource map[string]string

func (f fakeCredentialSource) Lookup(instance string) (string, bool) {
	v, ok := f[instance]
	return v, ok
}

// TestReloadCannotCommitAnOlderReadAfterANewerOne pins the ordering
// contract for concurrent reloads. Loading runs outside the holder lock
// (a slow read must not block readers of the current snapshot), so two
// reloads can be in flight at once; if the one that read the file first
// is allowed to install last, the holder ends on a configuration older
// than one it already committed — it disagrees with the file the newer
// reload read, which is what makes the verdict's repro observable as a
// toggle or login that "did not take".
func TestReloadCannotCommitAnOlderReadAfterANewerOne(t *testing.T) {
	mk := func(name string) (*registry.Registry, *credentials.Store, error) {
		r, err := registry.Load(
			registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
			registry.WithStateRoot(t.TempDir()),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
			registry.WithInstances(map[string]registry.Provider{
				name: {Base: "openai-compatible", APIKey: "k", Transport: registry.Transport{BaseURL: "http://127.0.0.1:9/v1"}},
			}),
		)
		return r, nil, err
	}
	firstStarted := make(chan struct{})
	secondLoadStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	inFlight := 0
	overlaps := 0
	h := NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		mu.Lock()
		calls++
		n := calls
		inFlight++
		if inFlight > 1 {
			overlaps++
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()
		if n == 1 {
			// Stand in for a slow read: this snapshot is the file as it
			// looked before the second reload read it.
			close(firstStarted)
			<-releaseFirst
			return mk("older-instance")
		}
		close(secondLoadStarted)
		return mk("newer-instance")
	})

	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- h.Reload() }()
	<-firstStarted
	go func() { second <- h.Reload() }()
	// Synchronize on the second LOAD itself rather than on a sleep: an
	// unserialized holder starts it at once, which is the interleaving
	// that lets the older read install last, while a serialized holder
	// cannot start it until the first reload finishes — so this waits out
	// the window instead of guessing at it.
	select {
	case <-secondLoadStarted:
		// Unserialized: the second read ran while the first was parked.
	case <-time.After(2 * time.Second):
		// Serialized: the second reload is still queued behind the first.
	}
	close(releaseFirst)
	if err := <-first; err != nil {
		t.Fatalf("first Reload: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second Reload: %v", err)
	}
	mu.Lock()
	seen := overlaps
	mu.Unlock()
	if seen != 0 {
		t.Fatalf("%d reloads loaded at the same time: an older read can install after a newer one", seen)
	}
	reg := h.Get()
	if reg == nil {
		t.Fatal("holder has no registry after two successful reloads")
	}
	names := make([]string, 0, 2)
	for _, inst := range reg.Instances() {
		names = append(names, inst.Name)
	}
	if _, ok := reg.Instance("newer-instance"); !ok {
		t.Fatalf("the holder committed the older read after the newer one: instances = %v", names)
	}
	if _, ok := reg.Instance("older-instance"); ok {
		t.Fatalf("the holder kept both reads' instances: %v", names)
	}
}

// TestReloadKeepsReadersUnblockedWhileItLoads pins the other half of the
// ordering fix: reloads are serialized against each other, never against
// readers. Loading runs outside the state lock, so a reader still sees the
// current snapshot while a reload is parked in the loader — a fix that
// serialized by holding mu across the load would stall every Get/Instances
// call behind a slow file read.
func TestReloadKeepsReadersUnblockedWhileItLoads(t *testing.T) {
	mk := func() (*registry.Registry, *credentials.Store, error) {
		r, err := registry.Load(
			registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
			registry.WithStateRoot(t.TempDir()),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
			registry.WithInstances(map[string]registry.Provider{
				"seed": {Base: "openai-compatible", APIKey: "k", Transport: registry.Transport{BaseURL: "http://127.0.0.1:9/v1"}},
			}),
		)
		return r, nil, err
	}
	blocked := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	h := NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n > 1 {
			close(blocked)
			<-release
		}
		return mk()
	})
	if err := h.Reload(); err != nil {
		t.Fatalf("seed Reload: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Reload() }()
	<-blocked
	read := make(chan *registry.Registry, 1)
	go func() { read <- h.Get() }()
	select {
	case reg := <-read:
		if reg == nil {
			t.Fatal("Get() returned nil while a reload was loading")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Get() blocked on a reload's load: readers must not wait for the loader")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("second Reload: %v", err)
	}
}

// TestBeginLiveFetchDoesNotMoveTheCacheGeneration pins the two clocks
// apart. Generation is what readers record to cache derived inventory (the
// model-list endpoint), and starting a fetch changes nothing a reader can
// observe: the background prefetch begins a fetch per instance on every
// pass, so counting starts there would defeat that cache's TTL outright.
// Landing a listing still moves it.
func TestBeginLiveFetchDoesNotMoveTheCacheGeneration(t *testing.T) {
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	before := h.Generation()
	_, _, _ = h.BeginLiveFetchReg("gw")
	if got := h.Generation(); got != before {
		t.Fatalf("Generation moved from %d to %d when a fetch began", before, got)
	}
	_, tok, id := h.BeginLiveFetchReg("gw")
	h.ReapplyLive(tok, "gw", id, []registry.Model{{ID: "gpt-live-x"}})
	if got := h.Generation(); got <= before {
		t.Fatalf("Generation = %d after a landed listing, want it moved past %d", got, before)
	}
}

func TestProviderRegistryDegradesOnOldSchema(t *testing.T) {
	configRoot := t.TempDir()
	path := filepath.Join(configRoot, "providers.toml")
	if err := os.WriteFile(path, []byte("default = \"openai\"\n[instances.openai]\ntype = \"openai\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("EVENER_PROVIDERS_CONFIG", path)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GROQ_API_KEY", "gk")

	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err == nil || !errors.Is(err, registry.ErrOldSchema) {
		t.Fatalf("Reload reports the pointer: %v", err)
	}
	if h.Get() == nil || !h.WritesRefused() {
		t.Fatal("the hub keeps an implicit-only registry and refuses writes (spec §10)")
	}
	if _, ok := h.Get().Instance("groq"); !ok {
		t.Fatal("implicit instances still exist without the user layer")
	}
	diags := strings.Join(h.Diagnostics(), "\n")
	if !strings.Contains(diags, "§14.1") || !strings.Contains(diags, "user layer: none") {
		t.Fatalf("diagnostics carry the pointer and the user-layer note: %s", diags)
	}

	if err := os.WriteFile(path, []byte("default = \"groq\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(); err != nil || h.WritesRefused() {
		t.Fatalf("a fixed file clears the refusal: %v %v", err, h.WritesRefused())
	}
	if got := h.LoadError(); got != nil {
		t.Fatalf("LoadError after a good reload = %v, want nil", got)
	}
	diags = strings.Join(h.Diagnostics(), "\n")
	if !strings.Contains(diags, "user layer: "+path) {
		t.Fatalf("diagnostics name the file that loaded: %s", diags)
	}
}

// TestReapplyLiveDiscardsSupersededFetch proves the token contract: a
// fetch that claims after a newer claim for the same instance (or after
// a Reload) must not overwrite the newer listing. Tokens are claimed
// once a fetch has a listing to publish, so a failed fetch never mints
// one at all.
func TestInstanceIdentityChangesOnCredentialRotation(t *testing.T) {
	// Rotating the credential behind a stable source label must
	// change the identity: rows fetched under the old secret must
	// not publish into the newly-credentialed instance.
	mkRegistry := func(t *testing.T, key string) *registry.Registry {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "providers.toml")
		cfg := "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key_env = [\"ROT_KEY\"]\n"
		if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := registry.Load(
			registry.WithConfigPath(path),
			registry.WithEnv(func(k string) (string, bool) {
				if k == "ROT_KEY" {
					return key, true
				}
				return "", false
			}),
			registry.WithCredentials(fakeCredentialSource{}),
			registry.WithStateRoot(t.TempDir()),
			registry.WithOffline(true),
			registry.WithoutCache(),
		)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r1 := mkRegistry(t, "sk-old")
	r2 := mkRegistry(t, "sk-new-rotated-longer")
	if instanceIdentity(r1, "gw") == instanceIdentity(r2, "gw") {
		t.Fatal("identity unchanged across credential rotation")
	}
}

func TestInstanceIdentityChangesOnSameLengthRotation(t *testing.T) {
	// A same-length rotation changes no shape or length the old
	// fingerprint hashed: only the secret bytes differ. The identity
	// must still change, or rows fetched under the old secret publish
	// into the newly-credentialed instance.
	mkRegistry := func(t *testing.T, key string) *registry.Registry {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "providers.toml")
		cfg := "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key_env = [\"ROT_KEY\"]\n"
		if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := registry.Load(
			registry.WithConfigPath(path),
			registry.WithEnv(func(k string) (string, bool) {
				if k == "ROT_KEY" {
					return key, true
				}
				return "", false
			}),
			registry.WithCredentials(fakeCredentialSource{}),
			registry.WithStateRoot(t.TempDir()),
			registry.WithOffline(true),
			registry.WithoutCache(),
		)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r1 := mkRegistry(t, "sk-aaaa")
	r2 := mkRegistry(t, "sk-bbbb")
	if instanceIdentity(r1, "gw") == instanceIdentity(r2, "gw") {
		t.Fatal("identity unchanged across same-length credential rotation")
	}
}

// The identity carries the protocol the listing actually resolves
// through — the default row's — so a row protocol override with an
// unchanged endpoint still rotates the identity.
func TestInstanceIdentityUsesListingRowProtocol(t *testing.T) {
	mk := func(t *testing.T, rowProtocol string) *registry.Registry {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "providers.toml")
		cfg := "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\nprotocol = \"openai-chat\"\napi_key_env = [\"WORK_KEY\"]\ndefault_model = \"m\"\n" +
			"[providers.gw.models.\"m\"]\nprotocol = \"" + rowProtocol + "\"\n"
		if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := registry.Load(
			registry.WithConfigPath(path),
			registry.WithEnv(func(k string) (string, bool) {
				if k == "WORK_KEY" {
					return "sk-test", true
				}
				return "", false
			}),
			registry.WithStateRoot(t.TempDir()),
			registry.WithOffline(true),
			registry.WithoutCache(),
		)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if instanceIdentity(mk(t, "openai-chat"), "gw") == instanceIdentity(mk(t, "openai-responses"), "gw") {
		t.Fatal("identity unchanged across a default-row protocol override")
	}
}

// The identity fingerprint never executes a credential command: the hub
// fingerprints at load, before any session exists, so running one would
// prompt the user's password manager with no session launched and spend
// one-time mints. Command-bearing material contributes its authored text
// instead, so a rotating mint must not churn the identity — every TTL
// rollover would otherwise prune the cached live rows and force a re-fetch.
func TestInstanceIdentityNeverMintsAndIsStableAcrossRotations(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return fmt.Sprintf("token-%d", runs), nil
	}
	mkRegistry := func(t *testing.T) *registry.Registry {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "providers.toml")
		cfg := "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = '''$(gw-mint)'''\n"
		if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := registry.Load(
			registry.WithConfigPath(path),
			registry.WithStateRoot(t.TempDir()),
			registry.WithOffline(true),
			registry.WithoutCache(),
		)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := mkRegistry(t)
	first := instanceIdentity(r, "gw")
	if runs != 0 {
		t.Fatalf("the identity fingerprint executed the credential command %d time(s) with no session launched", runs)
	}
	// A TTL rollover re-mints a different token; the identity must not
	// move with it.
	valueexpr.ResetForTest()
	valueexpr.RunCommand = func(string) (string, error) { return "token-rotated", nil }
	if second := instanceIdentity(r, "gw"); second != first {
		t.Fatalf("identity changed across a re-mint (%q vs %q); a rotating token must not churn the live-row cache", first, second)
	}
}

func TestInstanceIdentityChangesOnOAuthAccountSwap(t *testing.T) {
	// Swapping the signed-in account behind the same record filename
	// changes no source label or transport: only the record's account
	// claims differ. The identity must still change, or rows fetched
	// for account A publish onto the instance now signed in as B.
	// (A token refresh alone — same claims, new tokens — must NOT
	// change it: the fingerprint reads only the stable claims.)
	mkRegistry := func(t *testing.T, account, token string) *registry.Registry {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "providers.toml")
		cfg := "[providers.codex]\nbase = \"openai-codex\"\n"
		if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		state := t.TempDir()
		rec := `{"version":1,"provider":"openai-codex","source":"oauth","obtained_at":"2026-01-01T00:00:00Z","token_type":"Bearer","access_token":"` + token + `","refresh_token":"ref","expiry":"2027-01-01T00:00:00Z","email":"a@example.com","account_id":"` + account + `"}`
		if err := os.MkdirAll(filepath.Join(state, "auth"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "auth", "codex.json"), []byte(rec), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := registry.Load(
			registry.WithConfigPath(path),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
			registry.WithStateRoot(state),
			registry.WithOffline(true),
			registry.WithoutCache(),
		)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r1 := mkRegistry(t, "acct_A", "tok-1")
	r2 := mkRegistry(t, "acct_B", "tok-2")
	if instanceIdentity(r1, "codex") == instanceIdentity(r2, "codex") {
		t.Fatal("identity unchanged across OAuth account swap")
	}
	r3 := mkRegistry(t, "acct_A", "tok-rotated")
	if instanceIdentity(r1, "codex") != instanceIdentity(r3, "codex") {
		t.Fatal("identity changed on token refresh alone; only stable account claims may feed it")
	}
}

func TestInstanceIdentityFallsBackToIDTokenClaims(t *testing.T) {
	// Legacy records predate the persisted top-level account fields:
	// with only an id_token carrying the account, a switch must still
	// change the identity, or rows fetched for account A publish onto
	// the instance now signed in as B. Mirrors the OAuth request path
	// (top-level first, token claims fill the gaps).
	mkRegistry := func(t *testing.T, account string) *registry.Registry {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "providers.toml")
		cfg := "[providers.codex]\nbase = \"openai-codex\"\n"
		if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		state := t.TempDir()
		payload, err := json.Marshal(map[string]any{"account_id": account})
		if err != nil {
			t.Fatal(err)
		}
		idToken := "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
		rec := `{"version":1,"provider":"openai-codex","source":"oauth","access_token":"tok","refresh_token":"ref","id_token":"` + idToken + `"}`
		if err := os.MkdirAll(filepath.Join(state, "auth"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "auth", "codex.json"), []byte(rec), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := registry.Load(
			registry.WithConfigPath(path),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
			registry.WithStateRoot(state),
			registry.WithOffline(true),
			registry.WithoutCache(),
		)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r1 := mkRegistry(t, "acct_A")
	r2 := mkRegistry(t, "acct_B")
	if instanceIdentity(r1, "codex") == instanceIdentity(r2, "codex") {
		t.Fatal("identity unchanged across id_token-only account switch")
	}
}

func TestADCFileSwapChangesIdentity(t *testing.T) {
	// Rewriting the ADC file behind a stable "adc" source label must
	// change the identity: the resolution carries no credential value,
	// so only the file bytes distinguish the old account from the new.
	// (adcFileFingerprint is the unit under test; adcFingerprint only
	// resolves the path and delegates.)
	dir := t.TempDir()
	adc := filepath.Join(dir, "adc.json")
	if err := os.WriteFile(adc, []byte(`{"type":"authorized_user","client_id":"a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := adcFileFingerprint(adc)
	if err := os.WriteFile(adc, []byte(`{"type":"authorized_user","client_id":"b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if adcFileFingerprint(adc) == before {
		t.Fatal("fingerprint unchanged across ADC file rewrite")
	}
	// Resolution order mirrors adcAvailable: the env var wins over
	// the well-known path.
	home := t.TempDir()
	wellKnown := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	got := adcFilePath(
		func(k string) string {
			if k == "GOOGLE_APPLICATION_CREDENTIALS" {
				return adc
			}
			return ""
		},
		func() (string, error) { return home, nil },
	)
	if got != adc {
		t.Fatalf("adcFilePath = %q, want the env var %q", got, adc)
	}
	got = adcFilePath(
		func(string) string { return "" },
		func() (string, error) { return home, nil },
	)
	if got != wellKnown {
		t.Fatalf("adcFilePath = %q, want the well-known path %q", got, wellKnown)
	}
}

func TestReloadPrunesLastGoodLiveForRemovedInstances(t *testing.T) {
	// A removed instance's snapshot must not linger: if the name
	// returns later with a matching identity, the old rows must not
	// resurrect.
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if h.Get() == nil {
		t.Fatal("holder has no registry after Reload")
	}
	_, tok, id := h.BeginLiveFetchReg("gw")
	h.ReapplyLive(tok, "gw", id, []registry.Model{{ID: "gpt-live-x"}})
	h.mu.Lock()
	_, kept := h.lastGoodLive["gw"]
	h.mu.Unlock()
	if !kept {
		t.Fatal("no snapshot recorded for gw")
	}
	// Simulate a successful reload whose registry no longer knows gw
	// by pruning against the hermetic registry with gw forgotten: use
	// a fresh empty-config holder registry instead.
	empty, err := registry.Load(
		registry.WithNoUserLayer(),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithStateRoot(t.TempDir()),
		registry.WithOffline(true),
		registry.WithoutCache(),
	)
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.pruneLastGoodLive(empty, instanceIdentities(empty))
	_, kept = h.lastGoodLive["gw"]
	h.mu.Unlock()
	if kept {
		t.Fatal("snapshot for removed instance survived pruning")
	}
}

// TestReloadDoesNotCarryRowsIntoANewIncarnation pins the carry half of the
// incarnation contract: a name the last successful snapshot did not have is
// a new incarnation, so the rows an earlier registry holds for it — a failed
// reload's fallback knows implicit instances the last good snapshot did not
// — must not carry forward. The fetch tokens refuse the same staleness; the
// identity check alone cannot see it, because the recreated entry's endpoint
// and credentials are byte-identical.
func TestReloadDoesNotCarryRowsIntoANewIncarnation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	valid := "[providers.other]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"sk\"\n"
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", path)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GROQ_API_KEY", "")
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, ok := h.Get().Instance("groq"); ok {
		t.Fatal("groq exists before its key does, so this test proves nothing")
	}
	// The key appears, and a broken file parks the holder on the fallback,
	// which knows groq implicitly. Live rows for it land there.
	t.Setenv("GROQ_API_KEY", "gk")
	if err := os.WriteFile(path, []byte("default = \"openai\"\n[instances.openai]\ntype = \"openai\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(); err == nil {
		t.Fatal("broken file must fail Reload")
	}
	if _, ok := h.Get().Instance("groq"); !ok {
		t.Fatal("the implicit-only fallback should know groq")
	}
	h.ReapplyLive(mustBegin(t, h, "groq"), "groq", h.identityOf(t, "groq"), []registry.Model{{ID: "gpt-live-x"}})
	if got := h.Get().LiveModels("groq"); len(got) == 0 {
		t.Fatal("no live rows on the fallback")
	}
	// The file loads again: groq is new to the successful snapshot, so its
	// incarnation changes and the fallback's rows belong to the old one.
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, ok := h.Get().Instance("groq"); !ok {
		t.Fatal("groq missing after the successful reload")
	}
	if got := h.Get().LiveModels("groq"); len(got) != 0 {
		t.Fatalf("live ids = %+v: rows from the previous incarnation carried forward", got)
	}
}

// TestReloadForgetsSnapshotsForRecreatedNames pins the other half of the
// incarnation contract: a name new to a successful snapshot is a new
// incarnation, so the cached inventory an earlier snapshot recorded for it —
// a failed reload's fallback can record one — must not survive to be handed
// to whatever instance takes that name next.
func TestReloadForgetsSnapshotsForRecreatedNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	valid := "[providers.other]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"sk\"\n"
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", path)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GROQ_API_KEY", "")
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// The key appears, a broken file parks the holder on the fallback, and a
	// fetch lands rows for the implicit instance there.
	t.Setenv("GROQ_API_KEY", "gk")
	if err := os.WriteFile(path, []byte("default = \"openai\"\n[instances.openai]\ntype = \"openai\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(); err == nil {
		t.Fatal("broken file must fail Reload")
	}
	h.ReapplyLive(mustBegin(t, h, "groq"), "groq", h.identityOf(t, "groq"), []registry.Model{{ID: "gpt-live-x"}})
	h.mu.Lock()
	_, recorded := h.lastGoodLive["groq"]
	h.mu.Unlock()
	if !recorded {
		t.Fatal("the fallback's landing recorded no snapshot, so this test proves nothing")
	}
	// The file loads again: groq is new to the successful snapshot, so the
	// snapshot the fallback recorded belongs to a dead incarnation.
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	h.mu.Lock()
	_, kept := h.lastGoodLive["groq"]
	h.mu.Unlock()
	if kept {
		t.Fatal("snapshot for a recreated name survived the reload")
	}
}

func TestReloadFailurePreservesLiveForRestoredInstances(t *testing.T) {
	// A failed reload parks the holder on the implicit-only fallback;
	// when the file is fixed and the last-good registry comes back,
	// live-only ids for its explicit instances must come back too —
	// not just the ids the fallback knew.
	t.Setenv("GROQ_API_KEY", "gk")
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(path, []byte("[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"sk\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", path)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	h.ReapplyLive(mustBegin(t, h, "gw"), "gw", h.identityOf(t, "gw"), []registry.Model{{ID: "gpt-live-x"}})
	if got := h.Get().LiveModels("gw"); len(got) == 0 {
		t.Fatal("no live rows before failure")
	}
	// Break the file: the holder falls back to implicit-only, which
	// knows no gw at all.
	if err := os.WriteFile(path, []byte("default = \"openai\"\n[instances.openai]\ntype = \"openai\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(); err == nil {
		t.Fatal("broken file must fail Reload")
	}
	// Fix the file: gw comes back with its live-only id.
	if err := os.WriteFile(path, []byte("[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"sk\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := h.Get().LiveModels("gw"); len(got) == 0 {
		t.Fatal("live ids lost through failed-reload recovery")
	}
}

func mustBegin(t *testing.T, h *ProviderRegistry, instance string) LiveToken {
	t.Helper()
	_, tok, _ := h.BeginLiveFetchReg(instance)
	return tok
}

func (h *ProviderRegistry) identityOf(t *testing.T, instance string) string {
	t.Helper()
	if h.Get() == nil {
		t.Fatalf("no registry to fingerprint %s", instance)
	}
	return instanceIdentity(h.Get(), instance)
}

func TestReapplyLiveDiscardsListingForChangedEndpoint(t *testing.T) {
	// A fetch that began against endpoint A must not publish into a
	// registry whose instance now points at endpoint B: the rows came
	// from the wrong transport. Simulate the re-point by recording a
	// new snapshot identity for the name, then replaying the old
	// token: the rows must be dropped.
	t.Setenv("GROQ_API_KEY", "gk")
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if h.Get() == nil {
		t.Fatal("holder has no registry after Reload")
	}
	reg, tok, _ := h.BeginLiveFetchReg("groq")
	if reg == nil {
		t.Fatal("no registry snapshot")
	}
	if _, ok := reg.Instance("groq"); !ok {
		t.Fatal("no groq instance to fingerprint")
	}
	// The real race: the instance is re-pointed and fetch B begins;
	// A's apply is then judged against the endpoint A actually
	// queried, not the latest recorded.
	_, _, _ = h.BeginLiveFetchReg("groq")
	h.ReapplyLive(tok, "groq", "stale-endpoint-X", []registry.Model{{ID: "gpt-live-x"}})
	if got := h.Get().LiveModels("groq"); len(got) != 0 {
		t.Fatalf("live ids = %+v, want stale-endpoint rows dropped", got)
	}
}

func TestReapplyLiveDiscardsListingForRecreatedInstance(t *testing.T) {
	// Removing an instance and putting the very same entry back leaves
	// the name, endpoint, and credentials byte-identical, so the
	// endpoint identity cannot tell the two incarnations apart: without
	// an incarnation in the token, a fetch begun before the removal
	// plants the old instance's rows on its replacement. The holder
	// counts incarnations per name, so the apply is dropped.
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	entry := "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"sk\"\n"
	other := "[providers.other]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"sk\"\n"
	if err := os.WriteFile(path, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", path)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	_, tok, id := h.BeginLiveFetchReg("gw")
	// Remove gw, then put the identical entry back.
	if err := os.WriteFile(path, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload after removal: %v", err)
	}
	if _, ok := h.Get().Instance("gw"); ok {
		t.Fatal("gw survived its removal")
	}
	if err := os.WriteFile(path, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload after re-add: %v", err)
	}
	if _, ok := h.Get().Instance("gw"); !ok {
		t.Fatal("gw did not come back")
	}
	if got := h.identityOf(t, "gw"); got != id {
		t.Fatalf("identity changed across the remove/re-add (%q -> %q), so this test would pass for the wrong reason", id, got)
	}
	h.ReapplyLive(tok, "gw", id, []registry.Model{{ID: "gpt-live-x"}})
	if got := h.Get().LiveModels("gw"); len(got) != 0 {
		t.Fatalf("live ids = %+v: the pre-removal incarnation's rows published onto the recreated instance", got)
	}
}

func TestReapplyLiveFailedNewerDoesNotBlockOlderSuccess(t *testing.T) {
	// Fetch A starts first and succeeds; fetch B starts after but fails
	// and so never applies. B's failure must not invalidate A's success:
	// the older listing still lands.
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if h.Get() == nil {
		t.Fatal("holder has no registry after Reload")
	}
	_, older, idOlder := h.BeginLiveFetchReg("gw")
	_, _, _ = h.BeginLiveFetchReg("gw") // newer fetch, fails: applies nothing
	h.ReapplyLive(older, "gw", idOlder, []registry.Model{{ID: "gpt-live-x"}})
	got := h.Get().LiveModels("gw")
	ids := make([]string, 0, len(got))
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		t.Fatal("live ids empty, want the older success to land despite the failed newer fetch")
	}
}

func TestReapplyLiveDiscardsOutOfOrderFetch(t *testing.T) {
	// Fetch A starts first but finishes last: its token was minted at
	// request start, so the faster fetch B's apply supersedes it and A's
	// stale listing must not overwrite B's newer one.
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if h.Get() == nil {
		t.Fatal("holder has no registry after Reload")
	}
	_, slow, idSlow := h.BeginLiveFetchReg("gw")
	_, fast, idFast := h.BeginLiveFetchReg("gw")
	h.ReapplyLive(fast, "gw", idFast, []registry.Model{{ID: "gpt-new-x"}})
	h.ReapplyLive(slow, "gw", idSlow, []registry.Model{{ID: "gpt-old-x"}})
	got := h.Get().LiveModels("gw")
	ids := make([]string, 0, len(got))
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	for _, id := range ids {
		if id == "gpt-old-x" {
			t.Fatalf("live ids = %v, want out-of-order gpt-old discarded", ids)
		}
	}
	if len(ids) == 0 {
		t.Fatal("live ids empty, want gpt-new applied")
	}
}

func TestReapplyLiveDiscardsSupersededFetch(t *testing.T) {
	// The genuinely out-of-order pair: the older token applies first,
	// then the newer claim overwrites it and a repeat of the older
	// apply is discarded — coverage the failed-newer and
	// out-of-order arrivals above do not give.
	h := NewProviderRegistry(hermeticLoader)
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if h.Get() == nil {
		t.Fatal("holder has no registry after Reload")
	}
	_, older, idOld := h.BeginLiveFetchReg("gw")
	_, newer, idNew := h.BeginLiveFetchReg("gw")
	h.ReapplyLive(older, "gw", idOld, []registry.Model{{ID: "gpt-old-x"}})
	h.ReapplyLive(newer, "gw", idNew, []registry.Model{{ID: "gpt-new-x"}})
	h.ReapplyLive(older, "gw", idOld, []registry.Model{{ID: "gpt-old-x"}})
	got := h.Get().LiveModels("gw")
	ids := make([]string, 0, len(got))
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	for _, id := range ids {
		if id == "gpt-old-x" {
			t.Fatalf("live ids = %v, want replayed older apply discarded", ids)
		}
	}
	if len(ids) == 0 {
		t.Fatal("live ids empty, want gpt-new applied")
	}
}

// TestProviderRegistryOrdersConcurrentReloads: a reload is a load and a commit,
// and mu alone serializes only the commit - so two reloads that load
// concurrently can commit in the opposite order, leaving the holder serving the
// view the slower one read. Here the reload that reads first is held inside its
// load until the other has finished, which is the worst case: it commits last.
// (Its wait expires when the two are properly ordered, because then the other
// cannot finish while this one is still loading.)
func TestProviderRegistryOrdersConcurrentReloads(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stateDir := t.TempDir()

	olderPath := filepath.Join(configRoot, "older.toml")
	newerPath := filepath.Join(configRoot, "newer.toml")
	if err := os.WriteFile(olderPath, []byte("[providers.work]\nbase = \"openai\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newerPath, []byte("[providers.work2]\nbase = \"openai\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var loads int
	secondDone := make(chan struct{})
	load := func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		mu.Lock()
		loads++
		n := loads
		mu.Unlock()
		path := newerPath
		if n == 1 {
			path = olderPath
			select {
			case <-secondDone:
			case <-time.After(2 * time.Second):
			}
		}
		opts := append([]registry.Option{}, extra...)
		opts = append(opts, registry.WithConfigPath(path), registry.WithStateRoot(stateDir))
		return hermeticLoader(opts...)
	}

	holder := NewProviderRegistry(load)
	firstErr := make(chan error, 1)
	go func() { firstErr <- holder.Reload() }()
	for deadline := time.Now().Add(2 * time.Second); ; {
		mu.Lock()
		started := loads >= 1
		mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first reload never entered its load")
		}
		time.Sleep(time.Millisecond)
	}

	secondErr := make(chan error, 1)
	go func() {
		secondErr <- holder.Reload()
		close(secondDone)
	}()

	if err := <-firstErr; err != nil {
		t.Fatalf("first Reload: %v", err)
	}
	if err := <-secondErr; err != nil {
		t.Fatalf("second Reload: %v", err)
	}
	// The reload that read last wins: the holder has to serve the newer file,
	// not the view a reload that merely committed last happened to load.
	if _, ok := holder.Get().Instance("work2"); !ok {
		t.Fatal("the holder serves the older view: the reload that read first committed last")
	}
	if _, ok := holder.Get().Instance("work"); ok {
		t.Fatal("the holder still serves the instance only the older file has")
	}
}
