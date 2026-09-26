package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/internal/shellquote"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// The bounds on the deploy live check. Unlike the sibling read-only host check,
// this one drives an attach that must deploy first: the attach RPC is
// synchronous and carries the whole cross-compile, push, first-attach hub
// bootstrap, and attach handshake inside one call. That is minutes of real work,
// so the bound is larger than the read-only check's four minutes. It is a
// tripwire, not the mechanism: the call returns as soon as the deploy and attach
// finish, and the poll interval only decides how often the row is re-read.
const (
	hostDeployAttachTimeout = 12 * time.Minute
	hostDeploySSHTimeout    = 2 * time.Minute
)

// hostDeployDirPrefix names the test-owned directory on the host. The deploy
// writes its binary under it, never over the host's own install; the directory
// carries a per-run token (hostDeployRunID) and is removed when the case
// finishes, so it is always one this run created rather than one already there.
const hostDeployDirPrefix = "evener-deploy-e2e"

// hostDeployToml is the private hub.toml each case writes on the host. The
// launched host hub reads it (hubBootstrapArgv passes --config) so it uses the
// test's own state root and port: the host's real hub keeps its lock, its state,
// and its port untouched.
const hostDeployToml = "hub.toml"

// hostDeployRunID is the per-run token every directory this test creates on a
// host carries. The pid separates runs of the test binary and the nanosecond
// timestamp separates concurrent ones, so a name is never reused across a rerun
// that left a directory behind — which is what lets the cleanup treat the whole
// directory as this run's own and remove it.
func hostDeployRunID() string {
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
}

// TestHostDeployNoEvenerE2E is the live check for the deploy slice's criteria 1
// and 2 (docs/superpowers/specs/2026-09-22-multi-host-deploy-slice.md): a host
// with no evener at the run target is given one by the controller and attaches,
// and the binary the controller left on it reports the controller's own
// buildinfo.Version() from `launch-check`.
//
// It drives the hub's own flags over the wire path the sibling
// TestHostAddAttachForwardedDiscoveryE2E uses (the same private loopback hub, the
// same evener/host/add -> evener/host/attach sequence), with two cases:
//
//   - build-source: the hub cross-compiles this checkout for the host's target
//     and pushes it (the primary path; the binary's identity is true by
//     construction);
//   - deploy-binary: the hub pushes an operator-supplied artifact this test
//     staged by cross-compiling the same tree (the path whose identity is proven
//     on the host after the push).
//
// It is gated by EVENER_SSH_E2E_DEPLOY=1 ON TOP OF EVENER_SSH_E2E=1 and
// EVENER_SSH_E2E_HOST, so default `make test` and `go test ./...` perform no ssh
// and need no host. EVENER_SSH_E2E_DEPLOY is separate from the read-mostly
// sibling's gate because this check WRITES to the host, and a developer running
// the sibling check should not be signed up for a write. The write is confined
// to a directory this test creates and removes, with the run target's basename
// `evener` (checkRunTarget requires exactly that); the host's own install at
// ~/.local/bin/evener is hashed before and after and must be unchanged.
//
// The controller builds its own `evener` binary stamped with this checkout's
// HEAD, so the version the host must report identifies a commit rather than the
// "dev" two unstamped builds share. That makes a clean checkout a prerequisite:
// -build-source refuses a dirty controller by design, and this test skips with
// that reason rather than failing. Go and the checkout must be on the controller;
// -build-source also needs the embedded web UI built (make build-web).
func TestHostDeployNoEvenerE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("live SSH deploy test: builds a stamped evener, cross-compiles for a remote host, and writes to it")
	}
	// The write gate is checked first so an un-opted-in run's skip line states the
	// contract plainly: this check WRITES to the host, and it needs all three
	// variables. It is a separate opt-in from the read-mostly sibling check's, so
	// a developer running that one is not signed up for a write.
	if os.Getenv("EVENER_SSH_E2E_DEPLOY") != "1" {
		t.Skip("set EVENER_SSH_E2E_DEPLOY=1 (with EVENER_SSH_E2E=1 and EVENER_SSH_E2E_HOST) to run the live host deploy test; this check WRITES to the host — it creates its own run-target directory there and starts a hub from it")
	}
	if os.Getenv("EVENER_SSH_E2E") != "1" {
		t.Skip("set EVENER_SSH_E2E=1 and EVENER_SSH_E2E_HOST to run the live host deploy test")
	}
	dest := os.Getenv("EVENER_SSH_E2E_HOST")
	if dest == "" {
		t.Skip("set EVENER_SSH_E2E_HOST to a disposable ssh destination (an ssh alias or user@host) to run the live host deploy test")
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
		t.Fatalf("host %s HOME = %q, want an absolute path (the deploy target has to be an absolute path the host hub can be launched from)", host.target, home)
	}
	goos, goarch := hostTarget(t, host)

	hubBin, version := deployE2EHubBinary(t, repoRoot)
	t.Logf("controller build: %s (version %q); host target %s/%s", hubBin, version, goos, goarch)

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	staged := stageDeployArtifact(t, repoRoot, goos, goarch)

	runID := hostDeployRunID()
	cases := []struct {
		name     string
		hostName string
		dirName  string
		addr     string
		args     []string
	}{
		// The private loopback port is fixed per case because the controller must
		// be told the exact address the private hub listens on. A listener there
		// could therefore be an unrelated process, so cleanup never kills by port
		// alone: stopHostListener signals only a pid whose command line names the
		// private config path this case wrote, and a cleanup that cannot stop it
		// fails the test instead of passing silently.
		{
			name:     "build-source",
			hostName: "e2e-deploy-source",
			dirName:  hostDeployDirPrefix + "-source-" + runID,
			addr:     "127.0.0.1:19180",
			args:     []string{"-build-source", repoRoot},
		},
		{
			name:     "deploy-binary",
			hostName: "e2e-deploy-binary",
			dirName:  hostDeployDirPrefix + "-binary-" + runID,
			addr:     "127.0.0.1:19181",
			args:     []string{"-deploy-binary", staged},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runHostDeployCase(t, provider, hubBin, version, dest, os.Getenv("EVENER_SSH_E2E_USER"), home, goos, goarch, tc.hostName, tc.dirName, tc.addr, tc.args)
		})
	}
}

