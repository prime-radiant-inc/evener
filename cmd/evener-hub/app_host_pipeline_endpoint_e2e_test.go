package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/hubapi"
)

type hostPipelineEndpointCase struct {
	name        string
	defaultPath bool
	bootstrap   bool
	wildcard    bool
	portZero    bool
}

// Each row gets a fresh container. Only immutable image/build inputs are shared.
var hostPipelineEndpointCases = []hostPipelineEndpointCase{
	{name: "legacy_bootstrap_addr", bootstrap: true},
	{name: "default_path_custom_port", defaultPath: true},
	{name: "explicit_config_custom_root"},
	{name: "wildcard", wildcard: true},
	{name: "port_zero", portZero: true},
}

type hostPipelineEndpointWant struct {
	binary, config, root, listen, source, version string
}

// Deliberately decode the on-disk schema here, without the product resolver or
// its computed URLs. Expectations come from the test's written configuration.
type hostPipelineEndpointRecord struct {
	SchemaVersion int       `json:"schema_version"`
	BootID        string    `json:"boot_id"`
	PID           int       `json:"pid"`
	StartedAt     time.Time `json:"started_at"`
	Config        string    `json:"config_path"`
	Root          string    `json:"hub_state_root"`
	Listen        string    `json:"requested_listen_addr"`
	Source        string    `json:"listen_source"`
	Bound         string    `json:"bound_addr"`
	Version       string    `json:"version"`
	Build         string    `json:"backend_git_sha"`
}

type hostPipelineEndpointSnapshot struct {
	record hostPipelineEndpointRecord
	birth  string // independent kernel boot ID and /proc stat start ticks
	health hubapi.HealthResponse
}

