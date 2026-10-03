package memory

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func str(s string) *string { return &s }
func image(t *testing.T, tx pendingTransaction, name string) []byte {
	t.Helper()
	for _, w := range tx.Writes {
		if w.Name == name {
			return w.Bytes
		}
	}
	t.Fatalf("no after-image %s", name)
	return nil
}
func settled(t *testing.T, old Snapshot, tx pendingTransaction) Snapshot {
	t.Helper()
	next := Snapshot{Meta: old.Meta, Index: old.Index, Pages: map[string]Page{}}
	for id, p := range old.Pages {
		next.Pages[id] = p
	}
	for _, name := range tx.Deletes {
		delete(next.Pages, strings.TrimSuffix(name, ".md"))
	}
	for _, w := range tx.Writes {
		switch w.Name {
		case ".metadata.json":
			if err := json.Unmarshal(w.Bytes, &next.Meta); err != nil {
				t.Fatal(err)
			}
		case "index.md":
			next.Index = string(w.Bytes)
		default:
			if strings.HasSuffix(w.Name, ".md") {
				id := strings.TrimSuffix(w.Name, ".md")
				next.Pages[id] = Page{Body: string(w.Bytes)}
			}
		}
	}
	for id, p := range next.Pages {
		p.Meta = next.Meta.Pages[id]
		next.Pages[id] = p
	}
	return next
}
func batch(t *testing.T, s Snapshot, r ApplyRequest, now time.Time) (Snapshot, pendingTransaction, bool) {
	t.Helper()
	r.ExpectedRevision = s.Meta.Revision
	tx, changed, err := buildBatch(s, r, Actor{SessionRef: "session-sentinel"}, now)
	if err != nil {
		t.Fatal(err)
	}
	return settled(t, s, tx), tx, changed
}
func assertCode(t *testing.T, err error, code string) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("got %v, want typed %s", err, code)
	}
	return e
}
func assertLimit(t *testing.T, err error, kind string, actual, maximum int) {
	t.Helper()
	e := assertCode(t, err, "limit")
	if e.Limit == nil || *e.Limit != (LimitDetail{Kind: kind, Actual: actual, Maximum: maximum}) {
		t.Fatalf("limit %#v", e.Limit)
	}
}