// runHostDeployCase runs one deploy case end to end: it prepares a private run
// target on the host, boots a controller hub with the case's deploy flags, adds
// the host pointed at that private target, attaches it, and asserts the three
// facts the criterion names — the row reports attached with the host's live
// facts, the deployed file exists at the configured path, and the host's own
// launch-check reports the controller's version.
func runHostDeployCase(t *testing.T, provider *fakellm.Server, hubBin, version, dest, user, home, goos, goarch, hostName, dirName, addr string, deployArgs []string) {
	t.Helper()

	host := newHostSSH(t, dest, user)
	hostDir := home + "/" + dirName
	runTarget := hostDir + "/bin/evener"

	// The test creates the host directory itself, because the deploy path needs
	// the target's parent to exist (deployTarget probes `test -d`). bin/ holds
	// the run target; the other two are the private hub's config and state. The
	// name carries a per-run token, and an existing path is refused HERE, before
	// anything is created: `mkdir -p` on a name that is already there would adopt
	// a directory this run did not make, and the cleanup would then delete
	// whatever was already inside it.
	if _, err := host.run("test -e " + shellquote.RemoteWord(hostDir)); err == nil {
		t.Fatalf("host %s already has %s; the deploy check creates and removes this directory itself, so it must not adopt an existing one", host.target, hostDir)
	}
	host.mustRun("mkdir -p " + shellquote.RemoteWord(hostDir+"/bin") + " " + shellquote.RemoteWord(hostDir+"/state"))
	// The criterion is a host with NO evener at the run target. The directory is
	// new this run, so nothing can already sit at the target; the assertion is
	// kept so the precondition is proven rather than assumed.
	if _, err := host.run("test -e " + shellquote.RemoteWord(runTarget)); err == nil {
		t.Fatalf("host %s already has %s at the start of the case; the deploy case needs a run target with no evener", host.target, runTarget)
	}

	// The private hub.toml keeps the launched host hub on its own port and state
	// root, so it never touches the host's real hub (its 9180 listener, its
	// lock, or its state).
	host.writeFile(hostDir+"/"+hostDeployToml, []byte(fmt.Sprintf("addr = %q\nhub_state_root = %q\nplugin_auto_upgrade = false\n", addr, hostDir+"/state")))

	// The host's own install, if it has one, must come out of this unchanged.
	installPath := home + "/.local/bin/evener"
	installHash := host.sha256IfFile(installPath)

	t.Cleanup(func() {
		stopHostListener(t, host, addr, hostDir+"/"+hostDeployToml)
		// hostDir is this run's own: the per-run token makes it unique and the
		// existence check above refused to proceed if anything was already there,
		// so this removes only what the case created. A failure is reported, not
		// logged away — a leftover directory must not pass as a clean run.
		if err := host.tryRun("rm -rf " + shellquote.RemoteWord(hostDir)); err != nil {
			t.Errorf("remove the test-owned directory %s on host %s: %v", hostDir, host.target, err)
		}
		if installHash == "" {
			// The host had no install here when the case began. Skipping the check
			// would let the case create one and still pass, which is the violation
			// the assertion exists to catch, so assert it is still absent.
			if got := host.sha256IfFile(installPath); got != "" {
				t.Errorf("the deploy created %s on host %s (sha256 %s); it must write only to the test-owned %s", installPath, host.target, got, runTarget)
			}
			return
		}
		if got := host.sha256IfFile(installPath); got != installHash {
			t.Errorf("the host's own install %s changed during the deploy case (sha256 %s -> %s); the deploy must write only to the test-owned %s", installPath, installHash, got, runTarget)
		}
	})

	stack := startDeployStack(t, provider, hubBin, deployArgs...)

	ctx, cancel := context.WithTimeout(context.Background(), hostDeployAttachTimeout+2*time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)

	row, err := clientRequest[appwire.HostRow](ctx, client, appwire.MethodEvenerHostAdd, appwire.HostAddParams{
		Entry: appwire.HostEntry{
			Name:       hostName,
			Address:    dest,
			User:       user,
			EvenerPath: runTarget,
			ConfigPath: hostDir + "/" + hostDeployToml,
			Addr:       addr,
		},
	})
	if err != nil {
		t.Fatalf("step evener/host/add (ssh destination %q, run target %q): %v", dest, runTarget, err)
	}
	if row.Origin != "sidecar" {
		t.Fatalf("step evener/host/add: added row origin = %q, want %q (a host added through the wire is a sidecar entry)", row.Origin, "sidecar")
	}

	attached := awaitHostAttachedWithin(ctx, t, client, hostName, hostDeployAttachTimeout)
	t.Logf("attached %s: os=%s arch=%s hubVersion=%s evenerPath=%s", hostName, attached.OS, attached.Arch, attached.HubVersion, attached.EvenerPath)

	// The row reports the host's live facts: the OS/arch the controller probed
	// from `uname`, and the build the controller deployed. HubVersion is the
	// controller's own version (remoteHostFacts), which is exactly what the
	// deployed build must report, so it is asserted against the same value the
	// launch-check assertion below uses.
	if attached.OS != goos || attached.Arch != goarch {
		t.Fatalf("attached row for %q reports os/arch %s/%s, want %s/%s (the host's probed target)", hostName, attached.OS, attached.Arch, goos, goarch)
	}
	if attached.HubVersion != version {
		t.Fatalf("attached row for %q reports hubVersion %q, want the controller's %q", hostName, attached.HubVersion, version)
	}
	if p := attached.EvenerPath; path.Base(p) != "evener" || !strings.Contains(p, dirName) {
		t.Fatalf("attached row for %q reports evenerPath %q, want the test's run target under %q (a path named evener)", hostName, p, dirName)
	}

	// The deploy's file effect: the binary the hub pushed exists at the private
	// path and is executable.
	size := host.filesize(t, runTarget)
	t.Logf("deployed %s on %s: %d bytes", runTarget, host.target, size)

	// The identity claim the whole path rests on, read from the host's own
	// launch-check rather than inferred from the deploy: the binary left there
	// reports the controller's buildinfo.Version(), speaks the controller's
	// appwire protocol, and carries the launch flag a host hub must have.
	lc := host.launchCheck(t, runTarget)
	if lc.Version != version {
		t.Fatalf("the deployed binary at %s reports launch-check version %q, want the controller's %q", runTarget, lc.Version, version)
	}
	if lc.Protocol != appwire.ProtocolVersion {
		t.Fatalf("the deployed binary at %s reports protocol %q, want %q", runTarget, lc.Protocol, appwire.ProtocolVersion)
	}
	if !slices.Contains(lc.LaunchFlags, "api-log") {
		t.Fatalf("the deployed binary at %s reports launch_flags %v, want one containing %q", runTarget, lc.LaunchFlags, "api-log")
	}
}