func hostPipelineEndpointRead(t *testing.T, host *hostSSH, want hostPipelineEndpointWant, phase string) hostPipelineEndpointSnapshot {
	t.Helper()
	raw := host.output("cat " + shellquote.RemoteWord(want.root+"/hub-endpoint.json"))
	var record hostPipelineEndpointRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatalf("%s endpoint record: %v: %s", phase, err, raw)
	}
	if record.SchemaVersion != 1 || record.PID <= 0 || record.BootID == "" || record.StartedAt.IsZero() {
		t.Fatalf("%s incomplete selected generation: %+v", phase, record)
	}
	if record.Config != want.config || record.Root != want.root || record.Listen != want.listen || record.Source != want.source {
		t.Fatalf("%s selected endpoint differs from written inputs: got %+v, want %+v", phase, record, want)
	}
	// Publication currently omits these optional fields. Build proof below is
	// mandatory on both the live socket and the independently executing inode.
	if (record.Version != "" && record.Version != want.version) || (record.Build != "" && record.Build != want.version) {
		t.Fatalf("%s optional record build disagrees with expected %s: %+v", phase, want.version, record)
	}
	proc := "/proc/" + strconv.Itoa(record.PID)
	cmdline, err := host.runStdout("cat " + proc + "/cmdline")
	if err != nil {
		t.Fatalf("%s selected process argv: %v", phase, err)
	}
	argv := strings.Split(strings.TrimSuffix(string(cmdline), "\x00"), "\x00")
	if len(argv) < 2 || argv[0] != want.binary || argv[1] != "hub" {
		t.Fatalf("%s selected PID %d argv=%q, want owned binary %s hub", phase, record.PID, argv, want.binary)
	}
	if want.source == "config" && (slices.Contains(argv, "--addr") || slices.Contains(argv, "-addr")) {
		t.Fatalf("%s config-owned listener was overridden in argv: %q", phase, argv)
	}
	exe := host.output("readlink " + proc + "/exe")
	if exe != want.binary && exe != want.binary+" (deleted)" {
		t.Fatalf("%s selected executable=%q, want owned %q", phase, exe, want.binary)
	}
	stat := host.output("cat " + proc + "/stat")
	end := strings.LastIndex(stat, ") ")
	if end < 0 || len(strings.Fields(stat[end+2:])) < 20 {
		t.Fatalf("%s invalid owned process stat: %q", phase, stat)
	}
	birth := host.output("cat /proc/sys/kernel/random/boot_id") + ":" + strings.Fields(stat[end+2:])[19]
	_, port, err := net.SplitHostPort(record.Bound)
	if err != nil || port == "0" || port == "" {
		t.Fatalf("%s actual bound listener=%q: %v", phase, record.Bound, err)
	}
	assertHostPipelinePIDListener(t, host, record.PID, port)
	// Numeric loopback is derived only from the observed bound port, never a
	// guessed/default URL or a wildcard destination.
	healthRaw := host.output("curl -q --noproxy '*' -fsS --connect-timeout 2 --max-time 5 " + shellquote.RemoteWord("http://"+net.JoinHostPort("127.0.0.1", port)+"/api/health"))
	var health hubapi.HealthResponse
	if err := json.Unmarshal([]byte(healthRaw), &health); err != nil {
		t.Fatalf("%s numeric-loopback health: %v: %s", phase, err, healthRaw)
	}
	if health.BootID != record.BootID || !health.StartedAt.Equal(record.StartedAt) || health.HubAddr != record.Bound || health.Version != want.version || health.BackendGitSha != want.version {
		t.Fatalf("%s owned listener health=%+v differs from record=%+v", phase, health, record)
	}
	// Inspect the executing inode, not the replaced path on disk. Endpoint
	// capability is a separate field from AppWire protocol compatibility.
	lcRaw := host.output(proc + "/exe launch-check --json")
	var capability struct {
		Version        string `json:"version"`
		Protocol       string `json:"protocol"`
		EndpointSchema int    `json:"hub_endpoint_schema"`
	}
	if err := json.Unmarshal([]byte(lcRaw), &capability); err != nil {
		t.Fatalf("%s running-image launch-check: %v", phase, err)
	}
	if capability.Version != want.version || capability.Protocol != appwire.ProtocolVersion || capability.EndpointSchema != 1 {
		t.Fatalf("%s running-image capability=%+v", phase, capability)
	}
	t.Logf("%s selected: pid=%d boot=%s birth=%s config=%s root=%s requested=%s source=%s bound=%s build=%s executing=%s endpointSchema=%d appwire=%s", phase, record.PID, record.BootID, birth, record.Config, record.Root, record.Listen, record.Source, record.Bound, health.BackendGitSha, exe, capability.EndpointSchema, capability.Protocol)
	return hostPipelineEndpointSnapshot{record: record, birth: birth, health: health}
}

func assertHostPipelinePIDListener(t *testing.T, host *hostSSH, pid int, port string) {
	t.Helper()
	out := host.output(fmt.Sprintf("lsof -nP -a -p %d -iTCP -sTCP:LISTEN -Fn", pid))
	lines := strings.Split(out, "\n")
	if !slices.Contains(lines, "p"+strconv.Itoa(pid)) {
		t.Fatalf("owned PID %d missing from socket evidence: %s", pid, out)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "n") && strings.HasSuffix(line, ":"+port) {
			return
		}
	}
	t.Fatalf("owned PID %d has no listening socket on port %s: %s", pid, port, out)
}

func assertHostPipelineSameEndpoint(t *testing.T, before, after hostPipelineEndpointSnapshot) {
	t.Helper()
	if before.record != after.record || before.birth != after.birth {
		t.Fatalf("unexpected process replacement: before=%+v birth=%s after=%+v birth=%s", before.record, before.birth, after.record, after.birth)
	}
}

