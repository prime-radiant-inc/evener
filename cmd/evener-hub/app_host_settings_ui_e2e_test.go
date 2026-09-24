package hub

// TestHostSettingsUIDisposableHostE2E is the live acceptance check for the
// remote-host settings UI (component 07b,
// docs/superpowers/specs/2026-09-14-multi-host-07-remote-admin.md): the
// PRODUCTION hub web app (the hub's embedded frontend/dist) driven in real
// Chrome with a REAL remote host selected, asserting each host-scoped settings
// pane renders THAT host's own data, then writing the host's AGENTS.md through
// the UI.
//
// # The disposable host side
//
// It reuses the credential-push check's disposable-host mechanism
// (app_host_push_credentials_e2e_test.go, app_host_disposable_e2e_test.go): a
// per-run, test-owned directory on the host holds a private providers.toml,
// credentials.toml, AGENTS.md, launch.toml, and hub.toml; the test stages an
// unstamped build of THIS checkout for the host's target INTO that directory and
// starts a hub from it over ssh with XDG_CONFIG_HOME pointed there. The host's
// real install and real config root are hashed before anything starts and must
// come out byte-identical.
//
// # Build order (the embed)
//
// The hub embeds frontend/dist (`//go:embed all:frontend/dist`, webnext.go), so
// the UI a hub serves is whatever that directory held when the hub was BUILT.
// This check therefore builds the frontend — ALWAYS, because `npm run build`
// runs clean-dist.mjs first and a failed or skipped build would otherwise leave
// a stale SPA or the tracked placeholder in place — and then the controller hub
// binary, in that order, from the tree under test (settingsUIControllerBinary).
// A build that cannot run (no node_modules) FAILS LOUDLY naming the command an
// operator must run. Before the browser is driven, the check also asserts the
// hub answers with the real SPA, not webnext.go's documented 503.
//
// # Why the controller runs on an isolated HOME
//
// startHubStackOnProviderWithEvener gives the controller hub a throwaway HOME
// with XDG dirs inside it, so the controller's own credentials store, launch
// config, and AGENTS.md are test-owned and the "the controller did not change"
// assertion is meaningful.
//
// It is gated by EVENER_SSH_E2E_UI=1 ON TOP OF EVENER_SSH_E2E=1 and
// EVENER_SSH_E2E_HOST, so default `go test ./...` performs no ssh and starts no
// browser. The UI gate is separate from the push and deploy gates because this
// check WRITES to the host (its own directory) AND drives a browser, so a
// developer signed up for either of those is not signed up for a UI run.
import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/internal/shellquote"
	"primeradiant.com/evener/test/e2e/fakellm"
)

const (
	hostSettingsUIAttachTimeout = 8 * time.Minute
	hostSettingsUIDriverTimeout = 6 * time.Minute
	// The private loopback port the disposable host hub listens on. Fixed
	// because the controller must be told the exact address to probe and bridge
	// to; cleanup never kills by port alone (stopHostListener gates the kill on
	// the pid's command line naming this run's own config path).
	hostSettingsUIAddr = "127.0.0.1:19183"
	// hostSettingsUIDirPrefix names the per-run, test-owned directory on the host.
	hostSettingsUIDirPrefix = "evener-settings-ui-e2e"
	// hostSettingsUIToml is the private hub.toml written inside that directory.
	hostSettingsUIToml = "hub.toml"
	// hostSettingsUIName is the registry name the disposable host is added under.
	hostSettingsUIName = "e2e-settings-ui"
)

