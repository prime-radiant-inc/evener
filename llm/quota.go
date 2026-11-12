package llm

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/execsupport/fsatomic"
)

// QuotaLane orders waiters competing for one provider's next attempt. Roots
// come first: when a cooldown opens, a waiting root turn claims the slot and
// waiting delegates take the next one.
type QuotaLane int

const (
	// QuotaLaneRoot is a top-level session's lane. It is the zero value, so a
	// policy that never sets a lane competes as a root.
	QuotaLaneRoot QuotaLane = iota
	// QuotaLaneDelegate is a subagent's lane. It yields every opening to a
	// waiting root and waits longer for its own attempt.
	QuotaLaneDelegate
)

// QuotaWait is one rate-limited attempt group's claim on a provider's shared
// cooldown.
type QuotaWait struct {
	// Key names the shared quota domain, the provider instance. Empty
	// disables coordination.
	Key string
	// Lane is this session's priority; the zero value is root.
	Lane QuotaLane
	// Cooldown is the minimum wait this group asks for. A longer cooldown
	// another session already recorded wins. It is the full retry delay even
	// when Budget is shorter, so other sessions learn the provider's real
	// cooldown instead of this caller's clipped wait.
	Cooldown time.Duration
	// Budget bounds this wait. Callers pass the same remaining wall/run
	// budget they use for local backoff, so a shared cooldown can never carry
	// an attempt group past the run's real budget. Zero means unbounded.
	Budget time.Duration
}

const (
	// quotaProbeHold keeps a claimed slot closed for a moment, so an opening
	// admits one attempt rather than every waiter waking at once.
	quotaProbeHold = time.Second
	// quotaPollInterval is how long a waiter sleeps while a higher-priority
	// waiter owns the opening, so a root that arrives during the wait is seen
	// without a cross-process notification channel.
	quotaPollInterval = 250 * time.Millisecond
	// quotaWaiterGrace is added to a waiter's lease beyond the sleep it is
	// about to take. It only needs to cover scheduling and lock contention; a
	// process that dies while waiting is pruned once the grace passes.
	quotaWaiterGrace = 5 * time.Second
)

// errQuotaWaitExhausted reports that the shared cooldown outlived the attempt
// group's own budget. The retry loop maps it to the existing budget-exhausted
// error, which is the run-budget fatal when a run owns the deadline.
var errQuotaWaitExhausted = errors.New("quota cooldown outlived the retry budget")

// QuotaCoordinator shares one provider instance's rate-limit cooldown across
// sessions and processes on this host. State lives in one <provider>.json file
// per provider under Dir, guarded by an advisory <provider>.json.lock file;
// there is no daemon and no in-process state to diverge from what another
// process sees.
//
// The coordinator is best effort. If Dir is unusable, Wait falls back to the
// session's own cooldown, exactly the behavior a policy without a coordinator
// has. It never turns a model call into an error by itself.
type QuotaCoordinator struct {
	dir string
	// now is the clock the state file's timestamps are measured on. Nil uses
	// time.Now; tests inject a fake clock.
	now func() time.Time
}

// NewQuotaCoordinator returns a coordinator keeping shared state under dir.
func NewQuotaCoordinator(dir string) *QuotaCoordinator {
	return &QuotaCoordinator{dir: dir}
}

func (c *QuotaCoordinator) clock() time.Time {
	if c != nil && c.now != nil {
		return c.now()
	}
	return time.Now()
}

type quotaState struct {
	CooldownUntil int64         `json:"cooldown_until,omitempty"`
	NextSeq       int64         `json:"next_seq,omitempty"`
	Waiters       []quotaWaiter `json:"waiters,omitempty"`
}

type quotaWaiter struct {
	Token   string    `json:"token"`
	Lane    QuotaLane `json:"lane"`
	Seq     int64     `json:"seq"`
	Expires int64     `json:"expires"`
}

