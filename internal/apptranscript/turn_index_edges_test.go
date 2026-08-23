package apptranscript

import (
	"os"
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
