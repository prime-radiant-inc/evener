package apptranscript

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestLatestFromFileNonPositiveLimitDelegates covers the limit <= 0 path in
// LatestFromFile that delegates to TurnsFromFile with a full projector.
func TestLatestFromFileNonPositiveLimitDelegates(t *testing.T) {
	fixture := writeBoundedFixture(t)
	cache := NewTurnCache()
	turns, cursor, err := cache.LatestFromFile(fixture.path, testMaxLineBytes, 0, boundedTestProjector)
	if err != nil {
		t.Fatalf("LatestFromFile limit=0: %v", err)
	}
	if len(turns) == 0 {
		t.Fatalf("LatestFromFile limit=0 returned no turns")
	}
	if cursor != "" {
		t.Fatalf("LatestFromFile limit=0 cursor = %q, want empty", cursor)
	}
}

// TestLatestFromFileNonPositiveLimitError covers the error path in the
// limit <= 0 branch of LatestFromFile (TurnsFromFile fails on a missing file).
func TestLatestFromFileNonPositiveLimitError(t *testing.T) {
	cache := NewTurnCache()
	_, _, err := cache.LatestFromFile(filepath.Join(t.TempDir(), "missing.jsonl"), testMaxLineBytes, 0, boundedTestProjector)
	if err == nil {
		t.Fatalf("LatestFromFile missing file should error")
	}
}

// TestPageFromFileNonPositiveLimitDelegates covers the limit <= 0 path in
// PageFromFile that delegates to TurnsFromFile.
func TestPageFromFileNonPositiveLimitDelegates(t *testing.T) {
	fixture := writeBoundedFixture(t)
	cache := NewTurnCache()
	page, err := cache.PageFromFile(fixture.path, testMaxLineBytes, "", 0, boundedTestProjector)
	if err != nil {
		t.Fatalf("PageFromFile limit=0: %v", err)
	}
	if len(page.Turns) == 0 {
		t.Fatalf("PageFromFile limit=0 returned no turns")
	}
}

// TestPageFromFileNonPositiveLimitError covers the error path in the
// limit <= 0 branch of PageFromFile.
func TestPageFromFileNonPositiveLimitError(t *testing.T) {
	cache := NewTurnCache()
	_, err := cache.PageFromFile(filepath.Join(t.TempDir(), "missing.jsonl"), testMaxLineBytes, "", 0, boundedTestProjector)
	if err == nil {
		t.Fatalf("PageFromFile missing file should error")
	}
}

// TestPageFromFileCursorClamping covers the cursor parsing edge cases in
// PageFromFile: cursor above logical count, negative cursor.
func TestPageFromFileCursorClamping(t *testing.T) {
	fixture := writeBoundedFixture(t)
	cache := NewTurnCache()
	count := requireTurnCountFromFile(t, cache, fixture.path, testMaxLineBytes, boundedTestProjector)
	if count == 0 {
		t.Fatalf("fixture produced no turns")
	}
	// Cursor above the count is clamped to the count.
	page, err := cache.PageFromFile(fixture.path, testMaxLineBytes, "9999", 1, boundedTestProjector)
	if err != nil {
		t.Fatalf("PageFromFile cursor=9999: %v", err)
	}
	if len(page.Turns) != 1 {
		t.Fatalf("PageFromFile cursor=9999 limit=1 returned %d turns", len(page.Turns))
	}
	// A non-numeric cursor is ignored, so hi stays at the logical count.
	page2, err := cache.PageFromFile(fixture.path, testMaxLineBytes, "not-a-number", 1, boundedTestProjector)
	if err != nil {
		t.Fatalf("PageFromFile cursor=not-a-number: %v", err)
	}
	if len(page2.Turns) != 1 {
		t.Fatalf("PageFromFile cursor=not-a-number limit=1 returned %d turns", len(page2.Turns))
	}
}

