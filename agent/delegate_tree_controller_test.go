package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"golang.org/x/tools/go/packages"
	"primeradiant.com/serf/agent/internal/delegatestore"
)

func TestDelegateControllerDirectOwnerAuthorization(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerIdle(t, c, "dlg_direct", "")

	if err := c.AuthorizeMutation(rootDelegateActor("root-session"), "dlg_direct"); err != nil {
		t.Fatalf("root direct-owner authorization: %v", err)
	}

	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	parent := delegateActor{lease: &delegateLease{delegateID: "dlg_parent", generation: 1}}
	if err := c.AuthorizeMutation(parent, "dlg_child"); err != nil {
		t.Fatalf("delegate direct-owner authorization: %v", err)
	}
}

func TestDelegateControllerRejectsSiblingAndVisibleDescendantMutation(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 8, 2)
	seedDelegateControllerRunning(t, c, "dlg_left", "")
	seedDelegateControllerRunning(t, c, "dlg_right", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_left")
	seedDelegateControllerIdle(t, c, "dlg_grandchild", "dlg_child")

	left := delegateActor{lease: &delegateLease{delegateID: "dlg_left", generation: 1}}
	if err := c.AuthorizeMutation(left, "dlg_right"); !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("sibling authorization error = %v, want not controllable", err)
	}
	if err := c.AuthorizeMutation(left, "dlg_grandchild"); !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("visible descendant authorization error = %v, want not controllable", err)
	}
}

func TestDelegateControllerRejectsStaleActorLease(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")

	stale := delegateActor{lease: &delegateLease{delegateID: "dlg_parent", generation: 2}}
	if err := c.AuthorizeMutation(stale, "dlg_child"); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("stale actor authorization error = %v, want stale lease", err)
	}
}

func TestDelegateControllerReservationHoldsCapacity(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_first", "")
	seedDelegateControllerIdle(t, c, "dlg_second", "")

	first, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_first")
	if err != nil {
		t.Fatalf("ReserveStart first: %v", err)
	}
	if got := c.durable["dlg_first"].Generation; got != 0 {
		t.Fatalf("durable generation while reserved = %d, want 0", got)
	}
	if _, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_second"); !errors.Is(err, errTreeAtCapacity) {
		t.Fatalf("ReserveStart at capacity error = %v, want capacity", err)
	}
	if turns, drives := c.capacityInUse(); turns != 1 || drives != 0 {
		t.Fatalf("capacity in use = (%d, %d), want (1, 0)", turns, drives)
	}
	if err := c.AbortStart(first); err != nil {
		t.Fatalf("AbortStart: %v", err)
	}
	if _, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_second"); err != nil {
		t.Fatalf("ReserveStart after abort: %v", err)
	}
}

func TestDelegateControllerConcurrentIdleReservationsChooseOne(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target")
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var succeeded, busy int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, errDelegateTargetBusy):
			busy++
		default:
			t.Fatalf("ReserveStart concurrent error = %v", err)
		}
	}
	if succeeded != 1 || busy != 1 {
		t.Fatalf("concurrent results success=%d busy=%d, want 1 and 1", succeeded, busy)
	}
	if turns, _ := c.capacityInUse(); turns != 1 {
		t.Fatalf("turn capacity = %d, want 1", turns)
	}
}

func TestDelegateControllerAppendFailureLeavesDurableAndLiveStateUnchanged(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	beforeBytes := readDelegateControllerFile(t, path)
	beforeDurable := cloneDelegateControllerState(t, c.durable)
	beforeLive := cloneDelegateControllerLive(c.live)

	if err := c.store.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}
	c.mu.Lock()
	_, err := c.appendLocked(delegateControllerRunStartedEvent("dlg_target", 1, delegatestore.TriggerOwnerInput, time.Unix(20, 0)))
	c.mu.Unlock()
	if err == nil {
		t.Fatal("appendLocked after close succeeded, want failure")
	}
	if got := readDelegateControllerFile(t, path); !bytes.Equal(got, beforeBytes) {
		t.Fatalf("store bytes changed after append failure:\n got %q\nwant %q", got, beforeBytes)
	}
	if !reflect.DeepEqual(c.durable, beforeDurable) {
		t.Fatalf("durable state changed after append failure:\n got %#v\nwant %#v", c.durable, beforeDurable)
	}
	if !reflect.DeepEqual(c.live, beforeLive) {
		t.Fatalf("live state changed after append failure:\n got %#v\nwant %#v", c.live, beforeLive)
	}
}

