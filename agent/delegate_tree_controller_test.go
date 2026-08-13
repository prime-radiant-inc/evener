package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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
	production, err := delegateControllerProductionFiles(".")
	if err != nil {
		t.Fatalf("enumerate production files: %v", err)
	}
	loadedFiles := make([]string, 0, len(pkg.Syntax))
	for _, file := range pkg.Syntax {
		loadedFiles = append(loadedFiles, filepath.Base(pkg.Fset.Position(file.Package).Filename))
	}
	if omitted := delegateControllerOmittedProductionFiles(production, loadedFiles); len(omitted) != 0 {
		t.Fatalf("packages.Load omitted top-level production files: %v", omitted)
	}

	var violations []delegateControllerDormancyViolation
	for _, file := range pkg.Syntax {
		violations = append(violations, delegateControllerDormancyViolations(pkg.Fset, file, pkg.TypesInfo, pkg.Types)...)
	}
	for _, inventoryError := range delegateControllerDormancyInventoryErrors(violations, delegateControllerDormancyExpectedInventory) {
		t.Error(inventoryError)
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
		{name: "session steering call", source: `package agent; func probe(session *Session) { session.appendDelegateSteeringDurably("steer") }`, symbol: "appendDelegateSteeringDurably"},
		{name: "bound session steering alias", source: `package agent; func probe(session *Session) { appendSteer := session.appendDelegateSteeringDurably; _ = appendSteer }`, symbol: "appendDelegateSteeringDurably"},
		{name: "session steering method expression", source: `package agent; func probe() { appendSteer := (*Session).appendDelegateSteeringDurably; _ = appendSteer }`, symbol: "appendDelegateSteeringDurably"},
		{name: "session steering method expression through type alias", source: `package agent; type child = Session; func probe() { appendSteer := (*child).appendDelegateSteeringDurably; _ = appendSteer }`, symbol: "appendDelegateSteeringDurably"},
		{name: "delivery commit call", source: `package agent; func probe(commit *delegateToolResultCommit) { commit.Complete(true) }`, symbol: "Complete"},
		{name: "bound delivery commit alias", source: `package agent; func probe(commit *delegateToolResultCommit) { complete := commit.Complete; _ = complete }`, symbol: "Complete"},
		{name: "delivery commit method expression", source: `package agent; func probe() { complete := (*delegateToolResultCommit).Complete; _ = complete }`, symbol: "Complete"},
		{name: "delivery commit method expression through type alias", source: `package agent; type deliveryCommit = delegateToolResultCommit; func probe() { complete := (*deliveryCommit).Complete; _ = complete }`, symbol: "Complete"},
		{name: "unrelated lifecycle call", source: `package agent; type unrelated struct{}; func (*unrelated) CommitStart() {}; func probe(other *unrelated) { other.CommitStart() }`},
		{name: "unrelated bound lifecycle reference", source: `package agent; type unrelated struct{}; func (*unrelated) CommitStart() {}; func probe(other *unrelated) { commit := other.CommitStart; _ = commit }`},
		{name: "unrelated lifecycle method expression", source: `package agent; type unrelated struct{}; func (*unrelated) CommitStart() {}; func probe() { commit := (*unrelated).CommitStart; _ = commit }`},
		{name: "unrelated session steering call", source: `package agent; type unrelated struct{}; func (*unrelated) appendDelegateSteeringDurably(string) {}; func probe(other *unrelated) { other.appendDelegateSteeringDurably("steer") }`},
		{name: "unrelated session steering method expression", source: `package agent; type unrelated struct{}; func (*unrelated) appendDelegateSteeringDurably(string) {}; func probe() { appendSteer := (*unrelated).appendDelegateSteeringDurably; _ = appendSteer }`},
		{name: "unrelated delivery commit call", source: `package agent; type unrelated struct{}; func (*unrelated) Complete(bool) {}; func probe(other *unrelated) { other.Complete(true) }`},
		{name: "unrelated delivery commit method expression", source: `package agent; type unrelated struct{}; func (*unrelated) Complete(bool) {}; func probe() { complete := (*unrelated).Complete; _ = complete }`},
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

func TestDelegateControllerDormancyGuardRejectsOmittedProductionFile(t *testing.T) {
	production := []string{"active.go", "inactive_windows.go"}
	loaded := []string{"active.go"}
	if got, want := delegateControllerOmittedProductionFiles(production, loaded), []string{"inactive_windows.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("omitted production files = %#v, want %#v", got, want)
	}
}

func TestDelegateControllerDormancyGuardRejectsExtraSameFunctionReference(t *testing.T) {
	files, file, info, pkg := typeCheckDelegateControllerDormancyFixture(t, "delegate_tree_steer.go", `package agent
func probe(session *Session) {
	session.appendDelegateSteeringDurably("first")
	session.appendDelegateSteeringDurably("second")
}`)
	violations := delegateControllerDormancyViolations(files, file, info, pkg)
	expected := map[delegateControllerDormancyInventoryKey]int{
		{filename: "delegate_tree_steer.go", function: "probe", kind: "session steering method", symbol: "appendDelegateSteeringDurably"}: 1,
	}
	if errors := delegateControllerDormancyInventoryErrors(violations, expected); len(errors) == 0 {
		t.Fatalf("extra same-function reference was accepted: violations=%#v", violations)
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

var delegateControllerDormancyExpectedInventory = map[delegateControllerDormancyInventoryKey]int{
	{filename: "delegate_tree_controller.go", function: "openDelegateTreeController", kind: "composite literal", symbol: "delegateTreeController"}:              1,
	{filename: "delegate_tree_steer.go", function: "(*delegateTreeController).Steer", kind: "session steering method", symbol: "appendDelegateSteeringDurably"}: 1,
	{filename: "delegate_delivery.go", function: "deliverDelegatePacket", kind: "lifecycle method", symbol: "BeginDelivery"}:                                    1,
	{filename: "delegate_delivery.go", function: "deliverDelegatePacket", kind: "lifecycle method", symbol: "CompleteDelivery"}:                                 4,
	{filename: "delegate_delivery.go", function: "(*delegateToolResultCommit).Complete", kind: "lifecycle method", symbol: "CompleteDelivery"}:                  1,
}

type delegateControllerDormancyViolation struct {
	function string
	kind     string
	symbol   string
	position token.Position
}

type delegateControllerDormancyInventoryKey struct {
	filename string
	function string
	kind     string
	symbol   string
}

func delegateControllerOmittedProductionFiles(production, loaded []string) []string {
	loadedSet := make(map[string]bool, len(loaded))
	for _, filename := range loaded {
		loadedSet[filename] = true
	}
	omitted := make([]string, 0)
	for _, filename := range production {
		if !loadedSet[filename] {
			omitted = append(omitted, filename)
		}
	}
	sort.Strings(omitted)
	return omitted
}

func delegateControllerDormancyInventoryErrors(violations []delegateControllerDormancyViolation, expected map[delegateControllerDormancyInventoryKey]int) []string {
	actual := make(map[delegateControllerDormancyInventoryKey]int)
	positions := make(map[delegateControllerDormancyInventoryKey][]token.Position)
	for _, violation := range violations {
		key := delegateControllerDormancyInventoryKey{
			filename: filepath.Base(violation.position.Filename),
			function: violation.function,
			kind:     violation.kind,
			symbol:   violation.symbol,
		}
		actual[key]++
		positions[key] = append(positions[key], violation.position)
	}
	keys := make(map[delegateControllerDormancyInventoryKey]bool, len(actual)+len(expected))
	for key := range actual {
		keys[key] = true
	}
	for key := range expected {
		keys[key] = true
	}
	ordered := make([]delegateControllerDormancyInventoryKey, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		return fmt.Sprintf("%s\x00%s\x00%s\x00%s", left.filename, left.function, left.kind, left.symbol) <
			fmt.Sprintf("%s\x00%s\x00%s\x00%s", right.filename, right.function, right.kind, right.symbol)
	})
	var inventoryErrors []string
	for _, key := range ordered {
		if actual[key] == expected[key] {
			continue
		}
		inventoryErrors = append(inventoryErrors, fmt.Sprintf(
			"dormant delegate reference inventory %s %s %s %s count=%d want=%d positions=%v",
			key.filename, key.function, key.kind, key.symbol, actual[key], expected[key], positions[key],
		))
	}
	return inventoryErrors
}

func delegateControllerProductionFiles(directory string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	production := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		production = append(production, name)
	}
	sort.Strings(production)
	return production, nil
}

func delegateControllerDormancyViolations(files *token.FileSet, file *ast.File, info *types.Info, pkg *types.Package) []delegateControllerDormancyViolation {
	controller := types.Unalias(pkg.Scope().Lookup("delegateTreeController").Type())
	session := types.Unalias(pkg.Scope().Lookup("Session").Type())
	deliveryCommit := types.Unalias(pkg.Scope().Lookup("delegateToolResultCommit").Type())
	constructor := pkg.Scope().Lookup("openDelegateTreeController")
	delivery := pkg.Scope().Lookup("deliverDelegatePacket")
	var violations []delegateControllerDormancyViolation
	appendViolation := func(position token.Pos, kind, symbol string) {
		violations = append(violations, delegateControllerDormancyViolation{
			function: delegateControllerEnclosingFunction(file, position, info),
			kind:     kind,
			symbol:   symbol,
			position: files.Position(position),
		})
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.Ident:
			if object := info.Uses[typed]; object == constructor || object == delivery {
				appendViolation(typed.Pos(), "dormant function", typed.Name)
			}
		case *ast.SelectorExpr:
			selection := info.Selections[typed]
			switch {
			case selection != nil && delegateControllerLifecycleMethods[typed.Sel.Name] && delegateControllerMethodHasReceiver(selection.Obj(), controller):
				appendViolation(typed.Pos(), "lifecycle method", typed.Sel.Name)
			case selection != nil && typed.Sel.Name == "appendDelegateSteeringDurably" && delegateControllerMethodHasReceiver(selection.Obj(), session):
				appendViolation(typed.Pos(), "session steering method", typed.Sel.Name)
			case selection != nil && typed.Sel.Name == "Complete" && delegateControllerMethodHasReceiver(selection.Obj(), deliveryCommit):
				appendViolation(typed.Pos(), "delivery commit method", typed.Sel.Name)
			}
		case *ast.CompositeLit:
			if isDelegateControllerType(info.TypeOf(typed), controller) {
				appendViolation(typed.Pos(), "composite literal", "delegateTreeController")
			}
		case *ast.CallExpr:
			identifier, isIdentifier := typed.Fun.(*ast.Ident)
			builtin, isBuiltin := info.Uses[identifier].(*types.Builtin)
			if isIdentifier && isBuiltin && builtin.Name() == "new" && len(typed.Args) == 1 && isDelegateControllerType(info.TypeOf(typed.Args[0]), controller) {
				appendViolation(typed.Pos(), "new expression", "delegateTreeController")
			}
		}
		return true
	})
	return violations
}

