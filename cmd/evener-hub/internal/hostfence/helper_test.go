package hostfence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The helper-gate tests pin crash-fencing spec §6 and §10: the pinned helper
// version, the read-only presence/version verification that refuses
// fail-closed before any remote mutation, and the wrapper command/response
// protocol the fencing worker drives.

func TestVerifyHelperPinsPresenceAndVersion(t *testing.T) {
	err := VerifyHelper("h1", HelperVersion, HelperProbe{Present: true, Reported: true, Version: HelperVersion})
	if err != nil {
		t.Fatalf("VerifyHelper(trusted) = %v, want nil", err)
	}
	for name, probe := range map[string]HelperProbe{
		"absent":     {Present: false},
		"no version": {Present: true},
	} {
		err := VerifyHelper("h1", HelperVersion, probe)
		var refusal *HelperGateError
		if !errors.As(err, &refusal) {
			t.Fatalf("%s: VerifyHelper = %v, want *HelperGateError", name, err)
		}
		if refusal.Discriminator != DiscriminatorHelperAbsent {
			t.Errorf("%s: discriminator = %q, want %q", name, refusal.Discriminator, DiscriminatorHelperAbsent)
		}
		if refusal.Host != "h1" || refusal.PinnedVersion != HelperVersion {
			t.Errorf("%s: refusal data = %+v, want host h1 and pinned version %d", name, refusal, HelperVersion)
		}
	}
	for name, probe := range map[string]HelperProbe{
		"older version":     {Present: true, Reported: true, Version: 0},
		"newer version":     {Present: true, Reported: true, Version: 2},
		"explicit distrust": {Present: true, Reported: true, Version: 1, Untrusted: true},
	} {
		err := VerifyHelper("h1", HelperVersion, probe)
		var refusal *HelperGateError
		if !errors.As(err, &refusal) || refusal.Discriminator != DiscriminatorHelperUntrusted {
			t.Fatalf("%s: VerifyHelper = %v, want a %s refusal", name, err, DiscriminatorHelperUntrusted)
		}
		if refusal.ObservedVersion != probe.Version && !probe.Untrusted {
			t.Errorf("%s: refusal observed version = %d, want %d", name, refusal.ObservedVersion, probe.Version)
		}
	}
	// The two refusals ride the conflict class: data names the host plus the
	// pinned helper version the operator must install out-of-band (§8).
	var refusal *HelperGateError
	if err := VerifyHelper("h1", HelperVersion, HelperProbe{Present: false}); !errors.As(err, &refusal) {
		t.Fatal("absent helper did not produce a HelperGateError")
	}
	if !strings.Contains(refusal.Error(), "h1") || !strings.Contains(refusal.Error(), "1") {
		t.Errorf("refusal message %q does not name the host and pinned version", refusal.Error())
	}
}

func TestParseHelperVersion(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		version int
		ok      bool
	}{
		{"1\n", 1, true},
		{"1", 1, true},
		{" 2 \n", 2, true},
		{"", 0, false},
		{"evener-fence 1", 0, false},
		{"one", 0, false},
	} {
		version, ok := ParseHelperVersion(tc.raw)
		if ok != tc.ok || version != tc.version {
			t.Errorf("ParseHelperVersion(%q) = (%d, %v), want (%d, %v)", tc.raw, version, ok, tc.version, tc.ok)
		}
	}
}

func TestShellQuote(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain", `'plain'`},
		{"two words", `'two words'`},
		{"it's", `'it'\''s'`},
		{"a;rm -rf /", `'a;rm -rf /'`},
		{"", `''`},
	} {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestWrapperOverridePathIsQuoted pins that a caller-supplied helper path never
// becomes shell syntax, while the trusted default keeps its remote-HOME
// expansion.
// TestGuardHighWaterRetiresOldBoots pins the durable per-boot high-water: an
// epoch the guard already admitted from that boot never takes over again, even
// after later boots have settled, while a newer epoch of the same boot still
// may.
func TestGuardHighWaterRetiresOldBoots(t *testing.T) {
	oldA := Epoch{BootID: "boot-a", OpSeq: 1}
	bootB := Epoch{BootID: "boot-b", OpSeq: 1}
	bootC := Epoch{BootID: "boot-c", OpSeq: 1}
	settled := GuardState{
		Version: 1, GuardEpoch: 6, Epoch: &bootC, Holder: &bootC, Superseded: &bootB,
		BootHighWater: map[string]uint64{"boot-a": 1, "boot-b": 1, "boot-c": 1},
	}
	if _, err := settled.Takeover(oldA); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("Takeover(old boot A epoch) = %v, want ErrStaleEpoch", err)
	}
	newerA := Epoch{BootID: "boot-a", OpSeq: 2}
	next, err := settled.Takeover(newerA)
	if err != nil {
		t.Fatalf("Takeover(newer epoch of an old boot) = %v, want success", err)
	}
	if got := next.BootHighWater["boot-a"]; got != 2 {
		t.Fatalf("high-water after takeover = %d, want 2", got)
	}
	if err := next.Validate(); err != nil {
		t.Fatalf("takeover result fails validation: %v", err)
	}
	if err := (GuardState{Version: 1, BootHighWater: map[string]uint64{"bad boot": 1}}).Validate(); !errors.Is(err, ErrInvalidGuard) {
		t.Fatalf("Validate(invalid high-water key) = %v, want ErrInvalidGuard", err)
	}
}

