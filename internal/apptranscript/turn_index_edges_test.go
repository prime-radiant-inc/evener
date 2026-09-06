package apptranscript

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestFullProjectorNilProject covers the nil-project path in fullProjector.
func TestFullProjectorNilProjectReturnsNil(t *testing.T) {
	fp := fullProjector(nil)
	turn := schema.NewTurn(schema.TurnUserInput, llm.User("hello"))
	items := fp(turn, "turn-1", 0)
	if items != nil {
		t.Fatalf("fullProjector(nil) should return nil, got %v", items)
	}
}

// TestFullProjectorNonNilProject covers the non-nil path in fullProjector.
func TestFullProjectorNonNilProjectInvokesProject(t *testing.T) {
	called := false
	project := func(turn schema.Turn, turnID string, turnIndex int, toolNames map[string]string) []appwire.ThreadItem {
		called = true
		return nil
	}
	fp := fullProjector(project)
	turn := schema.NewTurn(schema.TurnUserInput, llm.User("hello"))
	fp(turn, "turn-1", 0)
	if !called {
		t.Fatalf("fullProjector should have invoked the project function")
	}
}

// TestEqualToolNamesDifferentLengths covers the length-mismatch path.
func TestEqualToolNamesDifferentLengths(t *testing.T) {
	if equalToolNames(map[string]string{"a": "x"}, map[string]string{"a": "x", "b": "y"}) {
		t.Fatalf("equalToolNames with different lengths should return false")
	}
}

// TestEqualToolNamesDifferentValues covers the value-mismatch path.
func TestEqualToolNamesDifferentValues(t *testing.T) {
	if equalToolNames(map[string]string{"a": "x"}, map[string]string{"a": "y"}) {
		t.Fatalf("equalToolNames with different values should return false")
	}
}

// TestEqualToolNamesBothEmpty covers the both-empty path.
func TestEqualToolNamesBothEmpty(t *testing.T) {
	if !equalToolNames(map[string]string{}, map[string]string{}) {
		t.Fatalf("equalToolNames with both empty should return true")
	}
}

// TestEqualToolNamesEqual covers the equal path.
func TestEqualToolNamesEqual(t *testing.T) {
	if !equalToolNames(map[string]string{"a": "x", "b": "y"}, map[string]string{"a": "x", "b": "y"}) {
		t.Fatalf("equalToolNames with equal maps should return true")
	}
}

// TestReflectedUintNonIntKind covers the default (0) return for non-int/uint kinds.
func TestReflectedUintNonIntKind(t *testing.T) {
	v := reflect.ValueOf("string")
	if got := reflectedUint(v); got != 0 {
		t.Fatalf("reflectedUint on string should return 0, got %d", got)
	}
	v2 := reflect.ValueOf(3.14)
	if got := reflectedUint(v2); got != 0 {
		t.Fatalf("reflectedUint on float should return 0, got %d", got)
	}
}

// TestFileIdentityNilInfo covers the nil-info path in fileIdentity.
func TestFileIdentityNilInfo(t *testing.T) {
	if got := fileIdentity(nil); got != "" {
		t.Fatalf("fileIdentity(nil) should return empty, got %q", got)
	}
}

// TestFileIdentityNoSys covers the nil-Sys path in fileIdentity.
func TestFileIdentityNoSys(t *testing.T) {
	info := mockFileInfo{sys: nil}
	if got := fileIdentity(info); got != "" {
		t.Fatalf("fileIdentity with nil Sys should return empty, got %q", got)
	}
}

// TestFileIdentityNonStructSys covers the non-struct Sys path in fileIdentity.
func TestFileIdentityNonStructSys(t *testing.T) {
	info := mockFileInfo{sys: "not a struct"}
	if got := fileIdentity(info); got != "" {
		t.Fatalf("fileIdentity with non-struct Sys should return empty, got %q", got)
	}
}

// TestFileChangeIdentityNilInfo covers the nil-info path in fileChangeIdentity.
func TestFileChangeIdentityNilInfo(t *testing.T) {
	if got := fileChangeIdentity(nil); got != "" {
		t.Fatalf("fileChangeIdentity(nil) should return empty, got %q", got)
	}
}