// Timestamp and revision assertions catch accidental review on put and lost incarnation dates.
func TestBatchCreatedUpdatedReviewedIncarnation(t *testing.T) {
	t.Parallel()
	first := time.Date(2026, 10, 3, 23, 0, 0, 0, time.UTC)
	put := ApplyRequest{OperationID: "put", Pages: []PageOperation{{Kind: "put", PageID: "a", Body: str("abc")}}, Index: str("- [A](a.md): S\n")}
	s, tx, changed := batch(t, Snapshot{}, put, first.In(time.FixedZone("offset", 3600)))
	want := PageMeta{Hash: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", Created: first, Updated: first}
	if !changed || s.Meta.Revision != 1 || s.Meta.ChangeSequence != 1 || !reflect.DeepEqual(s.Pages["a"].Meta, want) {
		t.Fatalf("snapshot %#v", s)
	}
	if tx.Receipt.Actor.SessionRef != "session-sentinel" || tx.Receipt.At.Location() != time.UTC {
		t.Fatalf("receipt %#v", tx.Receipt)
	}
	put.Index = nil
	s, tx, changed = batch(t, s, put, first.Add(time.Hour))
	if changed || s.Meta.Revision != 1 || s.Meta.ChangeSequence != 1 || !reflect.DeepEqual(s.Pages["a"].Meta, want) || len(tx.Writes) != 0 {
		t.Fatalf("no-op %#v %#v", s, tx)
	}
	for _, at := range []time.Time{first.Add(2 * time.Hour), first.Add(3 * time.Hour)} {
		s, _, changed = batch(t, s, ApplyRequest{OperationID: "review", Pages: []PageOperation{{Kind: "review", PageID: "a"}}}, at)
		want.Reviewed = &at
		if !changed || !reflect.DeepEqual(s.Pages["a"].Meta, want) {
			t.Fatalf("review %#v", s.Pages["a"].Meta)
		}
	}
	s, tx, _ = batch(t, s, ApplyRequest{OperationID: "delete", Pages: []PageOperation{{Kind: "delete", PageID: "a"}}, Index: str("")}, first.Add(4*time.Hour))
	if len(s.Pages) != 0 || !reflect.DeepEqual(tx.Deletes, []string{"a.md"}) {
		t.Fatalf("delete %#v", tx)
	}
	put.Index = str("- [A](a.md): S\n")
	s, _, _ = batch(t, s, put, first.Add(5*time.Hour))
	if s.Pages["a"].Meta.Created != first.Add(5*time.Hour) || s.Pages["a"].Meta.Updated != first.Add(5*time.Hour) || s.Pages["a"].Meta.Reviewed != nil {
		t.Fatalf("recreate %#v", s.Pages["a"].Meta)
	}
}

func TestIndexSemanticChangeVersusMovement(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	s, _, _ := batch(t, Snapshot{}, ApplyRequest{OperationID: "create", Pages: []PageOperation{{Kind: "put", PageID: "a", Body: str("A")}, {Kind: "put", PageID: "b", Body: str("B")}}, Index: str("## Old\n\n- [A](a.md): One\n- [B](b.md): Two\n")}, at)
	oldHash := s.Meta.IndexHash
	s, _, changed := batch(t, s, ApplyRequest{OperationID: "move", Index: str("## New\n\n- [B](b.md): Two\n- [A](a.md): One\n")}, at.Add(time.Hour))
	if !changed || s.Meta.Revision != 2 || s.Meta.IndexHash == oldHash || s.Pages["a"].Meta.Updated != at || s.Pages["b"].Meta.Updated != at {
		t.Fatalf("movement %#v", s)
	}
	s, tx, _ := batch(t, s, ApplyRequest{OperationID: "rename", Index: str("## New\n\n- [B](b.md): Two\n- [Renamed](a.md): Different\n")}, at.Add(2*time.Hour))
	if s.Pages["a"].Meta.Updated != at.Add(2*time.Hour) || s.Pages["b"].Meta.Updated != at || len(tx.Receipt.Pages) != 1 || tx.Receipt.Pages[0].PageID != "a" {
		t.Fatalf("rename %#v %#v", s, tx.Receipt)
	}
	s, _, changed = batch(t, s, ApplyRequest{OperationID: "escape", Index: str("## New\n\n- [B](b.md): Two\n- [Renamed](a.md): Different\n")}, at.Add(3*time.Hour))
	if changed {
		t.Fatal("identical index changed")
	}
	s, _, _ = batch(t, s, ApplyRequest{OperationID: "body", Pages: []PageOperation{{Kind: "put", PageID: "a", Body: str("new")}}}, at.Add(4*time.Hour))
	if s.Pages["a"].Meta.Updated != at.Add(4*time.Hour) || s.Pages["a"].Meta.Created != at {
		t.Fatalf("body dates %#v", s.Pages["a"].Meta)
	}
}

func TestIndexCoverageAndFinalBatchLinks(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	create := ApplyRequest{OperationID: "create", Pages: []PageOperation{{Kind: "put", PageID: "a", Body: str("prefix\n[incoming](b.md#frag)\n")}, {Kind: "put", PageID: "b", Body: str("B")}}, Index: str("- [A](a.md): One\n- [B](b.md): Two\n")}
	for _, idx := range []string{"- [A](a.md): One\n", "- [A](a.md): One\n- [A](a.md): Duplicate\n- [B](b.md): Two\n", "- [A](a.md): One\n- [B](b.md): Two\n- [C](c.md): Orphan\n"} {
		bad := create
		bad.Index = str(idx)
		_, _, err := buildBatch(Snapshot{}, bad, Actor{}, at)
		assertCode(t, err, "invalid_input")
	}
	s, _, _ := batch(t, Snapshot{}, create, at)
	deletion := ApplyRequest{ExpectedRevision: s.Meta.Revision, OperationID: "delete", Pages: []PageOperation{{Kind: "delete", PageID: "b"}}, Index: str("- [A](a.md): One\n")}
	_, _, err := buildBatch(s, deletion, Actor{}, at.Add(time.Hour))
	e := assertCode(t, err, "invalid_input")
	if !reflect.DeepEqual(e.References, []Reference{{Source: "a", Destination: "b.md", Line: 2}}) {
		t.Fatalf("references %#v", e.References)
	}
	deletion.Index = nil
	_, _, err = buildBatch(s, deletion, Actor{}, at)
	assertCode(t, err, "invalid_input")
	deletion.Index = str("- [A](a.md): One\n")
	deletion.Pages = append(deletion.Pages, PageOperation{Kind: "put", PageID: "a", Body: str("repaired\n")})
	s, _, _ = batch(t, s, deletion, at.Add(2*time.Hour))
	if len(s.Pages) != 1 || s.Pages["a"].Body != "repaired\n" {
		t.Fatalf("final %#v", s)
	}
}

func TestBatchLimitsAndDuplicateOperations(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	for _, size := range []int{65536, 65537} {
		r := ApplyRequest{OperationID: "size", Pages: []PageOperation{{Kind: "put", PageID: "a", Body: str(strings.Repeat("x", size))}}, Index: str("- [A](a.md): S\n")}
		_, _, err := buildBatch(Snapshot{}, r, Actor{}, at)
		if size == 65536 {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			assertLimit(t, err, "topic", 65537, 65536)
		}
	}
	for _, size := range []int{16384, 16385} {
		_, _, err := buildBatch(Snapshot{}, ApplyRequest{OperationID: "index", Index: str(strings.Repeat("x", size))}, Actor{}, at)
		if size == 16384 {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			assertLimit(t, err, "index", 16385, 16384)
		}
	}
	for _, count := range []int{16, 17} {
		r := ApplyRequest{OperationID: "ops"}
		var idx strings.Builder
		for i := 0; i < count; i++ {
			id := string(rune('a' + i))
			r.Pages = append(r.Pages, PageOperation{Kind: "put", PageID: id, Body: str("")})
			idx.WriteString("- [P](" + id + ".md): S\n")
		}
		r.Index = str(idx.String())
		_, _, err := buildBatch(Snapshot{}, r, Actor{}, at)
		if count == 16 {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			assertLimit(t, err, "operations", 17, 16)
		}
	}
	// Submitted budget includes raw index, even if its supplied dates disappear.
	for _, extra := range []int{0, 1} {
		idx := "- [A](a.md): S\n- [B](b.md): S\n- [C](c.md): S\n- [D](d.md): S\n"
		r := ApplyRequest{OperationID: "submitted", Index: str(idx)}
		for i := 0; i < 4; i++ {
			n := 65536
			if i == 3 {
				n -= len(idx) - extra
			}
			r.Pages = append(r.Pages, PageOperation{Kind: "put", PageID: string(rune('a' + i)), Body: str(strings.Repeat("x", n))})
		}
		_, _, err := buildBatch(Snapshot{}, r, Actor{}, at)
		if extra == 0 {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			assertLimit(t, err, "submitted", 262145, 262144)
		}
	}
	for _, ops := range [][]PageOperation{
		{{Kind: "put", PageID: "a", Body: str("x")}, {Kind: "put", PageID: "a", Body: str("x")}},
		{{Kind: "put", PageID: "a", Body: str("x")}, {Kind: "review", PageID: "a"}},
	} {
		_, _, err := buildBatch(Snapshot{}, ApplyRequest{OperationID: "dup", Pages: ops}, Actor{}, at)
		assertCode(t, err, "invalid_input")
	}
}

func TestReviewMissingAndEmptyBodyPut(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	for _, kind := range []string{"review", "delete"} {
		_, _, err := buildBatch(Snapshot{}, ApplyRequest{OperationID: kind, Pages: []PageOperation{{Kind: kind, PageID: "a"}}}, Actor{}, at)
		assertCode(t, err, "missing_page")
	}
	r := ApplyRequest{OperationID: "empty", Pages: []PageOperation{{Kind: "put", PageID: "a", Body: str("")}}}
	_, _, err := buildBatch(Snapshot{}, r, Actor{}, at)
	assertCode(t, err, "invalid_input")
	r.Index = str("- [A](a.md): S\n")
	s, _, _ := batch(t, Snapshot{}, r, at)
	p, ok := s.Pages["a"]
	if !ok || p.Body != "" || p.Meta.Hash != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty %#v", s)
	}
}