func TestWrapperOverridePathIsQuoted(t *testing.T) {
	for name, path := range map[string]string{
		"space":    "/tmp/fence dir/evener-fence",
		"metachar": "/tmp/fence; rm -rf / #",
		"subshell": "/tmp/$(touch pwned)/fence",
		"backtick": "/tmp/`touch pwned`/fence",
	} {
		takeover, err := Wrapper{Path: path}.TakeoverCommand(Epoch{BootID: "boot-1", OpSeq: 1})
		if err != nil {
			t.Fatalf("%s: TakeoverCommand = %v", name, err)
		}
		if !strings.HasPrefix(takeover, shellQuote(path)+" ") {
			t.Errorf("%s: TakeoverCommand = %q, want the path single-quoted whole", name, takeover)
		}
		if strings.Contains(takeover, path+" ") && strings.ContainsAny(path, " ;$`") {
			t.Errorf("%s: TakeoverCommand = %q, want the raw path never interpolated unquoted", name, takeover)
		}
	}
	// The default stays the trusted double-quoted form: the remote shell must
	// still expand its own HOME.
	if !strings.HasPrefix(Wrapper{}.VersionCommand(), HelperRemotePath+" ") {
		t.Errorf("default command = %q, want the expandable default path", Wrapper{}.VersionCommand())
	}
}

func TestWrapperCommandsCarryThePresentedEpoch(t *testing.T) {
	epoch := Epoch{BootID: "boot-1", OpSeq: 7}
	takeover, err := Wrapper{}.TakeoverCommand(epoch)
	if err != nil {
		t.Fatalf("TakeoverCommand = %v", err)
	}
	if !strings.HasSuffix(takeover, "takeover 'boot-1' 7") {
		t.Errorf("TakeoverCommand = %q, want the epoch presented and quoted", takeover)
	}
	advance, err := Wrapper{}.AdvanceCommand(epoch)
	if err != nil {
		t.Fatalf("AdvanceCommand = %v", err)
	}
	if !strings.HasSuffix(advance, "advance 'boot-1' 7") {
		t.Errorf("AdvanceCommand = %q, want the epoch presented and quoted", advance)
	}
	if _, err := (Wrapper{}).TakeoverCommand(Epoch{}); err == nil {
		t.Error("TakeoverCommand(zero epoch) = nil error, want refusal")
	}
	// A boot id outside the shell-token-safe set never reaches a command line:
	// the epoch schema refuses it first, so no hostile value depends on quoting.
	if _, err := (Wrapper{}).TakeoverCommand(Epoch{BootID: "boot 1; rm -rf /", OpSeq: 1}); err == nil {
		t.Error("TakeoverCommand(hostile boot id) = nil error, want refusal")
	}
	hostile := `deploy "now"; rm -rf / #`
	perform, _, err := (Wrapper{}).PerformCommand(epoch, hostile)
	if err != nil {
		t.Fatalf("PerformCommand = %v", err)
	}
	if !strings.HasSuffix(perform, `perform 'boot-1' 7 'deploy "now"; rm -rf / #'`) {
		t.Errorf("PerformCommand = %q, want the command text single-quoted whole", perform)
	}
	if !strings.Contains(perform, HelperRemotePath) {
		t.Errorf("PerformCommand = %q, want the pinned helper path", perform)
	}
}

