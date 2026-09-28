package hostfence

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// The helper-gate and wrapper contract of crash-fencing §6 and §4: the pinned
// versioned shell helper, the read-only presence/version verification that
// refuses fail-closed before any remote mutation, and the command/response
// protocol a fencing worker drives.

const (
	// HelperName is the helper's name (§6).
	HelperName = "evener-fence"
	// HelperVersion is the helper version this build pins (§6: "version 1").
	// A remote helper at any other version is refused fail-closed with
	// `fencing-helper-untrusted`: there is no in-band migration.
	HelperVersion = 1
	// HelperInstallPath is the documented install path (§6), as the spec writes
	// it with the remote shell's own home.
	HelperInstallPath = "~/.local/share/evener/fence"
	// HelperRemotePath is the same path spelled for a remote shell command line,
	// with the remote HOME expanded. The install is out-of-band (S21 bootstrap);
	// this build only names where it lands.
	HelperRemotePath = `"${HOME}/.local/share/evener/fence"`
	// HelperStateDir is where the helper keeps its guard file, lease file, and
	// lock by default; HelperStateEnv overrides it (operators, and the tests'
	// scripted remote).
	HelperStateDir = "~/.local/state/evener/fence"
	// HelperStateEnv is the environment variable overriding HelperStateDir.
	HelperStateEnv = "EVENER_FENCE_STATE"
	// TokenEnv carries the per-invocation refusal token to the helper; the
	// helper clears it for the wrapped command, so only the helper can present
	// it back.
	TokenEnv = "EVENER_FENCE_TOKEN"
	// RefusalPrefix is the marker the helper's refusal reports carry on stderr.
	// A wrapped command's own stderr can therefore never masquerade as a
	// wrapper refusal: the decoder requires the marker and the helper's own
	// refusal exit code (HelperExitMalformed/HelperExitIO/HelperExitRefusal).
	RefusalPrefix = "evener-fence: "
)

// The helper's exit codes: success is 0, a wrapped command exits with its own
// status, and a refusal carries one of these. The Go side gates refusal
// decoding on them so a child that happens to exit with a refusal-shaped
// stderr is still the child's failure.
const (
	// HelperExitMalformed is a malformed request.
	HelperExitMalformed = 64
	// HelperExitIO is a corrupt state file or an I/O failure.
	HelperExitIO = 69
	// HelperExitRefusal is a fencing refusal (stale epoch, pending fence, busy).
	HelperExitRefusal = 75
)

// helperScript carries the helper's bytes. S21's bootstrap ships these bytes to
// the host and converges helperInstalled; this build keeps them in-repo beside
// the protocol that verifies them, so the script and the Go side can never
// drift apart unnoticed (the script tests execute exactly these bytes).
//
//go:embed evener-fence.sh
var helperScript []byte

// HelperScript returns a copy of the helper script's bytes.
func HelperScript() []byte { return bytes.Clone(helperScript) }

// Discriminators the helper gate's refusals ride (§8): the conflict-class
// names the client sees. They are data, never prose: the operator installs the
// pinned helper out-of-band.
const (
	// DiscriminatorHelperAbsent covers helper absent, a remote unable to run
	// the helper, and a lost or unverifiable bootstrap claim (§8). Data names
	// the host plus the pinned version the operator must install.
	DiscriminatorHelperAbsent = "fencing-helper-absent"
	// DiscriminatorHelperUntrusted covers an older, incompatible, or explicitly
	// untrusted helper, naming the distrusted version in place of the absent
	// one.
	DiscriminatorHelperUntrusted = "fencing-helper-untrusted"
)

// HelperProbe is the read-only pre-fence verification's answer: whether the
// helper is present, which version it reports, and whether the caller already
// knows it must not be trusted. §6 requires the verification before the
// kill/wait step, with no remote state written.
type HelperProbe struct {
	// Present reports whether a helper answered at the pinned path.
	Present bool
	// Reported reports whether the helper answered a version this layer could
	// read. A present helper that reported none is the "cannot run the helper"
	// case §6 refuses as absent.
	Reported bool
	// Version is the version number the helper reported, 0 when it reported
	// none this layer can read.
	Version int
	// Untrusted marks a helper the caller explicitly distrusts regardless of
	// its reported version (an operator blocklist, or a version the pin does
	// not admit).
	Untrusted bool
}