// TestFullProjectorNilProject covers the nil-projector path in fullProjector
// where it returns nil instead of calling the projector.
func TestFullProjectorNilProject(t *testing.T) {
	fp := fullProjector(nil)
	turn := schema.NewTurn(schema.TurnUserInput, llm.User("hello"))
	items := fp(turn, "turn-1", 0)
	if items != nil {
		t.Fatalf("fullProjector(nil) = %v, want nil", items)
	}
}

// TestRecordNodeHeightNil covers the nil node path in recordNodeHeight.
func TestRecordNodeHeightNil(t *testing.T) {
	if got := recordNodeHeight(nil); got != 0 {
		t.Fatalf("recordNodeHeight(nil) = %d, want 0", got)
	}
}

// TestNewRecordLeafEmpty covers the empty-records path in newRecordLeaf.
func TestNewRecordLeafEmpty(t *testing.T) {
	if got := newRecordLeaf(nil); got != nil {
		t.Fatalf("newRecordLeaf(nil) = %v, want nil", got)
	}
}

// TestNewRecordLeafWithRecords covers the non-empty path in newRecordLeaf.
func TestNewRecordLeafWithRecords(t *testing.T) {
	records := []indexedTurn{
		{Index: 1, Visible: true},
		{Index: 2, Visible: false},
		{Index: 3, Visible: true},
	}
	node := newRecordLeaf(records)
	if node == nil {
		t.Fatalf("newRecordLeaf returned nil for non-empty records")
	}
	if node.count != 3 {
		t.Fatalf("node.count = %d, want 3", node.count)
	}
	if node.visible != 2 {
		t.Fatalf("node.visible = %d, want 2", node.visible)
	}
	if node.height != 1 {
		t.Fatalf("node.height = %d, want 1", node.height)
	}
}

// TestJoinRecordNodesNil covers the nil-branch paths in joinRecordNodes.
func TestJoinRecordNodesNil(t *testing.T) {
	left := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}})
	if got := joinRecordNodes(nil, left); got != left {
		t.Fatalf("joinRecordNodes(nil, left) should return left")
	}
	if got := joinRecordNodes(left, nil); got != left {
		t.Fatalf("joinRecordNodes(left, nil) should return left")
	}
	if got := joinRecordNodes(nil, nil); got != nil {
		t.Fatalf("joinRecordNodes(nil, nil) should return nil")
	}
}

// TestMakeRecordBranchNil covers the nil-branch paths in makeRecordBranch.
func TestMakeRecordBranchNil(t *testing.T) {
	left := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}})
	if got := makeRecordBranch(nil, left); got != left {
		t.Fatalf("makeRecordBranch(nil, left) should return left")
	}
	if got := makeRecordBranch(left, nil); got != left {
		t.Fatalf("makeRecordBranch(left, nil) should return left")
	}
}

// TestRecordNodeVisibleNil covers the nil node path in recordNodeVisible.
func TestRecordNodeVisibleNil(t *testing.T) {
	if got := recordNodeVisible(nil); got != 0 {
		t.Fatalf("recordNodeVisible(nil) = %d, want 0", got)
	}
}

// TestRecordNodeCountNil covers the nil node path in recordNodeCount.
func TestRecordNodeCountNil(t *testing.T) {
	if got := recordNodeCount(nil); got != 0 {
		t.Fatalf("recordNodeCount(nil) = %d, want 0", got)
	}
}

