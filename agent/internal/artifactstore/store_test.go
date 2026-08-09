package artifactstore

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestStorePutOpenRoundTrip(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	want := []byte("first\x00second\n")
	ref, err := s.Put(want)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "artifact:") {
		t.Fatalf("ref=%q", ref)
	}

	f, err := s.Open(ref)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestStoreRejectsPathsAndUnknownRefs(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Open("artifact:../../etc/passwd"); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("err=%v", err)
	}
	if _, err := s.Open("artifact:00112233445566778899aabbccddeeff"); !errors.Is(err, ErrExpired) {
		t.Fatalf("err=%v", err)
	}
}

func TestStorePermissionsAndClose(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	dirInfo, err := os.Stat(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %o, want 700", got)
	}

	ref, err := s.Put([]byte("permissions"))
	if err != nil {
		t.Fatal(err)
	}
	path := s.refs[ref]
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o, want 600", got)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory after close: %v", err)
	}
	if _, err := s.Open(ref); !errors.Is(err, ErrExpired) {
		t.Fatalf("open after close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreConcurrentPutOpen(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	const workers = 32
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			want := []byte{byte(i), 0, '\n', byte(255 - i)}
			ref, err := s.Put(want)
			if err != nil {
				t.Errorf("Put: %v", err)
				return
			}
			f, err := s.Open(ref)
			if err != nil {
				t.Errorf("Open: %v", err)
				return
			}
			got, readErr := io.ReadAll(f)
			closeErr := f.Close()
			if readErr != nil {
				t.Errorf("ReadAll: %v", readErr)
			}
			if closeErr != nil {
				t.Errorf("file Close: %v", closeErr)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("got %v want %v", got, want)
			}
		}(i)
	}
	wg.Wait()

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStorePutCloseRaceDoesNotLeak(t *testing.T) {
	for i := 0; i < 32; i++ {
		s, err := New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		var putRef string
		var putErr error
		go func() {
			defer wg.Done()
			putRef, putErr = s.Put([]byte("race"))
		}()
		go func() {
			defer wg.Done()
			if err := s.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()
		wg.Wait()

		if putErr == nil {
			if _, err := s.Open(putRef); !errors.Is(err, ErrExpired) {
				t.Errorf("open raced ref after close: %v", err)
			}
		} else if !errors.Is(putErr, ErrExpired) {
			t.Errorf("Put: %v", putErr)
		}
		if _, err := os.Stat(s.dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("directory after race: %v", err)
		}
	}
}