func assertHostPipelineRemoteMarker(ctx context.Context, t *testing.T, client *appwire.Client, host *hostSSH, hostName, marker, contents string) {
	t.Helper()
	params := appwire.PathsCompleteParams{Prefix: filepath.Dir(marker) + "/", IncludeFiles: true}
	local, err := clientRequest[appwire.PathsCompleteResponse](ctx, client, appwire.MethodEvenerPathsComplete, params)
	if err != nil || slices.Contains(local.Data, marker) {
		t.Fatalf("controller-local marker negative control: data=%v error=%v", local.Data, err)
	}
	rawParams, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := forwardHostMethod(ctx, client, hostName, appwire.MethodEvenerPathsComplete, rawParams)
	if err != nil {
		t.Fatalf("authenticated forwarded AppWire marker request: %v", err)
	}
	var remote appwire.PathsCompleteResponse
	if err := json.Unmarshal(raw, &remote); err != nil || !slices.Contains(remote.Data, marker) {
		t.Fatalf("remote marker missing from actual AppWire result: data=%s error=%v", raw, err)
	}
	if got := host.output("cat " + shellquote.RemoteWord(marker)); got != contents {
		t.Fatalf("remote marker contents=%q, want %q", got, contents)
	}
	t.Logf("authenticated remote AppWire marker usable: %s", marker)
}

func startHostPipelineUnrelated(t *testing.T, host *hostSSH, staged, hostDir, version string) (hostPipelineEndpointWant, hostPipelineEndpointSnapshot) {
	t.Helper()
	dir := hostDir + "/unrelated"
	want := hostPipelineEndpointWant{binary: dir + "/evener", config: dir + "/hub.toml", root: dir + "/state", listen: "127.0.0.1:9180", source: "config", version: version}
	host.mustRun("mkdir -p " + shellquote.RemoteWord(want.root))
	artifact, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	host.writeFile(want.binary, artifact)
	host.mustRun("chmod 700 " + shellquote.RemoteWord(want.binary))
	host.writeFile(want.config, []byte(fmt.Sprintf("addr = %q\nhub_state_root = %q\nplugin_auto_upgrade = false\n", want.listen, want.root)))
	host.writeFile(want.root+"/unrelated-marker", []byte("unrelated-"+filepath.Base(hostDir)))
	host.mustRun("nohup " + shellquote.RemoteWord(want.binary) + " hub --config " + shellquote.RemoteWord(want.config) + " >" + shellquote.RemoteWord(dir+"/hub.log") + " 2>&1 </dev/null &")
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := host.run("test -s " + shellquote.RemoteWord(want.root+"/hub-endpoint.json") + " && curl -q --noproxy '*' -fsS --max-time 2 http://127.0.0.1:9180/api/health >/dev/null")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unrelated disposable hub failed to start: %v, log=%s", err, host.output("cat "+shellquote.RemoteWord(dir+"/hub.log")))
		}
		time.Sleep(100 * time.Millisecond)
	}
	return want, hostPipelineEndpointRead(t, host, want, "unrelated-before-attach")
}

func assertHostPipelineUnrelated(t *testing.T, host *hostSSH, want hostPipelineEndpointWant, before hostPipelineEndpointSnapshot, phase string) {
	t.Helper()
	assertHostPipelineSameEndpoint(t, before, hostPipelineEndpointRead(t, host, want, phase))
	if got := host.output("cat " + shellquote.RemoteWord(want.root+"/unrelated-marker")); got != "unrelated-"+filepath.Base(filepath.Dir(filepath.Dir(want.root))) {
		t.Fatalf("unrelated marker changed: %q", got)
	}
}