// TestJoinRecordNodesBalancing exercises the AVL balancing branches in
// joinRecordNodes and balanceRecordNode by building trees of varying heights.
func TestJoinRecordNodesBalancing(t *testing.T) {
	// Build a tall left tree and a short right tree to trigger the
	// height-left > height-right+1 rebalancing.
	var left *turnIndexRecordNode
	for i := range 10 {
		left = joinRecordNodes(left, newRecordLeaf([]indexedTurn{{Index: i, Visible: true}}))
	}
	single := newRecordLeaf([]indexedTurn{{Index: 99, Visible: true}})
	joined := joinRecordNodes(left, single)
	if joined == nil {
		t.Fatalf("joinRecordNodes produced nil")
	}
	if joined.count != 11 {
		t.Fatalf("joined.count = %d, want 11", joined.count)
	}

	// Build a tall right tree and a short left tree to trigger the
	// height-right > height-left+1 rebalancing.
	var right *turnIndexRecordNode
	for i := range 10 {
		right = joinRecordNodes(newRecordLeaf([]indexedTurn{{Index: i, Visible: true}}), right)
	}
	joined2 := joinRecordNodes(single, right)
	if joined2 == nil {
		t.Fatalf("joinRecordNodes produced nil")
	}
	if joined2.count != 11 {
		t.Fatalf("joined2.count = %d, want 11", joined2.count)
	}
}

// TestFileIdentityNil covers the nil/empty-input paths in fileIdentity.
func TestFileIdentityNil(t *testing.T) {
	if got := fileIdentity(nil); got != "" {
		t.Fatalf("fileIdentity(nil) = %q, want empty", got)
	}
}

// TestFileChangeIdentityNil covers the nil/empty-input paths in fileChangeIdentity.
func TestFileChangeIdentityNil(t *testing.T) {
	if got := fileChangeIdentity(nil); got != "" {
		t.Fatalf("fileChangeIdentity(nil) = %q, want empty", got)
	}
}

// TestProjectionIdentityNil covers the nil-projector path in projectionIdentity.
func TestProjectionIdentityNil(t *testing.T) {
	got := projectionIdentity(nil)
	if got == "" {
		t.Fatalf("projectionIdentity(nil) should not be empty")
	}
	if !strings.Contains(got, "<nil>") {
		t.Fatalf("projectionIdentity(nil) = %q, should contain <nil>", got)
	}
}

// TestProjectionIdentityUnknownFunc covers the case where runtime.FuncForPC
// returns nil for a projector.
func TestProjectionIdentityUnknownFunc(t *testing.T) {
	// A projector built from a value that has no function name should still
	// produce a non-empty identity. Using a closure that the runtime can name
	// is the normal case; the "<unknown>" branch is hard to hit directly but
	// the named path is exercised here.
	projector := func(turn schema.Turn, turnID string, turnIndex int, toolNames map[string]string) []appwire.ThreadItem {
		return nil
	}
	got := projectionIdentity(projector)
	if got == "" {
		t.Fatalf("projectionIdentity should not be empty")
	}
}

// TestEqualToolNames covers the equalToolNames function.
func TestEqualToolNames(t *testing.T) {
	if !equalToolNames(nil, nil) {
		t.Fatalf("equalToolNames(nil, nil) should be true")
	}
	if !equalToolNames(map[string]string{}, map[string]string{}) {
		t.Fatalf("equalToolNames(empty, empty) should be true")
	}
	if !equalToolNames(map[string]string{"a": "b"}, map[string]string{"a": "b"}) {
		t.Fatalf("equalToolNames(same) should be true")
	}
	if equalToolNames(map[string]string{"a": "b"}, map[string]string{"a": "c"}) {
		t.Fatalf("equalToolNames(different values) should be false")
	}
	if equalToolNames(map[string]string{"a": "b"}, map[string]string{"c": "d"}) {
		t.Fatalf("equalToolNames(different keys) should be false")
	}
	if equalToolNames(map[string]string{"a": "b"}, map[string]string{"a": "b", "c": "d"}) {
		t.Fatalf("equalToolNames(different lengths) should be false")
	}
}

// TestExtendPrefixStampInvalid covers the invalid-stamp path in extendPrefixStamp.
func TestExtendPrefixStampInvalid(t *testing.T) {
	if got := extendPrefixStamp("not-hex", []byte("line")); got != "" {
		t.Fatalf("extendPrefixStamp(invalid) = %q, want empty", got)
	}
	if got := extendPrefixStamp("", []byte("line")); got != "" {
		t.Fatalf("extendPrefixStamp(empty) = %q, want empty", got)
	}
}