// HelperGateError is the typed fail-closed refusal of §6 and §8: a helper absent
// or untrusted refuses before any remote mutation, with no auto-install and no
// in-band migration. It carries the two pieces of data the client renders: the
// host and the pinned helper version the operator must install.
type HelperGateError struct {
	// Host is the host name the refusal names.
	Host string
	// Discriminator is the typed conflict-class discriminator.
	Discriminator string
	// PinnedVersion is the version this build requires.
	PinnedVersion int
	// ObservedVersion is the version the remote reported, 0 when absent or
	// unreadable.
	ObservedVersion int
}

// Error renders the refusal with the host and pinned version, so a log or a
// client surface never has to guess which helper to install.
func (e *HelperGateError) Error() string {
	if e.Discriminator == DiscriminatorHelperAbsent {
		return fmt.Sprintf("host %q refuses: %s — the pinned %s version %d is not installed or cannot run; install it out-of-band (no auto-install, no in-band migration)",
			e.Host, e.Discriminator, HelperName, e.PinnedVersion)
	}
	return fmt.Sprintf("host %q refuses: %s — %s version %d is distrusted or incompatible with the pinned version %d; install the pinned helper out-of-band",
		e.Host, e.Discriminator, HelperName, e.ObservedVersion, e.PinnedVersion)
}

// VerifyHelper applies §6's fail-closed helper gate to one probe. A helper the
// caller cannot confirm at the pinned version refuses; nothing here installs,
// migrates, or proceeds unfenced.
func VerifyHelper(host string, pinned int, probe HelperProbe) error {
	switch {
	case !probe.Present || !probe.Reported:
		// Present but unable to report a version is the "remote cannot run the
		// helper at all" case §6 folds into the absent class: the operator
		// upgrades the remote itself out-of-band.
		return &HelperGateError{
			Host: host, Discriminator: DiscriminatorHelperAbsent, PinnedVersion: pinned, ObservedVersion: probe.Version,
		}
	case probe.Untrusted || probe.Version != pinned:
		return &HelperGateError{
			Host: host, Discriminator: DiscriminatorHelperUntrusted, PinnedVersion: pinned, ObservedVersion: probe.Version,
		}
	}
	return nil
}

// ParseHelperVersion reads the helper's `version` output: one decimal version
// number. Anything else is not a version this layer admits, and the caller
// treats it as an untrusted helper.
func ParseHelperVersion(raw string) (int, bool) {
	version, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || version < 1 {
		return 0, false
	}
	return version, true
}

// shellQuote renders one argument for a POSIX shell command line: single-quoted
// with embedded single quotes escaped. Every value this layer interpolates into
// a remote command goes through it, so a presented epoch or command can never
// become shell syntax.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// Runner runs one remote command and reports its streams and exit status. S18's
// worker supplies the ssh-backed implementation over the deploy pipeline's
// remote exec seam; the tests supply a local adapter over the real helper
// script. err is a transport failure, never a remote refusal.
type Runner interface {
	Run(ctx context.Context, command string) (stdout, stderr string, exitCode int, err error)
}

// PerformResult is one wrapped command's outcome: the child's streams and exit
// status. A nonzero ExitCode is the command's own failure, not a fencing
// refusal; refusals arrive as typed errors.
type PerformResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Wrapper drives the remote helper through a Runner. Its command builders are
// the one place the helper protocol's spelling lives; S18 sequences the
// operations (verify, takeover, bounded kill/wait, advance, then perform) and
// never composes a raw helper invocation itself.
type Wrapper struct {
	// Runner is the remote exec seam. A Wrapper used only to build commands may
	// leave it nil.
	Runner Runner
	// Host names the host for refusals and logs.
	Host string
	// Path overrides the helper's remote path; empty takes HelperRemotePath.
	Path string
}

// remotePath is the helper path this wrapper addresses. The default is the
// trusted constant whose double quotes let the remote shell expand its own
// HOME; a caller-supplied override is single-quoted whole, so a path with
// spaces or metacharacters can never become shell syntax.
func (w Wrapper) remotePath() string {
	if strings.TrimSpace(w.Path) != "" {
		return shellQuote(w.Path)
	}
	return HelperRemotePath
}

