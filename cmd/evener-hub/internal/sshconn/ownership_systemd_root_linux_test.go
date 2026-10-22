//go:build linux

package sshconn

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"primeradiant.com/evener/execsupport/shellquote"
)

// The process, service and endpoint are strict command fixtures. The selected
// lock files, flock and /proc fd device/inode comparison are real Linux evidence.
func TestSystemdFallbackRequiresSelectedRootEvidence(t *testing.T) {
	for _, name := range []string{"retarget-invalid-lock", "retarget-other-lock", "retarget-symlink-lock", "retarget-primary-symlink-lock", "unlocked-read-only", "shared-lock", "lock-query-unavailable", "lock-query-ambiguous", "candidate-holds-selected-lock", "retained-witness", "same-root-invalid-no-witness", "changed-birth-witness", "changed-registration-witness", "healthy-fallback", "primary-lock-path"} {
		t.Run(name, func(t *testing.T) {
			rootA, rootB := filepath.Join(t.TempDir(), "root A"), filepath.Join(t.TempDir(), "root B")
			for _, root := range []string{rootA, rootB} {
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
			}
			retarget := strings.HasPrefix(name, "retarget-")
			initialRoot := rootB
			if retarget {
				initialRoot = rootA
			}
			lock, err := os.OpenFile(filepath.Join(initialRoot, "hub.lock"), os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = lock.Close() })
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			pid := strconv.Itoa(os.Getpid())
			host, commands := ownerCommands(t)
			currentRoot, fallback, effects, vnodeReads := initialRoot, false, 0, 0
			birth := "12500"
			fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, _ io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				report := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "boot-one")
				report.HubStateRoot, report.Running.HubStateRoot, report.Running.PID = currentRoot, currentRoot, os.Getpid()
				if fallback && name != "healthy-fallback" && name != "primary-lock-path" {
					report.State, report.Running = "unavailable", nil
				}
				if remote == selectedLockRemote(currentRoot) {
					if name == "retarget-primary-symlink-lock" {
						return exec.CommandContext(ctx, "/bin/sh", "-c", remote).Output()
					}
					if fallback && name != "primary-lock-path" {
						return nil, errOwnerEvidence
					}
					return []byte(pid + "\n"), nil
				}
				if remote == serviceLockVnodeTestCommand(currentRoot, pid) {
					vnodeReads++
					prefix := ""
					switch name {
					case "lock-query-unavailable":
						prefix = "awk() { return 23; }; "
					case "lock-query-ambiguous":
						// Read the real table twice to inject duplicate owner records.
						prefix = "awk() { command awk \"$@\" /proc/locks; }; "
					}
					return exec.CommandContext(ctx, "/bin/sh", "-c", prefix+remote).CombinedOutput()
				}
				if remote == ownerGenerationRemote(pid) {
					return []byte("kernel-boot\n" + birth + "\n"), nil
				}
				if remote == systemctlListUnits {
					return []byte("evener-hub-selected.service loaded active running\n"), nil
				}
				if remote == systemctlListUnitsUser {
					return nil, nil
				}
				if remote == "systemctl restart evener-hub-selected.service" {
					effects++
					return nil, errReplacementFixture
				}
				if strings.HasPrefix(remote, "kill ") || strings.Contains(remote, "cat >") || strings.Contains(remote, "nohup ") {
					t.Errorf("unexpected mutation: %s", remote)
					return nil, errOwnerEvidence
				}
				for _, cmd := range commands {
					command := strings.ReplaceAll(cmd.command, "4242", pid)
					if command == "canonical" {
						command = resolveDeployCommand("/opt/evener")
					}
					if remote != command {
						continue
					}
					if strings.Contains(remote, "hub endpoint --json") {
						return []byte(mustOwnerJSON(t, report)), nil
					}
					return []byte(strings.ReplaceAll(cmd.output, "4242", pid)), nil
				}
				if remote == "/proc/"+pid+"/exe launch-check --json" || remote == "/opt/evener launch-check --json" {
					return []byte(goodLaunchCheck), nil
				}
				if remote == "systemctl show --property=User,EnvironmentFiles,PassEnvironment,UnsetEnvironment,SetLoginEnvironment -- evener-hub-selected.service" {
					return []byte("User=dev\nEnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=\nSetLoginEnvironment=yes\n"), nil
				}
				if remote == "getent passwd dev | cut -d: -f6" {
					return []byte("/home/dev\n"), nil
				}
				if remote == "values=$(systemctl show --property=Environment --value -- evener-hub-selected.service) || exit; printf '%s\\n' \"$values\" | "+narrowEnvironmentFilter {
					return nil, nil
				}
				t.Errorf("unexpected command: %s", remote)
				return nil, errOwnerEvidence
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			host, _ = m.reg.Get(host.Name)
			facts := selectedRestartFacts()
			prior, err := m.resolveOwnedEndpoint(t.Context(), host, facts)
			if err != nil {
				t.Fatal(err)
			}
			currentRoot, fallback = rootB, true
			if name != "retained-witness" && name != "changed-birth-witness" && name != "changed-registration-witness" && !retarget {
				delete(m.ownerWitnesses, host.Name)
			}
			if name == "changed-birth-witness" {
				birth = "12501"
			}
			if name == "changed-registration-witness" {
				witness := m.ownerWitnesses[host.Name]
				witness.host.Generation++
				m.ownerWitnesses[host.Name] = witness
			}
			if name == "unlocked-read-only" {
				if err := lock.Close(); err != nil {
					t.Fatal(err)
				}
				lock, err = os.Open(filepath.Join(rootB, "hub.lock"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if name == "shared-lock" {
				if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			}
			invalid := name == "retarget-invalid-lock" || name == "retained-witness" || name == "same-root-invalid-no-witness" || strings.HasSuffix(name, "-witness")
			if invalid {
				if !retarget {
					if err := lock.Close(); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(filepath.Join(rootB, "hub.lock")); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Mkdir(filepath.Join(rootB, "hub.lock"), 0700); err != nil {
					t.Fatal(err)
				}
			} else if strings.HasSuffix(name, "symlink-lock") {
				if err := os.Symlink(filepath.Join(rootA, "hub.lock"), filepath.Join(rootB, "hub.lock")); err != nil {
					t.Fatal(err)
				}
			} else if retarget {
				if err := os.WriteFile(filepath.Join(rootB, "hub.lock"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			// This retained intent makes the final stop boundary observable even if
			// the initial fallback now refuses to create a new owner snapshot.
			selected := prior.clone()
			selected.report.HubStateRoot, selected.report.Running = rootB, nil
			pending := m.setSelectedRestart(host, selected, "systemctl restart evener-hub-selected.service", "release-selected", "")
			before := m.pendingRestart(host.Name)
			witness := m.ownerWitnesses[host.Name]
			got, selectionErr := m.resolveOwnedEndpoint(t.Context(), host, facts)
			wantRefusal := retarget || name == "unlocked-read-only" || name == "shared-lock" || strings.HasPrefix(name, "lock-query-") || name == "same-root-invalid-no-witness" || name == "changed-birth-witness" || name == "changed-registration-witness"
			if wantRefusal {
				gateErr := m.restartSelectedPending(t.Context(), host, facts, &pending, true)
				if !errors.Is(selectionErr, errOwnerEvidence) || !errors.Is(gateErr, errOwnerEvidence) || effects != 0 || !reflect.DeepEqual(before, m.pendingRestart(host.Name)) {
					t.Fatalf("root association/stop accepted: selected=%+v selection=%v gate=%v effects=%d pendingPreserved=%v", got, selectionErr, gateErr, effects, reflect.DeepEqual(before, m.pendingRestart(host.Name)))
				}
				if name == "retarget-primary-symlink-lock" && !reflect.DeepEqual(witness, m.ownerWitnesses[host.Name]) {
					t.Fatal("refused root replay changed the verified owner witness")
				}
			} else if selectionErr != nil || got.pid != pid || got.report.HubStateRoot != rootB {
				t.Fatalf("honest association refused: owner=%+v err=%v", got, selectionErr)
			}
			if name == "candidate-holds-selected-lock" || name == "healthy-fallback" {
				if vnodeReads == 0 {
					t.Fatal("candidate's open selected lock was not checked")
				}
			}
			if name == "retained-witness" || name == "primary-lock-path" {
				if vnodeReads != 0 {
					t.Fatalf("existing independent proof unnecessarily probed vnode %d times", vnodeReads)
				}
			}
		})
	}
}

// This literal allowlist is independent of the production command constructor.
func serviceLockVnodeTestCommand(root, pid string) string {
	return "lock=" + shellquote.RemoteWord(filepath.Join(root, "hub.lock")) + "; [ ! -L \"$lock\" ] && [ -f \"$lock\" ] || exit 1; identity=$(stat -c '%d %i' \"$lock\") || exit; found=; [ -r /proc/" + pid + "/fd ] && [ -x /proc/" + pid + "/fd ] || exit 1; for fd in /proc/" + pid + "/fd/*; do current=$(stat -Lc '%d %i' \"$fd\" 2>/dev/null) || continue; if [ \"$current\" = \"$identity\" ]; then found=yes; break; fi; done; [ \"$found\" = yes ] || exit 1; set -- $identity; [ \"$#\" -eq 2 ] || exit 1; dev=$1; ino=$2; major=$(( ((dev >> 8) & 4095) | ((dev >> 32) & 4294963200) )); minor=$(( (dev & 255) | ((dev >> 12) & 4294967040) )); key=$(printf '%02x:%02x:%s' \"$major\" \"$minor\" \"$ino\"); awk -v key=\"$key\" -v pid=" + pid + " '$2 == \"FLOCK\" && $3 == \"ADVISORY\" && $4 == \"WRITE\" && $6 == key { count++; owner=$5 } END { if (count != 1 || owner != pid) exit 1 }' /proc/locks || exit; [ ! -L \"$lock\" ] && [ -f \"$lock\" ] || exit 1; current=$(stat -c '%d %i' \"$lock\") || exit; [ \"$current\" = \"$identity\" ] || exit 1; printf 'match\\n'"
}