func TestBatchRequestValidationAndIsolation(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"", "/bad", "a.b", strings.Repeat("x", 65), "é"} {
		_, _, err := buildBatch(Snapshot{}, ApplyRequest{OperationID: id}, Actor{}, at)
		assertCode(t, err, "invalid_input")
	}
	for _, id := range []string{"A_2-ok", strings.Repeat("X", 64)} {
		_, changed, err := buildBatch(Snapshot{}, ApplyRequest{OperationID: id}, Actor{}, at)
		if err != nil || changed {
			t.Fatalf("id %q changed %v err %v", id, changed, err)
		}
	}
	for _, op := range []PageOperation{{Kind: "put", PageID: "a"}, {Kind: "other", PageID: "a"}, {Kind: "review", PageID: "a", Body: str("")}, {Kind: "delete", PageID: "a", Body: str("")}, {Kind: "put", PageID: "a", Body: str("\xff")}} {
		_, _, err := buildBatch(Snapshot{}, ApplyRequest{OperationID: "bad", Pages: []PageOperation{op}}, Actor{}, at)
		assertCode(t, err, "invalid_input")
	}
	s, _, _ := batch(t, Snapshot{}, ApplyRequest{OperationID: "create", Pages: []PageOperation{{Kind: "put", PageID: "a", Body: str("abc")}}, Index: str("- [A](a.md): S\n")}, at)
	original := s.Pages["a"]
	_, _, err := buildBatch(s, ApplyRequest{OperationID: "stale", ExpectedRevision: 0}, Actor{}, at)
	assertCode(t, err, "conflict")
	_, _, _ = buildBatch(s, ApplyRequest{OperationID: "fail", ExpectedRevision: 1, Pages: []PageOperation{{Kind: "delete", PageID: "a"}}}, Actor{}, at)
	if !reflect.DeepEqual(s.Pages["a"], original) || len(s.Meta.Pages) != 1 {
		t.Fatal("input snapshot mutated")
	}
}

// Fragments are not wiki identity and can contain arbitrary body text or paths.
func TestBatchReferenceErrorsAreBounded(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("[missing](z.md#fragment)\n", 17) + "[private](z.md#" + strings.Repeat("private-sentinel", 1000) + ")\n"
	r := ApplyRequest{OperationID: "references", Pages: []PageOperation{{Kind: "put", PageID: "a", Body: str(body)}}, Index: str("- [A](a.md): S\n")}
	_, _, err := buildBatch(Snapshot{}, r, Actor{}, time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))
	e := assertCode(t, err, "invalid_input")
	if len(e.References) != 16 || !e.MoreReferences {
		t.Fatalf("reference count %d more %v", len(e.References), e.MoreReferences)
	}
	for _, ref := range e.References {
		if ref.Source != "a" || ref.Destination != "z.md" {
			t.Fatalf("unbounded/noncanonical reference source=%q destination bytes=%d", ref.Source, len(ref.Destination))
		}
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(e.Error()), &fields); err != nil {
		t.Fatal(err)
	}
	if fields["code"] != "invalid_input" || len(e.Error()) > 4096 || strings.Contains(e.Error(), "private-sentinel") {
		t.Fatalf("error code %v bytes %d", fields["code"], len(e.Error()))
	}
}