// TestFileChangeIdentityNoSys covers the nil-Sys path in fileChangeIdentity.
func TestFileChangeIdentityNoSys(t *testing.T) {
	info := mockFileInfo{sys: nil}
	if got := fileChangeIdentity(info); got != "" {
		t.Fatalf("fileChangeIdentity with nil Sys should return empty, got %q", got)
	}
}

// TestFileChangeIdentityNonStructSys covers the non-struct Sys path.
func TestFileChangeIdentityNonStructSys(t *testing.T) {
	info := mockFileInfo{sys: "not a struct"}
	if got := fileChangeIdentity(info); got != "" {
		t.Fatalf("fileChangeIdentity with non-struct Sys should return empty, got %q", got)
	}
}

// TestReflectedTimeIdentityNilValue covers the nil-value path.
func TestReflectedTimeIdentityNilValue(t *testing.T) {
	if got := reflectedTimeIdentity(reflect.Value{}); got != "" {
		t.Fatalf("reflectedTimeIdentity on invalid value should return empty, got %q", got)
	}
}

// TestReflectedTimeIdentityInt covers the int kind path.
func TestReflectedTimeIdentityInt(t *testing.T) {
	v := reflect.ValueOf(int64(12345))
	if got := reflectedTimeIdentity(v); got != "12345" {
		t.Fatalf("reflectedTimeIdentity on int64 12345 should return '12345', got %q", got)
	}
}

// TestReflectedTimeIdentityUint covers the uint kind path.
func TestReflectedTimeIdentityUint(t *testing.T) {
	v := reflect.ValueOf(uint64(42))
	if got := reflectedTimeIdentity(v); got != "42" {
		t.Fatalf("reflectedTimeIdentity on uint 42 should return '42', got %q", got)
	}
}

// TestReflectedTimeIdentityStructWithSecNsec covers the struct kind path with
// Sec/Nsec fields.
func TestReflectedTimeIdentityStructWithSecNsec(t *testing.T) {
	type timespec struct {
		Sec  int64
		Nsec int64
	}
	v := reflect.ValueOf(timespec{Sec: 100, Nsec: 200})
	if got := reflectedTimeIdentity(v); got != "100:200" {
		t.Fatalf("reflectedTimeIdentity on struct with Sec/Nsec should return '100:200', got %q", got)
	}
}

// TestReflectedTimeIdentityStructWithTvSec covers the struct kind path with
// Tv_sec/Tv_nsec fields.
func TestReflectedTimeIdentityStructWithTvSec(t *testing.T) {
	type timespec struct {
		Tv_sec  int64
		Tv_nsec int64
	}
	v := reflect.ValueOf(timespec{Tv_sec: 300, Tv_nsec: 400})
	if got := reflectedTimeIdentity(v); got != "300:400" {
		t.Fatalf("reflectedTimeIdentity on struct with Tv_sec/Tv_nsec should return '300:400', got %q", got)
	}
}

// TestReflectedTimeIdentityStringKindReturnsEmpty covers the default path for
// non-matching kinds like string.
func TestReflectedTimeIdentityStringKindReturnsEmpty(t *testing.T) {
	v := reflect.ValueOf("hello")
	if got := reflectedTimeIdentity(v); got != "" {
		t.Fatalf("reflectedTimeIdentity on string should return empty, got %q", got)
	}
}

// TestReflectedTimeIdentityStructWithoutMatchingFields covers the struct path
// where none of the expected field name pairs match.
func TestReflectedTimeIdentityStructWithoutMatchingFields(t *testing.T) {
	type other struct {
		Foo int64
		Bar int64
	}
	v := reflect.ValueOf(other{Foo: 1, Bar: 2})
	if got := reflectedTimeIdentity(v); got != "" {
		t.Fatalf("reflectedTimeIdentity on struct without matching fields should return empty, got %q", got)
	}
}