// hostTarget probes the host's uname and maps it to GOOS/GOARCH exactly as the
// deploy path does. Only linux/amd64 and darwin/arm64 ship a build, so any other
// host is announced as a skip rather than a failure.
func hostTarget(t *testing.T, host *hostSSH) (goos, goarch string) {
	t.Helper()
	switch host.output("uname -s") {
	case "Darwin":
		goos = "darwin"
	case "Linux":
		goos = "linux"
	default:
		t.Skipf("host %s uname -s is not Linux or Darwin; the deploy live check supports linux/amd64 and darwin/arm64", host.target)
	}
	switch host.output("uname -m") {
	case "x86_64", "amd64":
		goarch = "amd64"
	case "arm64", "aarch64":
		goarch = "arm64"
	default:
		t.Skipf("host %s uname -m is not x86_64 or arm64; the deploy live check supports linux/amd64 and darwin/arm64", host.target)
	}
	if goos != "linux" || goarch != "amd64" {
		if goos != "darwin" || goarch != "arm64" {
			t.Skipf("host %s target %s/%s has no shipped evener build; the deploy live check supports linux/amd64 and darwin/arm64", host.target, goos, goarch)
		}
	}
	return goos, goarch
}

// startDeployStack boots the controller hub on an isolated HOME pointed at the
// fake provider, with the deploy case's own hub flags. It is the sibling stack
// helper with the deploy flags threaded through.
func startDeployStack(t *testing.T, provider *fakellm.Server, evenerBin string, deployArgs ...string) hubStack {
	t.Helper()
	return startHubStackOnProviderWithEvener(t, fmt.Sprintf(`
default = "fake"

[providers.fake]
base     = "openai-compatible"
base_url = %q
api_key  = "fakellm-not-a-secret"
`, provider.BaseURL()), "fake/"+fakellm.ModelID, evenerBin, deployArgs...)
}