// The holder competes only for the retired ephemeral port. It never signals a
// process. Starting it before restart makes the successor choose another port
// while also proving an unrelated listener cannot block selected-owner exit.
func startHostPipelineRetiredPortHolder(t *testing.T, host *hostSSH, hostDir, goos, goarch, bound string) string {
	t.Helper()
	const source = `package main
import ("net"; "os"; "strconv"; "time")
func main() {
 deadline := time.Now().Add(10*time.Minute)
 for time.Now().Before(deadline) {
  listener, err := net.Listen("tcp", os.Args[1])
  if err != nil { time.Sleep(time.Millisecond); continue }
  if err := os.WriteFile(os.Args[2], []byte(strconv.Itoa(os.Getpid())), 0600); err != nil { panic(err) }
  time.Sleep(time.Until(deadline)); listener.Close(); return
 }
 os.Exit(1)
}
`
	dir := t.TempDir()
	src, binary := filepath.Join(dir, "holder.go"), filepath.Join(dir, "holder")
	if err := os.WriteFile(src, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", binary, src)
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build disposable port holder: %v: %s", err, out)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	path := hostDir + "/retired-port-holder"
	ready := hostDir + "/retired-port-holder.ready"
	host.writeFile(path, data)
	host.mustRun("chmod 700 " + shellquote.RemoteWord(path))
	_, port, err := net.SplitHostPort(bound)
	if err != nil {
		t.Fatal(err)
	}
	addr := net.JoinHostPort("127.0.0.1", port)
	host.mustRun("nohup " + shellquote.RemoteWord(path) + " " + shellquote.RemoteWord(addr) + " " + shellquote.RemoteWord(ready) + " >" + shellquote.RemoteWord(hostDir+"/retired-port-holder.log") + " 2>&1 </dev/null &")
	return ready
}

func assertHostPipelineRetiredPort(t *testing.T, host *hostSSH, hostDir, ready, before, after string) {
	t.Helper()
	_, oldPort, err := net.SplitHostPort(before)
	if err != nil {
		t.Fatal(err)
	}
	_, newPort, err := net.SplitHostPort(after)
	if err != nil || oldPort == newPort {
		t.Fatalf("port-zero successor did not change actual port: %s -> %s (%v)", before, after, err)
	}
	pid, err := strconv.Atoi(host.output("cat " + shellquote.RemoteWord(ready)))
	if err != nil || pid <= 0 {
		t.Fatalf("retired-port holder did not acquire predecessor port: pid=%d error=%v", pid, err)
	}
	argv, err := host.runStdout(fmt.Sprintf("cat /proc/%d/cmdline", pid))
	if err != nil || !strings.HasPrefix(string(argv), hostDir+"/retired-port-holder\x00"+net.JoinHostPort("127.0.0.1", oldPort)+"\x00") {
		t.Fatalf("retired-port holder identity mismatch: %q (%v)", argv, err)
	}
	assertHostPipelinePIDListener(t, host, pid, oldPort)
	t.Logf("port-zero successor changed %s -> %s, unrelated owned holder PID %d retains predecessor port", before, after, pid)
}

// Diagnostic-only wrapper: retain anomalous credential-free selected-lock
// results, forwarding exact stdout, stderr and exit status without argv changes.
// Redirection still changes timing, so this is not an uninstrumented execution.
func installHostPipelineLockTrace(t *testing.T) {
	t.Helper()
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	trace := filepath.Join(dir, "lock-trace")
	script := hostPipelineLockTraceScript(realSSH, dir)
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		matches, err := os.ReadFile(filepath.Join(dir, "matches"))
		if !t.Skipped() && (err != nil || len(matches) == 0) {
			t.Errorf("selected-lock diagnostic wrapper was not exercised: %v", err)
		}
		t.Logf("selected-lock diagnostic wrapper: %d real SSH invocations captured", len(matches))
		data, err := os.ReadFile(trace)
		if os.IsNotExist(err) {
			t.Log("selected-lock failure capture: no anomalous result observed")
			return
		}
		if err != nil {
			t.Logf("lock trace read: %v", err)
			return
		}
		t.Logf("actual product selected-lock anomalous results:\n%s", data)
	})
}

