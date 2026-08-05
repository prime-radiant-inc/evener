package hostlock

import (
	"encoding/hex"
	"path/filepath"
	"testing"
)

func FuzzAcquireLock(f *testing.F) {
	root := f.TempDir()
	for _, seed := range [][]byte{nil, []byte("hub"), {0, 1, 2, 255}} {
		f.Add(seed)
	}
	f.Add(make([]byte, 64))
	f.Fuzz(func(t *testing.T, seed []byte) {
		if len(seed) > 64 {
			t.Skip()
		}
		path := filepath.Join(root, hex.EncodeToString(seed)+".lock")
		release, err := AcquireLock(path)
		if err != nil {
			t.Fatalf("AcquireLock: %v", err)
		}
		if secondRelease, err := AcquireLock(path); err == nil {
			secondRelease()
			release()
			t.Fatal("second lock acquisition succeeded")
		}
		release()
		release, err = AcquireLock(path)
		if err != nil {
			t.Fatalf("AcquireLock after release: %v", err)
		}
		release()
	})
}