// deployE2EHubBinary builds the controller `evener` this check runs and returns
// its path and the version it reports. It is stamped with this checkout's HEAD,
// so the version the host must end up reporting names a commit instead of the
// bare "dev" that two unstamped builds share.
//
// It is deliberately not liveStackBinaries: that shared helper's unstamped build
// is the sibling read-only check's controller, whose host is expected to carry a
// matching (also unstamped) build — stamping it there would change that check's
// contract. The build is cached once per test binary, like the sibling's.
func deployE2EHubBinary(t *testing.T, repoRoot string) (bin, version string) {
	t.Helper()
	deployE2EHubBuild.once.Do(func() {
		sha, dirty, ok := cleanRevision(t, repoRoot)
		if !ok {
			return
		}
		dir := filepath.Join(testEnv.Root, "deploy-e2e-bin")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			deployE2EHubBuild.err = fmt.Errorf("create %s: %w", dir, err)
			return
		}
		stdout := filepath.Join(dir, "evener")
		build := exec.Command("go", "build", "-ldflags", deployLdflags(sha, dirty), "-o", stdout, "./cmd/evener/")
		build.Dir = repoRoot
		if out, buildErr := build.CombinedOutput(); buildErr != nil {
			deployE2EHubBuild.err = fmt.Errorf("build ./cmd/evener/: %w\n%s", buildErr, out)
			return
		}
		reported, err := binaryVersion(stdout)
		if err != nil {
			deployE2EHubBuild.err = err
			return
		}
		deployE2EHubBuild.bin, deployE2EHubBuild.version = stdout, reported
	})
	if deployE2EHubBuild.err != nil {
		t.Fatalf("build the deploy check's controller: %v", deployE2EHubBuild.err)
	}
	return deployE2EHubBuild.bin, deployE2EHubBuild.version
}

var deployE2EHubBuild struct {
	once    sync.Once
	bin     string
	version string
	err     error
}

