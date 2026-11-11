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
	// another session already recorded wins.
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
// sessions and processes on this host. State lives in one JSON file per
// provider under Dir, guarded by an advisory file lock; there is no daemon and
// no in-process state to diverge from what another process sees.
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
	if err := c.reportAndEnqueue(ctx, req, token); err != nil {
		// Coordination is best effort: a state root that cannot be used keeps
		// the per-session cooldown instead of failing the model call.
		return quotaSleepWithin(ctx, sleep, req.Cooldown, req.Budget)
	}
	claimed := false
	defer func() {
		if !claimed {
			_ = c.dropWaiter(ctx, req.Key, token) // best-effort lease cleanup
		}
	}()
	waited := time.Duration(0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if req.Budget > 0 && waited >= req.Budget {
			return errQuotaWaitExhausted
		}
		remaining := time.Duration(0)
		if req.Budget > 0 {
			remaining = req.Budget - waited
		}
		claimedNow, next, err := c.step(ctx, req.Key, token, req.Lane, remaining)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				// A canceled wait must not turn into a dispatch.
				return ctxErr
			}
			// The state store became unusable mid-wait. The attempt has
			// already announced its retry; dispatch it rather than blocking
			// the caller on quota bookkeeping.
			return nil
		}
		if claimedNow {
			claimed = true
			return nil
		}
		if err := sleep(ctx, next); err != nil {
			return err
		}
		waited += next
	}
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
	path := filepath.Join(c.dir, quotaFileName(key))
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
