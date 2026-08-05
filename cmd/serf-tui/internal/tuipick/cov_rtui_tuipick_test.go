package tuipick

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompleteLastPathSegment(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	mustWrite("apple.txt")
	mustWrite(".hidden")
	if err := os.Mkdir(filepath.Join(dir, "apricot"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	sep := string(filepath.Separator)

	t.Run("prefix completes to first match", func(t *testing.T) {
		got := CompleteLastPathSegment(filepath.Join(dir, "ap"), nil)
		if got != filepath.Join(dir, "apple.txt") {
			t.Fatalf("got %q, want apple.txt completion", got)
		}
	})

	t.Run("directory gets trailing separator", func(t *testing.T) {
		got := CompleteLastPathSegment(filepath.Join(dir, "apr"), nil)
		if got != filepath.Join(dir, "apricot")+sep {
			t.Fatalf("got %q, want apricot with trailing separator", got)
		}
	})

	t.Run("DirEntry predicate excludes files", func(t *testing.T) {
		got := CompleteLastPathSegment(filepath.Join(dir, "ap"), DirEntry())
		if got != filepath.Join(dir, "apricot")+sep {
			t.Fatalf("got %q, want apricot (files excluded)", got)
		}
	})

	t.Run("no prefix skips hidden entries", func(t *testing.T) {
		got := CompleteLastPathSegment(dir+sep, nil)
		if got != filepath.Join(dir, "apple.txt") {
			t.Fatalf("got %q, want first non-hidden entry apple.txt", got)
		}
	})

	t.Run("unreadable directory returns input unchanged", func(t *testing.T) {
		in := filepath.Join(dir, "does-not-exist", "foo")
		if got := CompleteLastPathSegment(in, nil); got != in {
			t.Fatalf("got %q, want input unchanged", got)
		}
	})

	t.Run("comma-separated last segment preserves prefix", func(t *testing.T) {
		in := "first," + filepath.Join(dir, "ap")
		got := CompleteLastPathSegment(in, nil)
		want := "first," + filepath.Join(dir, "apple.txt")
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestDirEntryPredicate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	accept := DirEntry()
	for _, e := range entries {
		if got, want := accept(e), e.IsDir(); got != want {
			t.Errorf("DirEntry()(%q) = %v, want %v", e.Name(), got, want)
		}
	}
}

func TestPickerPanel_ConstructorAndGetters(t *testing.T) {
	// Non-positive width falls back to the default.
	if p := NewPickerPanel("t", nil, 0); p.Width() != 80 {
		t.Fatalf("default width = %d, want 80", p.Width())
	}

	items := []PickerPanelItem{
		{ID: "one", Label: "First", Detail: "alpha"},
		{ID: "two", Label: "Second", Detail: "beta"},
	}
	p := NewPickerPanel("Pick", items, 40)
	if p.Title() != "Pick" || p.Width() != 40 {
		t.Fatalf("title/width = %q/%d, want Pick/40", p.Title(), p.Width())
	}
	if p.Done() || p.Cancelled() || p.Selected() != "" || p.Cursor() != 0 || p.Filter() != "" {
		t.Fatalf("fresh panel state unexpected: %+v", p)
	}
	if len(p.Filtered()) != 2 {
		t.Fatalf("no filter should return all items, got %d", len(p.Filtered()))
	}
}

func TestPickerPanel_SetFilterMatchesAllFields(t *testing.T) {
	items := []PickerPanelItem{
		{ID: "alpha-id", Label: "Alpha", Detail: "first"},
		{ID: "beta-id", Label: "Beta", Detail: "second"},
		{ID: "gamma-id", Label: "Gamma", Detail: "alphabetagamma"},
	}
	p := NewPickerPanel("t", items, 40)

	p.SetFilter("beta") // matches Beta label and gamma detail
	if p.Filter() != "beta" {
		t.Fatalf("Filter() = %q, want beta", p.Filter())
	}
	got := p.Filtered()
	if len(got) != 2 {
		t.Fatalf("filter beta matched %d items, want 2 (%+v)", len(got), got)
	}

	p.SetFilter("alpha-id") // matches only by ID
	if len(p.Filtered()) != 1 {
		t.Fatalf("filter by id matched %d, want 1", len(p.Filtered()))
	}
}

func TestModelPicker_ConstructorsAndGetters(t *testing.T) {
	items := []ModelPickerItem{{ID: "m1", Display: "Model 1"}}

	tp := NewTranscriptPicker(items, "m1", 40)
	if tp.Done() || tp.Selected() != "" {
		t.Fatalf("fresh transcript picker: done=%v selected=%q", tp.Done(), tp.Selected())
	}

	ap := NewActionPicker("Actions", "footer", items, 40)
	ap.SetTitle("Renamed")
	if ap.View() == "" {
		t.Fatal("action picker View() empty")
	}
}