// stageDeployArtifact cross-compiles this checkout for the host's target and
// returns the staged path the -deploy-binary case is given. It carries the same
// stamped identity as the controller, so the artifact the hub pushes reports the
// controller's version.
func stageDeployArtifact(t *testing.T, repoRoot, goos, goarch string) string {
	t.Helper()
	sha, dirty, ok := cleanRevision(t, repoRoot)
	if !ok {
		return ""
	}
	dir := filepath.Join(testEnv.Root, "deploy-e2e-artifact", goos+"-"+goarch)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	out := filepath.Join(dir, "evener")
	build := exec.Command("go", "build", "-ldflags", deployLdflags(sha, dirty), "-o", out, "./cmd/evener/")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	if combined, err := build.CombinedOutput(); err != nil {
		t.Fatalf("stage a %s/%s artifact to %s: %v\n%s", goos, goarch, out, err, combined)
	}
	return out
}

// cleanRevision returns this checkout's HEAD and whether it is dirty. A dirty
// checkout is a skip, not a failure: the deploy path is stamped from HEAD and
// -build-source refuses a controller it cannot reproduce from a clean tree, so
// the case genuinely cannot run (the production refusal, not a test defect).
func cleanRevision(t *testing.T, repoRoot string) (sha, dirty string, ok bool) {
	t.Helper()
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD in %s: %v", repoRoot, err)
	}
	sha = strings.TrimSpace(string(out))
	status, err := exec.Command("git", "-C", repoRoot, "status", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git status --porcelain in %s: %v", repoRoot, err)
	}
	if strings.TrimSpace(string(status)) == "" {
		return sha, "", true
	}
	t.Skipf("the deploy live check needs a clean checkout: it stamps the controller with HEAD (%s) and -build-source refuses a dirty tree; commit or stash first", sha)
	return "", "", false
}

// buildinfoPkgPath is the package the deploy path stamps, and deployLdflags
// mirrors sshconn's buildLdflags for the two values the version assertion
// reads.
const buildinfoPkgPath = "primeradiant.com/evener/buildinfo"

func deployLdflags(sha, dirty string) string {
	set := func(name, value string) string { return "-X " + buildinfoPkgPath + "." + name + "=" + value }
	flags := []string{set("GitSHA", sha)}
	if dirty != "" {
		flags = append(flags, set("GitDirty", dirty))
	}
	return strings.Join(flags, " ")
}

// binaryVersion runs a locally built evener and returns the version it reports
// (`evener <buildinfo.VersionLong()>`), so the assertion compares two values
// read from a binary rather than one read from a binary and one assumed from the
// ldflags.
func binaryVersion(bin string) (string, error) {
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("%s --version: %w", bin, err)
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 2 || fields[0] != "evener" {
		return "", fmt.Errorf("%s --version = %q, want `evener <version>`", bin, strings.TrimSpace(string(out)))
	}
	return fields[1], nil
}

// hostSSH runs non-interactive remote commands for the test itself, separate
// from the manager's own dialing. It is how the test creates and removes its
// private directory and how it reads the deployed binary's contract.
type hostSSH struct {
	t      *testing.T
	target string
}

func newHostSSH(t *testing.T, dest, user string) *hostSSH {
	t.Helper()
	target := dest
	if user != "" && !strings.Contains(dest, "@") {
		target = user + "@" + dest
	}
	return &hostSSH{t: t, target: target}
}

func (h *hostSSH) argv(script string) []string {
	return []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "--", h.target, script}
}

// run runs one remote script and returns its combined output. A non-nil error
// is the remote command's own failure (including its exit status).
func (h *hostSSH) run(script string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), hostDeploySSHTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", h.argv(script)...)
	return cmd.CombinedOutput()
}

// runStdout runs one remote script and returns its stdout alone, keeping the
// remote command's stderr out of the answer. The listener probe's proved-empty
// marker is a stdout contract: a tool that warns on stderr while printing the
// marker on stdout (lsof does on some hosts) would otherwise turn a cleared port
// into an unrecognized answer. A non-nil error is still the command's own
// failure, so a nonzero probe stays "cannot say".
func (h *hostSSH) runStdout(script string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), hostDeploySSHTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", h.argv(script)...)
	return cmd.Output()
}

// output runs one remote script and returns its trimmed stdout, failing the
// test on any error.
func (h *hostSSH) output(script string) string {
	h.t.Helper()
	out, err := h.run(script)
	if err != nil {
		h.t.Fatalf("host %s: %q: %v: %s", h.target, script, err, out)
	}
	return strings.TrimSpace(string(out))
}