// TestDecodersValidateHelperJSON pins the decode-validation posture: helper
// responses are a trust boundary, so an object outside the schema this package
// emits is refused, never half-understood.
func TestDecodersValidateHelperJSON(t *testing.T) {
	statusJSON := `{"version":1,"guardEpoch":4,"epoch":{"bootId":"b1","opSeq":3},` +
		`"fence":null,"superseded":{"bootId":"b0","opSeq":9},"holder":{"bootId":"b1","opSeq":3},"entries":2,` +
		`"bootHighWater":{"b1":3}}`
	status, err := DecodeStatus([]byte(statusJSON))
	if err != nil {
		t.Fatalf("DecodeStatus = %v", err)
	}
	if status.GuardEpoch != 4 || status.Epoch == nil || status.Epoch.BootID != "b1" || status.Entries != 2 {
		t.Fatalf("DecodeStatus = %+v, want the emitted values", status)
	}
	refused := map[string]string{
		"unknown field":  `{"version":1,"guardEpoch":4,"epoch":null,"fence":null,"superseded":null,"holder":null,"entries":2,"extra":true}`,
		"duplicate key":  `{"version":1,"guardEpoch":4,"guardEpoch":5,"epoch":null,"fence":null,"superseded":null,"holder":null,"entries":0}`,
		"wrong version":  `{"version":2,"guardEpoch":4,"epoch":null,"fence":null,"superseded":null,"holder":null,"entries":0}`,
		"zero boot id":   `{"version":1,"guardEpoch":4,"epoch":{"bootId":"","opSeq":3},"fence":null,"superseded":null,"holder":null,"entries":0}`,
		"fence no epoch": `{"version":1,"guardEpoch":4,"epoch":null,"fence":{"epoch":null,"superseded":null,"guardEpoch":0},"superseded":null,"holder":null,"entries":0}`,
		"trailing bytes": `{"version":1,"guardEpoch":4,"epoch":null,"fence":null,"superseded":null,"holder":null,"entries":0} junk`,
	}
	for name, raw := range refused {
		if _, err := DecodeStatus([]byte(raw)); err == nil {
			t.Errorf("%s: DecodeStatus = nil error, want refusal", name)
		}
	}
}

func TestDecodeRefusalMapsTypedErrors(t *testing.T) {
	for reason, want := range map[string]error{
		"stale-epoch":   ErrStaleEpoch,
		"fenced":        ErrFenced,
		"busy":          ErrHelperBusy,
		"state-corrupt": ErrStateCorrupt,
		"malformed":     ErrMalformed,
		"io-error":      ErrHelperIO,
	} {
		raw := RefusalPrefix + `{"version":1,"refused":true,"error":"` + reason + `","detail":"detail text"}`
		err := DecodeRefusal([]byte(raw))
		if !errors.Is(err, want) {
			t.Errorf("DecodeRefusal(%s) = %v, want %v", reason, err, want)
		}
		var refusal *HelperRefusalError
		if !errors.As(err, &refusal) || refusal.Detail != "detail text" {
			t.Errorf("DecodeRefusal(%s) detail lost: %+v", reason, err)
		}
	}
	if err := DecodeRefusal([]byte(RefusalPrefix + `{"version":1,"refused":true,"error":"stale-epoch-typo"}`)); err == nil {
		t.Error("DecodeRefusal(unknown reason) = nil error, want refusal")
	}
	if err := DecodeRefusal([]byte(`{"version":1,"error":"stale-epoch"}`)); err == nil {
		t.Error("DecodeRefusal(unmarked refusal) = nil error, want refusal")
	}
	// An unprefixed object — what a wrapped command's own stderr looks like — is
	// never a wrapper refusal.
	if err := DecodeRefusal([]byte(`{"version":1,"refused":true,"error":"stale-epoch"}`)); !errors.Is(err, ErrMalformed) {
		t.Errorf("DecodeRefusal(unprefixed) = %v, want ErrMalformed", err)
	}
}

