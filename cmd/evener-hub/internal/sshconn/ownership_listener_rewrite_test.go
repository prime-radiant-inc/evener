package sshconn

import (
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/hubendpoint"
)

// Supervisor, process and reports are independent strict command fixtures. The
// manager must associate the rewritten live argv, then restart the original unit.
func TestSupervisedInPlaceListenerRewrite(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		for _, tc := range []struct {
			name, definition, live string
			refuse                 bool
		}{
			{"short-space", "-addr 127.0.0.1:0", "-addr 127.0.0.1:19371", false},
			{"long-space", "--addr 127.0.0.1:0", "--addr 127.0.0.1:19371", false},
			{"short-equals", "-addr=127.0.0.1:0", "-addr=127.0.0.1:19371", false},
			{"long-equals", "--addr=127.0.0.1:0", "--addr=127.0.0.1:19371", false},
			{"long-equals-empty-port", "--addr=127.0.0.1:", "--addr=127.0.0.1:19371", false},
			{"config-ephemeral", "", "-addr 127.0.0.1:19371", false},
			{"env-ephemeral", "", "-addr 127.0.0.1:19371", false},
			{"env-missing", "", "-addr 127.0.0.1:19371", true},
			{"env-fixed", "", "-addr 127.0.0.1:19371", true},
			{"env-bound-mismatch", "", "-addr 127.0.0.1:19372", true},
			{"env-second-listener", "", "-addr 127.0.0.1:19371 -addr 127.0.0.1:19371", true},
			{"env-service-only", "", "-addr 127.0.0.1:19371", false},
			{"env-service-only-no-captured-env", "", "-addr 127.0.0.1:19371", true},
			{"env-service-only-bound-missing", "", "-addr 127.0.0.1:19371", true},
			{"env-service-only-bound-mismatch", "", "-addr 127.0.0.1:19372", true},
			{"env-service-only-second-listener", "", "-addr 127.0.0.1:19371 -addr 127.0.0.1:19371", true},
			{"env-service-only-non-listener-change", "", "-addr 127.0.0.1:19371 --public-url=https://other.example", true},
			{"unresponsive-flag", "-addr 127.0.0.1:0", "-addr 127.0.0.1:19371", false},
			{"unresponsive-config", "", "-addr 127.0.0.1:19371", false},
			{"unresponsive-no-witness", "-addr 127.0.0.1:0", "-addr 127.0.0.1:19371", false},
			{"unresponsive-wrong-generation", "-addr 127.0.0.1:0", "-addr 127.0.0.1:19371", false},
			{"unresponsive-wrong-registration", "-addr 127.0.0.1:0", "-addr 127.0.0.1:19371", false},
			{"unresponsive-wrong-bound", "-addr 127.0.0.1:0", "-addr 127.0.0.1:19371", false},
			{"fixed-exact", "-addr 127.0.0.1:19371", "-addr 127.0.0.1:19371", false},
			{"non-listener-change", "-addr 127.0.0.1:0", "-addr 127.0.0.1:19371 --public-url=https://other.example", true},
			{"fixed-change", "-addr 127.0.0.1:9180", "-addr 127.0.0.1:19371", true},
			{"bound-mismatch", "-addr 127.0.0.1:0", "-addr 127.0.0.1:19372", true},
			{"bound-unavailable", "-addr 127.0.0.1:0", "-addr 127.0.0.1:19371", true},
			{"fixed-config", "", "-addr 127.0.0.1:19371", true},
			{"wrong-appended-flag", "", "--addr 127.0.0.1:19371", true},
			{"duplicate-listener", "-addr 127.0.0.1:0", "-addr 127.0.0.1:19371 -addr 127.0.0.1:19371", true},
		} {
			t.Run(platform+"/"+tc.name, func(t *testing.T) {
				host, _ := ownerCommands(t)
				prefix := []string{"/opt/evener", "hub", "--config=/srv/selected.toml"}
				definition := append(append([]string(nil), prefix...), strings.Fields(tc.definition)...)
				live := append(append([]string(nil), prefix...), strings.Fields(tc.live)...)
				facts := selectedRestartFacts()
				facts.OS, facts.UID = platform, "1000"
				mutated, effects, writes := false, 0, 0
				unresponsive := false
				report := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "boot-one")
				ownerEnv := []string{"HOME=/home/dev"}
				envAddr := ""
				serviceOnly := strings.HasPrefix(tc.name, "env-service-only")
				if strings.HasPrefix(tc.name, "env-") {
					report.ListenSource, report.ConfiguredListenAddr = "env", "127.0.0.1:0"
					report.Running.ListenSource, report.Running.RequestedListenAddr = "flag", "127.0.0.1:19371"
					if tc.name != "env-missing" && tc.name != "env-service-only-no-captured-env" {
						envAddr = "127.0.0.1:0"
						if tc.name == "env-fixed" {
							envAddr = "127.0.0.1:9180"
						}
						ownerEnv = append(ownerEnv, "EVENER_HUB_LISTEN_ADDR="+envAddr)
						slices.Sort(ownerEnv)
					}
				}
				if serviceOnly {
					// Inspection runs under SSH login HOME with no listener env.
					// The rewritten process record reports its appended flag instead.
					report.ListenSource, report.ConfiguredListenAddr = "default", "127.0.0.1:9180"
					if tc.name == "env-service-only-bound-missing" {
						report.Running.BoundAddr = ""
					}
				}
				if tc.name == "fixed-config" {
					report.ConfiguredListenAddr = "127.0.0.1:9180"
					report.Running.RequestedListenAddr = "127.0.0.1:9180"
				}
				if tc.name == "bound-unavailable" {
					report.State, report.Running = "unavailable", nil
				}
				commands := identityOwnerCommands(t, platform, false)
				fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
					remote := strings.Join(argv[12:], " ")
					current := live
					currentReport := report
					if unresponsive {
						currentReport.State, currentReport.Running = "unavailable", nil
					}
					if mutated {
						current = definition // The manager starts the original supervisor argv.
						currentReport = remoteEndpointFixture(host.ConfigPath, "127.0.0.1:24519", "boot-two")
						currentReport.Running.PID, currentReport.Running.Version = 4343, "release-selected"
						if strings.HasPrefix(tc.name, "env-") {
							currentReport.ListenSource, currentReport.ConfiguredListenAddr = "env", envAddr
							currentReport.Running.ListenSource, currentReport.Running.RequestedListenAddr = "env", envAddr
							if serviceOnly {
								currentReport.ListenSource, currentReport.ConfiguredListenAddr = "default", "127.0.0.1:9180"
							}
						}
						if tc.name == "fixed-exact" {
							currentReport = remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "boot-two")
							currentReport.Running.PID, currentReport.Running.Version = 4343, "release-selected"
						}
					}
					pid := "4242"
					if mutated {
						pid = "4343"
					}
					if strings.Contains(remote, "cat >") || strings.Contains(remote, "nohup ") || strings.HasPrefix(remote, "kill -- ") {
						writes++
						return nil, errOwnerEvidence
					}
					mutation := identityRestartCommand(platform).command
					if remote == mutation {
						effects++
						mutated = true
						return nil, nil
					}
					if remote == selectedLockRemote("/srv/state") {
						return []byte(pid + "\n"), nil
					}
					if remote == ownerGenerationRemote(pid) {
						ticks := "12500"
						if mutated {
							ticks = "13500"
						}
						return []byte("kernel-boot\n" + ticks + "\n"), nil
					}
					if remote == "if [ -r /proc/"+pid+"/cmdline ]; then cat /proc/"+pid+"/cmdline; fi" {
						return []byte(strings.Join(current, "\x00") + "\x00"), nil
					}
					if remote == hubArgvRemote(pid) {
						return nil, nil
					}
					if remote == "ps -p "+pid+" -ww -o command=" {
						return []byte(strings.Join(current, " ") + "\n"), nil
					}
					if remote == ownerEnvironmentRemote(pid) || remote == darwinEnvironmentRemote(pid) {
						return []byte(strings.Join(ownerEnv, "\x00") + "\x00"), nil
					}
					for _, args := range [][]string{live, definition} {
						endpoint := []string{"/opt/evener", "hub", "endpoint", "--json", "--config", "/srv/selected.toml"}
						if addr, ok := hubAddrFlag(args); ok {
							endpoint = append(endpoint, "--addr", addr)
						}
						for i := range endpoint {
							endpoint[i] = shellquote.RemoteWord(endpoint[i])
						}
						if remote == ownerContext("/srv", ownerEnv)+strings.Join(endpoint, " ") {
							return []byte(mustOwnerJSON(t, currentReport)), nil
						}
					}
					if remote == "/opt/evener launch-check --json" {
						return launchContract("release-selected"), nil
					}
					if remote == "systemctl show --property=User,EnvironmentFiles,PassEnvironment,UnsetEnvironment,SetLoginEnvironment -- evener-hub-selected.service" {
						return []byte("User=dev\nEnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=\nSetLoginEnvironment=yes\n"), nil
					}
					if remote == "getent passwd dev | cut -d: -f6" {
						return []byte("/home/dev\n"), nil
					}
					if remote == "values=$(systemctl show --property=Environment --value -- evener-hub-selected.service) || exit; printf '%s\\n' \"$values\" | "+narrowEnvironmentFilter {
						if envAddr != "" {
							return []byte("EVENER_HUB_LISTEN_ADDR=" + envAddr + "\x00"), nil
						}
						return nil, nil
					}
					if mutated {
						commands = identitySuccessorCommands(t, platform, false, false)
					}
					for _, cmd := range commands {
						command := cmd.command
						if command == "canonical" {
							command = resolveDeployCommand("/opt/evener")
						}
						if command != remote {
							continue
						}
						out := cmd.output
						if remote == "/opt/evener hub endpoint --json --config /srv/selected.toml" {
							return []byte(mustOwnerJSON(t, currentReport)), nil
						}
						if strings.HasPrefix(remote, "systemctl show --property=Id,") && strings.Contains(out, "Id=evener-hub-selected.service\n") {
							out = strings.ReplaceAll(out, "--addr=[::]:0", tc.definition)
							out = strings.ReplaceAll(out, "selected.toml  ;", "selected.toml ;")
						}
						if strings.HasPrefix(remote, "launchctl print ") {
							out = strings.ReplaceAll(out, "--addr=[::]:0\n", strings.Join(strings.Fields(tc.definition), "\n")+"\n")
							out = strings.ReplaceAll(out, "selected.toml\n\n}", "selected.toml\n}")
							if envAddr != "" {
								out = strings.ReplaceAll(out, "HOME => /home/dev", "HOME => /home/dev\nEVENER_HUB_LISTEN_ADDR => "+envAddr)
							}
						}
						return []byte(out), nil
					}
					t.Errorf("unexpected command: %s", remote)
					return nil, errOwnerEvidence
				}}
				m := newTestManager(t, testRegistry(t, host), fr, Options{sleep: func(context.Context, time.Duration) error { return nil }})
				host, _ = m.reg.Get(host.Name)
				owner, err := m.resolveOwnedEndpoint(t.Context(), host, facts)
				if tc.refuse {
					wantErr := errOwnerEvidence
					if tc.name == "env-service-only-bound-missing" {
						wantErr = ErrPreflightDecode
					}
					if !errors.Is(err, wantErr) || effects != 0 || writes != 0 || m.pendingRestart(host.Name).active() {
						t.Fatalf("invalid rewrite accepted: owner=%+v err=%v effects=%d writes=%d", owner, err, effects, writes)
					}
					return
				}
				if err != nil || !reflect.DeepEqual(owner.argv, live) || owner.supervisor.kind == supervisorNone {
					t.Fatalf("valid live association refused or argv changed: owner=%+v err=%v", owner, err)
				}
				if serviceOnly && (!slices.Equal(owner.env, ownerEnv) || owner.report.ListenSource != "default" || owner.report.Running.ListenSource != "flag") {
					t.Fatalf("service-only provenance not independently captured: env=%q report=%+v", owner.env, owner.report)
				}
				pending, err := m.prepareSelectedRestart(t.Context(), host, facts, owner, "release-selected", "")
				if err != nil {
					t.Fatal(err)
				}
				pendingSnapshot := m.pendingRestart(host.Name)
				if strings.HasPrefix(tc.name, "unresponsive-") {
					unresponsive = true
					witness := m.ownerWitnesses[host.Name].clone()
					switch tc.name {
					case "unresponsive-no-witness":
						delete(m.ownerWitnesses, host.Name)
					case "unresponsive-wrong-generation":
						witness.generation = "other-birth"
						m.ownerWitnesses[host.Name] = witness
					case "unresponsive-wrong-registration":
						witness.host.Generation++
						m.ownerWitnesses[host.Name] = witness
					case "unresponsive-wrong-bound":
						witness.report.Running.BoundAddr = "127.0.0.1:19372"
						m.ownerWitnesses[host.Name] = witness
					}
				}
				if strings.HasPrefix(tc.name, "unresponsive-wrong-") || tc.name == "unresponsive-no-witness" {
					err := m.restartSelectedPending(t.Context(), host, facts, &pending, true)
					if !errors.Is(err, errOwnerEvidence) || effects != 0 || writes != 0 || !reflect.DeepEqual(pendingSnapshot, m.pendingRestart(host.Name)) {
						t.Fatalf("invalid witness accepted: err=%v effects=%d writes=%d", err, effects, writes)
					}
					return
				}
				if err := m.restartSelectedPending(t.Context(), host, facts, &pending, true); err != nil {
					t.Fatalf("following restart: %v, retained=%+v", err, pendingSnapshot)
				}
				if effects != 1 || writes != 0 || !mutated || m.pendingRestart(host.Name).active() || len(fr.recordedStarts()) != 0 {
					t.Fatalf("restart did not settle: effects=%d writes=%d pending=%+v", effects, writes, m.pendingRestart(host.Name))
				}
			})
		}
	}
}

