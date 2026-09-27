package hub

// TestHostPushCredentialsDisposableHostE2E is the live acceptance check for
// evener/host/pushCredentials (component 07c,
// docs/superpowers/specs/2026-09-14-multi-host-07-remote-admin.md) against a real
// remote host. It is the check that lets a credential push run "anywhere": the
// host side is made disposable FIRST, so the push writes a host store the test
// owns and the host's real evener install and real credentials.toml come out
// byte-identical.
//
// # Why this is not simply the sibling add/attach check plus a push
//
// The host hub resolves its config root from XDG_CONFIG_HOME (else
// ~/.config), and the hub config schema (cmd/evener-hub/config.go) has
// hub_state_root but no credential-path or config-root field. So a private
// hub.toml redirects the host hub's *state* but not its credential file. The
// launch argv sshconn builds (channelArgv / hubBootstrapArgv,
// internal/sshconn/runner.go) carries no environment seam and, by design, none
// is added: a wire field would have to survive the ps-based relaunch path
// (internal/sshconn/version.go relaunchCommand), which rebuilds argv from `ps`
// where an inline env assignment has already been consumed by the shell.
//
// The disposable host side is therefore built entirely here, in the test:
//
//   - a per-run, test-owned directory on the host holds a private providers.toml,
//     a private credentials.toml, and a private hub.toml (addr +
//     hub_state_root);
//   - the test cross-compiles this checkout's own evener for the host's target
//     (unstamped, so its version matches the controller's test build), stages it
//     INTO that directory, and launches that hub over ssh with XDG_CONFIG_HOME
//     pointed into the directory, so the hub resolves
//     <dir>/evener/providers.toml and <dir>/evener/credentials.toml. The host's
//     own install is used only as a fallback identity check — it is never
//     executed and never written. The staged build is required rather than the
//     host's install because the push's host half (evener/auth/apiKey/
//     conditionalSet) is new: a host hub built from an older main answers the
//     push's conditional set with "method not found";
//   - the controller then adds and attaches to that host. Because a hub is
//     already running and healthy at the configured address, sshconn's Ensure
//     attaches through the bridge form (channelArgv: `hub attach --stdio
//     --config p --addr a`) — which "never starts a hub, never writes
//     credentials" (attach.go) — instead of bootstrapping a second host hub with
//     the host's real environment.
//
// # The load-bearing assertion
//
// The push must write the DISPOSABLE host store and leave the host's REAL
// store byte-identical. The test fails if the real file changed, or if it did
// not exist before but does after. That guard is proven able to fail: see
// hostPushGuardedCredentials's EVENER_SSH_E2E_PUSH_GUARD_DISPOSABLE hook, used
// by the falsification run.
//
// It is gated by EVENER_SSH_E2E_PUSH=1 ON TOP OF EVENER_SSH_E2E=1 and
// EVENER_SSH_E2E_HOST, so default `go test ./...` performs no ssh. The push
// gate is separate, like the deploy check's EVENER_SSH_E2E_DEPLOY, because this
// check WRITES to the host: it creates its own directory and starts a hub from
// it. The write is confined to that directory, removed on the way out. The
// controller and host hubs both run unstamped builds of this checkout, so the
// version ladder matches ("dev") and the attach bridges; the check stages the
// host build itself rather than deploying over the host's real install, which is
// hashed before and after and must be unchanged.
import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