// The fixture vocabulary the runner requires and is told to expect. Host values
// are seeded in the DISPOSABLE host root; controller values sit in the
// controller's isolated HOME. They DIFFER, so a pane that silently rendered the
// controller's data would fail the read assertion.
const (
	hostSettingsUIHostAgentsDoc       = "# HOST_AGENTS_DOC_s3tt1ngs_d1s\n"
	hostSettingsUIControllerAgentsDoc = "# CONTROLLER_AGENTS_DOC_c0ntr0l_d1s\n"
	hostSettingsUIWrittenAgentsDoc    = "# WRITTEN_THROUGH_THE_UI_77a3_d1s\n"

	hostSettingsUIHostInstance       = "onhost"
	hostSettingsUIControllerInstance = "controlleronly"

	hostSettingsUIHostLaunchAgent       = "host-launch-agent-7c1"
	hostSettingsUIControllerLaunchAgent = "controller-launch-agent-9d2"

	hostSettingsUIHostSkillsDir       = "/host/skills-7c1"
	hostSettingsUIControllerSkillsDir = "/controller/skills-9d2"

	hostSettingsUIHostPluginsDir       = "/host/plugins-7c1"
	hostSettingsUIControllerPluginsDir = "/controller/plugins-9d2"

	hostSettingsUIHostMCPConfig       = "/host/mcp-7c1.json"
	hostSettingsUIControllerMCPConfig = "/controller/mcp-9d2.json"

	// The project pane's seed. The working directory is created only on the HOST,
	// under its per-run directory, and holds a project-layer launch file naming an
	// agent unlike any other value in this check. This hub cannot resolve that
	// path at all, so a pane that renders the sentinel can only have read the
	// SELECTED host - the same provenance the seeded-value panes get, which the
	// project pane could not have while it asserted structure alone.
	//
	// The project LAYER is not trust-gated (internal/launchconfig/resolver.go
	// loads it unconditionally), unlike the in-repo layer, which needs a trust
	// record in the host's own project metadata - the reason `inrepo` still
	// asserts structure.
	hostSettingsUIProjectAgent = "host-project-agent-5b2"
	hostSettingsUIProjectDir   = "project-cwd"

	// The in-repo pane's seed: another host-only working directory, this one
	// holding the in-repo layer (<cwd>/.evener/launch.toml). That layer is
	// contributed only once the HOST has trusted the file, so the driver drives
	// the pane's own Trust action rather than pretending the value applies - and
	// the trust write reaches the host through the proxy (launch/trustRepo is
	// allow-listed, and classified as a mutation).
	hostSettingsUIInRepoAgent = "host-inrepo-agent-9d4"
	hostSettingsUIInRepoDir   = "inrepo-cwd"
)

// hostSettingsUIGate returns the t.Skip reason when the live settings-UI check
// may not run, and "" when it may. The write gate is checked BEFORE the master
// gate, matching the push check's order convention, so an un-opted-in run's
// skip line names the contract plainly: this check WRITES to the host and
// drives a browser. It is a separate opt-in from the push and deploy checks'.
func hostSettingsUIGate(short bool, ui, master, dest string) string {
	if short {
		return "live SSH settings-UI test: starts a disposable hub on a remote host, writes its config root, and drives a real browser"
	}
	if ui != "1" {
		return "set EVENER_SSH_E2E_UI=1 (with EVENER_SSH_E2E=1 and EVENER_SSH_E2E_HOST) to run the live settings-UI test; this check WRITES to the host — it creates its own config-root directory there, starts a hub from it, and drives a real browser"
	}
	if master != "1" {
		return "set EVENER_SSH_E2E=1 and EVENER_SSH_E2E_HOST to run the live settings-UI test"
	}
	if dest == "" {
		return "set EVENER_SSH_E2E_HOST to a disposable ssh destination (an ssh alias or user@host) to run the live settings-UI test"
	}
	return ""
}

// hostSettingsUIGuardedCredentials is the host file the check proves was never
// touched: the host's REAL credential store under its real config root.
//
// EVENER_SSH_E2E_UI_GUARD_DISPOSABLE is a falsification hook, and the reason
// the guard can be trusted at all: a guard never seen to fail proves nothing. It
// points the guard at the test-owned disposable store instead of the real one,
// which the check's own write WOULD then touch, so the guard must fire. It is
// off in a normal run and never risks the host's real file either way.
func hostSettingsUIGuardedCredentials(home, hostDir string) string {
	if os.Getenv("EVENER_SSH_E2E_UI_GUARD_DISPOSABLE") == "1" {
		return hostDir + "/evener/credentials.toml"
	}
	return home + "/.config/evener/credentials.toml"
}