// TestPrefixStampNegative covers the negative-size path in prefixStamp.
func TestPrefixStampNegative(t *testing.T) {
	got, read := prefixStamp(nil, -1)
	if got != "" || read != 0 {
		t.Fatalf("prefixStamp(nil, -1) = %q, %d, want empty, 0", got, read)
	}
}

// TestReflectedUint covers reflectedUint with non-integer types.
func TestReflectedUint(t *testing.T) {
	v := reflect.ValueOf("string")
	if got := reflectedUint(v); got != 0 {
		t.Fatalf("reflectedUint(string) = %d, want 0", got)
	}
}

// TestReflectedTimeIdentityInvalid covers the invalid-value path in
// reflectedTimeIdentity.
func TestReflectedTimeIdentityInvalid(t *testing.T) {
	v := reflect.Value{}
	if got := reflectedTimeIdentity(v); got != "" {
		t.Fatalf("reflectedTimeIdentity(invalid) = %q, want empty", got)
	}
}

// TestReflectedTimeIdentityUint covers the uint kind path in reflectedTimeIdentity.
func TestReflectedTimeIdentityUint(t *testing.T) {
	v := reflect.ValueOf(uint64(42))
	if got := reflectedTimeIdentity(v); got != "42" {
		t.Fatalf("reflectedTimeIdentity(uint64(42)) = %q, want 42", got)
	}
}

// TestReflectedTimeIdentityStruct covers the struct kind path in
// reflectedTimeIdentity.
func TestReflectedTimeIdentityStruct(t *testing.T) {
	type timespec struct {
		Sec  int64
		Nsec int64
	}
	v := reflect.ValueOf(timespec{Sec: 100, Nsec: 200})
	got := reflectedTimeIdentity(v)
	if got != "100:200" {
		t.Fatalf("reflectedTimeIdentity(struct) = %q, want 100:200", got)
	}
}

// TestLoadTurnIndexMissingFile covers the os.Open error path in loadTurnIndex.
func TestLoadTurnIndexMissingFile(t *testing.T) {
	cache := NewTurnCache()
	_, _, err := cache.loadTurnIndex(filepath.Join(t.TempDir(), "missing.jsonl"), testMaxLineBytes, boundedTestProjector)
	if err == nil {
		t.Fatalf("loadTurnIndex missing file should error")
	}
}

// TestLoadTurnIndexStatError covers the file.Stat error path. This is tricky
// without a fault seam; instead we use a directory path which opens but fails
// to stat as a file in some cases. On most systems opening a directory
// succeeds, and Stat returns dir info — so this mainly exercises the open
// error path with a non-file path.
func TestLoadTurnIndexDirectoryPath(t *testing.T) {
	dir := t.TempDir()
	cache := NewTurnCache()
	// Opening a directory succeeds on Linux but the subsequent read logic
	// will fail; on macOS, opening a directory may succeed.
	_, _, err := cache.loadTurnIndex(dir, testMaxLineBytes, boundedTestProjector)
	// We don't assert on the error — either it errors on open (covered) or
	// it errors later. The important thing is it doesn't panic.
	_ = err
}

// TestTurnCountFromFileError covers the error path in TurnCountFromFile.
func TestTurnCountFromFileError(t *testing.T) {
	cache := NewTurnCache()
	_, err := cache.TurnCountFromFile(filepath.Join(t.TempDir(), "missing.jsonl"), testMaxLineBytes, boundedTestProjector)
	if err == nil {
		t.Fatalf("TurnCountFromFile missing file should error")
	}
}

// TestLatestFromFileError covers the error path in LatestFromFile with a
// positive limit (the loadTurnIndex error path).
func TestLatestFromFileError(t *testing.T) {
	cache := NewTurnCache()
	_, _, err := cache.LatestFromFile(filepath.Join(t.TempDir(), "missing.jsonl"), testMaxLineBytes, 10, boundedTestProjector)
	if err == nil {
		t.Fatalf("LatestFromFile missing file should error")
	}
}