func TestDecodeEntriesAndRecheck(t *testing.T) {
	raw := `{"version":1,"entries":[{"id":"n1","command":"deploy --now","registeredAt":"2026-09-28T10:00:00Z",` +
		`"ownership":{"pid":41,"pidStartTime":"777"},"state":"running"},` +
		`{"id":"n2","command":"restart","registeredAt":"2026-09-28T09:00:00Z",` +
		`"ownership":{"nonce":"n2"},"state":"exited","exit":0,"exitedAt":"2026-09-28T09:00:05Z"}]}`
	entries, err := DecodeEntries([]byte(raw))
	if err != nil {
		t.Fatalf("DecodeEntries = %v", err)
	}
	if len(entries) != 2 || entries[0].Ownership.Kind() != OwnershipPID || entries[1].Ownership.Kind() != OwnershipNonce {
		t.Fatalf("DecodeEntries = %+v, want one pid-owned and one nonce-owned entry", entries)
	}
	if entries[1].Exit == nil || *entries[1].Exit != 0 {
		t.Fatalf("DecodeEntries exit = %+v, want 0", entries[1].Exit)
	}
	// §9's remote-fencing boundary carries {command, registeredAt, ownership}.
	ref := entries[0].BoundaryRef()
	projected, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("marshal boundary ref: %v", err)
	}
	if string(projected) != `{"command":"deploy --now","registeredAt":"2026-09-28T10:00:00Z","ownership":{"pid":41,"pidStartTime":"777"}}` {
		t.Fatalf("BoundaryRef = %s, want the three-field §9 projection", projected)
	}
	recheck, err := DecodeRecheck([]byte(`{"version":1,"id":"n1","live":true,"state":"running","ownership":{"pid":41,"pidStartTime":"777"}}`))
	if err != nil {
		t.Fatalf("DecodeRecheck = %v", err)
	}
	if !recheck.Live || recheck.ID != "n1" || recheck.Ownership.Kind() != OwnershipPID {
		t.Fatalf("DecodeRecheck = %+v", recheck)
	}

	bad := map[string]string{
		"ownership both variants": `{"version":1,"entries":[{"id":"n","command":"c","registeredAt":"t","ownership":{"pid":1,"pidStartTime":"s","nonce":"n"},"state":"running"}]}`,
		"ownership none":          `{"version":1,"entries":[{"id":"n","command":"c","registeredAt":"t","ownership":{},"state":"running"}]}`,
		"unknown state":           `{"version":1,"entries":[{"id":"n","command":"c","registeredAt":"t","ownership":{"nonce":"n"},"state":"gone"}]}`,
		"empty command":           `{"version":1,"entries":[{"id":"n","command":"","registeredAt":"t","ownership":{"nonce":"n"},"state":"running"}]}`,
		"unknown field":           `{"version":1,"entries":[{"id":"n","command":"c","registeredAt":"t","ownership":{"nonce":"n"},"state":"running","extra":1}]}`,
		"version only":            `{"version":1}`,
		"missing entries array":   `{"version":1,"envelope":[]}`,
		"null entries":            `{"version":1,"entries":null}`,
		"entry missing ownership": `{"version":1,"entries":[{"id":"n","command":"c","registeredAt":"t","state":"running"}]}`,
	}
	for name, raw := range bad {
		if _, err := DecodeEntries([]byte(raw)); err == nil {
			t.Errorf("%s: DecodeEntries = nil error, want refusal", name)
		}
	}
	for name, raw := range map[string]string{
		"bad live":          `{"version":1,"id":"n1","live":"yes"}`,
		"version only":      `{"version":1}`,
		"missing live":      `{"version":1,"id":"n1","state":"running","ownership":{"nonce":"n1"}}`,
		"missing id":        `{"version":1,"live":true,"state":"running","ownership":{"nonce":"n1"}}`,
		"missing state":     `{"version":1,"id":"n1","live":true,"ownership":{"nonce":"n1"}}`,
		"missing own":       `{"version":1,"id":"n1","live":true,"state":"running"}`,
		"null ownership":    `{"version":1,"id":"n1","live":true,"state":"running","ownership":null}`,
		"live while exited": `{"version":1,"id":"n1","live":true,"state":"exited","ownership":{"nonce":"n1"}}`,
	} {
		if _, err := DecodeRecheck([]byte(raw)); err == nil {
			t.Errorf("%s: DecodeRecheck = nil error, want refusal", name)
		}
	}
	// A settled guard may still be reported with no live entry, and a recheck
	// whose entry is gone carries an empty state.
	if _, err := DecodeRecheck([]byte(`{"version":1,"id":"n1","live":false,"state":"","ownership":{"nonce":"n1"}}`)); err != nil {
		t.Errorf("DecodeRecheck(absent entry) = %v, want nil", err)
	}
}

// fakeRunner is the scripted-remote seam the Wrapper drives: it records the
// commands and answers canned stdout/stderr/exit, so no test ever opens a
// connection.
type fakeRunner struct {
	commands []string
	stdout   string
	stderr   string
	exit     int
}

func (f *fakeRunner) Run(ctx context.Context, command string) (string, string, int, error) {
	f.commands = append(f.commands, command)
	return f.stdout, f.stderr, f.exit, nil
}