// Wait records this group's cooldown in the shared state and blocks until the
// coordinator admits its next attempt, the group's budget is spent, or ctx
// ends. A nil error means the caller may dispatch exactly one attempt; the
// admitted slot is held closed for quotaProbeHold so another waiter cannot
// dispatch at the same opening.
func (c *QuotaCoordinator) Wait(ctx context.Context, req QuotaWait, sleep SleepFunc) error {
	if sleep == nil {
		sleep = DefaultSleep
	}
	if c == nil || strings.TrimSpace(c.dir) == "" || strings.TrimSpace(req.Key) == "" {
		return quotaSleepWithin(ctx, sleep, req.Cooldown, req.Budget)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	token := newQuotaToken()
	waited := time.Duration(0)
	reportStart := c.clock()
	reportCtx, cancelReport := quotaContext(ctx, req.Budget, waited)
	err := c.reportAndEnqueue(reportCtx, req, token)
	cancelReport()
	waited += max(c.clock().Sub(reportStart), 0)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		// Coordination is best effort: a state root that cannot be used keeps
		// the per-session cooldown instead of failing the model call.
		remaining, ok := quotaBudgetRemaining(req.Budget, waited)
		if !ok {
			return errQuotaWaitExhausted
		}
		return quotaSleepWithin(ctx, sleep, req.Cooldown, remaining)
	}
	claimed := false
	defer func() {
		if !claimed {
			// Cleanup is bound by what is left of the retry budget too, so a
			// suspended lock holder cannot prolong a turn that is already done
			// waiting.
			cleanupCtx, cancelCleanup := quotaContext(ctx, req.Budget, waited)
			defer cancelCleanup()
			_ = c.dropWaiter(cleanupCtx, req.Key, token) // best-effort lease cleanup
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining, ok := quotaBudgetRemaining(req.Budget, waited)
		if !ok {
			return errQuotaWaitExhausted
		}
		stepStart := c.clock()
		stepCtx, cancelStep := quotaContext(ctx, req.Budget, waited)
		claimedNow, next, err := c.step(stepCtx, req.Key, token, req.Lane, remaining)
		cancelStep()
		waited += max(c.clock().Sub(stepStart), 0)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				// A canceled wait must not turn into a dispatch.
				return ctxErr
			}
			// The state store became unusable mid-wait. The attempt has already
			// announced its retry, so wait out the rest of this session's own
			// cooldown rather than dispatching immediately.
			slept, waitErr := waitOutCooldown(ctx, sleep, req, waited)
			waited += slept
			return waitErr
		}
		if claimedNow {
			claimed = true
			return nil
		}
		if req.Budget > 0 {
			left, ok := quotaBudgetRemaining(req.Budget, waited)
			if !ok {
				return errQuotaWaitExhausted
			}
			if next > left {
				next = left
			}
		}
		if err := sleep(ctx, next); err != nil {
			return err
		}
		waited += next
	}
}

// quotaBudgetRemaining reports how much of budget is left after spent. An
// unbounded budget always reports ok. A bounded budget that is fully spent
// reports ok=false, which the caller maps to errQuotaWaitExhausted.
func quotaBudgetRemaining(budget, spent time.Duration) (time.Duration, bool) {
	if budget <= 0 {
		return 0, true
	}
	if spent >= budget {
		return 0, false
	}
	return budget - spent, true
}

// quotaContext bounds one coordination step -- a lock acquisition and its
// state write -- by the part of budget still unspent. An unbounded budget
// leaves the caller's context alone. A spent budget yields an already-expired
// context, so even cleanup cannot block on a suspended lock holder.
func quotaContext(ctx context.Context, budget, spent time.Duration) (context.Context, context.CancelFunc) {
	if budget <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, max(budget-spent, 0))
}

// waitOutCooldown sleeps whatever remains of this group's own cooldown after a
// state-store failure mid-wait, clipped to the budget left for the wait, and
// reports how long it slept so cleanup is bounded by what is left afterward. A
// retry must not dispatch before the backoff it already announced.
func waitOutCooldown(ctx context.Context, sleep SleepFunc, req QuotaWait, waited time.Duration) (time.Duration, error) {
	left := req.Cooldown - waited
	if left <= 0 {
		return 0, nil
	}
	if req.Budget > 0 {
		remaining, ok := quotaBudgetRemaining(req.Budget, waited)
		if !ok {
			return 0, errQuotaWaitExhausted
		}
		if left > remaining {
			left = remaining
		}
	}
	if err := sleep(ctx, left); err != nil {
		return 0, err
	}
	return left, nil
}

// reportAndEnqueue pushes this group's cooldown and joins the waiter queue in
// one locked write. Joining before waiting is what lets a delegate see a root
// that arrives later.
func (c *QuotaCoordinator) reportAndEnqueue(ctx context.Context, req QuotaWait, token string) error {
	return c.withState(ctx, req.Key, func(st *quotaState, now time.Time) error {
		pruneQuotaWaiters(st, now)
		if until := now.Add(req.Cooldown).UnixNano(); until > st.CooldownUntil {
			st.CooldownUntil = until
		}
		enqueueQuotaWaiter(st, token, req.Lane, now)
		return nil
	})
}