// TestProjectionIdentityNil covers the nil-project path.
func TestProjectionIdentityNil(t *testing.T) {
	got := projectionIdentity(nil)
	if !strings.Contains(got, "<nil>") {
		t.Fatalf("projectionIdentity(nil) should contain '<nil>', got %q", got)
	}
}

// TestExtendPrefixStampInvalidHex covers the invalid-hex path in extendPrefixStamp.
func TestExtendPrefixStampInvalidHex(t *testing.T) {
	if got := extendPrefixStamp("not-hex", []byte("data")); got != "" {
		t.Fatalf("extendPrefixStamp with invalid hex should return empty, got %q", got)
	}
}

// TestExtendPrefixStampWrongLength covers the wrong-length path.
func TestExtendPrefixStampWrongLength(t *testing.T) {
	// Valid hex but wrong length (not 32 bytes / 64 hex chars)
	if got := extendPrefixStamp("abcd", []byte("data")); got != "" {
		t.Fatalf("extendPrefixStamp with wrong length should return empty, got %q", got)
	}
}

// TestRecordNodeVisibleNil covers the nil path.
func TestRecordNodeVisibleNil(t *testing.T) {
	if got := recordNodeVisible(nil); got != 0 {
		t.Fatalf("recordNodeVisible(nil) should return 0, got %d", got)
	}
}

// TestRecordNodeHeightNil covers the nil path.
func TestRecordNodeHeightNil(t *testing.T) {
	if got := recordNodeHeight(nil); got != 0 {
		t.Fatalf("recordNodeHeight(nil) should return 0, got %d", got)
	}
}

// TestMakeRecordBranchNilLeft covers the nil-left path.
func TestMakeRecordBranchNilLeft(t *testing.T) {
	right := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}})
	got := makeRecordBranch(nil, right)
	if got != right {
		t.Fatalf("makeRecordBranch(nil, right) should return right")
	}
}

// TestMakeRecordBranchNilRight covers the nil-right path.
func TestMakeRecordBranchNilRight(t *testing.T) {
	left := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}})
	got := makeRecordBranch(left, nil)
	if got != left {
		t.Fatalf("makeRecordBranch(left, nil) should return left")
	}
}

// TestMakeRecordBranchBothNil covers the both-nil path.
func TestMakeRecordBranchBothNil(t *testing.T) {
	got := makeRecordBranch(nil, nil)
	if got != nil {
		t.Fatalf("makeRecordBranch(nil, nil) should return nil")
	}
}

// TestJoinRecordNodesNilLeft covers the nil-left path.
func TestJoinRecordNodesNilLeft(t *testing.T) {
	right := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}})
	got := joinRecordNodes(nil, right)
	if got != right {
		t.Fatalf("joinRecordNodes(nil, right) should return right")
	}
}

// TestJoinRecordNodesNilRight covers the nil-right path.
func TestJoinRecordNodesNilRight(t *testing.T) {
	left := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}})
	got := joinRecordNodes(left, nil)
	if got != left {
		t.Fatalf("joinRecordNodes(left, nil) should return left")
	}
}

// TestJoinRecordNodesBothNil covers the both-nil path.
func TestJoinRecordNodesBothNil(t *testing.T) {
	got := joinRecordNodes(nil, nil)
	if got != nil {
		t.Fatalf("joinRecordNodes(nil, nil) should return nil")
	}
}

// TestJoinRecordNodesHeightImbalanceLeft covers the path where the left
// subtree is more than 1 taller than the right.
func TestJoinRecordNodesHeightImbalanceLeft(t *testing.T) {
	// Build a left subtree of height 3 by joining multiple leaves
	left := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}})
	for i := 2; i <= 10; i++ {
		left = joinRecordNodes(left, newRecordLeaf([]indexedTurn{{Index: i, Visible: true}}))
	}
	// A single right leaf at height 1
	right := newRecordLeaf([]indexedTurn{{Index: 11, Visible: true}})
	got := joinRecordNodes(left, right)
	if got == nil {
		t.Fatalf("joinRecordNodes with height imbalance should not return nil")
	}
}