func TestWrapperTakeoverAdvancesDecodesAndRefuses(t *testing.T) {
	runner := &fakeRunner{stdout: `{"version":1,"guardEpoch":5,"epoch":{"bootId":"b1","opSeq":3},` +
		`"fence":{"epoch":{"bootId":"b1","opSeq":4},"superseded":{"bootId":"b1","opSeq":3},"guardEpoch":5},` +
		`"superseded":null,"holder":{"bootId":"b1","opSeq":4},"entries":1,"bootHighWater":{"b1":4}}`}
	wrapper := Wrapper{Runner: runner, Host: "h1"}
	status, err := wrapper.Takeover(context.Background(), Epoch{BootID: "b1", OpSeq: 4})
	if err != nil {
		t.Fatalf("Takeover = %v", err)
	}
	if status.Fence == nil || status.Fence.Epoch.OpSeq != 4 || status.Holder == nil || status.Holder.OpSeq != 4 {
		t.Fatalf("Takeover status = %+v, want the decoded fence and holder", status)
	}
	if len(runner.commands) != 1 || !strings.Contains(runner.commands[0], "takeover 'b1' 4") {
		t.Fatalf("Takeover commands = %q, want one takeover command", runner.commands)
	}
	// A refusal on stderr with a nonzero exit is the typed server-side refusal,
	// never a decoded success.
	refusing := &fakeRunner{stderr: RefusalPrefix + `{"version":1,"refused":true,"error":"stale-epoch","detail":"older"}`, exit: 75}
	if _, err := (Wrapper{Runner: refusing, Host: "h1"}).Takeover(context.Background(), Epoch{BootID: "b1", OpSeq: 4}); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("Takeover(refusal) = %v, want ErrStaleEpoch", err)
	}
	// A success exit with unparsable output is refused, never treated as an
	// empty state that authorizes a mutation.
	garbled := &fakeRunner{stdout: `not json`}
	if _, err := (Wrapper{Runner: garbled, Host: "h1"}).Takeover(context.Background(), Epoch{BootID: "b1", OpSeq: 4}); err == nil {
		t.Fatal("Takeover(garbled) = nil error, want refusal")
	}
	// The wrapper refuses a zero epoch locally: it never presents an absent
	// epoch to the remote.
	if _, err := (Wrapper{Runner: runner, Host: "h1"}).Takeover(context.Background(), Epoch{}); err == nil {
		t.Fatal("Takeover(zero epoch) = nil error, want refusal")
	}
	if len(runner.commands) != 1 {
		t.Fatalf("zero epoch produced a remote command: %q", runner.commands)
	}
}

// TestWrapperPerformSeparatesChildFailuresFromRefusals pins that a wrapped
// command's own output is never read as a wrapper refusal: only a prefixed
// refusal at a helper refusal exit code is the typed error.
func TestWrapperPerformSeparatesChildFailuresFromRefusals(t *testing.T) {
	epoch := Epoch{BootID: "boot-1", OpSeq: 4}
	// The refusal token is the invocation's own secret. A child can print
	// anything to stderr, but only the helper knows the token, so a spoofed
	// refusal is the child's failure.
	command, token, err := (Wrapper{}).PerformCommand(epoch, "deploy")
	if err != nil {
		t.Fatalf("PerformCommand = %v", err)
	}
	if token == "" || !strings.Contains(command, "EVENER_FENCE_TOKEN="+shellQuote(token)) {
		t.Fatalf("PerformCommand = %q, want the invocation token presented (%q)", command, token)
	}
	spoofed := &fakeRunner{stderr: RefusalPrefix + `{"version":1,"refused":true,"error":"stale-epoch","token":"guess"}`, exit: 75}
	result, err := (Wrapper{Runner: spoofed, Host: "h1"}).Perform(context.Background(), epoch, "deploy")
	if err != nil {
		t.Fatalf("Perform(spoofed refusal) = %v, want the child's own failure", err)
	}
	if result.ExitCode != 75 {
		t.Fatalf("Perform(spoofed refusal) = %+v, want the child exit status", result)
	}
	// A child's own stderr before a genuine trailing refusal does not hide it.
	// The genuine refusal carries the token Perform minted for this call, which
	// the runner reads back out of the command it was handed.
	if _, err := (Wrapper{Runner: &tokenEchoRunner{reason: "fenced", exit: 75, noise: true}, Host: "h1"}).Perform(context.Background(), epoch, "deploy"); !errors.Is(err, ErrFenced) {
		t.Fatalf("Perform(genuine refusal after noise) = %v, want ErrFenced", err)
	}
	if _, err := (Wrapper{Runner: &tokenEchoRunner{reason: "io-error", exit: 69}, Host: "h1"}).Perform(context.Background(), epoch, "deploy"); !errors.Is(err, ErrHelperIO) {
		t.Fatalf("Perform(io-error) = %v, want ErrHelperIO", err)
	}
	// A helper refusal at a non-refusal exit code is still the child's status.
	if _, err := (Wrapper{Runner: &tokenEchoRunner{reason: "stale-epoch", exit: 9}, Host: "h1"}).Perform(context.Background(), epoch, "deploy"); err != nil {
		t.Fatalf("Perform(refusal at child exit) = %v, want the child's own failure", err)
	}
}