func TestDelegateControllerRejectedBatchLeavesBytesSequenceAndFoldUnchanged(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	beforeBytes := readDelegateControllerFile(t, path)
	beforeDurable := cloneDelegateControllerState(t, c.durable)

	c.mu.Lock()
	_, err := c.appendLocked(delegateControllerRunStartedEvent("dlg_target", 2, delegatestore.TriggerOwnerInput, time.Unix(20, 0)))
	c.mu.Unlock()
	if err == nil {
		t.Fatal("appendLocked invalid generation succeeded")
	}
	if got := readDelegateControllerFile(t, path); !bytes.Equal(got, beforeBytes) {
		t.Fatalf("store bytes changed after rejected batch:\n got %q\nwant %q", got, beforeBytes)
	}
	if !reflect.DeepEqual(c.durable, beforeDurable) {
		t.Fatalf("durable fold changed after rejected batch:\n got %#v\nwant %#v", c.durable, beforeDurable)
	}
	c.mu.Lock()
	appended, err := c.appendLocked(delegateControllerRunStartedEvent("dlg_target", 1, delegatestore.TriggerOwnerInput, time.Unix(21, 0)))
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("appendLocked valid event after rejection: %v", err)
	}
	if got := appended[0].Seq; got != 2 {
		t.Fatalf("sequence after rejected batch = %d, want 2", got)
	}
}

func TestDelegateControllerSnapshotReturnsOneStableRow(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerIdle(t, c, "dlg_only", "")

	plan := c.Snapshot()
	if len(plan.rows) != 1 {
		t.Fatalf("snapshot rows = %d, want 1", len(plan.rows))
	}
	if row := plan.rows[0]; row.id != "dlg_only" || row.lifecycle != delegateLifecycleIdle || row.revision != 1 {
		t.Fatalf("snapshot row = %#v, want stable idle delegate at revision 1", row)
	}
}

func TestDelegateControllerMutationReturnsCapturedSnapshot(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	first := started.plan
	if _, err := c.FinishGeneration(started.lease, delegateFinish{
		outcome: delegatestore.OutcomeFailed,
		reason:  "test_finished",
	}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}

	if row := first.rows[0]; row.lifecycle != delegateLifecycleRunning || row.lastOutcome != nil {
		t.Fatalf("first captured row mutated after finish: %#v", row)
	}
}

func TestDelegateControllerCapturedSnapshotsCarryMonotonicRevision(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	finished, err := c.FinishGeneration(started.lease, delegateFinish{
		outcome: delegatestore.OutcomeFailed,
		reason:  "test_finished",
	})
	if err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}

	first, second := started.plan.rows[0], finished.updates[0].rows[0]
	if first.revision >= second.revision {
		t.Fatalf("captured revisions = %d then %d, want increasing", first.revision, second.revision)
	}
	if first.revision != 2 || second.revision != 4 {
		t.Fatalf("captured revisions = %d then %d, want 2 then 4", first.revision, second.revision)
	}
}

