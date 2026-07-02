package appsource

import (
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestLocalDaemonThreadStatus(t *testing.T) {
	known := []string{
		appwire.ThreadStatusActive,
		appwire.ThreadStatusAwaiting,
		appwire.ThreadStatusWarning,
		appwire.ThreadStatusSystemError,
		appwire.ThreadStatusClosed,
		appwire.ThreadStatusNotLoaded,
		appwire.ThreadStatusIdle,
	}
	for _, s := range known {
		if got := localDaemonThreadStatus("  " + s + " "); got != s {
			t.Errorf("localDaemonThreadStatus(%q) = %q, want %q", s, got, s)
		}
	}
	if got := localDaemonThreadStatus("bogus"); got != appwire.ThreadStatusIdle {
		t.Errorf("unknown status = %q, want idle", got)
	}
	if got := localDaemonThreadStatus(""); got != appwire.ThreadStatusIdle {
		t.Errorf("empty status = %q, want idle", got)
	}
}

func TestFirstLocalHelpers(t *testing.T) {
	if got := firstLocalDaemonValue("", "", "third"); got != "third" {
		t.Errorf("firstLocalDaemonValue = %q, want third", got)
	}
	if got := firstLocalDaemonValue("", ""); got != "" {
		t.Errorf("firstLocalDaemonValue all-empty = %q, want empty", got)
	}
	if got := firstLocalNonEmpty("", "a", "b"); got != "a" {
		t.Errorf("firstLocalNonEmpty = %q, want a", got)
	}
	if got := firstLocalNonEmpty(); got != "" {
		t.Errorf("firstLocalNonEmpty() = %q, want empty", got)
	}
}

func TestLocalThreadTimestamps(t *testing.T) {
	// UpdatedAt wins when present; otherwise falls back to CreatedAt.
	if got := localThreadUpdatedAt(appwire.Thread{UpdatedAt: 10, CreatedAt: 5}); got != 10 {
		t.Errorf("updatedAt = %d, want 10", got)
	}
	if got := localThreadUpdatedAt(appwire.Thread{CreatedAt: 5}); got != 5 {
		t.Errorf("updatedAt fallback = %d, want 5", got)
	}
	if got := localThreadUpdatedAt(appwire.Thread{}); got != 0 {
		t.Errorf("updatedAt zero = %d, want 0", got)
	}
	// CreatedAt wins when present; otherwise falls back to UpdatedAt.
	if got := localThreadCreatedAt(appwire.Thread{CreatedAt: 7, UpdatedAt: 3}); got != 7 {
		t.Errorf("createdAt = %d, want 7", got)
	}
	if got := localThreadCreatedAt(appwire.Thread{UpdatedAt: 3}); got != 3 {
		t.Errorf("createdAt fallback = %d, want 3", got)
	}
	if got := localThreadCreatedAt(appwire.Thread{}); got != 0 {
		t.Errorf("createdAt zero = %d, want 0", got)
	}
}

func TestLocalThreadTitle(t *testing.T) {
	if got := localThreadTitle(appwire.Thread{Preview: "prev", SessionID: "sid"}); got != "prev" {
		t.Errorf("title = %q, want prev", got)
	}
	if got := localThreadTitle(appwire.Thread{Name: "Named", Preview: "prev"}); got != "Named" {
		t.Errorf("title prefers Name = %q, want Named", got)
	}
}

func TestCompareLocalOrderText(t *testing.T) {
	if compareLocalOrderText("apple", "banana") >= 0 {
		t.Error("apple should sort before banana")
	}
	if compareLocalOrderText("banana", "apple") <= 0 {
		t.Error("banana should sort after apple")
	}
	// Case-insensitive primary key; when folded-equal, ASCII order breaks the tie.
	if compareLocalOrderText("Apple", "apple") >= 0 {
		t.Error("uppercase 'Apple' should precede 'apple' on the case tiebreak")
	}
	if compareLocalOrderText("apple", "Apple") <= 0 {
		t.Error("'apple' should follow 'Apple' on the case tiebreak")
	}
	if compareLocalOrderText(" same ", "same") != 0 {
		t.Error("trimmed-equal strings should compare equal")
	}
}

func TestLocalThreadLess(t *testing.T) {
	// More-recently-updated sorts first (descending).
	newer := appwire.Thread{UpdatedAt: 200}
	older := appwire.Thread{UpdatedAt: 100}
	if !localThreadLess(newer, older) {
		t.Error("newer thread should sort before older")
	}
	if localThreadLess(older, newer) {
		t.Error("older thread should not sort before newer")
	}

	// Equal updated: fall back to createdAt descending.
	a := appwire.Thread{UpdatedAt: 100, CreatedAt: 50}
	b := appwire.Thread{UpdatedAt: 100, CreatedAt: 40}
	if !localThreadLess(a, b) {
		t.Error("later-created thread should sort first on the createdAt tiebreak")
	}

	// Equal timestamps: fall back to title order.
	ta := appwire.Thread{UpdatedAt: 100, CreatedAt: 50, Preview: "aaa"}
	tb := appwire.Thread{UpdatedAt: 100, CreatedAt: 50, Preview: "bbb"}
	if !localThreadLess(ta, tb) {
		t.Error("title 'aaa' should sort before 'bbb'")
	}

	// Equal everything but ID: fall back to ID order.
	ia := appwire.Thread{UpdatedAt: 100, CreatedAt: 50, Preview: "x", ID: "id-1"}
	ib := appwire.Thread{UpdatedAt: 100, CreatedAt: 50, Preview: "x", ID: "id-2"}
	if !localThreadLess(ia, ib) {
		t.Error("id-1 should sort before id-2")
	}
}
