package main

import (
	"regexp"
	"testing"
	"testing/fstest"
)

func TestFrontendDistHash_Deterministic(t *testing.T) {
	// Same FS hashed twice should produce identical hashes.
	fs := fstest.MapFS{
		"index.html":      &fstest.MapFile{Data: []byte("<html>app</html>")},
		"webassets/app.js": &fstest.MapFile{Data: []byte("console.log('hi');")},
	}
	hash1, err1 := frontendDistHash(fs)
	hash2, err2 := frontendDistHash(fs)
	if err1 != nil {
		t.Fatalf("first hash: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("second hash: %v", err2)
	}
	if hash1 != hash2 {
		t.Fatalf("determinism: hash1=%q, hash2=%q, want equal", hash1, hash2)
	}
}

func TestFrontendDistHash_ContentChange(t *testing.T) {
	// Changing file content should change the hash.
	fs1 := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>v1</html>")},
	}
	fs2 := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>v2</html>")},
	}
	hash1, err1 := frontendDistHash(fs1)
	hash2, err2 := frontendDistHash(fs2)
	if err1 != nil || err2 != nil {
		t.Fatalf("hash errors: %v, %v", err1, err2)
	}
	if hash1 == hash2 {
		t.Fatalf("content change should change hash: both got %q", hash1)
	}
}

func TestFrontendDistHash_FilePathChange(t *testing.T) {
	// Renaming a file (different path, same content) should change the hash.
	// This proves the hash includes the file path, not just content.
	fs1 := fstest.MapFS{
		"old_name.js": &fstest.MapFile{Data: []byte("const x = 1;")},
	}
	fs2 := fstest.MapFS{
		"new_name.js": &fstest.MapFile{Data: []byte("const x = 1;")},
	}
	hash1, err1 := frontendDistHash(fs1)
	hash2, err2 := frontendDistHash(fs2)
	if err1 != nil || err2 != nil {
		t.Fatalf("hash errors: %v, %v", err1, err2)
	}
	if hash1 == hash2 {
		t.Fatalf("file rename should change hash: both got %q", hash1)
	}
}

func TestFrontendDistHash_Format(t *testing.T) {
	// Hash should be exactly 12 hexadecimal characters.
	fs := fstest.MapFS{
		"file.txt": &fstest.MapFile{Data: []byte("content")},
	}
	hash, err := frontendDistHash(fs)
	if err != nil {
		t.Fatalf("hash error: %v", err)
	}
	if len(hash) != 12 {
		t.Fatalf("hash length: got %d, want 12", len(hash))
	}
	// Verify all characters are hex digits.
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(hash) {
		t.Fatalf("hash format: got %q, want 12 hex digits", hash)
	}
}

func TestFrontendDistHash_EmptyFS(t *testing.T) {
	// Empty FS should produce a valid hash (not an error).
	fs := fstest.MapFS{}
	hash, err := frontendDistHash(fs)
	if err != nil {
		t.Fatalf("empty FS error: %v", err)
	}
	if hash == "" {
		t.Fatal("empty FS should still produce a hash")
	}
	if len(hash) != 12 {
		t.Fatalf("empty FS hash length: got %d, want 12", len(hash))
	}
}