func TestDelegateControllerRemainsDormant(t *testing.T) {
	loaded, err := packages.Load(&packages.Config{Mode: packages.LoadSyntax, Dir: "."}, ".")
	if err != nil {
		t.Fatalf("load agent package: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded agent packages = %d, want 1", len(loaded))
	}
	pkg := loaded[0]
	if len(pkg.Errors) != 0 {
		t.Fatalf("load agent package: %s", pkg.Errors[0])
	}
	for _, file := range pkg.Syntax {
		name := filepath.Base(pkg.Fset.Position(file.Package).Filename)
		if delegateControllerImplementationFiles[name] {
			continue
		}
		for _, violation := range delegateControllerDormancyViolations(pkg.Fset, file, pkg.TypesInfo, pkg.Types) {
			t.Errorf("active production caller references dormant delegate controller %s %s at %s", violation.kind, violation.symbol, violation.position)
		}
	}
}

func TestDelegateControllerDormancyGuardRejectsConstructionAndAliases(t *testing.T) {
	tests := []struct {
		name   string
		source string
		symbol string
	}{
		{name: "constructor call", source: `package agent; func probe() { _, _ = openDelegateTreeController(delegateTreeControllerConfig{}) }`, symbol: "openDelegateTreeController"},
		{name: "constructor alias", source: `package agent; func probe() { constructor := openDelegateTreeController; _ = constructor }`, symbol: "openDelegateTreeController"},
		{name: "delivery call", source: `package agent; func probe() { deliverDelegatePacket() }`, symbol: "deliverDelegatePacket"},
		{name: "controller composite", source: `package agent; func probe() { _ = delegateTreeController{} }`, symbol: "delegateTreeController"},
		{name: "controller pointer composite", source: `package agent; func probe() { _ = &delegateTreeController{} }`, symbol: "delegateTreeController"},
		{name: "controller new", source: `package agent; func probe() { _ = new(delegateTreeController) }`, symbol: "delegateTreeController"},
		{name: "controller alias composite", source: `package agent; type active = delegateTreeController; func probe() { _ = active{} }`, symbol: "delegateTreeController"},
		{name: "controller alias new", source: `package agent; type active = delegateTreeController; func probe() { _ = new(active) }`, symbol: "delegateTreeController"},
		{name: "lifecycle call", source: `package agent; func probe(controller *delegateTreeController, reservation *delegateStartReservation) { _, _ = controller.CommitStart(reservation) }`, symbol: "CommitStart"},
		{name: "bound lifecycle alias", source: `package agent; func probe(controller *delegateTreeController) { commit := controller.CommitStart; _ = commit }`, symbol: "CommitStart"},
		{name: "lifecycle method expression", source: `package agent; func probe() { commit := (*delegateTreeController).CommitStart; _ = commit }`, symbol: "CommitStart"},
		{name: "bound lifecycle reference through type alias", source: `package agent; type active = delegateTreeController; func probe(controller *active) { commit := controller.CommitStart; _ = commit }`, symbol: "CommitStart"},
		{name: "lifecycle method expression through type alias", source: `package agent; type active = delegateTreeController; func probe() { commit := (*active).CommitStart; _ = commit }`, symbol: "CommitStart"},
		{name: "unrelated lifecycle call", source: `package agent; type unrelated struct{}; func (*unrelated) CommitStart() {}; func probe(other *unrelated) { other.CommitStart() }`},
		{name: "unrelated bound lifecycle reference", source: `package agent; type unrelated struct{}; func (*unrelated) CommitStart() {}; func probe(other *unrelated) { commit := other.CommitStart; _ = commit }`},
		{name: "unrelated lifecycle method expression", source: `package agent; type unrelated struct{}; func (*unrelated) CommitStart() {}; func probe() { commit := (*unrelated).CommitStart; _ = commit }`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files, file, info, pkg := typeCheckDelegateControllerDormancyFixture(t, test.name+".go", test.source)
			violations := delegateControllerDormancyViolations(files, file, info, pkg)
			if test.symbol == "" {
				if len(violations) != 0 {
					t.Fatalf("violations = %#v, want unrelated lifecycle method ignored", violations)
				}
				return
			}
			if len(violations) != 1 || violations[0].symbol != test.symbol {
				t.Fatalf("violations = %#v, want one semantic reference to %s", violations, test.symbol)
			}
		})
	}
}