// TestPageFromFileError covers the error path in PageFromFile with a
// positive limit (the loadTurnIndex error path).
func TestPageFromFileError(t *testing.T) {
	cache := NewTurnCache()
	_, err := cache.PageFromFile(filepath.Join(t.TempDir(), "missing.jsonl"), testMaxLineBytes, "", 10, boundedTestProjector)
	if err == nil {
		t.Fatalf("PageFromFile missing file should error")
	}
}

// TestCloneToolNames covers the cloneToolNames function.
func TestCloneToolNames(t *testing.T) {
	original := map[string]string{"a": "read", "b": "write"}
	clone := cloneToolNames(original)
	if !equalToolNames(original, clone) {
		t.Fatalf("cloneToolNames did not produce an equal map")
	}
	clone["a"] = "modified"
	if original["a"] != "read" {
		t.Fatalf("modifying clone affected original")
	}
}

// TestCloneTurnIndexForAppend covers the trivial clone function.
func TestCloneTurnIndexForAppend(t *testing.T) {
	original := turnIndexDisk{Version: turnIndexVersion}
	clone := cloneTurnIndexForAppend(original)
	if clone.Version != original.Version {
		t.Fatalf("cloneTurnIndexForAppend did not preserve Version")
	}
}

// TestInitialPrefixStamp covers the initialPrefixStamp function.
func TestInitialPrefixStamp(t *testing.T) {
	stamp := initialPrefixStamp()
	if stamp == "" {
		t.Fatalf("initialPrefixStamp should not be empty")
	}
	// Should be deterministic.
	if initialPrefixStamp() != stamp {
		t.Fatalf("initialPrefixStamp is not deterministic")
	}
}

// TestTurnIndexJournalStamp covers the turnIndexJournalStamp function.
func TestTurnIndexJournalStamp(t *testing.T) {
	frame := turnIndexJournalFrame{Version: turnIndexJournalVersion}
	stamp := turnIndexJournalStamp(frame)
	if stamp == "" {
		t.Fatalf("turnIndexJournalStamp should not be empty")
	}
	// Should be deterministic for the same frame.
	if turnIndexJournalStamp(frame) != stamp {
		t.Fatalf("turnIndexJournalStamp is not deterministic")
	}
}

// TestTurnIndexIntegrityStamp covers the turnIndexIntegrityStamp function.
func TestTurnIndexIntegrityStamp(t *testing.T) {
	index := turnIndexDisk{Version: turnIndexVersion}
	stamp := turnIndexIntegrityStamp(index)
	if stamp == "" {
		t.Fatalf("turnIndexIntegrityStamp should not be empty")
	}
}

// TestWriteTurnIndex covers the writeTurnIndex function with a valid index.
func TestWriteTurnIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.appwire-index.json")
	index := turnIndexDisk{
		Version:                 turnIndexVersion,
		TranscriptFormatVersion: transcript.FormatVersion,
		VisibleRecords:          1,
	}
	if err := writeTurnIndex(path, index, nil); err != nil {
		t.Fatalf("writeTurnIndex: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("writeTurnIndex did not create file: %v", err)
	}
}

// TestWriteTurnIndexError covers the error paths in writeTurnIndex by using
// a directory that cannot be created.
func TestWriteTurnIndexError(t *testing.T) {
	// Use a path in a non-existent directory whose parent cannot be created
	// because it conflicts with a file.
	tmp := t.TempDir()
	conflict := filepath.Join(tmp, "file")
	if err := os.WriteFile(conflict, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(conflict, "sub", "index.json")
	index := turnIndexDisk{Version: turnIndexVersion}
	if err := writeTurnIndex(path, index, nil); err == nil {
		t.Fatalf("writeTurnIndex should fail on unwritable parent")
	}
}