// step makes one admission decision. It returns claimed when this waiter owns
// the opening, or the next sleep duration while another waiter or the
// cooldown itself holds the slot. The waiter's lease is extended to cover the
// sleep it is about to take, so a slow-but-alive waiter is not pruned.
func (c *QuotaCoordinator) step(ctx context.Context, key, token string, lane QuotaLane, remaining time.Duration) (bool, time.Duration, error) {
	var claimed bool
	var next time.Duration
	err := c.withState(ctx, key, func(st *quotaState, now time.Time) error {
		pruneQuotaWaiters(st, now)
		idx := quotaWaiterIndex(st, token)
		if idx < 0 {
			// Our lease lapsed while we slept. Rejoin at the tail instead of
			// treating a lost lease as an admission.
			idx = enqueueQuotaWaiter(st, token, lane, now)
		}
		wait := time.Duration(st.CooldownUntil - now.UnixNano())
		if wait <= 0 {
			if quotaFirstWaiter(st) == token {
				removeQuotaWaiter(st, idx)
				st.CooldownUntil = now.Add(quotaProbeHold).UnixNano()
				claimed = true
				return nil
			}
			wait = quotaPollInterval
		}
		if remaining > 0 && wait > remaining {
			wait = remaining
		}
		st.Waiters[idx].Expires = now.Add(wait + quotaWaiterGrace).UnixNano()
		next = wait
		return nil
	})
	return claimed, next, err
}

func (c *QuotaCoordinator) dropWaiter(ctx context.Context, key, token string) error {
	return c.withState(ctx, key, func(st *quotaState, now time.Time) error {
		pruneQuotaWaiters(st, now)
		if idx := quotaWaiterIndex(st, token); idx >= 0 {
			removeQuotaWaiter(st, idx)
		}
		return nil
	})
}

// withState serializes one read-modify-write of a provider's state file under
// an advisory whole-file lock. Every process on the host opens its own
// descriptor, so the lock arbitrates across processes as well as goroutines.
func (c *QuotaCoordinator) withState(ctx context.Context, key string, fn func(*quotaState, time.Time) error) error {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	path := c.statePath(key)
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close() //nolint:errcheck // the lock is advisory and the fd is done
	if err := lockQuotaFile(ctx, lock); err != nil {
		return err
	}
	defer unlockQuotaFile(lock) //nolint:errcheck // closing the descriptor releases it too
	var st quotaState
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if jsonErr := json.Unmarshal(data, &st); jsonErr != nil {
			// A torn or foreign file cannot be trusted; one clean overwrite
			// restores a usable state.
			st = quotaState{}
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if err := fn(&st, c.clock()); err != nil {
		return err
	}
	data, err = json.Marshal(st)
	if err != nil {
		return err
	}
	return fsatomic.WriteWithoutSync(path, data, 0o600)
}

// statePath names a provider instance's JSON state file. The .json suffix keeps
// a state path from ever colliding with another key's ".lock" guard file:
// without it, key "openai.lock"'s state would replace "openai"'s locked inode.
func (c *QuotaCoordinator) statePath(key string) string {
	return filepath.Join(c.dir, quotaFileName(key)+".json")
}

func pruneQuotaWaiters(st *quotaState, now time.Time) {
	st.Waiters = slices.DeleteFunc(st.Waiters, func(w quotaWaiter) bool {
		return w.Expires <= now.UnixNano()
	})
}

func quotaWaiterIndex(st *quotaState, token string) int {
	return slices.IndexFunc(st.Waiters, func(w quotaWaiter) bool { return w.Token == token })
}

func removeQuotaWaiter(st *quotaState, idx int) {
	st.Waiters = slices.Delete(st.Waiters, idx, idx+1)
}

// enqueueQuotaWaiter appends a waiter and returns its index.
func enqueueQuotaWaiter(st *quotaState, token string, lane QuotaLane, now time.Time) int {
	st.NextSeq++
	st.Waiters = append(st.Waiters, quotaWaiter{
		Token:   token,
		Lane:    lane,
		Seq:     st.NextSeq,
		Expires: now.Add(quotaWaiterGrace).UnixNano(),
	})
	return len(st.Waiters) - 1
}

// quotaFirstWaiter names the waiter allowed to claim the next opening: lowest
// lane first (root before delegate), then registration order.
func quotaFirstWaiter(st *quotaState) string {
	if len(st.Waiters) == 0 {
		return ""
	}
	first := slices.MinFunc(st.Waiters, func(a, b quotaWaiter) int {
		if lane := cmp.Compare(a.Lane, b.Lane); lane != 0 {
			return lane
		}
		return cmp.Compare(a.Seq, b.Seq)
	})
	return first.Token
}

// quotaSleepWithin is the no-coordination path: the caller's own cooldown,
// clipped to its budget so a wall budget still bounds the wait.
func quotaSleepWithin(ctx context.Context, sleep SleepFunc, cooldown, budget time.Duration) error {
	if budget > 0 && cooldown > budget {
		cooldown = budget
	}
	if cooldown <= 0 {
		return nil
	}
	return sleep(ctx, cooldown)
}

// quotaFileName maps a provider instance to a filename without allowing the
// key to escape Dir.
func quotaFileName(key string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, key)
	if name == "" {
		return "_"
	}
	return name
}

func newQuotaToken() string {
	return rand.Text()
}
