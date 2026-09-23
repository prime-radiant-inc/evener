package valueexpr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"primeradiant.com/evener/internal/procgroup"
)

// A command expression's output is a credential: it is cached in memory
// exactly like a resolved key and never logged. Failures carry only the exit
// status and the command's own stderr, never its stdout, and CommandError
// never names the command — the author knows their config; a warning naming
// the field is the caller's job.

const (
	// refreshMargin is how far before a token's exp claim a re-mint happens,
	// so a token is never used in the last minute of its life.
	refreshMargin = 60 * time.Second
	// defaultTTL is how long a value with no readable expiry claim is
	// cached; long-lived keys cost one cheap re-run per window, minted
	// tokens cost one per minute-of-margin.
	defaultTTL = 5 * time.Minute
	// maxOutput caps what a command may print before it is refused, so a
	// runaway command cannot balloon memory.
	maxOutput = 1 << 20
	// defaultCommandTimeout is the timeout the command seam starts at and
	// ResetForTest restores; naming it keeps the two from drifting apart.
	defaultCommandTimeout = 30 * time.Second
	// defaultDrainGrace is the WaitDelay the drain seam starts at and
	// ResetForTest restores: the bound a shell that exited cleanly waits
	// for a descendant that still holds the captured pipes.
	defaultDrainGrace = 5 * time.Second
)

// commandTimeout bounds one command run; a test seam makes the timeout itself
// testable without a real 30-second wait.
var commandTimeout = defaultCommandTimeout

// drainGrace bounds the post-exit drain (the WaitDelay) the same way
// commandTimeout bounds the run, and is a test seam for the same reason:
// the ErrWaitDelay path is reachable only when the grace is strictly
// shorter than the run budget, which production's 30s/5s pair is and no
// small test pair can be without shrinking the grace too.
var drainGrace = defaultDrainGrace

// Now is the clock seam; RunCommand is the executor seam, initialized to the
// real shell invocation. Hosts' tests swap these to keep unit tests off real
// processes; ResetForTest clears the cache and restores both.
var (
	Now        = time.Now
	RunCommand = realRunCommand
)

// Result is one minted value and the instant it stops being fresh.
type Result struct {
	Value     string
	ExpiresAt time.Time
}

// CommandError reports one failed command run. Status is the exit status
// (0 when the failure had none, such as a timeout or empty output) and Detail
// is the command's own stderr first line, truncated, or a reason phrase.
type CommandError struct {
	Status  int
	Timeout bool
	Detail  string
}

func (e *CommandError) Error() string {
	switch {
	case e.Timeout:
		return "command timed out"
	case e.Status != 0 && e.Detail != "":
		return fmt.Sprintf("command exited with status %d: %s", e.Status, e.Detail)
	case e.Status != 0:
		return fmt.Sprintf("command exited with status %d", e.Status)
	case e.Detail != "":
		return e.Detail
	default:
		return "command failed"
	}
}

// The cache is keyed by the command text: instances and surfaces that share
// a command share one mint. The singleflight group collapses concurrent
// callers onto one run per command.
var (
	evaluateMu sync.Mutex
	cache      = map[string]Result{}
	mintGroup  singleflight.Group
)

// ResetForTest clears the cache and restores the seams; nothing else may
// touch the package's mutable state.
func ResetForTest() {
	evaluateMu.Lock()
	cache = map[string]Result{}
	evaluateMu.Unlock()
	RunCommand = realRunCommand
	Now = time.Now
	commandTimeout = defaultCommandTimeout
	drainGrace = defaultDrainGrace
}

// evaluate returns the command's value, cached until it stops being fresh. A
// failed run is returned uncached, so the next call retries.
func evaluate(command string) (Result, error) {
	// Every freshness judgment reads the clock under the cache lock, never
	// before it: a caller that waits — on the lock, or behind a flight —
	// must not carry an earlier instant past an expiry the wait crossed.
	evaluateMu.Lock()
	if res, ok := cache[command]; ok && Now().Before(res.ExpiresAt) {
		evaluateMu.Unlock()
		return res, nil
	}
	evaluateMu.Unlock()

	res, err, _ := mintGroup.Do(command, func() (any, error) {
		// Re-check under the flight: a caller that queued behind a finished
		// leader may find the leader's result already cached and fresh.
		evaluateMu.Lock()
		if cached, ok := cache[command]; ok && Now().Before(cached.ExpiresAt) {
			evaluateMu.Unlock()
			return cached, nil
		}
		evaluateMu.Unlock()
		minted, err := mint(command)
		evaluateMu.Lock()
		if err == nil {
			cache[command] = minted
		}
		// Pruning rides the flight's return, success or failure: an
		// expired entry holds a secret nobody can be served again, so it
		// leaves memory with the next flight that ran, and a stream of
		// failing mints must not pin it forever. The prune clock is read
		// under the lock like every other judgment.
		prune := Now()
		for k, v := range cache {
			if !prune.Before(v.ExpiresAt) {
				delete(cache, k)
			}
		}
		evaluateMu.Unlock()
		return minted, err
	})
	if err != nil {
		return Result{}, err
	}
	return res.(Result), nil
}

func mint(command string) (Result, error) {
	raw, err := RunCommand(command)
	if err != nil {
		return Result{}, err
	}
	// The freshness clock starts when the command finishes, not when the
	// resolve began: a slow command must not eat into the value's own TTL.
	now := Now()
	value := strings.TrimSpace(raw)
	if value == "" {
		return Result{}, &CommandError{Detail: "command produced no output"}
	}
	if exp, ok := tokenExpiry(value); ok {
		fresh := exp.Add(-refreshMargin)
		if !fresh.After(now) {
			// A token at or past its refresh margin is dead on arrival:
			// handing it out would send a credential the cache already
			// considers expired, so the mint fails and nothing is cached.
			return Result{}, &CommandError{Detail: "token at or past its refresh margin"}
		}
		return Result{Value: value, ExpiresAt: fresh}, nil
	}
	// A value with no readable expiry claim stays fresh for the default TTL,
	// anchored at completion.
	return Result{Value: value, ExpiresAt: now.Add(defaultTTL)}, nil
}

