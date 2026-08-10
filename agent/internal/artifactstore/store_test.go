package artifactstore

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
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
	if len(ref) != len("artifact:")+32 {
		t.Fatalf("ref length = %d, want %d: %q", len(ref), len("artifact:")+32, ref)
	}
	for _, c := range ref[len("artifact:"):] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("ref contains non-lowercase-hex ID: %q", ref)
		}
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

func TestStoreFileModeUnderRestrictiveUmask(t *testing.T) {
	if os.Getenv("ARTIFACTSTORE_UMASK_HELPER") == "1" {
		s, err := New(os.Getenv("ARTIFACTSTORE_UMASK_BASE"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		ref, err := s.Put([]byte("restrictive umask"))
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(s.refs[ref])
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("file mode under umask 0777 = %o, want 600", got)
		}
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no process umask")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("restrictive-umask shell helper unavailable: %v", err)
	}

	cmd := exec.Command("sh", "-c", "umask 0777; exec \"$1\" -test.run '^TestStoreFileModeUnderRestrictiveUmask$' -test.v", "artifactstore-umask", os.Args[0])
	cmd.Env = append(os.Environ(), "ARTIFACTSTORE_UMASK_HELPER=1", "ARTIFACTSTORE_UMASK_BASE="+t.TempDir())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("restrictive-umask helper: %v\n%s", err, output)
	}
}

func TestStorePutAfterClose(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Put([]byte("must not be written")); !errors.Is(err, ErrExpired) {
		t.Fatalf("Put after Close: %v", err)
	}
	if _, err := os.Stat(s.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory after Put following Close: %v", err)
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
	for i := range workers {
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
	for range 32 {
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

func TestStoreOpenCloseRaceIsLinearizable(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("mapped bytes\x00\n")
	ref, err := s.Put(want)
	if err != nil {
		t.Fatal(err)
	}

	const openers = 32
	start := make(chan struct{})
	ready := make(chan struct{}, openers+1)
	type openResult struct {
		data []byte
		err  error
	}
	results := make(chan openResult, openers)
	var wg sync.WaitGroup
	wg.Add(openers)
	for range openers {
		go func() {
			defer wg.Done()
			ready <- struct{}{}
			<-start
			f, err := s.Open(ref)
			if err != nil {
				results <- openResult{err: err}
				return
			}
			data, readErr := io.ReadAll(f)
			closeErr := f.Close()
			if readErr != nil {
				results <- openResult{err: readErr}
				return
			}
			if closeErr != nil {
				results <- openResult{err: closeErr}
				return
			}
			results <- openResult{data: data}
		}()
	}
	closeResult := make(chan error, 1)
	go func() {
		ready <- struct{}{}
		<-start
		closeResult <- s.Close()
	}()
	for range openers + 1 {
		<-ready
	}
	close(start)
	wg.Wait()
	if err := <-closeResult; err != nil {
		t.Fatal(err)
	}
	close(results)
	for result := range results {
		if result.err != nil && !errors.Is(result.err, ErrExpired) {
			t.Errorf("Open during Close: %v", result.err)
			continue
		}
		if result.err == nil && !bytes.Equal(result.data, want) {
			t.Errorf("opened bytes = %q, want %q", result.data, want)
		}
	}
	if _, err := os.Stat(s.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory after Open/Close race: %v", err)
	}
}
