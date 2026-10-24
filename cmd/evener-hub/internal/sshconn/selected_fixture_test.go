package sshconn

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/execsupport/shellquote"
)

// selectedProcessFixture models the external process/service boundary for the
// older Ensure/recorder tests. It does NOT turn endpoint commands into health
// commands or call Manager internals. Its declared predecessor and successor
// are independent of those tests' legacy health fixture. New ownership/security
// tests use ordered command lists rather than this shared model.
type selectedProcessFixture struct {
	exe, before, after, sha       string
	mutated, stopped, unavailable bool
	endpointPolls                 int
	adhoc, darwin, noSystemctl    bool
	dormant, unsafeLabel          bool
	delay, successorPolls         int
	advisory                      bool
	endpointAlias                 string
	launchAddr                    string
	observe                       func(*selectedProcessFixture)
	reportedStart                 *time.Time
	initialUnavailable            bool
	executableAbsent              bool
}

func modelSelectedProcess(t *testing.T, fr *fakeRunner, exe, before, after string) *selectedProcessFixture {
	t.Helper()
	f := &selectedProcessFixture{exe: exe, before: before, after: after}
	base := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
		remote := strings.Join(argv[12:], " ")
		if f.observe != nil && (remote == f.exe+" hub endpoint --json" || (f.endpointAlias != "" && remote == f.endpointAlias)) {
			f.observe(f)
		}
		pid := "4242"
		ticks := "12500"
		boot := "boot-one"
		version := f.before
		if f.mutated {
			if remote == f.exe+" hub endpoint --json" || (f.endpointAlias != "" && remote == f.endpointAlias) {
				f.successorPolls++
			}
		}
		if f.mutated && f.successorPolls > f.delay {
			pid = "4343"
			ticks = "13500"
			boot = "boot-two"
			version = f.after
		}
		config := "/home/dev/.config/evener/hub.toml"
		root := "/home/dev/.local/state/evener"
		endpoint := f.exe + " hub endpoint --json"
		contextEndpoint := "CDPATH='' cd -P -- /home/dev || exit; unset HOME XDG_CONFIG_HOME XDG_STATE_HOME EVENER_HUB_LISTEN_ADDR; export HOME=/home/dev; " + endpoint
		launchArgv := []string{f.exe, "hub"}
		if f.launchAddr != "" {
			contextEndpoint += " --addr " + shellquote.RemoteWord(f.launchAddr)
			launchArgv = append(launchArgv, "--addr", f.launchAddr)
		}
		report := remoteEndpointFixture(config, "127.0.0.1:9180", boot)
		report.HubStateRoot, report.Running.HubStateRoot = root, root
		report.Running.PID, _ = strconv.Atoi(pid)
		report.Running.Version, report.Running.BackendGitSHA = version, f.sha
		if f.reportedStart != nil {
			report.Running.StartedAt = *f.reportedStart
		}
		if f.launchAddr != "" {
			report.Running.RequestedListenAddr, report.Running.ListenSource = f.launchAddr, "flag"
		}
		if f.stopped {
			report.State, report.Running = "stopped", nil
		}
		if f.initialUnavailable || (f.unavailable && f.mutated) {
			report.State, report.Reason, report.Running = "unavailable", "unreachable", nil
		}
		switch remote {
		case endpoint, contextEndpoint, ownerContext("/home/dev", []string{"HOME=/home/dev"}) + "/proc/" + pid + "/exe hub endpoint --json":
			if f.executableAbsent && (remote == endpoint || remote == contextEndpoint) {
				return nil, exitStatus(t, 127)
			}
			f.endpointPolls++
			return json.Marshal(report)
		case selectedLockRemote(root):
			if f.stopped {
				return nil, nil
			}
			return []byte(pid + "\n"), nil
		case ownerGenerationRemote(pid):
			return []byte("kernel-boot\n" + ticks + "\n"), nil
		case "id -un", "ps -o user= -p " + pid:
			return []byte("dev\n"), nil
		case hubArgvRemote(pid):
			return []byte(strings.Join(launchArgv, "\x00") + "\x00"), nil
		case "readlink /proc/" + pid + "/cwd":
			return []byte("/home/dev\n"), nil
		case ownerEnvironmentRemote(pid):
			return []byte("HOME=/home/dev\x00"), nil
		case "pwd", "CDPATH='' cd -P -- /home/dev && pwd -P":
			return []byte("/home/dev\n"), nil
		case freshOwnerEnvironmentRemote:
			return []byte("HOME=/home/dev\x00"), nil
		case "cat /proc/" + pid + "/cgroup":
			return []byte("0::/user.slice/session.scope\n"), nil
		case "systemctl list-units --type=service --all --no-legend --plain":
			if f.noSystemctl {
				return []byte("systemctl: not found\n"), errReplacementFixture
			}
			if f.adhoc && !f.dormant {
				return nil, nil
			}
			status := "active running"
			if f.stopped || f.dormant {
				status = "inactive dead"
			}
			return []byte("evener-hub.service loaded " + status + "\n"), nil
		case "systemctl --user list-units --type=service --all --no-legend --plain":
			if f.noSystemctl {
				return []byte("systemctl: not found\n"), errReplacementFixture
			}
			return nil, nil
		case "systemctl show --property=Id,MainPID,User,ExecStart,WorkingDirectory -- evener-hub.service", "systemctl --user show --property=Id,MainPID,User,ExecStart,WorkingDirectory -- evener-hub.service":
			if f.stopped || f.dormant {
				pid = "0"
			}
			return []byte("Id=evener-hub.service\nMainPID=" + pid + "\nUser=dev\nExecStart={ path=" + f.exe + " ; argv[]=" + f.exe + " hub ; ignore_errors=no ; }\nWorkingDirectory=/home/dev\n"), nil
		case serviceDefinitionRemote(supervisor{kind: supervisorSystemd, label: "evener-hub.service"}):
			return []byte("1234 567\n"), nil
		case serviceDefinitionRemote(supervisor{kind: supervisorSystemdUser, label: "evener-hub.service"}):
			return []byte("1234 567\n"), nil
		case "/proc/" + pid + "/exe launch-check --json":
			return []byte(goodLaunchCheck), nil
		case "command -v evener", "command -v evener || echo __sshconn_no_evener__":
			return []byte(f.exe + "\n"), nil
		case "getent passwd dev | cut -d: -f6":
			return []byte("/home/dev\n"), nil
		case "systemctl show --property=User,EnvironmentFiles,PassEnvironment,UnsetEnvironment,SetLoginEnvironment -- evener-hub.service":
			return []byte("User=dev\nEnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=\nSetLoginEnvironment=yes\n"), nil
		case "systemctl --user show --property=User,EnvironmentFiles,PassEnvironment,UnsetEnvironment,SetLoginEnvironment -- evener-hub.service":
			return []byte("User=dev\nEnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=\nSetLoginEnvironment=yes\n"), nil
		case "values=$(systemctl show --property=Environment --value -- evener-hub.service) || exit; printf '%s\\n' \"$values\" | " + narrowEnvironmentFilter:
			return nil, nil
		case "values=$(systemctl --user show --property=Environment --value -- evener-hub.service) || exit; printf '%s\\n' \"$values\" | " + narrowEnvironmentFilter:
			return nil, nil
		case "values=$(systemctl --user show-environment) || exit; printf '%s\\n' \"$values\" | " + narrowEnvironmentFilter:
			return []byte("HOME=/home/dev\x00"), nil
		case "launchctl list":
			if f.adhoc {
				if f.unsafeLabel {
					return []byte("1234\t0\tcom.example/evener-hub\n"), nil
				}
				return nil, nil
			}
			return []byte(pid + "\t0\tcom.example.evener-hub\n"), nil
		case launchdDefinitionRemote("1000", "com.example.evener-hub"):
			if f.stopped {
				pid = "0"
			}
			return []byte("pid = " + pid + "\nprogram = " + f.exe + "\nworking directory = /home/dev\narguments = {\n" + f.exe + "\nhub\n}\nHOME => /home/dev\n"), nil
		case "ps -p " + pid + " -o lstart=":
			return []byte("Fri Oct 16 10:00:" + ticks + " 2026\n"), nil
		case "lsof -a -p " + pid + " -d cwd -Fn":
			return []byte("p" + pid + "\nfcwd\nn/home/dev\n"), nil
		case "stat -f '%d %i' " + shellquote.RemoteWord(f.exe):
			return []byte("1234 567\n"), nil
		case "lsof -a -p " + pid + " -d txt -F Di":
			return []byte("p" + pid + "\nD0x4d2\ni567\n"), nil
		case darwinEnvironmentRemote(pid):
			return []byte("HOME=/home/dev\x00"), nil
		}
		if f.endpointAlias != "" && remote == f.endpointAlias {
			f.endpointPolls++
			return json.Marshal(report)
		}
		// Canonicalization is exact, not a substring that could make another
		// executable match. Deploy's independent path probes still reach base.
		if remote == resolveDeployCommand(f.exe) {
			if f.executableAbsent {
				return nil, exitStatus(t, 1)
			}
			return []byte(f.exe + "\n"), nil
		}
		if remote == createTargetCommand(f.exe) && f.executableAbsent {
			return []byte(f.exe + "\n"), nil // The recorded target's parent still exists.
		}
		if remote == "kill -- 4242" && f.adhoc {
			out, err := base(ctx, argv, in)
			if err == nil {
				f.stopped = true
			}
			return out, err
		}
		relaunch := "CDPATH='' cd -P -- /home/dev || exit; unset HOME XDG_CONFIG_HOME XDG_STATE_HOME EVENER_HUB_LISTEN_ADDR; export HOME=/home/dev; " + relaunchCommand(launchArgv, "")
		if remote == "systemctl start evener-hub.service" || remote == "systemctl restart evener-hub.service" || remote == "launchctl kickstart -k gui/1000/com.example.evener-hub" || remote == "launchctl kickstart gui/1000/com.example.evener-hub" || (f.adhoc && remote == relaunch) {
			out, err := base(ctx, argv, in)
			if err == nil || f.advisory {
				f.mutated = true
				f.stopped = false
			}
			return out, err
		}
		return base(ctx, argv, in)
	}
	return f
}