func delegateControllerEnclosingFunction(file *ast.File, position token.Pos, info *types.Info) string {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || position < function.Pos() || position > function.End() {
			continue
		}
		object, ok := info.Defs[function.Name].(*types.Func)
		if !ok {
			return function.Name.Name
		}
		signature, ok := object.Type().(*types.Signature)
		if !ok || signature.Recv() == nil {
			return object.Name()
		}
		receiver := types.Unalias(signature.Recv().Type())
		pointer := false
		if typed, ok := receiver.(*types.Pointer); ok {
			pointer = true
			receiver = types.Unalias(typed.Elem())
		}
		named, ok := receiver.(*types.Named)
		if !ok {
			return object.FullName()
		}
		if pointer {
			return "(*" + named.Obj().Name() + ")." + object.Name()
		}
		return "(" + named.Obj().Name() + ")." + object.Name()
	}
	return "<package>"
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
type Session struct{}
type delegateToolResultCommit struct{}
func openDelegateTreeController(delegateTreeControllerConfig) (*delegateTreeController, error) { return nil, nil }
func deliverDelegatePacket() {}
func (*delegateTreeController) CommitStart(*delegateStartReservation) (delegateStartCommit, error) { return delegateStartCommit{}, nil }
func (*Session) appendDelegateSteeringDurably(string) {}
func (*delegateToolResultCommit) Complete(bool) {}
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