func hostPipelineLockTraceScript(realSSH, dir string) string {
	return fmt.Sprintf(`#!/bin/sh
umask 077
matched=false
for a in "$@"; do
 case "$a" in lock=*"/hub.lock"*'identity=$(stat -Lc'*'key=$(printf'*"/proc/locks"*) matched=true; command=$a;; esac
done
if ! "$matched"; then exec %s "$@"; fi
printf . >> %s
out=%s/out.$$
err=%s/err.$$
%s "$@" >"$out" 2>"$err"
status=$?
value=$(cat "$out")
case "$value" in ''|0|*[!0-9]*) anomalous=true;; *) anomalous=false;; esac
if "$anomalous" || [ "$status" -ne 0 ] || [ -s "$err" ]; then
 { printf 'command=%%s\nstatus=%%s\nstdout=[' "$command" "$status"; cat "$out"; printf ']\nstderr=['; cat "$err"; printf ']\n'; } >> %s
fi
cat "$out"
cat "$err" >&2
rm "$out" "$err"
exit "$status"
`, shellquote.RemoteWord(realSSH), shellquote.RemoteWord(filepath.Join(dir, "matches")), shellquote.RemoteWord(dir), shellquote.RemoteWord(dir), shellquote.RemoteWord(realSSH), shellquote.RemoteWord(filepath.Join(dir, "lock-trace")))
}

func hostPipelineLockDiagnostic(t *testing.T, host *hostSSH, selected hostPipelineEndpointSnapshot, phase string) {
	t.Helper()
	r := selected.record
	script := "printf 'root inode/device: '; stat -Lc '%d %i %f %n' " + shellquote.RemoteWord(r.Root+"/hub.lock") +
		"; printf 'kernel lock rows:\\n'; cat /proc/locks; printf 'selected generation:\\n'; cat /proc/" + strconv.Itoa(r.PID) + "/stat; " +
		"printf 'selected lock descriptors:\\n'; lsof -nP -a -p " + strconv.Itoa(r.PID) + " -Fn " + shellquote.RemoteWord(r.Root+"/hub.lock")
	out, err := host.run(script)
	t.Logf("%s lock diagnosis: root=%s selectedPID=%d boot=%s birth=%s status=%v\n%s", phase, r.Root, r.PID, r.BootID, selected.birth, err, out)
}

func TestHostPipelineLockTracePreservesSkip(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh is not installed")
	}
	t.Run("setup-unavailable", func(t *testing.T) {
		installHostPipelineLockTrace(t)
		t.Skip("live build or container setup is unavailable")
	})
}