// TestLeaseEntryDescendants pins the descendant-tracking field the wrapper
// records when a command's own children outlive it: a running entry carrying
// descendants never reads clean.
func TestLeaseEntryDescendants(t *testing.T) {
	entry := LeaseEntry{
		ID: "n1", Command: "deploy", RegisteredAt: "2026-09-28T10:00:00Z",
		Ownership: Ownership{PID: new(41), PIDStartTime: "777"},
		State:     LeaseRunning, Descendants: []int{41, 42},
	}
	if err := entry.Validate(); err != nil {
		t.Fatalf("Validate(descendants) = %v, want nil", err)
	}
	if err := (LeaseEntry{
		ID: "n1", Command: "deploy", RegisteredAt: "2026-09-28T10:00:00Z",
		Ownership: Ownership{Nonce: "n1"}, State: LeaseExited, Exit: new(0), Descendants: []int{7},
	}).Validate(); err == nil {
		t.Fatal("Validate(exited with descendants) = nil, want refusal")
	}
	if err := (LeaseEntry{
		ID: "n1", Command: "deploy", RegisteredAt: "2026-09-28T10:00:00Z",
		Ownership: Ownership{Nonce: "n1"}, State: LeaseRunning, Descendants: []int{0},
	}).Validate(); err == nil {
		t.Fatal("Validate(zero-pid descendant) = nil, want refusal")
	}
	// A not-live answer may carry the descendants it checked: each was verified
	// gone, and the list is the evidence of what was checked.
	if recheck, err := DecodeRecheck([]byte(`{"version":1,"id":"n1","live":false,"state":"running","ownership":{"pid":41,"pidStartTime":"777"},"descendants":[42]}`)); err != nil || recheck.Live || len(recheck.Descendants) != 1 {
		t.Fatalf("DecodeRecheck(checked descendants) = (%+v, %v), want not-live with the checked list", recheck, err)
	}
	if recheck, err := DecodeRecheck([]byte(`{"version":1,"id":"n1","live":true,"state":"running","ownership":{"pid":41,"pidStartTime":"777"},"descendants":[42]}`)); err != nil || len(recheck.Descendants) != 1 {
		t.Fatalf("DecodeRecheck(descendants) = (%+v, %v), want the descendants decoded", recheck, err)
	}
}

// tokenEchoRunner answers the way the helper does: the refusal carries the
// token the invocation presented, read back out of the command line, and may
// arrive after the wrapped command's own stderr noise.
type tokenEchoRunner struct {
	reason string
	exit   int
	noise  bool
}

func (r *tokenEchoRunner) Run(_ context.Context, command string) (string, string, int, error) {
	token := ""
	if _, rest, ok := strings.Cut(command, TokenEnv+"='"); ok {
		token, _, _ = strings.Cut(rest, "'")
	}
	noise := ""
	if r.noise {
		noise = "child noise on stderr\n"
	}
	return "", noise + RefusalPrefix +
		`{"version":1,"refused":true,"error":"` + r.reason + `","token":"` + token + `"}`, r.exit, nil
}

func TestWrapperPerformReturnsChildStatus(t *testing.T) {
	runner := &fakeRunner{stdout: "deployed\n", exit: 3}
	wrapper := Wrapper{Runner: runner, Host: "h1"}
	result, err := wrapper.Perform(context.Background(), Epoch{BootID: "b1", OpSeq: 4}, "deploy --now")
	if err != nil {
		t.Fatalf("Perform = %v", err)
	}
	if result.ExitCode != 3 || result.Stdout != "deployed\n" {
		t.Fatalf("Perform = %+v, want the child's status and output", result)
	}
	if len(runner.commands) != 1 || !strings.Contains(runner.commands[0], `'deploy --now'`) {
		t.Fatalf("Perform commands = %q", runner.commands)
	}
}
