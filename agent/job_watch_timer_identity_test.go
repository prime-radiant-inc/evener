package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

func TestConfigureWatch_TimersCoexistAsSeparateWatches(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	a, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 300, Note: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 300, Note: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if a.WatchID == b.WatchID || b.ReplacedExisting {
		t.Fatalf("identical timer creates must be two watches: %+v %+v", a, b)
	}
	ev, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", Events: []string{"assistant.tool"}})
	if err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	live := len(jm.watches)
	jm.mu.Unlock()
	if live != 3 || ev.ReplacedExisting {
		t.Fatalf("timers must not collide with a self event watch: live=%d ev=%+v", live, ev)
	}
}

func TestConfigureWatch_TimerCapIsEightPerManager(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	for i := range maxLiveTimers {
		if _, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 60}); err != nil {
			t.Fatalf("timer %d: %v", i+1, err)
		}
	}
	_, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 60})
	if err == nil || !strings.Contains(err.Error(), "too many timers (8 live); clear one first") {
		t.Fatalf("ninth timer: err = %v", err)
	}
}

func TestConfigureWatch_ConcurrentCreatesAtTheCapAdmitExactlyOne(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	for range maxLiveTimers - 1 {
		if _, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 60}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 60})
		})
	}
	wg.Wait()
	failures := 0
	for _, err := range errs {
		if err != nil {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("exactly one of two concurrent creates at the cap may succeed; failures=%d errs=%v", failures, errs)
	}
}

func TestWatchKeyMatchesClearRequest_SlotIsExact(t *testing.T) {
	t.Parallel()
	timer := watchKey{VisibleSessionID: "s", Target: "caller", Slot: "w1"}
	request := watchKey{VisibleSessionID: "s", Target: "caller"}
	if watchKeyMatchesClearRequest(timer, request) {
		t.Fatal("a slot-less clear request must not match a timer")
	}
	if !watchKeyMatchesClearRequest(timer, watchKey{VisibleSessionID: "s", Target: "caller", Slot: "w1"}) {
		t.Fatal("an exact slot must match")
	}
}

func TestConfigureWatch_TimerCreateDoesNotSweepOtherWatches(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	if _, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", Events: []string{"assistant.tool"}}); err != nil {
		t.Fatal(err)
	}
	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", AfterSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if res.ReplacedExisting {
		t.Fatal("a timer create must never report replacing the self event watch")
	}
}