var delegateControllerLifecycleMethods = map[string]bool{
	"ReserveCreate":         true,
	"ReserveStart":          true,
	"ReserveAttention":      true,
	"RegisterInlineWaiter":  true,
	"waitForDelegateInline": true,
	"AbortStart":            true,
	"CommitStart":           true,
	"AttachRuntime":         true,
	"AdmitStartInput":       true,
	"Steer":                 true,
	"BeginModelRequest":     true,
	"BeginTool":             true,
	"BeginSettlement":       true,
	"FinishGeneration":      true,
	"BeginDelivery":         true,
	"CompleteDelivery":      true,
	"ReplayDeliveries":      true,
	"AuthorizeMutation":     true,
	"Snapshot":              true,
}

var delegateControllerImplementationFiles = map[string]bool{
	"delegate_tree_controller.go": true,
	"delegate_tree_start.go":      true,
	"delegate_tree_steer.go":      true,
	"delegate_tree_finish.go":     true,
	"delegate_delivery.go":        true,
}

type delegateControllerDormancyViolation struct {
	kind     string
	symbol   string
	position token.Position
}

func delegateControllerDormancyViolations(files *token.FileSet, file *ast.File, info *types.Info, pkg *types.Package) []delegateControllerDormancyViolation {
	controller := types.Unalias(pkg.Scope().Lookup("delegateTreeController").Type())
	constructor := pkg.Scope().Lookup("openDelegateTreeController")
	delivery := pkg.Scope().Lookup("deliverDelegatePacket")
	var violations []delegateControllerDormancyViolation
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.Ident:
			if object := info.Uses[typed]; object == constructor || object == delivery {
				violations = append(violations, delegateControllerDormancyViolation{kind: "dormant function", symbol: typed.Name, position: files.Position(typed.Pos())})
			}
		case *ast.SelectorExpr:
			selection := info.Selections[typed]
			if selection != nil && delegateControllerLifecycleMethods[typed.Sel.Name] && delegateControllerMethodHasReceiver(selection.Obj(), controller) {
				violations = append(violations, delegateControllerDormancyViolation{kind: "lifecycle method", symbol: typed.Sel.Name, position: files.Position(typed.Pos())})
			}
		case *ast.CompositeLit:
			if isDelegateControllerType(info.TypeOf(typed), controller) {
				violations = append(violations, delegateControllerDormancyViolation{kind: "composite literal", symbol: "delegateTreeController", position: files.Position(typed.Pos())})
			}
		case *ast.CallExpr:
			identifier, isIdentifier := typed.Fun.(*ast.Ident)
			builtin, isBuiltin := info.Uses[identifier].(*types.Builtin)
			if isIdentifier && isBuiltin && builtin.Name() == "new" && len(typed.Args) == 1 && isDelegateControllerType(info.TypeOf(typed.Args[0]), controller) {
				violations = append(violations, delegateControllerDormancyViolation{kind: "new expression", symbol: "delegateTreeController", position: files.Position(typed.Pos())})
			}
		}
		return true
	})
	return violations
}

func delegateControllerMethodHasReceiver(object types.Object, controller types.Type) bool {
	method, ok := object.(*types.Func)
	if !ok {
		return false
	}
	signature, ok := method.Type().(*types.Signature)
	return ok && signature.Recv() != nil && isDelegateControllerType(signature.Recv().Type(), controller)
}

func isDelegateControllerType(candidate, controller types.Type) bool {
	if candidate == nil || controller == nil {
		return false
	}
	candidate = types.Unalias(candidate)
	if pointer, ok := candidate.(*types.Pointer); ok {
		candidate = types.Unalias(pointer.Elem())
	}
	return types.Identical(candidate, controller)
}

