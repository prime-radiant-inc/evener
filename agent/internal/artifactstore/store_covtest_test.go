package artifactstore

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNewStore_ChmodError covers the chmod-error path in New (lines 35-37).
// On most systems chmod will succeed, but the error path exists. We test
// the happy path and the MkdirTemp error path.
func TestNewStore_Success(t *testing.T) {
	t.Parallel()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if s.dir == "" {
		t.Fatal("expected non-empty dir")
	}
}

// TestNewStore_MkdirTempError covers the MkdirTemp error path (lines 31-33).
func TestNewStore_MkdirTempError(t *testing.T) {
	t.Parallel()
	// Pass a path that is a file, not a directory — MkdirTemp will fail.
	nonDir := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(nonDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(nonDir)
	if err == nil {
		t.Fatal("expected error for non-directory base")
	}
}

// TestPut_ClosedStore covers the closed-store path (lines 46-47).
func TestPut_ClosedStore(t *testing.T) {
	t.Parallel()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = s.Put([]byte("data"))
	if err != ErrExpired {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
}

// TestOpen_InvalidRef covers the invalid-ref path (lines 70-71).
func TestOpen_InvalidRef(t *testing.T) {
	t.Parallel()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.Open("not-a-valid-ref")
	if err != ErrInvalidRef {
		t.Fatalf("expected ErrInvalidRef, got %v", err)
	}
}

// TestOpen_Expired covers the expired path (lines 77-79).
func TestOpen_Expired(t *testing.T) {
	t.Parallel()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// Valid format but never registered.
	_, err = s.Open("artifact:0123456789abcdef0123456789abcdef")
	if err != ErrExpired {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
}

// TestOpen_AfterClose covers the closed-store path (lines 77-79).
func TestOpen_AfterClose(t *testing.T) {
	t.Parallel()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ref, err := s.Put([]byte("data"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = s.Open(ref)
	if err != ErrExpired {
		t.Fatalf("expected ErrExpired after close, got %v", err)
	}
}

// TestPutOpen_RoundTrip covers the happy path of Put then Open.
func TestPutOpen_RoundTrip(t *testing.T) {
	t.Parallel()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	data := []byte("hello artifact store")
	ref, err := s.Put(data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	f, err := s.Open(ref)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	got, err := readAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("data = %q, want %q", got, data)
	}
}

// TestOpen_ValidRef_NotRegistered covers a valid-format ref that was never Put.
func TestOpen_ValidRef_NotRegistered(t *testing.T) {
	t.Parallel()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.Open("artifact:0123456789abcdef0123456789abcdef")
	if err != ErrExpired {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
}

func readAll(f *os.File) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	buf := make([]byte, info.Size())
	_, err = f.Read(buf)
	return buf, err
}
