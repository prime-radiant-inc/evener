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

// mockFileInfo implements os.FileInfo for testing FileIdentity and fileChangeIdentity.
type mockFileInfo struct {
	sys any
}

func (m mockFileInfo) Name() string       { return "test" }
func (m mockFileInfo) Size() int64        { return 0 }
func (m mockFileInfo) Mode() os.FileMode  { return 0o644 }
func (m mockFileInfo) ModTime() time.Time { return time.Time{} }
func (m mockFileInfo) IsDir() bool        { return false }
func (m mockFileInfo) Sys() any           { return m.sys }

func TestFullProjectorNilProjectReturnsNil(t *testing.T) {
	fp := fullProjector(nil)
	turn := schema.NewTurn(schema.TurnUserInput, llm.User("hello"))
	items := fp(turn, "turn-1", 0)
	if items != nil {
		t.Fatalf("fullProjector(nil) should return nil, got %v", items)
	}
}

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

func TestInstallReadObserverForTestingObserveAndRestore(t *testing.T) {
	var observed []ReadStats
	restore := InstallReadObserverForTesting(func(stats ReadStats) {
		observed = append(observed, stats)
	})

	observeIndexRead(ReadStats{usageScans: 7})
	if len(observed) != 1 || observed[0].usageScans != 7 {
		t.Fatalf("observed = %+v, want one 7-scan report", observed)
	}

	restore()
	observeIndexRead(ReadStats{usageScans: 9})
	if len(observed) != 1 {
		t.Fatalf("observer remained installed after restore: %+v", observed)
	}
}

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

func TestFileIdentityNilInfo(t *testing.T) {
	if got := FileIdentity(nil); got != "" {
		t.Fatalf("FileIdentity(nil) should return empty, got %q", got)
	}
}

func TestFileIdentityNoSys(t *testing.T) {
	info := mockFileInfo{sys: nil}
	if got := FileIdentity(info); got != "" {
		t.Fatalf("FileIdentity with nil Sys should return empty, got %q", got)
	}
}

func TestFileIdentityNonStructSys(t *testing.T) {
	info := mockFileInfo{sys: "not a struct"}
	if got := FileIdentity(info); got != "" {
		t.Fatalf("FileIdentity with non-struct Sys should return empty, got %q", got)
	}
}

func TestFileIdentityWindowsStruct(t *testing.T) {
	type winStat struct {
		VolumeSerialNumber uint64
		FileIndexHigh      uint64
		FileIndexLow       uint64
	}
	info := mockFileInfo{sys: winStat{VolumeSerialNumber: 42, FileIndexHigh: 7, FileIndexLow: 3}}
	got := FileIdentity(info)
	if !strings.HasPrefix(got, "volume:") {
		t.Fatalf("expected 'volume:' prefix for Windows file identity, got %q", got)
	}
}

func TestFileIdentityWindowsShortNames(t *testing.T) {
	type winStat struct {
		vol   uint64
		idxhi uint64
		idxlo uint64
	}
	info := mockFileInfo{sys: winStat{vol: 99, idxhi: 1, idxlo: 2}}
	got := FileIdentity(info)
	if !strings.HasPrefix(got, "volume:99:") {
		t.Fatalf("expected 'volume:99:' prefix for Windows short names, got %q", got)
	}
}

func TestFileIdentityDevIno(t *testing.T) {
	type unixStat struct {
		Dev uint64
		Ino uint64
	}
	info := mockFileInfo{sys: unixStat{Dev: 10, Ino: 20}}
	got := FileIdentity(info)
	want := "dev:10:ino:20"
	if got != want {
		t.Fatalf("FileIdentity with Dev/Ino = %q, want %q", got, want)
	}
}

func TestFileIdentityNonIntField(t *testing.T) {
	type weirdStat struct {
		Dev string // wrong kind
		Ino uint64
	}
	info := mockFileInfo{sys: weirdStat{Dev: "not-a-number", Ino: 20}}
	got := FileIdentity(info)
	// Dev field is a string so field("Dev") returns false; should fall through
	if got != "" {
		t.Fatalf("FileIdentity with non-int Dev should return empty, got %q", got)
	}
}

func TestFileIdentityNoMatchingFields(t *testing.T) {
	type emptyStat struct {
		Foo string
	}
	info := mockFileInfo{sys: emptyStat{Foo: "bar"}}
	got := FileIdentity(info)
	if got != "" {
		t.Fatalf("FileIdentity with no matching fields should return empty, got %q", got)
	}
}