// mustRun runs one remote script, failing the test on any error.
func (h *hostSSH) mustRun(script string) {
	h.t.Helper()
	if out, err := h.run(script); err != nil {
		h.t.Fatalf("host %s: %q: %v: %s", h.target, script, err, out)
	}
}

// tryRun runs one remote script and logs (without failing) a failure. It is for
// cleanup, where a teardown error must not hide the assertion that already
// failed.
func (h *hostSSH) tryRun(script string) error {
	out, err := h.run(script)
	if err != nil {
		h.t.Logf("host %s: %q: %v: %s", h.target, script, err, out)
	}
	return err
}

// writeFile writes content to path on the host.
func (h *hostSSH) writeFile(path string, content []byte) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), hostDeploySSHTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", h.argv("cat > "+shellquote.RemoteWord(path))...)
	cmd.Stdin = bytes.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		h.t.Fatalf("write %s on host %s: %v: %s", path, h.target, err, out)
	}
}

// filesize returns the byte length of an executable file on the host, failing
// if it is missing or not executable.
func (h *hostSSH) filesize(t *testing.T, path string) int64 {
	t.Helper()
	q := shellquote.RemoteWord(path)
	out := h.output(fmt.Sprintf("if [ -x %s ]; then wc -c < %s; else echo MISSING; fi", q, q))
	var size int64
	if _, err := fmt.Sscanf(out, "%d", &size); err != nil || size <= 0 {
		t.Fatalf("the deployed binary %s on host %s is missing or empty (wc -c said %q); the deploy did not leave a runnable file at the configured path", path, h.target, out)
	}
	return size
}

// launchCheck runs the deployed binary's own launch-check on the host and
// returns its decoded contract. It addresses the file by its absolute path, the
// way the non-interactive PATH cannot.
func (h *hostSSH) launchCheck(t *testing.T, path string) launchContract {
	t.Helper()
	raw := h.output(shellquote.RemoteWord(path) + " launch-check --protocol " + shellquote.RemoteWord(appwire.ProtocolVersion) + " --json")
	var lc launchContract
	if err := json.Unmarshal([]byte(raw), &lc); err != nil {
		t.Fatalf("the deployed binary %s on host %s did not answer launch-check with a contract: %v: %s", path, h.target, err, raw)
	}
	return lc
}

// launchContract is the subset of `evener launch-check --json` the assertion
// reads.
type launchContract struct {
	Protocol    string   `json:"protocol"`
	Version     string   `json:"version"`
	LaunchFlags []string `json:"launch_flags"`
}