// TestConfigureWatch_TimerConfigCarriesItsSlotAndFields pins the one-id rule: a
// timer's key Slot, its config slot and watchID, and the id the caller sees are
// all the same value. It also pins the timer fields the config carries and the
// two config-side key predicates that compare the slot.
func TestConfigureWatch_TimerConfigCarriesItsSlotAndFields(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	repeating, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 300, Note: "n"})
	if err != nil {
		t.Fatal(err)
	}
	oneShot, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", AfterSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		watchID string
		oneShot bool
		seconds int
		note    string
	}{
		{"repeat_seconds", repeating.WatchID, false, 300, "n"},
		{"after_seconds", oneShot.WatchID, true, 600, ""},
	} {
		// Everything the assertions read is gathered under jm.mu, so the
		// assertions themselves never touch shared state.
		var got struct {
			found              bool
			keySlot            string
			slot               string
			watchID            string
			timer              bool
			oneShot            bool
			timerSeconds       int
			progressIntervalMS int
			note               string
			matchesOwnKey      bool
			matchesSlotlessKey bool
			sendKeyMatches     bool
			slotlessSendKey    bool
		}
		jm.mu.Lock()
		for key, cfg := range jm.watches {
			if cfg.watchID != tc.watchID {
				continue
			}
			slotless := key
			slotless.Slot = ""
			pending := jobstore.WatchSendKey{VisibleSessionID: key.VisibleSessionID, WatchTarget: key.Target}
			got.found = true
			got.keySlot = key.Slot
			got.slot = cfg.slot
			got.watchID = cfg.watchID
			got.timer = cfg.timer
			got.oneShot = cfg.oneShot
			got.timerSeconds = cfg.timerSeconds
			got.progressIntervalMS = cfg.progressIntervalMS
			got.note = cfg.note
			got.matchesOwnKey = watchConfigMatchesWatchKey(cfg, key)
			got.matchesSlotlessKey = watchConfigMatchesWatchKey(cfg, slotless)
			got.sendKeyMatches = watchSendKeyMatchesWatchKey(pending, key)
			got.slotlessSendKey = watchSendKeyMatchesWatchKey(pending, slotless)
		}
		jm.mu.Unlock()
		if !got.found {
			t.Fatalf("%s: no installed watch with id %q", tc.name, tc.watchID)
		}
		if got.keySlot != got.slot || got.slot != got.watchID || got.watchID != tc.watchID {
			t.Errorf("%s: one id expected; key.Slot=%q cfg.slot=%q cfg.watchID=%q result=%q",
				tc.name, got.keySlot, got.slot, got.watchID, tc.watchID)
		}
		if !got.timer {
			t.Errorf("%s: cfg.timer = false", tc.name)
		}
		if got.oneShot != tc.oneShot {
			t.Errorf("%s: cfg.oneShot = %v, want %v", tc.name, got.oneShot, tc.oneShot)
		}
		if got.timerSeconds != tc.seconds {
			t.Errorf("%s: cfg.timerSeconds = %d, want %d", tc.name, got.timerSeconds, tc.seconds)
		}
		if got.progressIntervalMS != tc.seconds*1000 {
			t.Errorf("%s: cfg.progressIntervalMS = %d, want %d", tc.name, got.progressIntervalMS, tc.seconds*1000)
		}
		if got.note != tc.note {
			t.Errorf("%s: cfg.note = %q, want %q", tc.name, got.note, tc.note)
		}
		if !got.matchesOwnKey {
			t.Errorf("%s: a timer config must match its own key", tc.name)
		}
		if got.matchesSlotlessKey {
			t.Errorf("%s: a timer config must not match the same key without its slot", tc.name)
		}
		if got.sendKeyMatches {
			t.Errorf("%s: a durable send key must never match a slotted key", tc.name)
		}
		if !got.slotlessSendKey {
			t.Errorf("%s: the same send key must still match the slot-less key", tc.name)
		}
	}
}

// watchIDMintFunc is the generator call a timer create allocates its id from.
const watchIDMintFunc = "NewWatchID"

// watchCreateValidators are the checks a create must pass before it is entitled
// to an id, in the order configureWatchWithHooks runs them.
var watchCreateValidators = []string{"validateWatchTarget", "validateWatchEventArgs", "validateWatchTriggerShape"}

// TestConfigureWatch_TimerIDIsMintedAfterValidation is a source scan, not a
// behavior test, because a burned watch id leaves nothing to observe: the ids
// are random UUIDv7 values from a stateless generator, so a create that mints
// one and is then rejected moves no counter and opens no gap that a later
// create's id would show. What CAN be checked is where the mint sits in
// configureWatchWithHooks — after every validation that can reject a create, so
// no rejected path reaches it, and the one id still threads into both the key
// and the config.
func TestConfigureWatch_TimerIDIsMintedAfterValidation(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "job_watch.go", nil, 0)
	if err != nil {
		t.Fatalf("parse job_watch.go: %v", err)
	}
	var body *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "configureWatchWithHooks" {
			body = fn
		}
	}
	if body == nil {
		t.Fatal("configureWatchWithHooks is gone from job_watch.go: this guard now checks nothing")
	}
	firstCall := map[string]token.Pos{}
	var mints []token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call.Fun)
		if name == watchIDMintFunc {
			mints = append(mints, call.Pos())
		}
		if _, seen := firstCall[name]; !seen {
			firstCall[name] = call.Pos()
		}
		return true
	})
	if len(mints) != 1 {
		t.Fatalf("configureWatchWithHooks calls %s %d times, want exactly one mint for the timer slot", watchIDMintFunc, len(mints))
	}
	for _, validator := range watchCreateValidators {
		pos, ok := firstCall[validator]
		if !ok {
			t.Fatalf("configureWatchWithHooks no longer calls %s: this guard now checks nothing", validator)
		}
		if mints[0] < pos {
			t.Errorf("%s is minted at %s, before %s at %s: a create rejected by that check burns an id",
				watchIDMintFunc, fset.Position(mints[0]), validator, fset.Position(pos))
		}
	}
}