// TestHostPipelineLockTrace executes the actual wrapper in /bin/sh. The command
// fixture is the exact selectedLockRemote output at the continuation base, not
// a rewritten lock probe. The fake boundary below is SSH, not product logic.
func TestHostPipelineLockTrace(t *testing.T) {
	const selectedCommand = `lock=/run/selected/hub-state/hub.lock; identity=$(stat -Lc '%d %i' "$lock") || exit; set -- $identity; [ "$#" -eq 2 ] || exit 1; dev=$1; ino=$2; major=$(( ((dev >> 8) & 4095) | ((dev >> 32) & 4294963200) )); minor=$(( (dev & 255) | ((dev >> 12) & 4294967040) )); key=$(printf '%02x:%02x:%s' "$major" "$minor" "$ino"); for attempt in 1 2 3; do current=$(stat -Lc '%d %i' "$lock") || exit; [ "$current" = "$identity" ] || exit 1; owners=$(awk -v key="$key" '$2 == "FLOCK" && $3 == "ADVISORY" && $4 == "WRITE" && $6 == key { print $5 }' /proc/locks && printf '.') || exit; owners=${owners%.}; current=$(stat -Lc '%d %i' "$lock") || exit; [ "$current" = "$identity" ] || exit 1; if [ -n "$owners" ]; then printf '%s' "$owners"; exit; fi; done`
	for _, tc := range []struct {
		name, command, stdout, stderr string
		status                        int
		matched, capture              bool
	}{
		{name: "normal", command: selectedCommand, stdout: "12345\n", matched: true},
		{name: "empty", command: selectedCommand, matched: true, capture: true},
		{name: "zero", command: selectedCommand, stdout: "0\n", matched: true, capture: true},
		{name: "malformed", command: selectedCommand, stdout: "12\x00x3\n\n", matched: true, capture: true},
		{name: "stderr", command: selectedCommand, stdout: "12345\n", stderr: "warning\x00\n", matched: true, capture: true},
		{name: "failure", command: selectedCommand, stdout: "12345\n", status: 37, matched: true, capture: true},
		{name: "stat-only", command: "stat -Lc '%d %i' /run/selected/hub-state/hub.lock", status: 19},
		{name: "environment", command: "cat /proc/123/environ", stdout: "synthetic-private-value\n"},
		{name: "other-lock-command", command: "cat /run/selected/hub-state/hub.lock /proc/locks", status: 9},
		{name: "missing-key", command: strings.ReplaceAll(selectedCommand, "key=$(printf", "other=$(printf")},
		{name: "missing-lock", command: strings.ReplaceAll(selectedCommand, "/hub.lock", "/other.lock")},
		{name: "missing-proc", command: strings.ReplaceAll(selectedCommand, "/proc/locks", "/other/locks")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stdout, stderr, argv := filepath.Join(dir, "stdout"), filepath.Join(dir, "stderr"), filepath.Join(dir, "argv")
			for path, data := range map[string]string{stdout: tc.stdout, stderr: tc.stderr} {
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			realSSH := filepath.Join(dir, "real-ssh")
			stub := "#!/bin/sh\nprintf '%s\\000' \"$@\" > " + shellquote.RemoteWord(argv) + "\ncat " + shellquote.RemoteWord(stdout) + "\ncat " + shellquote.RemoteWord(stderr) + " >&2\nexit " + strconv.Itoa(tc.status) + "\n"
			shim := filepath.Join(dir, "ssh")
			for path, data := range map[string]string{realSSH: stub, shim: hostPipelineLockTraceScript(realSSH, dir)} {
				if err := os.WriteFile(path, []byte(data), 0700); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"-T", "--", "fixture-host", tc.command, "space and 'quote", ""}
			cmd := exec.Command("/bin/sh", append([]string{shim}, args...)...)
			var out, errOut bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errOut
			err := cmd.Run()
			status := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				status = exit.ExitCode()
			}
			if status != tc.status || out.String() != tc.stdout || errOut.String() != tc.stderr {
				t.Fatalf("forwarding: status=%d stdout=%q stderr=%q, want %d %q %q", status, out.String(), errOut.String(), tc.status, tc.stdout, tc.stderr)
			}
			gotArgv, err := os.ReadFile(argv)
			if err != nil || string(gotArgv) != strings.Join(args, "\x00")+"\x00" {
				t.Fatalf("argv changed: %q (%v)", gotArgv, err)
			}
			for name, want := range map[string]bool{"matches": tc.matched, "lock-trace": tc.capture} {
				path := filepath.Join(dir, name)
				data, err := os.ReadFile(path)
				if !want {
					if !os.IsNotExist(err) {
						t.Fatalf("unexpected %s: %q (%v)", name, data, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("missing %s: %v", name, err)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("private %s mode: %v (%v)", name, info, err)
				}
				if name == "matches" && string(data) != "." {
					t.Fatalf("match count=%q", data)
				}
				if name == "lock-trace" {
					wantData := "command=" + tc.command + "\nstatus=" + strconv.Itoa(tc.status) + "\nstdout=[" + tc.stdout + "]\nstderr=[" + tc.stderr + "]\n"
					if string(data) != wantData {
						t.Fatalf("capture differs: %q", data)
					}
				}
			}
			for _, pattern := range []string{"out.*", "err.*"} {
				files, err := filepath.Glob(filepath.Join(dir, pattern))
				if err != nil || len(files) != 0 {
					t.Fatalf("temporary output remains: %v (%v)", files, err)
				}
			}
		})
	}
}
