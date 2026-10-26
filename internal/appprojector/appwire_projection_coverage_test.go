package appprojector

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestDelegateActivityAfterEmptyCandidate(t *testing.T) {
	if delegateActivityAfter("", "2024-01-01T00:00:00Z") {
		t.Fatal("empty candidate should return false")
	}
}

func TestDelegateActivityAfterEmptyCurrent(t *testing.T) {
	if !delegateActivityAfter("2024-01-01T00:00:00Z", "") {
		t.Fatal("non-empty candidate with empty current should return true")
	}
}

func TestDelegateActivityAfterNewerCandidate(t *testing.T) {
	if !delegateActivityAfter("2024-01-02T00:00:00Z", "2024-01-01T00:00:00Z") {
		t.Fatal("newer candidate should return true")
	}
}

func TestDelegateActivityAfterOlderCandidate(t *testing.T) {
	if delegateActivityAfter("2024-01-01T00:00:00Z", "2024-01-02T00:00:00Z") {
		t.Fatal("older candidate should return false")
	}
}

func TestDelegateActivityAfterInvalidTimestamp(t *testing.T) {
	if delegateActivityAfter("not-a-timestamp", "2024-01-01T00:00:00Z") {
		t.Fatal("invalid candidate should return false")
	}
	if delegateActivityAfter("2024-01-01T00:00:00Z", "not-a-timestamp") {
		t.Fatal("invalid current should return false")
	}
}

func TestDelegateActivityAfterSameTimestamp(t *testing.T) {
	ts := "2024-01-01T00:00:00Z"
	if delegateActivityAfter(ts, ts) {
		t.Fatal("same timestamp should return false (not strictly after)")
	}
}

func TestMergeAppwireDelegateInfoNewIncoming(t *testing.T) {
	current := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 2, Status: "running"}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if !changed {
		t.Fatal("higher projection revision should trigger merge")
	}
	if merged.ProjectionRevision != 2 || merged.Status != "running" {
		t.Fatal("should merge incoming fields")
	}
}

func TestMergeAppwireDelegateInfoSameRevisionNewerActivity(t *testing.T) {
	current := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-02T00:00:00Z"}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-03T00:00:00Z"}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if !changed {
		t.Fatal("newer activity at same revision should trigger update")
	}
	if merged.LatestActivityAt != "2024-01-03T00:00:00Z" {
		t.Fatalf("should use newer activity timestamp, got %q", merged.LatestActivityAt)
	}
}

func TestMergeAppwireDelegateInfoNoChange(t *testing.T) {
	current := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-02T00:00:00Z"}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-01T00:00:00Z"}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if changed {
		t.Fatal("older activity at same revision should not trigger update")
	}
	if merged.LatestActivityAt != "2024-01-02T00:00:00Z" {
		t.Fatal("should keep current activity timestamp")
	}
}

func TestMergeAppwireDelegateInfoEmptyCurrent(t *testing.T) {
	current := appwire.EvenerDelegateInfo{}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if !changed {
		t.Fatal("empty current DelegateID should trigger merge")
	}
	if merged.DelegateID != "dlg_1" {
		t.Fatal("should use incoming")
	}
}