func TestHostPushCredentialsDisposableHostE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("live SSH credential-push test: starts a disposable hub on a remote host and writes its config root")
	}
	// The write gate is checked first so an un-opted-in run's skip line states
	// the contract plainly: this check WRITES to the host, and it needs all three
	// variables. It is a separate opt-in from the read-mostly sibling's and from
	// the deploy check's, so a developer running either is not signed up for a
	// credential write.
	if os.Getenv("EVENER_SSH_E2E_PUSH") != "1" {
		t.Skip("set EVENER_SSH_E2E_PUSH=1 (with EVENER_SSH_E2E=1 and EVENER_SSH_E2E_HOST) to run the live credential-push test; this check WRITES to the host — it creates its own config-root directory there and starts a hub from it")
	}
	if os.Getenv("EVENER_SSH_E2E") != "1" {
		t.Skip("set EVENER_SSH_E2E=1 and EVENER_SSH_E2E_HOST to run the live credential-push test")
	}
	dest := os.Getenv("EVENER_SSH_E2E_HOST")
	if dest == "" {
		t.Skip("set EVENER_SSH_E2E_HOST to a disposable ssh destination (an ssh alias or user@host) to run the live credential-push test")
	}
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)

	host := newHostSSH(t, dest, os.Getenv("EVENER_SSH_E2E_USER"))
	home := host.output(`printf '%s' "$HOME"`)
	if !strings.HasPrefix(home, "/") {
		t.Fatalf("host %s HOME = %q, want an absolute path (the disposable directory and the host binary must be addressed absolutely)", host.target, home)
	}
	// The host target is probed so a host this check cannot run against (no
	// shipped build) is announced as a skip, mirroring the deploy sibling.
	goos, goarch := hostTarget(t, host)

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	// The controller hub runs on its own isolated HOME. Its providers.toml
	// declares the same instance the disposable host will hold, so the local
	// credentials store can be seeded through the controller's own conditional
	// set instead of reaching into its file behind its back.
	stack := startHubStackOnProvider(t, controllerProvidersTOML(provider, hostPushInstance), "fake/"+fakellm.ModelID)

	ctx, cancel := context.WithTimeout(context.Background(), hostPushAttachTimeout+2*time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)

	// Seed the controller's store: the unit of the push is the local
	// credentials-store entry, whose key is the instance name.
	seed, err := clientRequest[appwire.ApiKeyConditionalSetResponse](ctx, client, appwire.MethodEvenerAuthApiKeyConditionalSet,
		appwire.ApiKeyConditionalSetParams{Provider: hostPushInstance, Value: hostPushKey})
	if err != nil {
		t.Fatalf("seed the controller's local credential store with %q: %v", hostPushInstance, err)
	}
	if seed.Action != appwire.ApiKeyConditionalSetActionAdded {
		t.Fatalf("seeding the controller store for %q returned action %q, want %q (the fixture needs the key present locally to push)", hostPushInstance, seed.Action, appwire.ApiKeyConditionalSetActionAdded)
	}

	runID := hostDeployRunID()
	hostDir := home + "/" + hostPushDirPrefix + "-" + runID
	cfgRoot := hostDir + "/evener"
	credsPath := cfgRoot + "/credentials.toml"
	configPath := hostDir + "/" + hostPushToml

	// The per-run token makes the name unique, and an existing path is refused
	// HERE, before anything is created: `mkdir -p` on a name already present
	// would adopt a directory this run did not make, and the cleanup would then
	// delete whatever was already inside it.
	if _, err := host.run("test -e " + shellquote.RemoteWord(hostDir)); err == nil {
		t.Fatalf("host %s already has %s; this check creates and removes that directory itself, so it must not adopt an existing one", host.target, hostDir)
	}
	hostBin := hostDir + "/bin/evener"
	host.mustRun("mkdir -p " + shellquote.RemoteWord(hostDir+"/state") + " " + shellquote.RemoteWord(hostDir+"/bin") + " " + shellquote.RemoteWord(cfgRoot))

	// The disposable host hub must run a build that carries evener/host's 07c
	// host half (evener/auth/apiKey/conditionalSet). The host's own install here
	// predates the feature — a live run against it answers "method not found" —
	// so this check stages a build of THIS checkout for the host's target into
	// the test-owned directory and runs the host hub from it. The host's real
	// install at ~/.local/bin/evener is never touched (hashed before/after below).
	host.writeFile(hostBin, stageHostTargetBinary(t, goos, goarch))
	host.mustRun("chmod 700 " + shellquote.RemoteWord(hostBin))

	// The disposable host config root: the provider instance the push joins
	// against, the credentials file the test controls (a sentinel entry the
	// push's merge must preserve), and the private hub.toml.
	host.writeFile(cfgRoot+"/providers.toml", []byte(hostPushProvidersTOML))
	host.writeFile(credsPath, []byte(hostPushSeededCredentialsTOML))
	// The credentials store refuses any credentials.toml whose group/world bits
	// are set (internal/credentials/store.go), and `cat >` over ssh lands the file
	// at the remote umask (0644). Tighten it, or the disposable hub refuses to
	// start — which the readiness wait reports with the hub's own log line.
	host.mustRun("chmod 600 " + shellquote.RemoteWord(credsPath))
	host.writeFile(configPath, []byte(fmt.Sprintf("addr = %q\nhub_state_root = %q\nplugin_auto_upgrade = false\n", hostPushAddr, hostDir+"/state")))

	// The two files this check must leave untouched: the host's REAL credential
	// store and its real install. They are hashed before anything is launched.
	realCredsPath := hostPushGuardedCredentials(home, hostDir)
	realCredsBefore := host.sha256IfFile(realCredsPath)
	installPath := home + "/.local/bin/evener"
	installBefore := host.sha256IfFile(installPath)

	// The disposable hub is stopped first, then the directory it runs from is
	// removed, and only then are the real files judged — a leftover directory or
	// a live hub must not pass as a clean run, and a failure is reported rather
	// than logged away.
	t.Cleanup(func() {
		stopHostListener(t, host, hostPushAddr, configPath)
		// Read the guarded files BEFORE removing the directory: with the
		// falsification hook the guarded path can live inside hostDir, and hashing
		// after the removal would fire on "absent" rather than on "changed".
		realCredsAfter := host.sha256IfFile(realCredsPath)
		installAfter := host.sha256IfFile(installPath)
		if err := host.tryRun("rm -rf " + shellquote.RemoteWord(hostDir)); err != nil {
			t.Errorf("remove the test-owned directory %s on host %s: %v", hostDir, host.target, err)
		}
		if realCredsBefore == "" {
			// The host had no real credential store when the check began.
			// Skipping would let the push create one and still pass — exactly the
			// violation the assertion exists to catch — so assert it is still
			// absent.
			if realCredsAfter != "" {
				t.Errorf("the push created the host's real credential store %s on host %s (sha256 %s); it must write only the test-owned %s", realCredsPath, host.target, realCredsAfter, credsPath)
			}
		} else if realCredsAfter != realCredsBefore {
			t.Errorf("the host's REAL credential store %s on %s changed during the push (sha256 %s -> %s); the push must write only the test-owned %s", realCredsPath, host.target, realCredsBefore, realCredsAfter, credsPath)
		}
		if installBefore == "" {
			if installAfter != "" {
				t.Errorf("this check created %s on host %s (sha256 %s); it must install nothing, only run the host's own binary", installPath, host.target, installAfter)
			}
			return
		}
		if installAfter != installBefore {
			t.Errorf("the host's own install %s changed during the check (sha256 %s -> %s); the check must not deploy over it", installPath, installBefore, installAfter)
		}
	})

	// Start the disposable host hub itself, so the attach below bridges to it
	// instead of bootstrapping a hub with the host's real environment.
	host.mustRun(hostHubLaunchScript(hostBin, configPath, hostDir, hostPushAddr))
	awaitHostHubHealth(t, host, hostPushAddr, hostDir+"/hub.log")

	row, err := clientRequest[appwire.HostRow](ctx, client, appwire.MethodEvenerHostAdd, appwire.HostAddParams{
		Entry: appwire.HostEntry{
			Name:       hostPushName,
			Address:    dest,
			User:       os.Getenv("EVENER_SSH_E2E_USER"),
			EvenerPath: hostBin,
			ConfigPath: configPath,
			Addr:       hostPushAddr,
		},
	})
	if err != nil {
		t.Fatalf("step evener/host/add (ssh destination %q, config %q, addr %q): %v", dest, configPath, hostPushAddr, err)
	}
	if row.Origin != "hub.toml" {
		t.Fatalf("step evener/host/add: added row origin = %q, want %q (every host lives in the machine-managed hub.toml)", row.Origin, "hub.toml")
	}

	attached := awaitHostAttachedWithin(ctx, t, client, hostPushName, hostPushAttachTimeout)
	t.Logf("attached disposable host %s: os=%s arch=%s hubVersion=%s evenerPath=%s", hostPushName, attached.OS, attached.Arch, attached.HubVersion, attached.EvenerPath)

	// The push itself, through the controller's real AppWire client.
	pushed, err := clientRequest[appwire.HostPushCredentialsResponse](ctx, client, appwire.MethodEvenerHostPushCredentials,
		appwire.HostPushCredentialsParams{Host: hostPushName})
	if err != nil {
		t.Fatalf("step evener/host/pushCredentials to %q: %v", hostPushName, err)
	}
	if pushed.Host != hostPushName {
		t.Fatalf("push response host = %q, want %q", pushed.Host, hostPushName)
	}
	result, ok := hostPushResultFor(pushed, hostPushInstance)
	if !ok {
		t.Fatalf("push response %+v carries no result for the seeded local entry %q", pushed.Results, hostPushInstance)
	}
	if result.Action != appwire.HostCredentialPushAdded {
		t.Fatalf("push result for %q = %+v, want the host's own action %q (the disposable host has no credential for it)", hostPushInstance, result, appwire.HostCredentialPushAdded)
	}
	if len(pushed.Results) != 1 {
		t.Fatalf("push response reports %d results %+v, want exactly one — the one entry the controller's store holds", len(pushed.Results), pushed.Results)
	}

	// The load-bearing read-back: the DISPOSABLE host store actually gained the
	// key, read from the host rather than inferred from the response. The
	// sentinel the file started with must survive, so a whole-file replace is not
	// mistaken for a merge.
	gained := hostPushReadCredentials(t, host, credsPath)
	if got := gained[hostPushInstance]; got != hostPushKey {
		t.Fatalf("the disposable host store %s on %s holds %q for %q, want the pushed key; entries = %+v", credsPath, host.target, got, hostPushInstance, gained)
	}
	if got := gained[hostPushSentinelInstance]; got != hostPushSentinelKey {
		t.Fatalf("the disposable host store %s lost its pre-existing %q entry (got %q, want %q); the push must merge, not replace", credsPath, hostPushSentinelInstance, got, hostPushSentinelKey)
	}
	t.Logf("disposable host store %s gained %q and kept %q", credsPath, hostPushInstance, hostPushSentinelInstance)
}

