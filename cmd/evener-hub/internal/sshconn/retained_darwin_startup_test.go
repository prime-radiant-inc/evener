package sshconn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostlock"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/hubendpoint"
)

// Publication, discovery/health enrichment and atomic target repair are real.
// Darwin process/launchd observations remain strict modeled evidence.
func TestRetainedDarwinStartupRecordRecovery(t *testing.T) {
	resolver := buildConfigIdentityResolver(t)
	for _, mode := range []string{"deleted", "nonexecutable", "persisted-mismatch", "version-mismatch", "sha-mismatch", "persisted-at-write", "build-at-write"} {
		t.Run(mode, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			config, target := filepath.Join(root, "hub.toml"), filepath.Join(root, "evener")
			if err := os.WriteFile(config, fmt.Appendf(nil, "addr = \"127.0.0.1:0\"\nhub_state_root = %q\n", root), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("predecessor"), 0o755); err != nil {
				t.Fatal(err)
			}
			release, err := hostlock.AcquireLock(filepath.Join(root, "hub.lock"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(release)
			started := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
			server := httptest.NewUnstartedServer(nil)
			server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/health" {
					t.Errorf("unexpected health path: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode(hubendpoint.VerifiedHealth{BootID: "startup-boot", HubAddr: server.Listener.Addr().String(), StartedAt: started, Version: "oldsha", BackendGitSha: "old-build-sha"})
			})
			server.Start()
			t.Cleanup(server.Close)
			startup := hubendpoint.Record{SchemaVersion: 1, BootID: "startup-boot", PID: os.Getpid(), StartedAt: started, ConfigPath: config, HubStateRoot: root, RequestedListenAddr: "127.0.0.1:0", ListenSource: "config", BoundAddr: server.Listener.Addr().String(), LocalURL: server.URL, LoopbackURL: server.URL}
			remove, err := hubendpoint.Publish(root, startup)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := remove(); err != nil {
					t.Error(err)
				}
			})
			file := filepath.Join(root, "hub-endpoint.json")
			original, err := os.ReadFile(file)
			if err != nil || bytes.Contains(original, []byte(`"version"`)) || bytes.Contains(original, []byte(`"backend_git_sha"`)) {
				t.Fatalf("startup record contains health-derived build fields: %s, %v", original, err)
			}
			discover := func(ctx context.Context) ([]byte, error) {
				return exec.CommandContext(ctx, resolver, "hub", "endpoint", "--json", "--config", config).Output()
			}
			data, err := discover(t.Context())
			if err != nil {
				t.Fatalf("real endpoint discovery: %v", err)
			}
			report, err := hubendpoint.DecodeReport(bytes.NewReader(data))
			if err != nil || report.Running == nil || report.Running.Version != "oldsha" || report.Running.BackendGitSHA != "old-build-sha" {
				t.Fatalf("discovery must enrich startup record from health: report=%+v err=%v", report, err)
			}
			unchanged, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(original, unchanged) {
				t.Fatalf("discovery modified startup publication: %v", err)
			}
			mutate := func() {
				record := startup
				switch mode {
				case "persisted-mismatch", "persisted-at-write":
					record.BootID = "different-boot"
				case "version-mismatch", "build-at-write":
					record.Version = "different-version"
				case "sha-mismatch":
					record.BackendGitSHA = "different-sha"
				}
				out, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, out, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "persisted-mismatch" || mode == "version-mismatch" || mode == "sha-mismatch" {
				mutate()
			}
			if mode == "nonexecutable" {
				if err := os.Chmod(target, 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", User: "dev", EvenerPath: target, ConfigPath: config}
			pid := strconv.Itoa(startup.PID)
			argv := []string{target, "hub", "--config", config}
			env := []string{"HOME=" + root}
			post := ownerContext(root, env) + shellquote.RemoteWord(target) + " hub endpoint --json --config " + shellquote.RemoteWord(config)
			recordRead := "if test -f " + shellquote.RemoteWord(file) + " && test ! -L " + shellquote.RemoteWord(file) + "; then head -c 65537 < " + shellquote.RemoteWord(file) + "; else exit 1; fi"
			writes, records, postReads := 0, 0, 0
			fr := &fakeRunner{runFn: func(ctx context.Context, command []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(command[12:], " ")
				run := func() ([]byte, error) {
					cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
					cmd.Stdin = in
					return cmd.Output()
				}
				switch remote {
				case "ps -p " + pid + " -o lstart=":
					return []byte("recorded-birth\n"), nil
				case "launchctl list":
					return nil, nil
				case "ps -o user= -p " + pid:
					return []byte("dev\n"), nil
				case hubArgvRemote(pid):
					return []byte(strings.Join(argv, "\x00") + "\x00"), nil
				case "lsof -a -p " + pid + " -d cwd -Fn":
					return []byte("p" + pid + "\nfcwd\nn" + root + "\n"), nil
				case darwinEnvironmentRemote(pid):
					return []byte(env[0] + "\x00"), nil
				case recordRead:
					records++
					return run()
				case "if test -x " + shellquote.RemoteWord(target) + "; then exit 0; else exit 1; fi", resolveDeployCommand(target), resolveRetainedDeployCommand(target), createTargetCommand(target):
					return run()
				case pushBinaryRemote(target, int64(len("staged-binary"))):
					writes++
					return run()
				case shellquote.RemoteWord(target) + " launch-check --protocol evener-appwire-v7 --json":
					return launchContract("newsha"), nil
				case post:
					postReads++
					return discover(ctx)
				default:
					return nil, fmt.Errorf("unexpected remote command: %s", remote)
				}
			}}
			boundary := false
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: func(ctx context.Context, goos, goarch, file string) error {
				boundary = true
				if strings.HasSuffix(mode, "-at-write") {
					mutate()
				}
				return writeStageBinary(ctx, goos, goarch, file)
			}})
			host, _ = m.reg.Get(host.Name)
			owner := selectedOwner{host: host, pid: pid, generation: "recorded-birth", argv: argv, cwd: root, cwdIdentity: root, env: env, report: report}
			pending := pendingRestartState{selected: &owner, expectedVersion: "newsha", deployment: true, installTarget: target}
			m.pendingRestarts[host.Name] = pending
			snapshot := owner.clone()
			pending.selected = &snapshot
			_, _, err = m.deployForOperation(t.Context(), host, Preflight{Host: host.Name, OS: "darwin", Arch: "arm64", UID: "1000", ExecutableMissing: true}, pending, nil)
			if mode == "deleted" || mode == "nonexecutable" {
				data, readErr := os.ReadFile(target)
				info, statErr := os.Stat(target)
				if err != nil || readErr != nil || statErr != nil || string(data) != "staged-binary" || info.Mode()&0o111 == 0 || writes != 1 || records != 2 || postReads != 1 || m.pendingRestart(host.Name).active() {
					t.Fatalf("authorized real repair: err=%v read=%v stat=%v writes=%d records=%d post=%d pending=%+v", err, readErr, statErr, writes, records, postReads, m.pendingRestart(host.Name))
				}
			} else if err == nil || writes != 0 || postReads != 0 || !reflect.DeepEqual(pending, m.pendingRestart(host.Name)) {
				t.Fatalf("mismatched evidence permitted repair: err=%v writes=%d post=%d pending=%+v", err, writes, postReads, m.pendingRestart(host.Name))
			}
			if err != nil && strings.Contains(err.Error(), "unexpected") {
				t.Fatalf("fixture gap: %v", err)
			}
			if strings.HasSuffix(mode, "-at-write") && (!boundary || records != 2) {
				t.Fatalf("write-boundary check not reached: boundary=%t records=%d", boundary, records)
			}
			if len(fr.recordedStarts()) != 0 {
				t.Fatal("repair attached or started a process")
			}
		})
	}
}
