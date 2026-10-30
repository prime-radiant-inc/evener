package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// Only service/process discovery is modeled. Directory lookup, account lookup,
// process user and endpoint selection run against the real local OS. No service
// or hub is started, and no network connection is made.
func TestOwnerOSIdentity(t *testing.T) {
	resolver := buildConfigIdentityResolver(t)
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	other := filepath.Join(root, "other")
	alias := filepath.Join(root, "alias")
	spaced := filepath.Join(root, "physical ")
	spacedAlias := filepath.Join(root, "spaced-alias")
	for _, dir := range []string{physical, other, spaced} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(spaced, spacedAlias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(physical, "inner"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(physical, "inner"), filepath.Join(root, "deep-link")); err != nil {
		t.Fatal(err)
	}
	physical, err = filepath.EvalSymlinks(physical)
	if err != nil {
		t.Fatal(err)
	}
	a, err := os.Stat(alias)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(physical)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(a, b) {
		t.Fatal("symlink fixture does not identify the same directory")
	}
	spaced, err = filepath.EvalSymlinks(spaced)
	if err != nil {
		t.Fatal(err)
	}
	a, err = os.Stat(spacedAlias)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(a, b) {
		t.Fatal("trailing-space directory unexpectedly has the same OS identity")
	}
	config := filepath.Join(root, "hub.toml")
	if err := os.WriteFile(config, fmt.Appendf(nil, "addr = \"127.0.0.1:0\"\nhub_state_root = %q\n", filepath.Join(root, "state")), 0600); err != nil {
		t.Fatal(err)
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, cwd, serviceUser     string
		processCwd                 string
		differentDir, differentUID bool
		userScope                  bool
	}{
		{name: "directory-symlink", cwd: alias, serviceUser: account.Username},
		{name: "directory-symlink-dotdot", cwd: root + "/deep-link/..", serviceUser: account.Username},
		{name: "trailing-space-alias", cwd: spacedAlias, processCwd: spaced, serviceUser: account.Username},
		{name: "trailing-space-literal", cwd: spaced, processCwd: spaced, serviceUser: account.Username},
		{name: "trailing-space-collision", cwd: spacedAlias, serviceUser: account.Username, differentDir: true},
		{name: "numeric-user", cwd: physical, serviceUser: account.Uid},
		{name: "combined", cwd: alias, serviceUser: account.Uid},
		{name: "named-user", cwd: physical, serviceUser: account.Username},
		{name: "different-directory", cwd: other, serviceUser: account.Username, differentDir: true},
		{name: "different-uid", cwd: physical, serviceUser: strconv.FormatUint(uid+1, 10), differentUID: true},
		{name: "absent-system-user", cwd: physical, differentUID: uid != 0},
		{name: "absent-user-service-user", cwd: alias, userScope: true},
	} {
		for _, stopped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stopped=%t", tc.name, stopped), func(t *testing.T) {
				processCwd := tc.processCwd
				if processCwd == "" {
					processCwd = physical
				}
				fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
					return nil, fmt.Errorf("unexpected command: %v", argv)
				}}
				fixture := modelSelectedProcess(t, fr, resolver, "old", "new")
				fixture.stopped = stopped
				sup := supervisor{kind: supervisorSystemd, label: "evener-hub.service"}
				if tc.userScope {
					sup.kind = supervisorSystemdUser
				}
				t.Cleanup(func() {
					for _, call := range fr.recordedRuns() {
						remote := strings.Join(call[12:], " ")
						if strings.HasPrefix(remote, "kill ") || strings.Contains(remote, "systemctl start ") || strings.Contains(remote, "systemctl restart ") || strings.Contains(remote, "nohup ") {
							t.Errorf("inspection mutated owner: %s", remote)
						}
					}
				})
				base := fr.runFn
				shell := func(ctx context.Context, remote string) ([]byte, error) {
					cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
					cmd.Dir = root
					cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root}
					out, err := cmd.Output()
					if err != nil {
						return out, fmt.Errorf("real OS command %s: %w", remote, err)
					}
					return out, nil
				}
				fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
					remote := strings.Join(argv[12:], " ")
					pid := "4242"
					if fixture.mutated {
						pid = "4343"
					}
					switch {
					case tc.userScope && remote == systemctlListUnits:
						return nil, nil
					case tc.userScope && remote == systemctlListUnitsUser:
						return []byte("evener-hub.service loaded inactive dead\n"), nil
					case remote == supervisorShowRemote(sup):
						servicePID := pid
						if fixture.stopped {
							servicePID = "0"
						}
						return fmt.Appendf(nil, "Id=evener-hub.service\nMainPID=%s\nUser=%s\nExecStart={ path=%s ; argv[]=%s hub --config %s ; ignore_errors=no ; }\nWorkingDirectory=%s\n", servicePID, tc.serviceUser, resolver, resolver, config, tc.cwd), nil
					case remote == hubArgvRemote(pid):
						return []byte(resolver + "\x00hub\x00--config\x00" + config + "\x00"), nil
					case remote == "readlink /proc/"+pid+"/cwd":
						return []byte(processCwd + "\n"), nil
					case remote == "ps -o user= -p "+pid:
						return shell(ctx, "ps -o user= -p "+strconv.Itoa(os.Getpid()))
					case remote == "id -un", strings.HasPrefix(remote, "id -u "), strings.HasSuffix(remote, "pwd -P"), strings.Contains(remote, " -ef "):
						return shell(ctx, remote)
					case strings.HasPrefix(remote, "getent passwd "), remote == "pwd":
						return []byte(root + "\n"), nil
					case remote == ownerEnvironmentRemote(pid), remote == freshOwnerEnvironmentRemote:
						return []byte("HOME=" + root + "\x00"), nil
					case strings.HasPrefix(remote, "values=$(systemctl --user show-environment)"):
						return []byte("HOME=" + root + "\x00"), nil
					case strings.Contains(remote, " hub endpoint --json"), strings.Contains(remote, "evener_resolve"):
						return shell(ctx, remote)
					}
					return base(ctx, argv, in)
				}
				host := hostreg.Host{Name: "fixture", SSH: account.Username + "@fixture.invalid", EvenerPath: resolver, ConfigPath: config}
				m := newTestManager(t, testRegistry(t, host), fr, Options{})
				report, err := m.resolveHostEndpoint(t.Context(), host, selectedRestartFacts())
				if err != nil {
					t.Fatal(err)
				}
				if stopped {
					prior, err := m.resolveStoppedOwner(t.Context(), host, selectedRestartFacts(), report)
					if err != nil {
						t.Fatal(err)
					}
					if tc.differentUID {
						if prior.supervisor.kind != supervisorNone {
							t.Fatal("different-UID stopped service selected")
						}
						return
					}
					if prior.supervisor != sup || prior.cwd != tc.cwd {
						t.Fatalf("lost stopped service launch intent: %+v", prior)
					}
					fixture.stopped = false
					fixture.mutated = true
					fixture.successorPolls = 1
					now, err := m.resolveLinuxOwner(t.Context(), host, report, "4343", false)
					if tc.differentDir {
						if !errors.Is(err, errOwnerEvidence) {
							t.Fatalf("different directory successor accepted: %v", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					now.report.State = "running"
					if prior.cwdIdentity != processCwd || now.cwdIdentity != processCwd {
						t.Fatalf("pathname bytes changed: prior=%q now=%q want=%q", prior.cwdIdentity, now.cwdIdentity, processCwd)
					}
					now.report.Running = remoteEndpointFixture(config, "127.0.0.1:19371", "successor-boot").Running
					now.report.Running.PID, now.report.Running.Version = 4343, "new"
					if !selectedSuccessor(now, prior, nil, "new", "") {
						t.Fatalf("same OS identity refused as successor: prior=%+v now=%+v", prior, now)
					}
				} else {
					now, err := m.resolveLinuxOwner(t.Context(), host, report, "4242", false)
					if tc.differentDir || tc.differentUID {
						if !errors.Is(err, errOwnerEvidence) {
							t.Fatalf("different owner accepted: %v", err)
						}
					} else if err != nil {
						t.Fatal(err)
					} else if now.cwdIdentity != processCwd {
						t.Fatalf("live pathname bytes changed: got=%q want=%q", now.cwdIdentity, processCwd)
					}
				}
			})
		}
	}
}

func TestProcessOwnerUIDIdentity(t *testing.T) {
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, sshUser string
		matches       bool
	}{
		{"named", account.Username, true},
		{"numeric", account.Uid, true},
		{"ambient", "", true},
		{"different-uid", strconv.FormatUint(uid+1, 10), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := hostreg.Host{Name: "fixture", SSH: "fixture.invalid", User: tc.sshUser}
			fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, _ io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				return exec.CommandContext(ctx, "/bin/sh", "-c", remote).Output()
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			err := m.checkHubOwner(t.Context(), host, strconv.Itoa(os.Getpid()))
			if tc.matches && err != nil || !tc.matches && !errors.Is(err, ErrRestart) {
				t.Fatalf("process UID comparison: matches=%t err=%v", tc.matches, err)
			}
		})
	}
}