func typeCheckDelegateControllerDormancyFixture(t *testing.T, filename, source string) (*token.FileSet, *ast.File, *types.Info, *types.Package) {
	t.Helper()
	files := token.NewFileSet()
	prelude, err := parser.ParseFile(files, "delegate_controller_prelude.go", `package agent
type delegateTreeController struct{}
type delegateTreeControllerConfig struct{}
type delegateStartReservation struct{}
type delegateStartCommit struct{}
func openDelegateTreeController(delegateTreeControllerConfig) (*delegateTreeController, error) { return nil, nil }
func deliverDelegatePacket() {}
func (*delegateTreeController) CommitStart(*delegateStartReservation) (delegateStartCommit, error) { return delegateStartCommit{}, nil }
`, 0)
	if err != nil {
		t.Fatalf("parse fixture prelude: %v", err)
	}
	file, err := parser.ParseFile(files, filename, source, 0)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	pkg, err := (&types.Config{}).Check("primeradiant.com/serf/agent/dormancyfixture", files, []*ast.File{prelude, file}, info)
	if err != nil {
		t.Fatalf("type-check source: %v", err)
	}
	return files, file, info, pkg
}

func newDelegateControllerTestHarness(t *testing.T, turnLimit, driveLimit int) (*delegateTreeController, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "delegate-events.jsonl")
	store, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("delegatestore.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	c, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: "root-session",
		stateDir:      root,
		worktreeRoot:  filepath.Join(root, "worktrees"),
		turnLimit:     turnLimit,
		driveLimit:    driveLimit,
		now:           func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("openDelegateTreeController: %v", err)
	}
	return c, path
}

func seedDelegateControllerIdle(t *testing.T, c *delegateTreeController, id, parentID string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.appendLocked(delegateControllerCreatedEvent(id, parentID))
	if err != nil {
		t.Fatalf("seed delegate %s: %v", id, err)
	}
}

func seedDelegateControllerRunning(t *testing.T, c *delegateTreeController, id, parentID string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.appendLocked(
		delegateControllerCreatedEvent(id, parentID),
		delegateControllerRunStartedEvent(id, 1, delegatestore.TriggerInitial, time.Unix(10, 0).UTC()),
	)
	if err != nil {
		t.Fatalf("seed running delegate %s: %v", id, err)
	}
	c.live[id] = &delegateLiveState{
		binding: &delegateRuntimeBinding{
			lease:  delegateLease{delegateID: id, generation: 1},
			cancel: func() {},
			ready:  true,
		},
	}
}

func delegateControllerCreatedEvent(id, parentID string) delegatestore.Event {
	return delegatestore.Event{
		Kind:       delegatestore.EventDelegateCreated,
		DelegateID: id,
		Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			ChildSessionID:   "child-" + id,
			TranscriptRef:    "local:child-" + id,
			ParentDelegateID: parentID,
			OwnerSessionID:   "root-session",
			Task:             "test task",
			AgentType:        "general",
			Resumable:        true,
		}},
	}
}

func readDelegateControllerFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return raw
}

func cloneDelegateControllerState(t *testing.T, state delegatestore.State) delegatestore.State {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal controller state clone: %v", err)
	}
	var clone delegatestore.State
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatalf("unmarshal controller state clone: %v", err)
	}
	return clone
}

func cloneDelegateControllerLive(live map[string]*delegateLiveState) map[string]*delegateLiveState {
	clone := make(map[string]*delegateLiveState, len(live))
	for id, state := range live {
		value := *state
		if state.binding != nil {
			binding := *state.binding
			value.binding = &binding
		}
		value.pendingSteers = append([]delegateSteeringAdmission(nil), state.pendingSteers...)
		value.attentionIDs = append([]string(nil), state.attentionIDs...)
		if state.waiters != nil {
			value.waiters = make(map[uint64]*delegateInlineWaiter, len(state.waiters))
			for generation, waiter := range state.waiters {
				value.waiters[generation] = waiter
			}
		}
		clone[id] = &value
	}
	return clone
}