// TestJoinRecordNodesHeightImbalanceRight covers the path where the right
// subtree is more than 1 taller than the left.
func TestJoinRecordNodesHeightImbalanceRight(t *testing.T) {
	right := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}})
	for i := 2; i <= 10; i++ {
		right = joinRecordNodes(right, newRecordLeaf([]indexedTurn{{Index: i, Visible: true}}))
	}
	left := newRecordLeaf([]indexedTurn{{Index: 12, Visible: true}})
	got := joinRecordNodes(left, right)
	if got == nil {
		t.Fatalf("joinRecordNodes with height imbalance should not return nil")
	}
}

// TestBalanceRecordNodeHeightImbalanceLeft covers the path where the left
// subtree is more than 1 taller than the right in balanceRecordNode.
func TestBalanceRecordNodeHeightImbalanceLeft(t *testing.T) {
	// Build a left subtree of height 3
	left := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}})
	for i := 2; i <= 8; i++ {
		left = joinRecordNodes(left, newRecordLeaf([]indexedTurn{{Index: i, Visible: true}}))
	}
	right := newRecordLeaf([]indexedTurn{{Index: 9, Visible: true}})
	got := balanceRecordNode(left, right)
	if got == nil {
		t.Fatalf("balanceRecordNode with height imbalance should not return nil")
	}
}

// TestBalanceRecordNodeHeightImbalanceRight covers the path where the right
// subtree is more than 1 taller than the left in balanceRecordNode.
func TestBalanceRecordNodeHeightImbalanceRight(t *testing.T) {
	right := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}})
	for i := 2; i <= 8; i++ {
		right = joinRecordNodes(right, newRecordLeaf([]indexedTurn{{Index: i, Visible: true}}))
	}
	left := newRecordLeaf([]indexedTurn{{Index: 9, Visible: true}})
	got := balanceRecordNode(left, right)
	if got == nil {
		t.Fatalf("balanceRecordNode with height imbalance should not return nil")
	}
}

// TestCloneToolNamesObservedWithStats covers the stats-recording path.
func TestCloneToolNamesObservedWithStats(t *testing.T) {
	names := map[string]string{"a": "x", "b": "y", "c": "z"}
	var stats ReadStats
	clone := cloneToolNamesObserved(names, &stats)
	if len(clone) != 3 {
		t.Fatalf("clone should have 3 entries, got %d", len(clone))
	}
	if clone["a"] != "x" || clone["b"] != "y" || clone["c"] != "z" {
		t.Fatalf("clone values wrong: %v", clone)
	}
	if stats.resolverEntriesCopied != 3 {
		t.Fatalf("resolverEntriesCopied should be 3, got %d", stats.resolverEntriesCopied)
	}
}

// mockFileInfo implements os.FileInfo for testing fileIdentity and fileChangeIdentity.
type mockFileInfo struct {
	sys any
}

func (m mockFileInfo) Name() string       { return "test" }
func (m mockFileInfo) Size() int64        { return 0 }
func (m mockFileInfo) Mode() os.FileMode  { return 0o644 }
func (m mockFileInfo) ModTime() time.Time { return time.Time{} }
func (m mockFileInfo) IsDir() bool        { return false }
func (m mockFileInfo) Sys() any           { return m.sys }

// TestPrefixStampNegativeSize covers the negative-completeSize path.
func TestPrefixStampNegativeSize(t *testing.T) {
	f, err := os.CreateTemp("", "test-*.transcript")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	_ = os.Remove(f.Name())
	stamp, readBytes, _ := prefixStamp(context.Background(), f, -1)
	if stamp != "" || readBytes != 0 {
		t.Fatalf("prefixStamp(-1) = %q, %d, want empty, 0", stamp, readBytes)
	}
}