// hostPushAttachTimeout bounds the add-then-attach wait. The attach RPC is
// synchronous; the disposable hub is already up when it runs, so this is a
// tripwire rather than the mechanism.
const hostPushAttachTimeout = 8 * time.Minute

// The private loopback port the disposable host hub listens on. Fixed because
// the controller must be told the exact address to probe and bridge to; cleanup
// never kills by port alone (stopHostListener gates the kill on the pid's command
// line naming this test's own config path).
const hostPushAddr = "127.0.0.1:19182"

// hostPushDirPrefix names the per-run, test-owned directory on the host.
const hostPushDirPrefix = "evener-push-e2e"

// hostPushToml is the private hub.toml written inside that directory.
const hostPushToml = "hub.toml"

// hostPushName is the registry name the disposable host is added under.
const hostPushName = "e2e-push"

// hostPushInstance is the local store entry (and host instance) the push joins
// on. hostPushKey is the value the controller pushes and the disposable host
// must end up holding; hostPushSentinelInstance/hostPushSentinelKey are an
// unrelated entry the disposable host store starts with, to prove the push
// merges rather than replaces the file.
const (
	hostPushInstance         = "pushcheck"
	hostPushKey              = "sk-push-e2e-4Vx1Qm7Lp9Kd2Ns8Zf6Hw3Ru5Tt"
	hostPushSentinelInstance = "keepme"
	hostPushSentinelKey      = "sentinel-not-a-real-key"
)

