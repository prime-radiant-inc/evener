package bundled

import (
	"errors"
	"io/fs"
	"testing"
)

func FuzzAssets(f *testing.F) {
	for _, seed := range []uint8{0, 1, 2} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, which uint8) {
		var filesystem fs.FS
		switch which % 3 {
		case 0:
			filesystem = Agents()
		case 1:
			filesystem = Skills()
		default:
			filesystem = Plugins()
		}
		entries, err := fs.ReadDir(filesystem, ".")
		if err != nil || len(entries) == 0 {
			t.Fatalf("embedded assets unavailable: entries=%d err=%v", len(entries), err)
		}
	})
}

func TestMustSubPanics(t *testing.T) {
	old := subFS
	subFS = func(fs.FS, string) (fs.FS, error) { return nil, errors.New("broken") }
	t.Cleanup(func() { subFS = old })
	defer func() {
		if recover() == nil {
			t.Fatal("mustSub did not panic")
		}
	}()
	_ = mustSub("missing")
}