// TestPrefixStampReadError covers the path where reading from the file fails
// (e.g., reading past EOF). We write a small file and try to read more than
// what's available, which should trigger a read error mid-loop.
func TestPrefixStampReadError(t *testing.T) {
	// Create a file with data but no trailing newline; prefixStamp reads
	// line by line until readBytes >= completeSize. A line without a newline
	// causes ReadBytes to return the data and io.EOF — that path returns
	// "", readBytes.
	dir := t.TempDir()
	path := filepath.Join(dir, "test.transcript")
	data := []byte("no newline here")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	stamp, readBytes, err := prefixStamp(context.Background(), f, int64(len(data)))
	if err != nil {
		t.Fatalf("prefixStamp on data without newline error = %v, want nil fallback error", err)
	}
	if stamp != "" {
		t.Fatalf("prefixStamp on data without newline should return empty stamp, got %q", stamp)
	}
	if readBytes != int64(len(data)) {
		t.Fatalf("readBytes = %d, want %d", readBytes, len(data))
	}
}

// TestReadTurnIndexMissingTranscript covers the os.Stat error path in readTurnIndex.
func TestReadTurnIndexMissingTranscript(t *testing.T) {
	_, err := readTurnIndex(filepath.Join(t.TempDir(), "missing.appwire-index.json"))
	if err == nil {
		t.Fatalf("readTurnIndex with missing transcript should error")
	}
}

// TestWriteTurnIndexCreateTempError covers the os.CreateTemp error path.
func TestWriteTurnIndexCreateTempError(t *testing.T) {
	// Use a path whose parent directory doesn't exist so CreateTemp fails.
	index := turnIndexDisk{Version: turnIndexVersion}
	err := writeTurnIndex(filepath.Join(t.TempDir(), "nonexistent-dir", "index.json"), index, nil)
	if err == nil {
		t.Fatalf("writeTurnIndex with nonexistent parent dir should error")
	}
}

// TestWriteTurnIndexRenameError covers the os.Rename error path.
func TestWriteTurnIndexRenameError(t *testing.T) {
	// Write to a path that cannot be renamed to (e.g., the destination is
	// in a different filesystem or the path is invalid).
	dir := t.TempDir()
	// Create a file at the target path's parent to make the target
	// path's parent a file, not a directory, so Rename fails.
	conflict := filepath.Join(dir, "conflict")
	if err := os.WriteFile(conflict, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(conflict, "index.json")
	index := turnIndexDisk{Version: turnIndexVersion}
	// CreateTemp uses filepath.Dir(path) which is "conflict" (a file),
	// so CreateTemp will fail.
	err := writeTurnIndex(targetPath, index, nil)
	if err == nil {
		t.Fatalf("writeTurnIndex with file-as-parent should error")
	}
}

// TestTurnIndexIntegrityStampObservedWithStats covers the stats-recording path.
func TestTurnIndexIntegrityStampObservedWithStats(t *testing.T) {
	index := turnIndexDisk{Version: turnIndexVersion, VisibleRecords: 5}
	var stats ReadStats
	stamp := turnIndexIntegrityStampObserved(index, &stats)
	if stamp == "" {
		t.Fatalf("integrity stamp should not be empty")
	}
	if stats.indexBytesSerialized == 0 {
		t.Fatalf("indexBytesSerialized should be non-zero")
	}
}

// TestTurnIndexJournalStampObservedWithStats covers the stats-recording path.
func TestTurnIndexJournalStampObservedWithStats(t *testing.T) {
	frame := turnIndexJournalFrame{Version: turnIndexJournalVersion}
	var stats ReadStats
	stamp := turnIndexJournalStampObserved(frame, &stats)
	if stamp == "" {
		t.Fatalf("journal stamp should not be empty")
	}
	if stats.indexBytesSerialized == 0 {
		t.Fatalf("indexBytesSerialized should be non-zero")
	}
}

// TestProjectionIdentityNonNilUnknown covers the path where the function pointer
// is non-nil but FuncForPC returns nil (which is very rare). Instead, we test
// the normal non-nil path.
func TestProjectionIdentityNonNil(t *testing.T) {
	project := func(turn schema.Turn, turnID string, turnIndex int, toolNames map[string]string) []appwire.ThreadItem {
		return nil
	}
	got := projectionIdentity(project)
	if !strings.Contains(got, "turn-index-v") {
		t.Fatalf("projectionIdentity should contain 'turn-index-v', got %q", got)
	}
}

// TestRecordNodeAtOutOfBounds covers the out-of-bounds path in recordNodeAt.
func TestRecordNodeAtOutOfBounds(t *testing.T) {
	node := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}})
	empty := indexedTurn{}
	// i < 0
	if got := recordNodeAt(node, -1); !reflect.DeepEqual(got, empty) {
		t.Fatalf("recordNodeAt(-1) should return zero indexedTurn, got %+v", got)
	}
	// i >= count
	if got := recordNodeAt(node, 10); !reflect.DeepEqual(got, empty) {
		t.Fatalf("recordNodeAt(10) should return zero indexedTurn, got %+v", got)
	}
}