// hostPushProvidersTOML is the disposable host's providers.toml: a key-capable
// instance with no credential, so the host's own locked conditional set
// classifies the pushed key "added" and writes its file layer.
const hostPushProvidersTOML = `default = "pushcheck"

[providers.pushcheck]
base     = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
`

// hostPushSeededCredentialsTOML is the disposable host's credentials.toml at
// the start of the run: an unrelated sentinel entry and nothing else. The push
// must add pushcheck and keep keepme.
const hostPushSeededCredentialsTOML = `schema = 1

[providers.keepme]
api_key = "` + hostPushSentinelKey + `"
`

// controllerProvidersTOML is the controller stack's providers.toml: the fake
// provider every stack needs, plus the instance the push will seed and join on.
func controllerProvidersTOML(provider *fakellm.Server, instance string) string {
	return fmt.Sprintf(`
default = "fake"

[providers.fake]
base     = "openai-compatible"
base_url = %q
api_key  = "fakellm-not-a-secret"

[providers.%s]
base     = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
`, provider.BaseURL(), instance)
}

// hostPushGuardedCredentials is the host file the check proves the push never
// touched: the host's REAL credential store under its real config root.
//
// EVENER_SSH_E2E_PUSH_GUARD_DISPOSABLE is a falsification hook, and the reason
// the guard can be trusted at all: a guard never seen to fail proves nothing. It
// points the guard at the test-owned disposable store instead of the real one,
// which the push DOES write, so the guard must fire. It is off in a normal run
// and never risks the host's real file either way.
func hostPushGuardedCredentials(home, hostDir string) string {
	if os.Getenv("EVENER_SSH_E2E_PUSH_GUARD_DISPOSABLE") == "1" {
		return hostDir + "/evener/credentials.toml"
	}
	return home + "/.config/evener/credentials.toml"
}

