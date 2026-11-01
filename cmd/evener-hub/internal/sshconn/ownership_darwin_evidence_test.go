package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"primeradiant.com/evener/internal/hubendpoint"
)

// TestDarwinOpenVnode arms the lsof -F pftDi parser: only a numeric file
// descriptor holding the exact regular-file vnode is evidence that the
// selected process opened the root's lock.
func TestDarwinOpenVnode(t *testing.T) {
	t.Parallel()
	const out = "p4242\nf3\ntREG\nD0x1000012\ni777\n"
	for _, tc := range []struct {
		name string
		out  string
		want bool
	}{
		{"matching lock descriptor", out, true},
		{"non-numeric fd is not a lock descriptor", "p4242\nfcwd\ntDIR\nD0x1000012\ni777\n", false},
		{"non-numeric fd before the matching one", "p4242\nfcwd\nf3\ntREG\nD0x1000012\ni777\n", true},
		{"no output", "", false},
		{"truncated field line", "p", false},
		{"empty line", "\n", false},
		{"other process", strings.Replace(out, "p4242", "p999", 1), false},
		{"descriptor before process", "f3\ntREG\nD0x1000012\ni777\n", false},
		{"type before descriptor", "p4242\ntREG\n", false},
		{"repeated type", "p4242\nf3\ntREG\ntREG\nD0x1000012\ni777\n", false},
		{"unknown field letter", "p4242\nf3\nxREG\nD0x1000012\ni777\n", false},
		{"device mismatch", "p4242\nf3\ntREG\nD0x1000011\ni777\n", false},
		{"inode mismatch", "p4242\nf3\ntREG\nD0x1000012\ni778\n", false},
		{"unparsable device", "p4242\nf3\ntREG\nD0xZZZZ\ni777\n", false},
		{"non-numeric inode", "p4242\nf3\ntREG\nD0x1000012\niabc\n", false},
		{"missing type", "p4242\nf3\nD0x1000012\ni777\n", false},
		{"non-regular file", "p4242\nf3\ntCHR\nD0x1000012\ni777\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := darwinOpenVnode([]byte(tc.out), "4242", 0x1000012, "777"); got != tc.want {
				t.Fatalf("darwinOpenVnode(%q) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}

// TestParseLaunchdDefinitionEnvDedup covers the replacement of a repeated
// environment key: the later assignment must be the only one retained.
func TestParseLaunchdDefinitionEnvDedup(t *testing.T) {
	t.Parallel()
	definition := "pid = 4242\nprogram = /opt/evener\nworking directory = /srv\narguments = {\n/opt/evener\nhub\n--config=/srv/selected.toml\n--addr=[::]:0\n}\nHOME => /home/one\nHOME => /home/two\n"
	def, err := parseLaunchdDefinition([]byte(definition))
	if err != nil {
		t.Fatal(err)
	}
	if len(def.env) != 1 || def.env[0] != "HOME=/home/two" {
		t.Fatalf("repeated environment key retained %q", def.env)
	}
	if def.pid != "4242" || def.program != "/opt/evener" || def.cwd != "/srv" {
		t.Fatalf("definition fields lost: %+v", def)
	}
}

// failingOwnerRunner wraps ownerRunner so commands containing needle fail with
// the given error, standing in for an ssh link that dies mid-proof.
func failingOwnerRunner(t *testing.T, commands []ownerCommand, needle string, failure error) (*fakeRunner, *int) {
	t.Helper()
	fr, index := ownerRunner(t, commands)
	base := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		if strings.Contains(strings.Join(argv[12:], " "), needle) {
			return nil, failure
		}
		return base(ctx, argv, stdin)
	}
	return fr, index
}

func darwinOwnerFacts() Preflight {
	facts := selectedRestartFacts()
	facts.OS, facts.UID = "darwin", "1000"
	return facts
}

// reportFromOwnerRead decodes the endpoint JSON the ladder answers for the
// owner-context re-read, so the report handed to resolveDarwinOwner matches it.
func reportFromOwnerRead(t *testing.T, commands []ownerCommand, at int) hubendpoint.Report {
	t.Helper()
	var report hubendpoint.Report
	if err := json.Unmarshal([]byte(commands[at].output), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestLaunchdOwnersEvidence(t *testing.T) {
	t.Parallel()
	host, _ := ownerCommands(t)
	const listing = "4242\t0\tcom.example.evener-hub\n"

	t.Run("non-numeric uid refuses before any listing", func(t *testing.T) {
		t.Parallel()
		fr, index := ownerRunner(t, nil)
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if _, err := m.launchdOwners(t.Context(), host, Preflight{UID: "not-numeric"}); !errors.Is(err, errOwnerEvidence) {
			t.Fatalf("uid refusal = %v", err)
		}
		if *index != 0 {
			t.Fatalf("uid refusal issued %d commands", *index)
		}
	})
	t.Run("listing read failure surfaces", func(t *testing.T) {
		t.Parallel()
		linkFailure := errors.New("ssh link died")
		fr, _ := failingOwnerRunner(t, []ownerCommand{{"launchctl list", listing}}, "launchctl list", linkFailure)
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if _, err := m.launchdOwners(t.Context(), host, Preflight{UID: "1000"}); !errors.Is(err, linkFailure) {
			t.Fatalf("listing failure = %v", err)
		}
	})
	t.Run("unparsable definition is refused", func(t *testing.T) {
		t.Parallel()
		commands := []ownerCommand{{"launchctl list", listing}, {launchdDefinitionRemote("1000", "com.example.evener-hub"), "pid = \n"}}
		fr, index := ownerRunner(t, commands)
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		_, err := m.launchdOwners(t.Context(), host, Preflight{UID: "1000"})
		if !errors.Is(err, errOwnerEvidence) {
			t.Fatalf("definition refusal = %v", err)
		}
		if *index != len(commands) {
			t.Fatalf("definition refusal consumed %d/%d commands", *index, len(commands))
		}
	})
}

func TestRevalidateStoppedLaunchdEvidence(t *testing.T) {
	t.Parallel()
	host, _ := ownerCommands(t)
	definition := "pid = 0\nprogram = /opt/evener\nworking directory = /srv\narguments = {\n/opt/evener\nhub\n--config=/srv/selected.toml\n--addr=[::]:0\n}\nHOME => /home/dev\n"
	def, err := parseLaunchdDefinition([]byte(definition))
	if err != nil {
		t.Fatal(err)
	}
	sup := supervisor{kind: supervisorLaunchd, label: "com.example.evener-hub"}
	owner := selectedOwner{host: host, supervisor: sup, domainUID: "1000", definition: launchdIntent(def), report: hubendpoint.Report{}}
	const listing = "0\t-\tcom.example.evener-hub\n"

	t.Run("changed domain is refused without commands", func(t *testing.T) {
		t.Parallel()
		fr, index := ownerRunner(t, nil)
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if err := m.revalidateStoppedLaunchd(t.Context(), host, Preflight{UID: "999"}, owner); !errors.Is(err, errOwnerEvidence) {
			t.Fatalf("domain change = %v", err)
		}
		if *index != 0 {
			t.Fatalf("domain change issued %d commands", *index)
		}
	})
	t.Run("listing failure surfaces", func(t *testing.T) {
		t.Parallel()
		linkFailure := errors.New("ssh link died")
		fr, _ := failingOwnerRunner(t, []ownerCommand{{"launchctl list", listing}}, "launchctl list", linkFailure)
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if err := m.revalidateStoppedLaunchd(t.Context(), host, Preflight{UID: "1000"}, owner); !errors.Is(err, linkFailure) {
			t.Fatalf("listing failure = %v", err)
		}
	})
	t.Run("changed definition is refused", func(t *testing.T) {
		t.Parallel()
		changed := strings.Replace(definition, "/srv/selected.toml", "/srv/other.toml", 1)
		commands := []ownerCommand{{"launchctl list", listing}, {launchdDefinitionRemote("1000", "com.example.evener-hub"), changed}}
		fr, index := ownerRunner(t, commands)
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if err := m.revalidateStoppedLaunchd(t.Context(), host, Preflight{UID: "1000"}, owner); !errors.Is(err, errOwnerEvidence) {
			t.Fatalf("definition change = %v", err)
		}
		if *index != len(commands) {
			t.Fatalf("definition change consumed %d/%d commands", *index, len(commands))
		}
	})
	t.Run("unchanged definition passes", func(t *testing.T) {
		t.Parallel()
		commands := []ownerCommand{{"launchctl list", listing}, {launchdDefinitionRemote("1000", "com.example.evener-hub"), definition}}
		fr, index := ownerRunner(t, commands)
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if err := m.revalidateStoppedLaunchd(t.Context(), host, Preflight{UID: "1000"}, owner); err != nil {
			t.Fatal(err)
		}
		if *index != len(commands) {
			t.Fatalf("revalidation consumed %d/%d commands", *index, len(commands))
		}
	})
}

// TestDarwinOwnerEvidenceArms mutates one answer of the healthy selected
// launchd ladder; each mutation must refuse the owner at that rung.
func TestDarwinOwnerEvidenceArms(t *testing.T) {
	t.Parallel()
	host, _ := ownerCommands(t)
	type mutation struct {
		name     string
		index    int
		output   string
		errorAt  int // command index consumed when the refusal fires
		needle   string
		rawError error
	}
	for _, tc := range []mutation{
		{name: "empty generation", index: 5, output: "\n", errorAt: 6},
		{name: "unrecoverable argv", index: 8, output: "", errorAt: 9},
		{name: "process cwd missing", index: 9, output: "p4242\nfcwd\n", errorAt: 10},
		{name: "generation moved under the owner", index: 12, output: "Fri Oct 16 10:00:01 2026\n", errorAt: 13},
		{name: "unparsable service definition", index: 1, output: "pid = 4242\n", errorAt: 2},
		{name: "listing read failure", needle: "launchctl list", rawError: errors.New("ssh link died"), errorAt: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			commands := darwinOwnerCommands(t, false, false)[1:]
			if tc.rawError == nil {
				commands[tc.index].output = tc.output
			}
			var fr *fakeRunner
			var index *int
			if tc.rawError != nil {
				fr, index = failingOwnerRunner(t, commands, tc.needle, tc.rawError)
			} else {
				fr, index = ownerRunner(t, commands)
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			report := reportFromOwnerRead(t, commands, 4)
			_, err := m.resolveDarwinOwner(t.Context(), host, darwinOwnerFacts(), report)
			switch {
			case tc.rawError != nil:
				if !errors.Is(err, tc.rawError) {
					t.Fatalf("refusal = %v, want the link failure", err)
				}
			default:
				if !errors.Is(err, errOwnerEvidence) {
					t.Fatalf("refusal = %v, want owner evidence failure", err)
				}
			}
			if *index != tc.errorAt {
				t.Fatalf("refusal consumed %d commands, want %d", *index, tc.errorAt)
			}
			if len(fr.recordedStarts()) != 0 {
				t.Fatal("evidence check started a process")
			}
		})
	}
	// A disagreeing process user keeps probing the uid of each candidate user
	// before refusing, exactly as the linux rejection ladder does.
	t.Run("hub owner user disagrees", func(t *testing.T) {
		t.Parallel()
		commands := darwinOwnerCommands(t, false, false)[1:]
		commands[6].output = "other\n"
		commands = append(commands[:7],
			ownerCommand{"id -u other", "1001\n"},
			ownerCommand{"id -u dev", "1000\n"})
		fr, index := ownerRunner(t, commands)
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		report := reportFromOwnerRead(t, commands, 4)
		if _, err := m.resolveDarwinOwner(t.Context(), host, darwinOwnerFacts(), report); !errors.Is(err, errOwnerEvidence) {
			t.Fatalf("refusal = %v, want owner evidence failure", err)
		}
		if *index != len(commands) {
			t.Fatalf("refusal consumed %d/%d commands", *index, len(commands))
		}
	})
}

// TestDarwinOwnerRootVnodeAndImageArms extends the damaged-report ladder to
// the root vnode and mapped-image proofs and mutates one answer per rung.
func TestDarwinOwnerRootVnodeAndImageArms(t *testing.T) {
	t.Parallel()
	host, _ := ownerCommands(t)
	const rootStat = "test ! -L /srv/state/hub.lock && test -f /srv/state/hub.lock && stat -f '%d %i' /srv/state/hub.lock"
	base := func(t *testing.T) []ownerCommand {
		commands := darwinOwnerCommands(t, true, false)
		last := commands[len(commands)-1]
		commands = commands[:len(commands)-1]
		commands = append(commands,
			ownerCommand{rootStat, "16777234 777\n"},
			ownerCommand{"lsof -a -p 4242 -F pftDi", "p4242\nf3\ntREG\nD0x1000012\ni777\n"},
			ownerCommand{rootStat, "16777234 777\n"},
			ownerCommand{"canonical", "/opt/evener\n"},
			ownerCommand{"stat -f '%d %i' /opt/evener", "16777234 12345\n"},
			ownerCommand{"lsof -a -p 4242 -d txt -F Di", "p4242\nftxt\nD0x1000012\ni12345\n"},
			ownerCommand{"/opt/evener launch-check --json", goodLaunchCheck},
			ownerCommand{"stat -f '%d %i' /opt/evener", "16777234 12345\n"},
			last)
		return commands
	}
	// Offsets of the appended rungs inside the full command slice.
	const (
		rootStatFirst = 13
		vnodeListing  = 14
		rootStatAgain = 15
		statImage     = 17
		mappedImage   = 18
		launchCheck   = 19
		statImageLast = 20
	)
	for _, tc := range []struct {
		name     string
		index    int
		output   string
		errorAt  int
		needle   string
		rawError error
	}{
		{name: "root stat without a device pair", index: rootStatFirst, output: "16777234\n", errorAt: rootStatFirst + 1},
		{name: "root stat with an unparsable device", index: rootStatFirst, output: "zz 777\n", errorAt: rootStatFirst + 1},
		{name: "root lock not held by the process", index: vnodeListing, output: "p4242\nf3\ntREG\nD0x1000012\ni999\n", errorAt: vnodeListing + 1},
		{name: "root identity moved during the vnode proof", index: rootStatAgain, output: "16777234 778\n", errorAt: rootStatAgain + 1},
		{name: "image stat without a device pair", index: statImage, output: "16777234\n", errorAt: statImage + 1},
		{name: "image stat with an unparsable device", index: statImage, output: "zz 12345\n", errorAt: statImage + 1},
		{name: "mapped image differs from the inspected disk", index: mappedImage, output: "p4242\nftxt\nD0x1000012\ni999\n", errorAt: mappedImage + 1},
		{name: "unparsable launch contract", index: launchCheck, output: "{", errorAt: launchCheck + 1},
		{name: "image moved after the capability read", index: statImageLast, output: "16777234 12346\n", errorAt: statImageLast + 1},
		{name: "image stat read failure", needle: "stat -f '%d %i' /opt/evener", rawError: errors.New("ssh link died")},
		{name: "mapped image read failure", needle: "lsof -a -p 4242 -d txt", rawError: errors.New("ssh link died")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			commands := base(t)
			var fr *fakeRunner
			var index *int
			if tc.rawError != nil {
				fr, index = failingOwnerRunner(t, commands, tc.needle, tc.rawError)
			} else {
				commands[tc.index].output = tc.output
				fr, index = ownerRunner(t, commands)
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			_, err := m.resolveOwnedEndpoint(t.Context(), host, darwinOwnerFacts())
			switch {
			case tc.rawError != nil:
				if !errors.Is(err, tc.rawError) {
					t.Fatalf("refusal = %v, want the link failure", err)
				}
			default:
				if !errors.Is(err, errOwnerEvidence) {
					t.Fatalf("refusal = %v, want owner evidence failure", err)
				}
				if *index != tc.errorAt {
					t.Fatalf("refusal consumed %d commands, want %d", *index, tc.errorAt)
				}
			}
			if len(fr.recordedStarts()) != 0 {
				t.Fatal("evidence check started a process")
			}
		})
	}
}
