package sshconn

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// launchctl, ps and lsof are strict Darwin command fixtures, not Darwin runtime.
func TestDarwinUnavailableRootEvidence(t *testing.T) {
	for _, name := range []string{"config-retarget-other-lock", "regular-open-lock", "retained-witness", "changed-witness-generation", "changed-witness-registration", "changed-witness-root", "symlink-lock", "directory-lock", "changed-lock", "missing-inventory"} {
		t.Run(name, func(t *testing.T) {
			host, _ := ownerCommands(t)
			facts := selectedRestartFacts()
			facts.OS, facts.UID = "darwin", "1000"
			healthy := true
			effects, rootReads := 0, 0
			stat := "test ! -L /srv/state/hub.lock && test -f /srv/state/hub.lock && stat -f '%d %i' /srv/state/hub.lock"
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if remote == identityRestartCommand("darwin").command {
					effects++
					return nil, errors.New("unexpected restart")
				}
				if remote == stat {
					rootReads++
					if name == "symlink-lock" || name == "directory-lock" {
						return nil, errors.New("not a regular nonsymlink lock")
					}
					if name == "changed-lock" && rootReads%2 == 0 {
						return []byte("16777234 778\n"), nil
					}
					return []byte("16777234 777\n"), nil
				}
				if remote == "lsof -a -p 4242 -F pftDi" {
					if name == "missing-inventory" {
						return nil, errors.New("inventory unavailable")
					}
					inode := "778"
					if name == "regular-open-lock" || name == "changed-lock" {
						inode = "777"
					}
					return []byte("p4242\nf3\ntREG\nD0x1000012\ni" + inode + "\n"), nil
				}
				if remote == "stat -f '%d %i' /opt/evener" {
					return []byte("16777234 12345\n"), nil
				}
				if remote == "lsof -a -p 4242 -d txt -F Di" {
					return []byte("p4242\nftxt\nD0x1000012\ni12345\n"), nil
				}
				if remote == "/opt/evener launch-check --json" || remote == "/opt/evener launch-check --protocol evener-appwire-v7 --json" {
					return []byte(goodLaunchCheck), nil
				}
				if remote == "if kill -s 0 -- 4242 2>/dev/null; then echo __sshconn_pid_alive__; else echo __sshconn_pid_gone__; fi" {
					return []byte("__sshconn_pid_alive__\n"), nil
				}
				for _, c := range darwinOwnerCommands(t, !healthy, false) {
					command := c.command
					if command == "canonical" {
						command = resolveDeployCommand("/opt/evener")
					}
					if remote == command {
						if !healthy && name == "changed-witness-generation" && strings.Contains(remote, "-o lstart=") {
							return []byte("Fri Oct 16 11:00:00 2026\n"), nil
						}
						out := c.output
						if healthy && name == "config-retarget-other-lock" {
							out = strings.ReplaceAll(out, "/srv/state", "/srv/root-a")
						}
						return []byte(out), nil
					}
				}
				t.Errorf("unexpected command: %s", remote)
				return nil, errOwnerEvidence
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			host, _ = m.reg.Get(host.Name)
			owner, err := m.resolveOwnedEndpoint(t.Context(), host, facts)
			if err != nil {
				t.Fatal(err)
			}
			m.setSelectedRestart(host, owner, selectedRelaunch(owner), "release-selected", "")
			before := m.pendingRestart(host.Name)
			healthy = false
			if name == "changed-witness-registration" || name == "changed-witness-root" {
				witness := m.ownerWitnesses[host.Name].clone()
				if name == "changed-witness-registration" {
					witness.host.Generation++
				} else {
					witness.report.HubStateRoot = "/srv/other-root"
				}
				m.ownerWitnesses[host.Name] = witness
			} else if name != "retained-witness" && name != "changed-witness-generation" {
				m.ownerWitnesses = nil
			}
			_, err = m.resolveOwnedEndpoint(t.Context(), host, facts)
			accept := name == "regular-open-lock" || name == "retained-witness"
			if accept && err != nil || !accept && !errors.Is(err, errOwnerEvidence) {
				t.Fatalf("root association: %v, accept=%v", err, accept)
			}
			if !accept {
				if _, err := m.RestartForOperation(t.Context(), host, facts, nil); !errors.Is(err, errOwnerEvidence) {
					t.Fatalf("unproven root restart: %v", err)
				}
			}
			if effects != 0 || !reflect.DeepEqual(before, m.pendingRestart(host.Name)) {
				t.Fatal("root inspection mutated pending intent or service")
			}
			if name == "retained-witness" && rootReads != 0 {
				t.Fatal("verified witness did not independently corroborate root")
			}
		})
	}
}

func TestDarwinOpenRootVnodeInventory(t *testing.T) {
	valid := "p4242\nf3\ntREG\nD0x1000012\ni777\n"
	for _, tc := range []struct {
		name, inventory string
		want            bool
	}{
		{"regular", valid, true},
		{"other-descriptors", "p4242\nf0\ntCHR\nf1\ntPIPE\nf3\ntREG\nD0x1000012\ni777\n", true},
		{"other-process", strings.ReplaceAll(valid, "p4242", "p4343"), false},
		{"missing-process", strings.TrimPrefix(valid, "p4242\n"), false},
		{"second-process", valid + "p4343\n", false},
		{"mapped-only", strings.ReplaceAll(valid, "f3", "ftxt"), false},
		{"directory", strings.ReplaceAll(valid, "tREG", "tDIR"), false},
		{"other-device", strings.ReplaceAll(valid, "D0x1000012", "D0x1000013"), false},
		{"other-inode", strings.ReplaceAll(valid, "i777", "i778"), false},
		{"bad-device", strings.ReplaceAll(valid, "D0x1000012", "Dinvalid"), false},
		{"missing-inode", strings.ReplaceAll(valid, "i777\n", ""), false},
		{"duplicate-inode", valid + "i777\n", false},
		{"unknown-field", valid + "k1\n", false},
		{"no-cross-record-fields", "p4242\nf3\ntREG\nD0x1000012\ni778\nf4\ntREG\ni777\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := darwinOpenVnode([]byte(tc.inventory), "4242", 16777234, "777"); got != tc.want {
				t.Fatalf("open root vnode=%v, want %v", got, tc.want)
			}
		})
	}
}
