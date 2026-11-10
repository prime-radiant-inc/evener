package rendezvous

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// FuzzRendezvousPersistence replays a program of Write / Remove / List
// operations against a real directory and checks List against a model of what
// the program has written: the last Write per pid wins, Remove deletes it, and
// every Write is visible to the next List.
//
// Entries carry their own StartedAt timestamp (Write mints no time of its own),
// so the program supplies deterministic timestamps directly.
func FuzzRendezvousPersistence(f *testing.F) {
	exerciseRendezvousBranches(f)

	f.Add([]byte{opWrite, 1, opWrite, 2, opList})
	f.Add([]byte{opWrite, 7, opRemove, 7, opList})
	f.Add([]byte{opWrite, 3, opWrite, 3, opRemove, 9, opList})
	f.Add([]byte{opRemove, 5, opList, opWrite, 200})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, program []byte) {
		dir := filepath.Join(t.TempDir(), "run")
		model := map[int]Entry{}

		r := &opReader{b: program}
		const maxOps = 128

		for ops := 0; ops < maxOps && r.more(); ops++ {
			switch r.next() % opCount {
			case opWrite:
				entry := r.entry()
				if _, err := Write(dir, entry); err != nil {
					t.Fatalf("Write: %v", err)
				}
				model[entry.PID] = entry
			case opRemove:
				pid := r.pid()
				if err := Remove(dir, pid); err != nil {
					t.Fatalf("Remove: %v", err)
				}
				delete(model, pid)
			case opList:
				requireListMatchesModel(t, dir, model)
			}
		}
		requireListMatchesModel(t, dir, model)
	})
}

func requireListMatchesModel(t *testing.T, dir string, model map[int]Entry) {
	t.Helper()
	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var want []Entry
	for _, e := range model {
		want = append(want, e)
	}
	byPID := func(entries []Entry) {
		sort.Slice(entries, func(i, j int) bool { return entries[i].PID < entries[j].PID })
	}
	byPID(got)
	byPID(want)
	if !bytes.Equal(marshal(t, got), marshal(t, want)) {
		t.Fatalf("List diverges from model:\n got =%s\n want=%s", marshal(t, got), marshal(t, want))
	}
}

func exerciseRendezvousBranches(t testing.TB) {
	t.Helper()

	t.Setenv("XDG_STATE_HOME", "")
	home := filepath.Join(string(filepath.Separator), "home", "test")
	t.Setenv("HOME", home)
	if got, err := DefaultDir(); err != nil || got != filepath.Join(home, ".local", "state", "evener", "run") {
		t.Fatalf("DefaultDir home = %q, %v", got, err)
	}

	dir := t.TempDir()
	entry := Entry{PID: 42, Address: "local"}
	path, err := Write(dir, entry)
	if err != nil {
		t.Fatalf("public Write: %v", err)
	}
	if entries, err := List(dir); err != nil || len(entries) != 1 {
		t.Fatalf("public List: entries=%v err=%v", entries, err)
	}
	if err := Remove(dir, entry.PID); err != nil {
		t.Fatalf("public Remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("public Remove left %q: %v", path, err)
	}

	removeFault := &rendezvousFaultFS{Fs: afero.NewMemMapFs(), op: "remove"}
	if err := removeFS(removeFault, "/run", 1); err == nil || !strings.Contains(err.Error(), "remove rendezvous file") {
		t.Fatalf("remove error = %v", err)
	}

	listFault := &rendezvousFaultFS{Fs: afero.NewMemMapFs(), op: "open-dir"}
	if _, err := listFS(listFault, "/run"); err == nil || !strings.Contains(err.Error(), "read rendezvous dir") {
		t.Fatalf("list error = %v", err)
	}
	if entries, err := listFS(afero.NewMemMapFs(), "/missing"); err != nil || entries != nil {
		t.Fatalf("missing list: entries=%v err=%v", entries, err)
	}

	filterFS := afero.NewMemMapFs()
	_ = filterFS.MkdirAll("/run/sub.json", 0o700)
	_ = afero.WriteFile(filterFS, "/run/note", []byte("x"), 0o600)
	_ = afero.WriteFile(filterFS, "/run/name.json", []byte("{}"), 0o600)
	_ = afero.WriteFile(filterFS, "/run/7.json", []byte("{}"), 0o600)
	readFault := &rendezvousFaultFS{Fs: filterFS, op: "open-entry"}
	if entries, err := listFS(readFault, "/run"); err != nil || len(entries) != 0 {
		t.Fatalf("filtered list: entries=%v err=%v", entries, err)
	}
}

type rendezvousFaultFS struct {
	afero.Fs
	op string
}

func (fs *rendezvousFaultFS) Remove(name string) error {
	if fs.op == "remove" {
		return os.ErrPermission
	}
	return fs.Fs.Remove(name)
}

func (fs *rendezvousFaultFS) Open(name string) (afero.File, error) {
	if fs.op == "open-dir" || fs.op == "open-entry" && strings.HasSuffix(name, "/7.json") {
		return nil, os.ErrPermission
	}
	return fs.Fs.Open(name)
}

// opReader is a cursor over the fuzz program that draws operations and their
// parameters one byte at a time.
type opReader struct {
	b []byte
	i int
}

const (
	opWrite = iota
	opRemove
	opList
	opCount
)

func (r *opReader) more() bool { return r.i < len(r.b) }

func (r *opReader) next() byte {
	if r.i >= len(r.b) {
		return 0
	}
	v := r.b[r.i]
	r.i++
	return v
}

// pid draws a small process id. The bounded range forces collisions across
// Write/Remove ops so the fuzzer exercises overwrite and delete-existing paths.
func (r *opReader) pid() int { return int(r.next()) }

// entry builds an Entry with a deterministic StartedAt so both lanes marshal the
// identical bytes. A few string fields are drawn from the program to vary the
// serialized payload (and thus the persisted bytes) across ops.
func (r *opReader) entry() Entry {
	pid := r.pid()
	return Entry{
		PID:       pid,
		Address:   fmt.Sprintf("127.0.0.1:%d", r.next()),
		Provider:  drawString(r.next()),
		Model:     drawString(r.next()),
		HubToken:  drawString(r.next()),
		StartedAt: time.Unix(int64(pid), 0).UTC(),
	}
}

// drawString maps a byte to one of a small fixed set of tokens (including the
// empty string, which exercises omitempty on the JSON side).
func drawString(b byte) string {
	tokens := []string{"", "openai", "anthropic", "gpt-5.5", "sess-1", "tok"}
	return tokens[int(b)%len(tokens)]
}

// marshal serializes a slice of entries for cross-lane comparison.
func marshal(t *testing.T, entries []Entry) []byte {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal entries: %v", err)
	}
	return data
}