// TestRecordNodeAtNilNode covers the nil-node path.
func TestRecordNodeAtNilNode(t *testing.T) {
	empty := indexedTurn{}
	if got := recordNodeAt(nil, 0); !reflect.DeepEqual(got, empty) {
		t.Fatalf("recordNodeAt(nil, 0) should return zero indexedTurn, got %+v", got)
	}
}

// TestRecordNodeAtBranchNode covers the branch-node traversal path.
func TestRecordNodeAtBranchNode(t *testing.T) {
	left := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}})
	right := newRecordLeaf([]indexedTurn{{Index: 2, Visible: true}})
	branch := makeRecordBranch(left, right)
	// Access the right child
	got := recordNodeAt(branch, 1)
	if got.Index != 2 {
		t.Fatalf("recordNodeAt(branch, 1) should return Index 2, got %d", got.Index)
	}
}

// TestVisibleNodeAtNilNode covers the nil path in visibleNodeAt.
func TestVisibleNodeAtNilNode(t *testing.T) {
	var stats ReadStats
	if _, ok := visibleNodeAt(nil, 0, &stats); ok {
		t.Fatalf("visibleNodeAt(nil, 0) should return false")
	}
}

// TestVisibleNodeAtNegativeRank covers the negative-rank path.
func TestVisibleNodeAtNegativeRank(t *testing.T) {
	node := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}})
	var stats ReadStats
	if _, ok := visibleNodeAt(node, -1, &stats); ok {
		t.Fatalf("visibleNodeAt with negative rank should return false")
	}
}

// TestVisibleNodeAtRankExceedsVisible covers the rank-exceeds-visible path.
func TestVisibleNodeAtRankExceedsVisible(t *testing.T) {
	node := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}})
	var stats ReadStats
	if _, ok := visibleNodeAt(node, 5, &stats); ok {
		t.Fatalf("visibleNodeAt with rank >= visible should return false")
	}
}

// TestVisibleNodeAtBranchNode covers the branch-node traversal path.
func TestVisibleNodeAtBranchNode(t *testing.T) {
	left := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}, {Index: 2, Visible: false}})
	right := newRecordLeaf([]indexedTurn{{Index: 3, Visible: true}})
	branch := makeRecordBranch(left, right)
	var stats ReadStats
	// rank 0 is in left (visible), rank 1 is in right (visible)
	got, ok := visibleNodeAt(branch, 1, &stats)
	if !ok || got.Index != 3 {
		t.Fatalf("visibleNodeAt(branch, 1) should return Index 3, got %+v, ok=%v", got, ok)
	}
}

// TestVisibleRecordAtNegativeRank covers the negative-rank path.
func TestVisibleRecordAtNegativeRank(t *testing.T) {
	d := turnIndexDisk{VisibleRecords: 5}
	if _, ok := d.visibleRecordAt(-1, nil); ok {
		t.Fatalf("visibleRecordAt(-1) should return false")
	}
}

// TestVisibleRecordAtRankExceedsVisible covers the rank-exceeds path.
func TestVisibleRecordAtRankExceedsVisible(t *testing.T) {
	d := turnIndexDisk{VisibleRecords: 2, Records: []indexedTurn{{Index: 1, Visible: true}, {Index: 2, Visible: true}}}
	if _, ok := d.visibleRecordAt(5, nil); ok {
		t.Fatalf("visibleRecordAt(5) with 2 visible records should return false")
	}
}