// tokenExpiry reads the exp claim out of a three-segment JWT payload, the
// shape every OIDC id token has. Anything else is not an expiry-bearing
// token, and a non-positive or out-of-range exp counts as none.
func tokenExpiry(value string) (time.Time, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, false
	}
	// An exp outside [1, 2^63) is no expiry at all: the float→int
	// conversion out of range is implementation-defined and on amd64
	// yields MinInt64, and a fractional exp below 1 truncates to a
	// non-positive epoch. 2^63 is exactly representable as a float64, so
	// the upper bound is the exclusive comparison.
	if claims.Exp < 1 || claims.Exp >= 9223372036854775808.0 {
		return time.Time{}, false
	}
	return time.Unix(int64(claims.Exp), 0), true
}

// realRunCommand runs command through the host shell with the process
// environment, a closed stdin (a prompting command reads EOF instead of
// hanging), and no TTY. It returns the raw stdout; trimming the value and
// refusing empty output is the evaluator's job, so every executor behaves
// uniformly — with one exception: a run that printed nothing to stdout but
// complained on stderr reports the complaint as its failure, because the
// author debugging an empty mint needs the command's own diagnosis, not a
// bare "no output". Extracting the exact value is the command's job, which
// is why gateway recipes pipe through what they need.
func realRunCommand(command string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	shell, flag := "sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/c"
	}
	cmd := exec.CommandContext(ctx, shell, flag, command)
	// The command runs in its own process group: the deadline kill must
	// reach the whole tree, because a killed shell leaves orphaned children
	// holding the captured pipes, and Wait blocks until they close. os/exec
	// calls Cancel at the deadline, before the process is reaped, so the pid
	// still names our child then.
	cmd.SysProcAttr = procgroup.SysProcAttr()
	// os/exec invokes Cancel only from the goroutine Start spawns, so
	// Process is always set; the nil check keeps the kill safe in this
	// closure without depending on the stdlib's internal contract.
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			procgroup.Kill(cmd.Process.Pid)
		}
		return nil
	}
	// WaitDelay makes the timeout a hard bound: a descendant that escaped
	// the process group and still holds the captured pipes cannot keep Wait
	// — and with it a request or a session start — blocked past the budget.
	// The drain grace stays a small fraction of the command budget, so the
	// whole run costs about one timeout, not two: the caller's ceiling is
	// the deadline plus the grace, never a second deadline.
	cmd.WaitDelay = min(commandTimeout, drainGrace)
	var stdout cappedBuffer
	var stderr cappedBuffer
	stdout.max = maxOutput
	stderr.max = 4 * 1024
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		// A spawn failure names its own cause; it cannot carry output.
		return "", &CommandError{Detail: firstLine(err.Error())}
	}
	err := cmd.Wait()
	// The group the command ran in dies with the run, success or failure:
	// a mint command is not a daemon launcher, and on the failure paths
	// Cancel never ran (os/exec calls it only from the Context's watcher,
	// and no deadline may have fired) while os/exec reports the shell's
	// own exit status in preference to the drain's ErrWaitDelay, so the
	// kill cannot live on any one branch. The post-reap cleanup is
	// group-only: on platforms with groups it signals the raw pgid the
	// reaped shell left named (the group holds the pgid while any member
	// lives, and a recycled pid needs a full pid-space wrap inside one
	// command run — the microscopic race devtool's Stop documents and
	// accepts, because closing it fully would need waitid(WNOWAIT),
	// which pure Go doesn't expose); where no groups exist it is a
	// no-op, because the direct child is gone and a pid the reaper
	// released can name any process.
	procgroup.KillGroupAfterReap(cmd.Process.Pid)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", &CommandError{Timeout: true}
		}
		if errors.Is(err, exec.ErrWaitDelay) {
			return "", &CommandError{Detail: firstLine(err.Error())}
		}
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return "", &CommandError{Status: exit.ExitCode(), Detail: firstLine(stderr.String())}
		}
		return "", &CommandError{Detail: firstLine(err.Error())}
	}
	if stdout.overflowed() {
		return "", &CommandError{Detail: "command output exceeds 1 MiB"}
	}
	// Whitespace-only stdout is no output — the value contract trims it to
	// nothing — so a command that only emitted blanks still hands its stderr
	// complaint to the diagnosis instead of the generic no-output error.
	if strings.TrimSpace(stdout.String()) == "" && stderr.String() != "" {
		return "", &CommandError{Detail: firstLine(stderr.String())}
	}
	return stdout.String(), nil
}

// cappedBuffer keeps the first bytes of a command's stream — max, fixed at
// construction — and notes when the stream ran past the cap, so a runaway
// command cannot balloon memory and a capped stdout is refused rather than
// silently truncated into a credential.
type cappedBuffer struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) <= room {
			c.buf.Write(p)
			return len(p), nil
		}
		c.buf.Write(p[:room])
	}
	c.over = true
	return len(p), nil
}

func (c *cappedBuffer) overflowed() bool { return c.over }

func (c *cappedBuffer) String() string { return c.buf.String() }

// firstLine keeps a stream's first line, trimmed and capped, so a chatty
// command cannot flood a warning.
func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 200 {
		return string(r[:200])
	}
	return s
}