// sha256IfFile returns the sha256 of path when it is a regular file on the host,
// and "" when it is absent. It prefers sha256sum and falls back to macOS's
// shasum.
func (h *hostSSH) sha256IfFile(path string) string {
	q := shellquote.RemoteWord(path)
	script := fmt.Sprintf("if [ -f %s ]; then if command -v sha256sum >/dev/null 2>&1; then sha256sum %s; else shasum -a 256 %s; fi; fi", q, q, q)
	out, err := h.run(script)
	if err != nil {
		h.t.Logf("hash %s on host %s: %v: %s", path, h.target, err, out)
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// stopHostListener stops the hub this case started and waits briefly for the
// port to clear, so the directory it runs from can be removed. The port is
// fixed, so a listener there could be an unrelated process: the kill is gated on
// the pid's command line naming the private config path this case wrote, so it
// can only ever target this test's own hub. A kill that fails, or a listener
// that outlives the wait, is reported rather than logged away — a silent cleanup
// failure would leave a hub running from a directory the test then removes.
func stopHostListener(t *testing.T, host *hostSSH, addr, configPath string) {
	t.Helper()
	port := addr[strings.LastIndex(addr, ":")+1:]
	kill := hostHubKillCommand(addr, configPath)
	if err := host.tryRun(kill); err != nil {
		t.Errorf("stopping the deploy check's host hub on %s (config %s): %v", addr, configPath, err)
	}
	probe := sshconn.ListenerProbeRemote(port)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		// Only a probe that ran (nil error) and answered NoListenerMarker proves
		// the port is clear. A probe that could not run, or an answer this code
		// does not recognize, proves nothing and must never be read as "the
		// listener is gone": returning here is what lets the caller remove the
		// directory a still-running hub serves from. The timeout below reports the
		// unproven cleanup.
		if out, err := host.runStdout(probe); err == nil && hostListenerProbeAbsent(out) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Errorf("the deploy check's host hub still holds %s after 10s; it may be serving from the directory this test is about to remove", addr)
}

// hostListenerProbeAbsent reports whether out — the answer of the shared
// listener probe sshconn.ListenerProbeRemote — proves that the port has no
// listener. The caller must already have a nil probe error: a probe that could
// not run is "cannot say", not absence. Only sshconn.NoListenerMarker proves
// absence; a pid, a listener-present answer, or anything unrecognized
// (including empty output) is not absence, because treating a silent or failing
// probe as "nothing is there" is the recognition bug that let cleanup remove a
// live hub's directory.
func hostListenerProbeAbsent(out []byte) bool {
	return strings.TrimSpace(string(out)) == sshconn.NoListenerMarker
}

// hostHubKillCommand is the remote command that stops the hub this case started
// and nothing else. It lists the pids listening on the case's private port and
// signals each only when `ps` shows the case's own config path in its command
// line — the marker the launched host hub was given (hubBootstrapArgv passes
// --config). The port alone is deliberately not enough: it is a fixed literal, so
// killing every listener on it could terminate an unrelated process.
//
// The pids come from the shared listener probe, not a bare `lsof`: that probe
// falls back to ss and to /proc/net/tcp, so a host without lsof is still probed,
// and it exits nonzero when no probe can run. The kill checks that status before
// the loop, because `for pid in $(...)` discards it: an unprobeable host must be
// a reported failure, never a silent "nothing to kill".
func hostHubKillCommand(addr, configPath string) string {
	port := addr[strings.LastIndex(addr, ":")+1:]
	// The probe's answer is a pid per line or one of its two markers; the case
	// guard keeps only numeric pids as kill candidates, so a marker can never
	// reach `ps` or `kill`.
	return fmt.Sprintf(
		"pids=$( %s ); s=$?; "+
			"if [ $s -ne 0 ]; then echo 'evener-hub deploy check cleanup: no listener probe is available on the host (lsof, ss, and /proc/net/tcp are all missing); refusing to conclude no hub is listening' >&2; exit $s; fi; "+
			"for pid in $pids; do "+
			"case \"$pid\" in ''|*[!0-9]*) continue;; esac; "+
			"if ps -ww -o command= -p \"$pid\" 2>/dev/null | grep -F -e %s >/dev/null; then kill \"$pid\"; fi; done",
		sshconn.ListenerProbeRemote(port), shellquote.RemoteWord(configPath))
}

// TestHostHubKillCommandTargetsOnlyThisTestsHub pins the cleanup's precision
// without a host: the command that stops the case's hub must gate the kill on the
// pid's command line carrying the case's own config path, so an unrelated
// process holding the fixed port is never signalled.
func TestHostHubKillCommandTargetsOnlyThisTestsHub(t *testing.T) {
	const addr = "127.0.0.1:19180"
	const config = "/home/dev/evener-deploy-e2e-source/hub.toml"
	got := hostHubKillCommand(addr, config)
	if !strings.Contains(got, "lsof -ti :19180 -sTCP:LISTEN") {
		t.Fatalf("kill command does not find the listener by the case's port: %q", got)
	}
	if !strings.Contains(got, "ps -ww -o command=") {
		t.Fatalf("kill command does not read the pid's command line: %q", got)
	}
	if guard := "grep -F -e " + shellquote.RemoteWord(config); !strings.Contains(got, guard) {
		t.Fatalf("kill command does not gate on this case's config path (%s): %q", guard, got)
	}
	if !strings.Contains(got, "; then kill \"$pid\"; fi") {
		t.Fatalf("kill is not conditioned on the config-path match: %q", got)
	}
	// The config path is the discriminator, so a different path must produce a
	// different command; otherwise the kill would match any listener on the port.
	if other := hostHubKillCommand(addr, "/home/dev/other/hub.toml"); other == got {
		t.Fatal("kill command ignores the config path, so it would signal any listener on the port")
	}
}

// TestHostHubKillCommandFailsClosedWithoutAProbe pins issue #2151 item 2's
// recognition defect in the cleanup's kill command. The command used to list
// pids with a bare `lsof -tiTCP:<port> ... 2>/dev/null`: on a host without lsof
// that answers with empty output and no error, which reads as "no listener", so
// the loop kills nothing and cleanup can remove the test's directory while its
// hub still runs from it. The pids must instead come from the shared probe that
// falls back to ss and /proc/net/tcp, and the probe's own exit status must be
// checked before the loop — `for pid in $(...)` discards it — so an unprobeable
// host is a reported failure rather than a silent "nothing to kill".
func TestHostHubKillCommandFailsClosedWithoutAProbe(t *testing.T) {
	const addr = "127.0.0.1:19180"
	const config = "/home/dev/evener-deploy-e2e-source/hub.toml"
	got := hostHubKillCommand(addr, config)
	if strings.Contains(got, "lsof -tiTCP:19180 -sTCP:LISTEN 2>/dev/null") {
		t.Fatalf("the kill still runs lsof alone and reads its silence as absence: %q", got)
	}
	for _, tier := range []string{"command -v lsof", "command -v ss", "/proc/net/tcp"} {
		if !strings.Contains(got, tier) {
			t.Fatalf("the kill's probe lost the %q tier, so a host without lsof kills nothing: %q", tier, got)
		}
	}
	if !strings.Contains(got, "no listener probe is available") {
		t.Fatalf("the kill does not distinguish an unprobeable host from an empty port: %q", got)
	}
	// The kill must check that status before the loop. Asserting the ordering,
	// not just the presence of `s=$?`/`exit $s` (which the embedded probe also
	// contains), keeps the test tied to the property rather than the spelling.
	guard := strings.Index(got, "evener-hub deploy check cleanup")
	loop := strings.Index(got, "for pid in $pids")
	if guard < 0 || loop < 0 || guard > loop {
		t.Fatalf("the kill does not check the probe's exit status before the pid loop (guard=%d loop=%d): %q", guard, loop, got)
	}
}

// TestHostListenerProbeAbsentOnlyOnTheMarker pins the poll's recognition without
// a host: only the probe's no-listener marker proves the port clear. A pid, a
// listener-present answer, and — the item-2 defect — empty or unrecognized
// output must all read as "not absent", so a silent probe never ends the poll
// early.
func TestHostListenerProbeAbsentOnlyOnTheMarker(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want bool
	}{
		{name: "no-listener marker proves absence", out: sshconn.NoListenerMarker + "\n", want: true},
		{name: "marker among noise", out: " " + sshconn.NoListenerMarker + "\n", want: true},
		{name: "a pid is not absence", out: "4242\n", want: false},
		{name: "a named pid brackets the marker", out: "4242\n" + sshconn.NoListenerMarker + "\n", want: false},
		{name: "listener-present marker is not absence", out: sshconn.ListenerPresentMarker + "\n", want: false},
		{name: "empty output is not absence", out: "", want: false},
		{name: "unrecognized output is not absence", out: "sshconn: probe blew up\n", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostListenerProbeAbsent([]byte(tc.out)); got != tc.want {
				t.Fatalf("hostListenerProbeAbsent(%q) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}

// TestStopHostListenerIgnoresProbeStderr pins the roborev finding on PR #2413:
// the probe's proved-empty answer is a stdout contract, but the poll read the
// remote command's combined output. A probe tool that warns on stderr while
// printing the marker on stdout (lsof does on some hosts) then looked like an
// unrecognized answer, so the poll waited out its deadline and reported a hub
// that had already stopped — a false e2e failure. The fake ssh makes the probe
// exit 0 with the marker on stdout and a warning on stderr: a poll that reads
// stdout alone returns at once, while one that reads combined output runs the
// full ten-second deadline and fails.
func TestStopHostListenerIgnoresProbeStderr(t *testing.T) {
	dir := t.TempDir()
	sshPath := filepath.Join(dir, "ssh")
	fake := "#!/bin/sh\n" +
		"script=\"\"\n" +
		"for a in \"$@\"; do script=\"$a\"; done\n" +
		"case \"$script\" in\n" +
		"  *'evener-hub deploy check cleanup'*) exit 0 ;;\n" +
		"esac\n" +
		"echo 'lsof: WARNING: can not stat() file system' >&2\n" +
		"echo " + sshconn.NoListenerMarker + "\n"
	if err := os.WriteFile(sshPath, []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	host := &hostSSH{t: t, target: "fake-host"}
	stopHostListener(t, host, "127.0.0.1:19180", "/tmp/hub.toml")
}