func TestHostSettingsUIDisposableHostE2E(t *testing.T) {
	if reason := hostSettingsUIGate(testing.Short(), os.Getenv("EVENER_SSH_E2E_UI"), os.Getenv("EVENER_SSH_E2E"), os.Getenv("EVENER_SSH_E2E_HOST")); reason != "" {
		t.Skip(reason)
	}
	dest := os.Getenv("EVENER_SSH_E2E_HOST")
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatalf("node is required to drive the settings UI: %v", err)
	}
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}

	host := newHostSSH(t, dest, os.Getenv("EVENER_SSH_E2E_USER"))
	home := host.output(`printf '%s' "$HOME"`)
	if !strings.HasPrefix(home, "/") {
		t.Fatalf("host %s HOME = %q, want an absolute path (the disposable directory and the host binary must be addressed absolutely)", host.target, home)
	}
	goos, goarch := hostTarget(t, host)

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	runID := hostDeployRunID()
	hostDir := home + "/" + hostSettingsUIDirPrefix + "-" + runID
	cfgRoot := hostDir + "/evener"
	credsPath := cfgRoot + "/credentials.toml"
	configPath := hostDir + "/" + hostSettingsUIToml
	hostAgentsDocPath := cfgRoot + "/AGENTS.md"

	// The per-run token makes the name unique, and an existing path is refused
	// HERE, before anything is created: `mkdir -p` on a name already present
	// would adopt a directory this run did not make, and the cleanup would then
	// delete whatever was already inside it.
	if _, err := host.run("test -e " + shellquote.RemoteWord(hostDir)); err == nil {
		t.Fatalf("host %s already has %s; this check creates and removes that directory itself, so it must not adopt an existing one", host.target, hostDir)
	}
	hostBin := hostDir + "/bin/evener"
	host.mustRun("mkdir -p " + shellquote.RemoteWord(hostDir+"/state") + " " + shellquote.RemoteWord(hostDir+"/bin") + " " + shellquote.RemoteWord(cfgRoot))

	// The disposable host hub runs a build of THIS checkout (carrying the host
	// half of 07b's agents-doc/proxy handlers). The host's own install at
	// ~/.local/bin/evener is never touched (hashed before/after below).
	host.writeFile(hostBin, stageHostTargetBinary(t, goos, goarch))
	host.mustRun("chmod 700 " + shellquote.RemoteWord(hostBin))

	// The disposable host config root: a provider instance distinct from the
	// controller's, a credentials file the store will accept, the host's own
	// AGENTS.md and global launch layer (the distinct seeded reads), and the
	// private hub.toml.
	host.writeFile(cfgRoot+"/providers.toml", []byte(hostSettingsUIHostProvidersTOML))
	host.writeFile(credsPath, []byte(hostSettingsUIHostCredentialsTOML))
	// The credentials store refuses any credentials.toml whose group/world bits
	// are set (internal/credentials/store.go), and `cat >` over ssh lands the
	// file at the remote umask (0644). Tighten it, or the disposable hub refuses
	// to start — which the readiness wait reports with the hub's own log line.
	host.mustRun("chmod 600 " + shellquote.RemoteWord(credsPath))
	host.writeFile(hostAgentsDocPath, []byte(hostSettingsUIHostAgentsDoc))
	host.writeFile(cfgRoot+"/launch.toml", []byte(hostSettingsUIHostLaunchTOML))
	// The project pane's host-only working directory: it exists on the host and
	// nowhere else, and its project-layer launch file names an agent no other
	// value in this check uses.
	projectCwd := filepath.Join(hostDir, hostSettingsUIProjectDir)
	host.mustRun("mkdir -p " + shellquote.RemoteWord(filepath.Join(projectCwd, ".evener")))
	host.writeFile(filepath.Join(projectCwd, ".evener", "launch.local.toml"),
		[]byte("agent = \""+hostSettingsUIProjectAgent+"\"\n"))
	// The in-repo pane's host-only working directory: the in-repo layer, untrusted
	// until the pane's own Trust action runs.
	inrepoCwd := filepath.Join(hostDir, hostSettingsUIInRepoDir)
	host.mustRun("mkdir -p " + shellquote.RemoteWord(filepath.Join(inrepoCwd, ".evener")))
	host.writeFile(filepath.Join(inrepoCwd, ".evener", "launch.toml"),
		[]byte("agent = \""+hostSettingsUIInRepoAgent+"\"\n"))
	host.writeFile(configPath, []byte(fmt.Sprintf("addr = %q\nhub_state_root = %q\nplugin_auto_upgrade = false\n", hostSettingsUIAddr, hostDir+"/state")))

	// The two files this check must leave untouched: the host's REAL credential
	// store and its real install. They are hashed before anything is launched,
	// and the comparison is registered BEFORE the hub starts so it runs on every
	// exit path — including a failure.
	realCredsPath := hostSettingsUIGuardedCredentials(home, hostDir)
	realCredsBefore := host.sha256IfFile(realCredsPath)
	installPath := home + "/.local/bin/evener"
	installBefore := host.sha256IfFile(installPath)

	t.Cleanup(func() {
		stopHostListener(t, host, hostSettingsUIAddr, configPath)
		// Read the guarded files BEFORE removing the directory: with the
		// falsification hook the guarded path can live inside hostDir, and
		// hashing after the removal would fire on "absent", not "changed".
		realCredsAfter := host.sha256IfFile(realCredsPath)
		installAfter := host.sha256IfFile(installPath)
		if err := host.tryRun("rm -rf " + shellquote.RemoteWord(hostDir)); err != nil {
			t.Errorf("remove the test-owned directory %s on host %s: %v", hostDir, host.target, err)
		}
		if realCredsBefore == "" {
			if realCredsAfter != "" {
				t.Errorf("this check created the host's real credential store %s on host %s (sha256 %s); it must write only the test-owned %s", realCredsPath, host.target, realCredsAfter, credsPath)
			}
		} else if realCredsAfter != realCredsBefore {
			t.Errorf("the host's REAL credential store %s on %s changed during the check (sha256 %s -> %s); it must write only the test-owned %s", realCredsPath, host.target, realCredsBefore, realCredsAfter, credsPath)
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

	// The controller hub runs from a binary built AFTER the frontend (the embed
	// constraint) on an isolated HOME, so the controller's own AGENTS.md,
	// launch config, and credential store are test-owned.
	//
	// Its provider and credential files are pinned to THIS check's own copies
	// before the stack starts: an evener-managed environment exports
	// EVENER_PROVIDERS_CONFIG, which redirects registry reads regardless of HOME,
	// so without this the controller would read a real config root. The pin is
	// process-wide (t.Setenv) and inherited by the hub child.
	controllerConfigDir := filepath.Join(testEnv.Root, "settings-ui-e2e-controller-"+hostDeployRunID())
	if err := os.MkdirAll(controllerConfigDir, 0o700); err != nil {
		t.Fatalf("create %s: %v", controllerConfigDir, err)
	}
	controllerProvidersPath := filepath.Join(controllerConfigDir, "providers.toml")
	if err := os.WriteFile(controllerProvidersPath, []byte(settingsUIControllerProvidersTOML(provider)), 0o600); err != nil {
		t.Fatalf("write the controller's isolated providers.toml: %v", err)
	}
	controllerCredsPath := filepath.Join(controllerConfigDir, "credentials.toml")
	if err := os.WriteFile(controllerCredsPath, []byte("schema = 1\n"), 0o600); err != nil {
		t.Fatalf("write the controller's isolated credentials.toml: %v", err)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", controllerProvidersPath)
	t.Setenv("EVENER_CREDENTIALS_CONFIG", controllerCredsPath)

	controllerBin := settingsUIControllerBinary(t, repoRoot)
	stack := startHubStackOnProviderWithEvener(t, settingsUIControllerProvidersTOML(provider), "fake/"+fakellm.ModelID, controllerBin)

	ctx, cancel := context.WithTimeout(context.Background(), hostSettingsUIAttachTimeout+2*time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)

	// The controller MUST be isolated, and this check does not take the isolated
	// HOME on faith: it reads the path the controller hub itself resolves for its
	// personal AGENTS.md and requires it under that HOME before it seeds
	// anything. The evener-managed environment this check may run under exports
	// EVENER_PROVIDERS_CONFIG (and friends), which redirects the registry paths
	// regardless of HOME — so a real config root is a live possibility and must
	// fail here rather than be written.
	controllerDoc, err := clientRequest[appwire.AgentsDocResponse](ctx, client, appwire.MethodEvenerSettingsAgentsDocGet, appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("read the controller hub's AGENTS.md path: %v", err)
	}
	controllerAgentsDocPath := controllerDoc.Path
	if !strings.HasPrefix(controllerAgentsDocPath, stack.home+string(os.PathSeparator)) {
		t.Fatalf("the controller hub resolves its AGENTS.md at %q, outside its isolated HOME %q: the controller must not read or write a real config root (an inherited EVENER_* path override defeats the isolated HOME)", controllerAgentsDocPath, stack.home)
	}
	if err := os.WriteFile(controllerAgentsDocPath, []byte(hostSettingsUIControllerAgentsDoc), 0o600); err != nil {
		t.Fatalf("seed the controller's AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(controllerAgentsDocPath), "launch.toml"), []byte(hostSettingsUIControllerLaunchTOML), 0o600); err != nil {
		t.Fatalf("seed the controller's launch.toml: %v", err)
	}

	// Start the disposable host hub so the attach below bridges to it instead of
	// bootstrapping a hub with the host's real environment.
	host.mustRun(hostHubLaunchScript(hostBin, configPath, hostDir, hostSettingsUIAddr))
	awaitHostHubHealth(t, host, hostSettingsUIAddr, hostDir+"/hub.log")

	row, err := clientRequest[appwire.HostRow](ctx, client, appwire.MethodEvenerHostAdd, appwire.HostAddParams{
		Entry: appwire.HostEntry{
			Name:       hostSettingsUIName,
			Address:    dest,
			User:       os.Getenv("EVENER_SSH_E2E_USER"),
			EvenerPath: hostBin,
			ConfigPath: configPath,
			Addr:       hostSettingsUIAddr,
		},
	})
	if err != nil {
		t.Fatalf("step evener/host/add (ssh destination %q, config %q, addr %q): %v", dest, configPath, hostSettingsUIAddr, err)
	}
	if row.Origin != "sidecar" {
		t.Fatalf("step evener/host/add: added row origin = %q, want %q (a host added through the wire is a sidecar entry)", row.Origin, "sidecar")
	}
	attached := awaitHostAttachedWithin(ctx, t, client, hostSettingsUIName, hostSettingsUIAttachTimeout)
	t.Logf("attached disposable host %s: os=%s arch=%s hubVersion=%s", hostSettingsUIName, attached.OS, attached.Arch, attached.HubVersion)

	// The host side must be the DISPOSABLE root, likewise read from the product:
	// the host hub's own AGENTS.md path must be the file this check seeded under
	// its per-run directory, not the host's real config root.
	hostDocRaw, err := forwardHostMethod(ctx, client, hostSettingsUIName, appwire.MethodEvenerSettingsAgentsDocGet, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("read the disposable host's AGENTS.md path through the proxy: %v", err)
	}
	var hostDoc appwire.AgentsDocResponse
	if err := json.Unmarshal(hostDocRaw, &hostDoc); err != nil {
		t.Fatalf("decode the disposable host's AGENTS.md response %s: %v", hostDocRaw, err)
	}
	if hostDoc.Path != hostAgentsDocPath {
		t.Fatalf("the disposable host hub resolves its AGENTS.md at %q, want the seeded %q; the host must read the disposable config root, never its real one", hostDoc.Path, hostAgentsDocPath)
	}

	// The hub must serve the tree's REAL SPA: a placeholder or half-built dist
	// answers webnext.go's documented 503 instead, and driving it would time out
	// confusingly at the first selector.
	assertHubServesBuiltSPA(t, stack.addr, stack.token)

	// The artifact directory. By default it is created under THIS PROCESS's temp
	// root, which the harness removes when the process exits — so "kept" would be a
	// promise this code cannot keep. EVENER_SSH_E2E_UI_ARTIFACT_DIR names a base
	// outside that root, and the keep message below says which of the two happened
	// rather than implying the screenshots outlived the run.
	artifactBase := os.Getenv("EVENER_SSH_E2E_UI_ARTIFACT_DIR")
	artifactRoot, err := os.MkdirTemp(artifactBase, "settingshostguard-")
	if err != nil {
		t.Fatalf("artifact root: %v", err)
	}
	artifactsSurvive := artifactBase != ""
	t.Cleanup(func() {
		if t.Failed() || os.Getenv("EVENER_SSH_E2E_UI_KEEP_ARTIFACTS") != "" {
			if artifactsSurvive {
				t.Logf("artifacts kept at %s (outside the run's temp root, so they outlive this process)", artifactRoot)
			} else {
				t.Logf("artifacts written to %s — that path is inside this run's temp root and is removed when the process exits; set EVENER_SSH_E2E_UI_ARTIFACT_DIR to a directory outside it, with EVENER_SSH_E2E_UI_KEEP_ARTIFACTS, to keep the screenshots and result.json", artifactRoot)
			}
			return
		}
		_ = os.RemoveAll(artifactRoot)
	})

	expectPath := filepath.Join(artifactRoot, "expect.json")
	if err := os.WriteFile(expectPath, settingsUIExpectJSON(projectCwd, inrepoCwd), 0o600); err != nil {
		t.Fatalf("write the runner fixture %s: %v", expectPath, err)
	}

	authURL := hubedge.AuthURLFor("http://"+stack.addr, stack.token)
	runnerPath := filepath.Join(repoRoot, "cmd", "evener-hub", "frontend", "scripts", "settingshostguard", "run.mjs")
	runnerCtx, runnerCancel := context.WithTimeout(context.Background(), hostSettingsUIDriverTimeout)
	defer runnerCancel()
	run := exec.CommandContext(runnerCtx, "node", runnerPath,
		"--url", authURL,
		"--artifact-dir", artifactRoot,
		"--host", hostSettingsUIName,
		"--expect", expectPath,
	)
	run.Dir = repoRoot
	combined, runErr := run.CombinedOutput()
	t.Logf("settingshostguard output:\n%s", combined)
	if runErr != nil {
		t.Fatalf("the settings-UI driver failed: %v (artifacts at %s)", runErr, artifactRoot)
	}

	// The host-side proof the runner cannot give: the write reached the
	// DISPOSABLE host config root. Read the file off the host itself, and compare
	// it BY HASH: hostSSH.output trims its stdout, so comparing that text against
	// the written value - which ends in a newline - could never match, and
	// comparing a trimmed value would accept a file whose trailing bytes differ
	// from what the UI wrote. A hash is exact either way.
	wantSum := fmt.Sprintf("%x", sha256.Sum256([]byte(hostSettingsUIWrittenAgentsDoc)))
	if gotSum := host.sha256IfFile(hostAgentsDocPath); gotSum != wantSum {
		written := host.output("cat " + shellquote.RemoteWord(hostAgentsDocPath))
		t.Fatalf("the disposable host's AGENTS.md %s on %s holds %q (sha256 %s), want the value written through the UI %q (sha256 %s)", hostAgentsDocPath, host.target, written, gotSum, hostSettingsUIWrittenAgentsDoc, wantSum)
	}
	// ...and the controller's own state did not change: the write must not have
	// landed in THIS hub's config root.
	controllerAfter, err := os.ReadFile(controllerAgentsDocPath)
	if err != nil {
		t.Fatalf("read the controller's AGENTS.md after the run: %v", err)
	}
	if string(controllerAfter) != hostSettingsUIControllerAgentsDoc {
		t.Fatalf("the controller's own AGENTS.md %s changed during the run (%q -> %q); the UI write must land only on the selected host", controllerAgentsDocPath, hostSettingsUIControllerAgentsDoc, string(controllerAfter))
	}
	t.Logf("disposable host AGENTS.md %s gained the UI-written value; the controller's AGENTS.md is unchanged", hostAgentsDocPath)
}

// settingsUIControllerBinary builds the controller `evener` this check runs,
// from the tree under test and AFTER the frontend (the hub embeds
// frontend/dist, so the order is load-bearing). It is cached once per test
// binary. Unlike liveStackBinaries (the read-only checks' shared unstamped
// build) this one owns its own once so the frontend build cannot be raced by an
// earlier caller's compile.
func settingsUIControllerBinary(t *testing.T, repoRoot string) string {
	t.Helper()
	settingsUIControllerBuild.once.Do(func() {
		if err := buildFrontendFromTree(repoRoot); err != nil {
			settingsUIControllerBuild.err = err
			return
		}
		dir := filepath.Join(testEnv.Root, "settings-ui-e2e-bin")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			settingsUIControllerBuild.err = fmt.Errorf("create %s: %w", dir, err)
			return
		}
		out := filepath.Join(dir, "evener")
		build := exec.Command("go", "build", "-o", out, "./cmd/evener/")
		build.Dir = repoRoot
		if combined, err := build.CombinedOutput(); err != nil {
			settingsUIControllerBuild.err = fmt.Errorf("build ./cmd/evener/: %w\n%s", err, combined)
			return
		}
		settingsUIControllerBuild.bin = out
	})
	if settingsUIControllerBuild.err != nil {
		t.Fatalf("build the settings-UI check's controller: %v", settingsUIControllerBuild.err)
	}
	return settingsUIControllerBuild.bin
}

var settingsUIControllerBuild struct {
	once sync.Once
	bin  string
	err  error
}

// buildFrontendFromTree builds cmd/evener-hub/frontend from the tree under test
// so the hub the check builds embeds THAT tree's UI. It always runs the build:
// `npm run build` starts with clean-dist.mjs (which reduces dist to the tracked
// PLACEHOLDER before the fallible typecheck), so an interrupted or skipped build
// cannot leave a stale SPA for `go build` to embed silently. A build that cannot
// run — this lane has no node_modules — fails naming the command an operator
// must run. It never runs `npm ci`/`npm install`: provisioning the lane's
// dependencies is the operator's, not this check's.
func buildFrontendFromTree(repoRoot string) error {
	frontendDir := filepath.Join(repoRoot, "cmd", "evener-hub", "frontend")
	build := exec.Command("npm", "run", "build")
	build.Dir = frontendDir
	if combined, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf(
			"build the production frontend (the hub embeds frontend/dist) in %s: %w\n%s\n"+
				"this check never runs npm ci/npm install: provision the frontend's node_modules first, then run `make build-web` (or `npm run build` in cmd/evener-hub/frontend) and rerun this check",
			frontendDir, err, combined)
	}
	return nil
}

// assertHubServesBuiltSPA proves the controller hub is answering with the built
// single-page app, not webnext.go's documented 503 ("evener-hub web app not
// built"). The check is made over HTTP with the hub's bearer token, the same
// credential a scripted client uses, so it is independent of the browser.
func assertHubServesBuiltSPA(t *testing.T, addr, token string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		t.Fatalf("build the SPA probe request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("probe the controller hub's index at http://%s/: %v", addr, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("read the controller hub's index: %v", err)
	}
	text := string(body)
	if strings.Contains(text, "web app not built") {
		t.Fatalf("the controller hub is serving the placeholder dist (webnext.go's 503) instead of the built SPA: the frontend build did not produce frontend/dist/index.html — build the frontend first (`make build-web`, or `npm run build` in cmd/evener-hub/frontend), then rebuild the hub before this check")
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(text, `id="root"`) {
		t.Fatalf("the controller hub's index at http://%s/ is not the built SPA (status %d, body %q); build the frontend and rebuild the hub before this check", addr, resp.StatusCode, text)
	}
}

// settingsUIExpectJSON is the fixture the runner asserts against: the host's
// seeded values it must find, and the controller's values it must never see.
// projectCwd and inrepoCwd are the working directories the host (and only the
// host) holds: the first with hostSettingsUIProjectAgent in its project layer,
// the second with hostSettingsUIInRepoAgent in its in-repo layer.
func settingsUIExpectJSON(projectCwd, inrepoCwd string) []byte {
	doc := map[string]any{
		"credentials": map[string]string{"hostText": hostSettingsUIHostInstance, "controllerText": hostSettingsUIControllerInstance},
		"agentsMd": map[string]string{
			"hostContent":       hostSettingsUIHostAgentsDoc,
			"controllerContent": hostSettingsUIControllerAgentsDoc,
			"writeContent":      hostSettingsUIWrittenAgentsDoc,
		},
		"launchAgent": map[string]string{"host": hostSettingsUIHostLaunchAgent, "controller": hostSettingsUIControllerLaunchAgent},
		"skillsDir":   map[string]string{"host": hostSettingsUIHostSkillsDir, "controller": hostSettingsUIControllerSkillsDir},
		"pluginsDir":  map[string]string{"host": hostSettingsUIHostPluginsDir, "controller": hostSettingsUIControllerPluginsDir},
		"mcpConfig":   map[string]string{"host": hostSettingsUIHostMCPConfig, "controller": hostSettingsUIControllerMCPConfig},
		"project":     map[string]string{"cwd": projectCwd, "hostAgent": hostSettingsUIProjectAgent},
		"inrepo":      map[string]string{"cwd": inrepoCwd, "hostAgent": hostSettingsUIInRepoAgent},
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("marshal the settings-UI fixture: %v", err))
	}
	return append(data, '\n')
}

// hostSettingsUIHostProvidersTOML is the disposable host's providers.toml: one
// instance whose name is unlike the controller's, so the credentials pane's
// read assertion can tell the host's listing from the controller's.
const hostSettingsUIHostProvidersTOML = `default = "onhost"

[providers.onhost]
base     = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
`

// hostSettingsUIHostCredentialsTOML is the disposable host's credentials.toml:
// an acceptably-formed store the hub will start with. `schema = 1` and no
// entries is the empty store.
const hostSettingsUIHostCredentialsTOML = "schema = 1\n"

// hostSettingsUIHostLaunchTOML is the disposable host's GLOBAL launch layer
// (<configRoot>/launch.toml): the values the host-scoped launch-evener, skills,
// plugins, and mcp panes must render. Each differs from the controller's.
const hostSettingsUIHostLaunchTOML = `agent       = "` + hostSettingsUIHostLaunchAgent + `"
skills_dirs = ["` + hostSettingsUIHostSkillsDir + `"]
plugin_dirs = ["` + hostSettingsUIHostPluginsDir + `"]
mcp_configs = ["` + hostSettingsUIHostMCPConfig + `"]
`

// hostSettingsUIControllerLaunchTOML is the controller's own global launch
// layer, seeded in its isolated HOME so the panes' values are distinguishable.
const hostSettingsUIControllerLaunchTOML = `agent       = "` + hostSettingsUIControllerLaunchAgent + `"
skills_dirs = ["` + hostSettingsUIControllerSkillsDir + `"]
plugin_dirs = ["` + hostSettingsUIControllerPluginsDir + `"]
mcp_configs = ["` + hostSettingsUIControllerMCPConfig + `"]
`

// settingsUIControllerProvidersTOML is the controller stack's providers.toml:
// the fake provider every stack needs plus an instance whose name is unlike the
// host's, so the credentials pane's "controller value absent" assertion is
// decisive.
func settingsUIControllerProvidersTOML(provider *fakellm.Server) string {
	return fmt.Sprintf(`
default = "fake"

[providers.fake]
base     = "openai-compatible"
base_url = %q
api_key  = "fakellm-not-a-secret"

[providers.%s]
base     = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
`, provider.BaseURL(), hostSettingsUIControllerInstance)
}

// TestHostSettingsUIGateSkipsWithoutOptIn pins the gate and the guard's default
// target with NO host involved, so both are provable in ordinary `go test ./...`
// (which must perform no ssh). The write gate must be checked BEFORE the master
// gate — an un-opted-in run's skip line must name the UI opt-in — and the guard
// must default to the host's REAL credential store, moving to the disposable one
// only under the falsification hook. A guard repointed at the disposable root
// (or a gate chain that checked the master first) fails here.
func TestHostSettingsUIGateSkipsWithoutOptIn(t *testing.T) {
	// The skip chain, in the order the live check applies it.
	if got := hostSettingsUIGate(true, "1", "1", "host"); !strings.Contains(got, "live SSH") {
		t.Fatalf("short mode reason = %q, want one naming the live SSH check", got)
	}
	got := hostSettingsUIGate(false, "", "1", "host")
	if !strings.Contains(got, "EVENER_SSH_E2E_UI=1") || !strings.Contains(got, "WRITES") {
		t.Fatalf("un-opted-in reason = %q, want one naming EVENER_SSH_E2E_UI=1 and the host write; the write gate must be checked before the master gate", got)
	}
	masterMissing := hostSettingsUIGate(false, "1", "", "host")
	if !strings.Contains(masterMissing, "EVENER_SSH_E2E=1") {
		t.Fatalf("missing master gate reason = %q, want one naming EVENER_SSH_E2E=1", masterMissing)
	}
	// The write gate is checked FIRST: with the master gate SATISFIED and the UI
	// opt-in absent, the reason must be the UI one, not the master one.
	if got == masterMissing {
		t.Fatalf("the UI write gate is not checked before the master gate: both absences give %q", got)
	}
	if got := hostSettingsUIGate(false, "1", "1", ""); !strings.Contains(got, "EVENER_SSH_E2E_HOST") {
		t.Fatalf("missing host reason = %q, want one naming EVENER_SSH_E2E_HOST", got)
	}
	if got := hostSettingsUIGate(false, "1", "1", "host"); got != "" {
		t.Fatalf("fully gated reason = %q, want no skip", got)
	}

	// The non-mutation guard's default target: the host's REAL credential store.
	const home = "/Users/dev"
	const hostDir = "/Users/dev/evener-settings-ui-e2e-123"
	if got, want := hostSettingsUIGuardedCredentials(home, hostDir), home+"/.config/evener/credentials.toml"; got != want {
		t.Fatalf("default guard target = %q, want the host's real store %q", got, want)
	}
	t.Setenv("EVENER_SSH_E2E_UI_GUARD_DISPOSABLE", "1")
	if got, want := hostSettingsUIGuardedCredentials(home, hostDir), hostDir+"/evener/credentials.toml"; got != want {
		t.Fatalf("falsification-hook guard target = %q, want the disposable store %q", got, want)
	}
}