func TestSupervisorArgvCapturedEnvironmentRewrite(t *testing.T) {
	for _, tc := range []struct {
		name, env, source string
		want              bool
	}{
		{"zero-port", "EVENER_HUB_LISTEN_ADDR=127.0.0.1:0", "default", true},
		{"empty-port", "EVENER_HUB_LISTEN_ADDR=127.0.0.1:", "default", true},
		{"inspection-flag", "EVENER_HUB_LISTEN_ADDR=127.0.0.1:0", "flag", true},
		{"missing-captured-env", "HOME=/home/dev", "default", false},
		{"fixed-port", "EVENER_HUB_LISTEN_ADDR=127.0.0.1:9180", "default", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition := []string{"/opt/evener", "hub", "--config=/srv/selected.toml"}
			live := append(slices.Clone(definition), "-addr", "127.0.0.1:19371")
			report := remoteEndpointFixture("/srv/selected.toml", "127.0.0.1:19371", "boot-one")
			report.ListenSource, report.ConfiguredListenAddr = tc.source, "127.0.0.1:9180"
			report.Running.ListenSource, report.Running.RequestedListenAddr = "flag", "127.0.0.1:19371"
			if got := supervisorArgvMatches(live, definition, report, []string{tc.env}); got != tc.want {
				t.Fatalf("captured environment rewrite match=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestSupervisorArgvRewriteLimits(t *testing.T) {
	for _, tc := range []struct {
		name, definition, live, bound string
		want                          bool
	}{
		{"ipv6", "--addr=[::]:0", "--addr=[::]:19371", "[::]:19371", true},
		{"missing-bound", "-addr :0", "-addr :19371", "", false},
		{"ephemeral-bound", "-addr :0", "-addr :0", ":0", true}, // Exact argv retains its existing behavior.
		{"invalid-bound", "-addr :0", "-addr nonsense", "nonsense", false},
		{"changed-flag", "--addr :0", "-addr :19371", ":19371", false},
		{"changed-form", "--addr=:0", "--addr :19371", ":19371", false},
		{"missing-value", "-addr", "-addr :19371", ":19371", false},
		{"two-definition-flags", "-addr :0 --addr :0", "-addr :19371 --addr :19371", ":19371", false},
		{"changed-config", "--config=a -addr :0", "--config=b -addr :19371", ":19371", false},
		{"not-appended", "--config=a", "-addr :19371 --config=a", ":19371", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition := append([]string{"/opt/evener", "hub"}, strings.Fields(tc.definition)...)
			live := append([]string{"/opt/evener", "hub"}, strings.Fields(tc.live)...)
			report := remoteEndpointFixture("/srv/selected.toml", tc.bound, "boot-one")
			beforeDefinition, beforeLive := slices.Clone(definition), slices.Clone(live)
			beforeReport := mustOwnerJSON(t, report)
			if got := supervisorArgvMatches(live, definition, report, nil); got != tc.want {
				t.Fatalf("rewrite match=%v, want %v", got, tc.want)
			}
			if !slices.Equal(definition, beforeDefinition) || !slices.Equal(live, beforeLive) || mustOwnerJSON(t, report) != beforeReport {
				t.Fatal("rewrite comparison mutated its inputs")
			}
		})
	}
}

func TestSupervisedLaunchIdentityAfterListenerRewrite(t *testing.T) {
	prior := selectedOwner{
		pid: "4242", generation: "birth-one", supervisor: supervisor{kind: supervisorSystemd, label: "evener-hub.service"},
		argv: []string{"/opt/evener", "hub", "-addr", ":19371"}, launchArgv: []string{"/opt/evener", "hub", "-addr", ":0"},
		report: hubendpoint.Report{ConfigPath: "/srv/selected.toml", HubStateRoot: "/srv/state"},
	}
	next := prior.clone()
	next.pid, next.generation = "4343", "birth-two"
	next.argv = slices.Clone(prior.launchArgv)
	if !sameLaunch(next, prior) {
		t.Fatal("successor's original supervisor argv did not match launch intent")
	}
	next.pid, next.generation = prior.pid, prior.generation
	if sameLaunch(next, prior) {
		t.Fatal("same-process argv change accepted")
	}
	next.pid, next.generation = "4343", "birth-two"
	next.launchArgv[3] = ":9180"
	if prior.launchArgv[3] != ":0" || sameLaunch(next, prior) {
		t.Fatal("launch argv aliased the snapshot or changed launch intent was accepted")
	}
	next = prior.clone()
	next.pid, next.generation, next.launchArgv = "4343", "birth-two", nil
	next.argv = slices.Clone(prior.launchArgv)
	if sameLaunch(next, prior) {
		t.Fatal("missing launch evidence relaxed argv identity")
	}
	prior.supervisor, next.supervisor = supervisor{}, supervisor{}
	next.launchArgv = slices.Clone(prior.launchArgv)
	if sameLaunch(next, prior) {
		t.Fatal("ad-hoc argv identity was relaxed")
	}
}