// calleeName is the bare function name of a call: f(...) and x.f(...) both
// report "f".
func calleeName(fun ast.Expr) string {
	switch node := fun.(type) {
	case *ast.Ident:
		return node.Name
	case *ast.SelectorExpr:
		return node.Sel.Name
	}
	return ""
}

// TestNormalizedWatchConfigHash_TimerModeAndNoteAreIdentity pins what a timer's
// configured identity is. A one-shot and a repeating timer collapse to the same
// derived interval, and the note is what the fire says when it arrives, so a
// hash built from the derived interval alone calls three different watches the
// same watch.
func TestNormalizedWatchConfigHash_TimerModeAndNoteAreIdentity(t *testing.T) {
	t.Parallel()
	base := watchArgs{Operation: "create", Source: "self", Target: "caller"}
	timer := func(mutate func(*watchArgs)) watchArgs {
		a := base
		mutate(&a)
		return a
	}
	hashes := map[string]string{}
	for _, tc := range []struct {
		name string
		args watchArgs
	}{
		{"after_300", timer(func(a *watchArgs) { a.AfterSeconds = 300 })},
		{"repeat_300", timer(func(a *watchArgs) { a.RepeatSeconds = 300 })},
		{"repeat_300_note", timer(func(a *watchArgs) { a.RepeatSeconds = 300; a.Note = "check the build" })},
		{"repeat_300_other_note", timer(func(a *watchArgs) { a.RepeatSeconds = 300; a.Note = "check the deploy" })},
	} {
		hash := normalizedWatchConfigHash(tc.args)
		if previous, clash := hashes[hash]; clash {
			t.Errorf("%s hashes the same as %s (%s); timer mode and note are part of the identity", tc.name, previous, hash)
			continue
		}
		hashes[hash] = tc.name
	}
}

// TestWatchConfigSnapshot_CarriesTimerModeAndNote pins the durable config
// snapshot inspect and history read back: the derived interval alone cannot say
// whether the timer fires once or repeats, or what it was for.
func TestWatchConfigSnapshot_CarriesTimerModeAndNote(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	for _, tc := range []struct {
		name          string
		args          watchArgs
		afterSeconds  int
		repeatSeconds int
		note          string
	}{
		{"one_shot", watchArgs{Operation: "create", Source: "self", Target: "caller", AfterSeconds: 600, Note: "wake me"}, 600, 0, "wake me"},
		{"repeating", watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 300}, 0, 300, ""},
	} {
		res, err := jm.configureWatch(tc.args)
		if err != nil {
			t.Fatalf("%s: configure: %v", tc.name, err)
		}
		jm.mu.Lock()
		var snapshot *jobstore.WatchConfigSnapshot
		for _, cfg := range jm.watches {
			if cfg.watchID == res.WatchID {
				snapshot = watchConfigSnapshot(cfg)
			}
		}
		jm.mu.Unlock()
		if snapshot == nil {
			t.Fatalf("%s: no installed watch with id %q", tc.name, res.WatchID)
		}
		if snapshot.AfterSeconds != tc.afterSeconds || snapshot.RepeatSeconds != tc.repeatSeconds || snapshot.Note != tc.note {
			t.Errorf("%s: snapshot after_seconds=%d repeat_seconds=%d note=%q, want %d/%d/%q",
				tc.name, snapshot.AfterSeconds, snapshot.RepeatSeconds, snapshot.Note, tc.afterSeconds, tc.repeatSeconds, tc.note)
		}
	}
}