// hostPushReadCredentials reads a credentials.toml back from the host and
// returns its instance->api_key map. It parses the real remote file, so the
// assertion is on what the host wrote, not on a local copy.
func hostPushReadCredentials(t *testing.T, host *hostSSH, path string) map[string]string {
	t.Helper()
	raw := host.output("cat " + shellquote.RemoteWord(path))
	var doc struct {
		Providers map[string]struct {
			APIKey string `toml:"api_key"`
		} `toml:"providers"`
	}
	if _, err := toml.Decode(raw, &doc); err != nil {
		t.Fatalf("parse the disposable host store %s on %s: %v: %s", path, host.target, err, raw)
	}
	out := make(map[string]string, len(doc.Providers))
	for name, section := range doc.Providers {
		out[strings.ToLower(name)] = section.APIKey
	}
	return out
}

// hostPushResultFor returns the one push result for instance.
func hostPushResultFor(resp appwire.HostPushCredentialsResponse, instance string) (appwire.HostCredentialPushResult, bool) {
	for _, r := range resp.Results {
		if r.Instance == instance {
			return r, true
		}
	}
	return appwire.HostCredentialPushResult{}, false
}

// TestHostPushGuardedCredentialsDefaultTargetsRealStore pins the guard's target
// without a host: a normal run guards the host's real credential store, and the
// falsification hook is the only thing that moves it. A guard pointed at the
// wrong file (or one that never moves) would make the live assertion vacuous.
func TestHostPushGuardedCredentialsDefaultTargetsRealStore(t *testing.T) {
	const home = "/Users/dev"
	const hostDir = "/Users/dev/evener-push-e2e-123"
	if got, want := hostPushGuardedCredentials(home, hostDir), home+"/.config/evener/credentials.toml"; got != want {
		t.Fatalf("default guard target = %q, want the host's real store %q", got, want)
	}
	t.Setenv("EVENER_SSH_E2E_PUSH_GUARD_DISPOSABLE", "1")
	if got, want := hostPushGuardedCredentials(home, hostDir), hostDir+"/evener/credentials.toml"; got != want {
		t.Fatalf("falsification-hook guard target = %q, want the disposable store %q", got, want)
	}
}