func TestFileChangeIdentityNilInfo(t *testing.T) {
	if got := fileChangeIdentity(nil); got != "" {
		t.Fatalf("fileChangeIdentity(nil) should return empty, got %q", got)
	}
}

func TestFileChangeIdentityNoSys(t *testing.T) {
	info := mockFileInfo{sys: nil}
	if got := fileChangeIdentity(info); got != "" {
		t.Fatalf("fileChangeIdentity with nil Sys should return empty, got %q", got)
	}
}

func TestFileChangeIdentityNonStructSys(t *testing.T) {
	info := mockFileInfo{sys: "not a struct"}
	if got := fileChangeIdentity(info); got != "" {
		t.Fatalf("fileChangeIdentity with non-struct Sys should return empty, got %q", got)
	}
}

func TestFileChangeIdentityWindowsHighLow(t *testing.T) {
	type winStat struct {
		ChangeTimeHigh uint64
		ChangeTimeLow  uint64
	}
	info := mockFileInfo{sys: winStat{ChangeTimeHigh: 5, ChangeTimeLow: 10}}
	got := fileChangeIdentity(info)
	if !strings.HasPrefix(got, "ChangeTime:") {
		t.Fatalf("expected 'ChangeTime:' prefix for Windows change identity, got %q", got)
	}
}

// TestFileChangeIdentityHighDateTime covers the HighDateTime/LowDateTime
// struct path in reflectedTimeIdentity.
func TestFileChangeIdentityHighDateTime(t *testing.T) {
	type winTimespec struct {
		HighDateTime int64
		LowDateTime  int64
	}
	// The Ctime/ChangeTime field name is "ChangeTime" on Windows, but the code
	// also checks "Ctime". Let's test HighDateTime/LowDateTime via a field
	// named "ChangeTime" that is a struct with HighDateTime/LowDateTime.
	type winStat struct {
		ChangeTime winTimespec
	}
	info := mockFileInfo{sys: winStat{ChangeTime: winTimespec{HighDateTime: 100, LowDateTime: 200}}}
	got := fileChangeIdentity(info)
	if !strings.HasPrefix(got, "ChangeTime:") {
		t.Fatalf("expected 'ChangeTime:' prefix for Windows HighDateTime, got %q", got)
	}
	if !strings.Contains(got, "100") || !strings.Contains(got, "200") {
		t.Fatalf("expected HighDateTime/LowDateTime values in %q", got)
	}
}

func TestFileChangeIdentityCTime(t *testing.T) {
	type unixStat struct {
		Ctime struct {
			Sec  int64
			Nsec int64
		}
	}
	info := mockFileInfo{sys: unixStat{Ctime: struct {
		Sec  int64
		Nsec int64
	}{Sec: 42, Nsec: 99}}}
	got := fileChangeIdentity(info)
	if !strings.HasPrefix(got, "Ctime:") {
		t.Fatalf("expected 'Ctime:' prefix, got %q", got)
	}
}

func TestReflectedTimeIdentityNilValue(t *testing.T) {
	if got := reflectedTimeIdentity(reflect.Value{}); got != "" {
		t.Fatalf("reflectedTimeIdentity on invalid value should return empty, got %q", got)
	}
}

func TestReflectedTimeIdentityInt(t *testing.T) {
	v := reflect.ValueOf(int64(12345))
	if got := reflectedTimeIdentity(v); got != "12345" {
		t.Fatalf("reflectedTimeIdentity on int64 12345 should return '12345', got %q", got)
	}
}

func TestReflectedTimeIdentityUint(t *testing.T) {
	v := reflect.ValueOf(uint64(42))
	if got := reflectedTimeIdentity(v); got != "42" {
		t.Fatalf("reflectedTimeIdentity on uint 42 should return '42', got %q", got)
	}
}

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

func TestReflectedTimeIdentityStringKindReturnsEmpty(t *testing.T) {
	v := reflect.ValueOf("hello")
	if got := reflectedTimeIdentity(v); got != "" {
		t.Fatalf("reflectedTimeIdentity on string should return empty, got %q", got)
	}
}

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

func TestReflectedTimeIdentityHighDateTimeStruct(t *testing.T) {
	type winTimespec struct {
		HighDateTime int64
		LowDateTime  int64
	}
	v := reflect.ValueOf(winTimespec{HighDateTime: 7, LowDateTime: 8})
	got := reflectedTimeIdentity(v)
	want := "7:8"
	if got != want {
		t.Fatalf("reflectedTimeIdentity on HighDateTime/LowDateTime = %q, want %q", got, want)
	}
}