// VersionCommand is the read-only self-test round trip §6 requires before the
// kill/wait step ("the next operation's pre-fence verification runs a helper
// self-test round-trip").
func (w Wrapper) VersionCommand() string {
	return w.remotePath() + " version"
}

// StatusCommand reads the guard and lease state without writing anything.
func (w Wrapper) StatusCommand() string {
	return w.remotePath() + " status"
}

// EntriesCommand enumerates the lease file's entries without writing anything.
func (w Wrapper) EntriesCommand() string {
	return w.remotePath() + " entries"
}

// TakeoverCommand builds §4's preemptive fence-takeover for the presented
// epoch: one atomic helper-mediated operation that revokes the superseded
// epoch and takes over the exclusive per-host remote lease before any
// kill/wait.
func (w Wrapper) TakeoverCommand(e Epoch) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s takeover %s %d", w.remotePath(), shellQuote(e.BootID), e.OpSeq), nil
}

// AdvanceCommand builds §4's compare-and-advance for the epoch a takeover
// installed: an older epoch never overwrites a newer one.
func (w Wrapper) AdvanceCommand(e Epoch) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s advance %s %d", w.remotePath(), shellQuote(e.BootID), e.OpSeq), nil
}

// PerformCommand builds one register-fence-perform guarded unit for the
// presented epoch: the wrapper registers the command in the lease file before
// its side effects start, holds the exclusive lease across the guard re-check
// and the command, and refuses server-side when the epoch no longer equals the
// guard.
//
// The returned token is the invocation's refusal token: it is presented to the
// helper's own environment, cleared for the wrapped command, and echoed in any
// refusal the helper writes. A command's stderr can therefore never be mistaken
// for a wrapper refusal — it cannot know the token.
func (w Wrapper) PerformCommand(e Epoch, command string) (string, string, error) {
	if err := e.Validate(); err != nil {
		return "", "", err
	}
	if command == "" {
		return "", "", errors.New("hostfence: a wrapped command carries no command text")
	}
	token, err := newRefusalToken()
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("%s=%s %s perform %s %d %s",
		TokenEnv, shellQuote(token), w.remotePath(), shellQuote(e.BootID), e.OpSeq, shellQuote(command)), token, nil
}

