package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/execsupport/shellquote"
)

// launchctl/ps/lsof remain ordered command fixtures on Linux. The directories
// and the generated physical-directory lookup run against the actual filesystem.
func TestDarwinOwnerDirectoryIdentity(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	other := filepath.Join(root, "other")
	alias := filepath.Join(root, "alias")
	spaced := filepath.Join(root, "physical ")
	spacedAlias := filepath.Join(root, "spaced-alias")
	for _, p := range []string{physical, other, spaced} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(spaced, spacedAlias); err != nil {
		t.Fatal(err)
	}
	var err error
	physical, err = filepath.EvalSymlinks(physical)
	if err != nil {
		t.Fatal(err)
	}
	spaced, err = filepath.EvalSymlinks(spaced)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, configured, actual string
		different                bool
	}{
		{"alias", alias, physical, false},
		{"different-directory", other, physical, true},
		{"trailing-space-alias", spacedAlias, spaced, false},
		{"trailing-space-literal", spaced, spaced, false},
		{"trailing-space-collision", spacedAlias, physical, true},
	} {
		for _, stopped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stopped=%t", tc.name, stopped), func(t *testing.T) {
				host, _ := ownerCommands(t)
				configured := tc.configured
				adapt := func(commands []ownerCommand) []ownerCommand {
					for i := range commands {
						commands[i].output = strings.ReplaceAll(commands[i].output, "working directory = /srv", "working directory = "+configured)
						commands[i].output = strings.ReplaceAll(commands[i].output, "n/srv\n", "n"+tc.actual+"\n")
						commands[i].command = strings.ReplaceAll(commands[i].command, "CDPATH='' cd -P -- /srv || exit;", "CDPATH='' cd -P -- "+shellquote.RemoteWord(configured)+" || exit;")
					}
					return commands
				}
				report := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "boot-two")
				report.Running.PID = 4343
				report.Running.Version = "release-selected"
				commands := adapt(darwinOwnerCommands(t, false, stopped))[1:]
				if stopped {
					priorCommands := adapt(darwinOwnerCommands(t, false, false))[1:6]
					priorCommands[0].output = "-\t0\tcom.example.evener-hub\n"
					priorCommands[1].output = strings.Replace(priorCommands[1].output, "pid = 4242", "pid = 0", 1)
					commands = append(priorCommands, commands...)
				} else {
					report.Running.PID = 4242
				}
				if tc.different {
					commands = commands[:len(commands)-1]
				}
				fr, index := ownerRunner(t, commands)
				base := fr.runFn
				probes := 0
				fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
					remote := strings.Join(argv[12:], " ")
					if strings.HasPrefix(remote, "CDPATH='' cd -P -- ") && strings.HasSuffix(remote, " && pwd -P") {
						probes++
						return exec.CommandContext(ctx, "/bin/sh", "-c", remote).Output()
					}
					return base(ctx, argv, in)
				}
				m := newTestManager(t, testRegistry(t, host), fr, Options{})
				facts := selectedRestartFacts()
				facts.OS = "darwin"
				facts.UID = "1000"
				var prior selectedOwner
				if stopped {
					stoppedReport := report
					stoppedReport.State = "stopped"
					stoppedReport.Running = nil
					prior, err = m.resolveStoppedOwner(t.Context(), host, facts, stoppedReport)
					if err != nil {
						t.Fatal(err)
					}
					if prior.cwd != configured || prior.supervisor.kind != supervisorLaunchd {
						t.Fatalf("lost launch intent: %+v", prior)
					}
				}
				now, err := m.resolveDarwinOwner(t.Context(), host, facts, report)
				if tc.different {
					if !errors.Is(err, errOwnerEvidence) {
						t.Fatalf("different directory accepted or wrong refusal: %v", err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if now.cwd != configured {
						t.Fatal("configured launch cwd rewritten")
					}
					if now.cwdIdentity != tc.actual || stopped && prior.cwdIdentity != tc.actual {
						t.Fatalf("pathname bytes changed: prior=%q now=%q want=%q", prior.cwdIdentity, now.cwdIdentity, tc.actual)
					}
					if stopped && !selectedSuccessor(now, prior, nil, "release-selected", "") {
						t.Fatal("same directory refused as stopped-service successor")
					}
				}
				wantProbes := 0
				if tc.actual != configured {
					wantProbes = 2
				}
				if stopped {
					wantProbes++
				}
				if *index != len(commands) || probes != wantProbes || len(fr.recordedStarts()) != 0 {
					t.Fatalf("incomplete ownership proof: commands=%d/%d probes=%d/%d", *index, len(commands), probes, wantProbes)
				}
			})
		}
	}
}