// TestVisibleRecordAtWithDeltaRoot covers the path where the visible record
// is in the delta root (beyond the base records).
func TestVisibleRecordAtWithDeltaRoot(t *testing.T) {
	base := []indexedTurn{{Index: 1, Visible: true}}
	d := turnIndexDisk{
		VisibleRecords: 2,
		Records:        base,
		deltaRoot:      newRecordLeaf([]indexedTurn{{Index: 2, Visible: true}}),
	}
	// baseVisible is nil, so it's computed from Records.
	got, ok := d.visibleRecordAt(1, nil)
	if !ok || got.Index != 2 {
		t.Fatalf("visibleRecordAt(1) should return Index 2 from delta root, got %+v, ok=%v", got, ok)
	}
}

// TestCloneToolNamesWithStats covers the stats-recording path of cloneToolNames.
func TestCloneToolNamesWithStats(t *testing.T) {
	names := map[string]string{"a": "x", "b": "y"}
	var stats ReadStats
	clone := cloneToolNamesObserved(names, &stats)
	if len(clone) != 2 {
		t.Fatalf("clone should have 2 entries, got %d", len(clone))
	}
	if stats.resolverEntriesCopied != 2 {
		t.Fatalf("resolverEntriesCopied should be 2, got %d", stats.resolverEntriesCopied)
	}
}

// TestNewRecordLeafEmpty covers the empty-input path.
func TestNewRecordLeafEmpty(t *testing.T) {
	if got := newRecordLeaf(nil); got != nil {
		t.Fatalf("newRecordLeaf(nil) should return nil")
	}
	if got := newRecordLeaf([]indexedTurn{}); got != nil {
		t.Fatalf("newRecordLeaf(empty) should return nil")
	}
}

// TestNewRecordLeafWithInvisibleRecords covers the visible-count computation.
func TestNewRecordLeafWithInvisibleRecords(t *testing.T) {
	records := []indexedTurn{
		{Index: 1, Visible: true},
		{Index: 2, Visible: false},
		{Index: 3, Visible: true},
	}
	node := newRecordLeaf(records)
	if node.visible != 2 {
		t.Fatalf("visible count should be 2, got %d", node.visible)
	}
	if node.count != 3 {
		t.Fatalf("count should be 3, got %d", node.count)
	}
}

// TestTranscriptAnchorsZeroSize covers the zero-size path.
func TestTranscriptAnchorsZeroSize(t *testing.T) {
	f, err := os.CreateTemp("", "test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	first, tail := transcriptAnchors(f, 0)
	if first != (turnIndexAnchor{}) || tail != (turnIndexAnchor{}) {
		t.Fatalf("transcriptAnchors(0) should return empty anchors, got %+v %+v", first, tail)
	}
}

// TestAnchorsMatchObservedEmptyAnchor covers the empty-anchor path.
func TestAnchorsMatchObservedEmptyAnchor(t *testing.T) {
	f, err := os.CreateTemp("", "test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if anchorsMatchObserved(f, nil, turnIndexAnchor{}) {
		t.Fatalf("anchorsMatchObserved with empty anchor should return false")
	}
}

// TestAnchorsMatchObservedReadError covers the path where reading fails.
func TestAnchorsMatchObservedReadError(t *testing.T) {
	f, err := os.CreateTemp("", "test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	// An anchor with a valid stamp but impossible offset/length should fail
	// to read.
	anchor := turnIndexAnchor{Offset: 999999, Length: 1, Stamp: "abc"}
	if anchorsMatchObserved(f, nil, anchor) {
		t.Fatalf("anchorsMatchObserved with unreadable offset should return false")
	}
}

// TestEqualToolNamesBothNil covers the both-nil path.
func TestEqualToolNamesBothNil(t *testing.T) {
	if !equalToolNames(nil, nil) {
		t.Fatalf("equalToolNames(nil, nil) should return true")
	}
}