// newRefusalToken mints the per-invocation refusal token.
func newRefusalToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("hostfence: mint a refusal token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// RecheckCommand builds a nonce re-presentation (§9): the verifier hands the
// lease entry's stored identity back to the wrapper, which answers whether a
// matching live holder remains.
func (w Wrapper) RecheckCommand(id string) (string, error) {
	if id == "" || len(id) > MaxBootIDBytes {
		return "", fmt.Errorf("%w: a lease entry id is required", ErrInvalidGuard)
	}
	return fmt.Sprintf("%s recheck %s", w.remotePath(), shellQuote(id)), nil
}

// run is the one place a wrapper command reaches the transport.
func (w Wrapper) run(ctx context.Context, command string) (stdout, stderr string, exit int, err error) {
	if w.Runner == nil {
		return "", "", 0, errors.New("hostfence: no remote runner is configured")
	}
	return w.Runner.Run(ctx, command)
}

// Probe runs the read-only version round trip and classifies its answer. A
// helper that is not there, or that answers something other than a version, is
// reported as absent or untrusted for VerifyHelper to refuse — never as an
// empty success.
func (w Wrapper) Probe(ctx context.Context) (HelperProbe, error) {
	stdout, _, exit, err := w.run(ctx, w.VersionCommand())
	if err != nil {
		return HelperProbe{}, err
	}
	if exit != 0 {
		return HelperProbe{Present: false}, nil
	}
	version, ok := ParseHelperVersion(stdout)
	if !ok {
		// A helper that answers but reports no readable version cannot run the
		// protocol: the absent class, refused before any mutation.
		return HelperProbe{Present: true}, nil
	}
	return HelperProbe{Present: true, Reported: true, Version: version}, nil
}

// Verify runs §6's helper presence-plus-version check and returns the typed
// refusal when the helper is absent or untrusted. It is the gate every fencing
// operation runs first; nothing this package does auto-installs a helper or
// advances a guard over an unverified one.
func (w Wrapper) Verify(ctx context.Context) error {
	probe, err := w.Probe(ctx)
	if err != nil {
		return err
	}
	return VerifyHelper(w.Host, HelperVersion, probe)
}

// Status reads the guard and lease state.
func (w Wrapper) Status(ctx context.Context) (Status, error) {
	return w.guardCall(ctx, w.StatusCommand())
}

// Takeover runs the preemptive fence-takeover and returns the state it landed.
func (w Wrapper) Takeover(ctx context.Context, e Epoch) (Status, error) {
	command, err := w.TakeoverCommand(e)
	if err != nil {
		return Status{}, err
	}
	return w.guardCall(ctx, command)
}

// Advance runs the compare-and-advance and returns the state it landed.
func (w Wrapper) Advance(ctx context.Context, e Epoch) (Status, error) {
	command, err := w.AdvanceCommand(e)
	if err != nil {
		return Status{}, err
	}
	return w.guardCall(ctx, command)
}

// Entries enumerates the lease file's entries for verification before any kill
// or clear.
func (w Wrapper) Entries(ctx context.Context) ([]LeaseEntry, error) {
	stdout, stderr, exit, err := w.run(ctx, w.EntriesCommand())
	if err != nil {
		return nil, err
	}
	if exit != 0 {
		return nil, w.refusal(stderr, exit)
	}
	return DecodeEntries([]byte(stdout))
}

// Recheck re-presents one lease entry's identity to the wrapper.
func (w Wrapper) Recheck(ctx context.Context, id string) (Recheck, error) {
	command, err := w.RecheckCommand(id)
	if err != nil {
		return Recheck{}, err
	}
	stdout, stderr, exit, err := w.run(ctx, command)
	if err != nil {
		return Recheck{}, err
	}
	if exit != 0 {
		return Recheck{}, w.refusal(stderr, exit)
	}
	return DecodeRecheck([]byte(stdout))
}

// Perform runs one register-fence-perform guarded unit. A nonzero ExitCode with
// no decoded refusal is the wrapped command's own failure; a refusal is the
// typed error and means the command never ran (or was aborted before its
// irreversible step).
func (w Wrapper) Perform(ctx context.Context, e Epoch, command string) (PerformResult, error) {
	full, token, err := w.PerformCommand(e, command)
	if err != nil {
		return PerformResult{}, err
	}
	stdout, stderr, exit, err := w.run(ctx, full)
	if err != nil {
		return PerformResult{}, err
	}
	result := PerformResult{Stdout: stdout, Stderr: stderr, ExitCode: exit}
	if exit == 0 {
		return result, nil
	}
	// A refusal is the helper's own report: its marker, at one of its own exit
	// codes. Anything else — however refusal-shaped — is the wrapped command's
	// own output and failure.
	if exit == HelperExitMalformed || exit == HelperExitIO || exit == HelperExitRefusal {
		if refusal := decodeRefusalBytes([]byte(stderr), token); refusal != nil {
			return PerformResult{}, refusal
		}
	}
	return result, nil
}

// guardCall runs one guard-state command and decodes its answer, mapping a
// refusal to the typed error.
func (w Wrapper) guardCall(ctx context.Context, command string) (Status, error) {
	stdout, stderr, exit, err := w.run(ctx, command)
	if err != nil {
		return Status{}, err
	}
	if exit != 0 {
		return Status{}, w.refusal(stderr, exit)
	}
	return DecodeStatus([]byte(stdout))
}

// refusal maps a failing helper invocation to the typed refusal. A helper
// failure without a decodable refusal is a transport-level error, never a
// silent success.
func (w Wrapper) refusal(stderr string, exit int) error {
	// The non-perform operations run no child, so no invocation token is in
	// play: any well-formed prefixed refusal at a helper exit code is theirs.
	if refusal := decodeRefusalBytes([]byte(stderr), ""); refusal != nil {
		return refusal
	}
	return fmt.Errorf("hostfence: helper on host %q exited %d: %s", w.Host, exit, strings.TrimSpace(stderr))
}

// ErrStaleEpoch reports a step whose presented epoch no longer equals the
// guard (§4): the helper refused server-side and the operation must abort.
var ErrStaleEpoch = errors.New("hostfence: stale fencing epoch")

// ErrFenced reports a mutating step refused while a fence-takeover is pending:
// until the guard advance lands the new operation performs no mutating remote
// step (§4).
var ErrFenced = errors.New("hostfence: a fence-takeover is pending")

// ErrHelperIO reports a helper I/O failure or corrupt state: the helper refused
// before (or after killing) the wrapped command, so the command must be
// treated as never having run.
var ErrHelperIO = errors.New("hostfence: the remote wrapper hit an I/O failure")

// ErrHelperBusy reports the helper's exclusive lease could not be taken within
// its bounded wait: another wrapper operation holds it. The caller's own
// bounded contexts decide what a stall means (S18's kill/wait deadlines).
var ErrHelperBusy = errors.New("hostfence: the remote lease is held")

// ErrStateCorrupt reports a guard or lease file outside the helper's schema.
// The helper fails closed on it — no mutation, no clear.
var ErrStateCorrupt = errors.New("hostfence: the helper state is corrupt")

// ErrMalformed reports a helper request or response outside the protocol: an
// unknown operation, a missing epoch, an undecodable refusal.
var ErrMalformed = errors.New("hostfence: malformed helper request or response")

// HelperRefusalError is the helper's one-object refusal report. Refused is the
// marker that distinguishes a wrapper refusal from a wrapped command's own
// stderr, Error is the closed reason set this layer maps to typed errors, and
// Token is the invocation's refusal token — a value only the helper and the
// invoking caller know, so a wrapped command can never forge one.
type HelperRefusalError struct {
	Version int    `json:"version"`
	Refused bool   `json:"refused"`
	Reason  string `json:"error"`
	Detail  string `json:"detail"`
	Token   string `json:"token"`
}

// Error renders the refusal with its reason.
func (r *HelperRefusalError) Error() string {
	if strings.TrimSpace(r.Detail) == "" {
		return "hostfence: remote wrapper refused: " + r.Reason
	}
	return fmt.Sprintf("hostfence: remote wrapper refused: %s (%s)", r.Reason, r.Detail)
}

// Unwrap maps the closed reason set to the sentinel a caller branches on.
func (r *HelperRefusalError) Unwrap() error {
	switch r.Reason {
	case "stale-epoch":
		return ErrStaleEpoch
	case "fenced":
		return ErrFenced
	case "busy":
		return ErrHelperBusy
	case "state-corrupt":
		return ErrStateCorrupt
	case "io-error":
		return ErrHelperIO
	case "malformed":
		return ErrMalformed
	default:
		return nil
	}
}

// DecodeRefusal decodes the helper's refusal report. Anything that is not one
// well-formed refusal object — including an object whose reason is outside the
// closed set — decodes to ErrMalformed, never to a zero refusal a caller might
// read as success.
func DecodeRefusal(raw []byte) error {
	if refusal := decodeRefusalBytes(raw, ""); refusal != nil {
		return refusal
	}
	return fmt.Errorf("%w: not a helper refusal: %s", ErrMalformed, strings.TrimSpace(string(raw)))
}

// decodeRefusalBytes decodes a refusal, returning nil when the bytes are not
// one. The helper's marker is required and stripped first, and the stream is
// searched from its end: a wrapped command's own output before a genuine
// trailing refusal must not hide it. token, when non-empty, is the invocation's
// refusal token the report must carry exactly.
func decodeRefusalBytes(raw []byte, token string) *HelperRefusalError {
	lines := strings.Split(string(raw), "\n")
	for _, line := range slices.Backward(lines) {
		body, marked := strings.CutPrefix(strings.TrimSpace(line), RefusalPrefix)
		if !marked {
			continue
		}
		var refusal HelperRefusalError
		if err := decodeStrict([]byte(strings.TrimSpace(body)), &refusal); err != nil {
			continue
		}
		if !refusal.Refused || refusal.Version != ProtocolVersion || refusal.Unwrap() == nil {
			continue
		}
		if token != "" && refusal.Token != token {
			continue
		}
		return &refusal
	}
	return nil
}
