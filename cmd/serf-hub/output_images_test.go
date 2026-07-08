package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestShellOutputImageCandidatesConservative(t *testing.T) {
	out := `wrote ./out.png
saved "screens/shot one.webp"
ignore https://example.com/nope.png
ignore notes.txt
absolute /tmp/project/chart.jpg
`
	got := shellOutputImageCandidates(out)
	want := []string{"./out.png", "screens/shot one.webp", "/tmp/project/chart.jpg"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates=%#v, want %#v", got, want)
	}
}

func TestShellOutputImageCandidatesCapsResults(t *testing.T) {
	var out string
	for i := 0; i < 40; i++ {
		out += "img" + strconv.Itoa(i) + ".png\n"
	}
	got := shellOutputImageCandidates(out)
	if len(got) != 20 {
		t.Fatalf("candidate count=%d, want cap 20", len(got))
	}
}

func TestResolveOutputImageFileBuildsDescriptor(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "out.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := resolveOutputImageFile("01DOC", cwd, "out.png", "shell-path")
	if !ok {
		t.Fatalf("resolveOutputImageFile rejected valid PNG")
	}
	h := sha256.Sum256(png)
	wantSHA := hex.EncodeToString(h[:])
	if got.Source != "shell-path" || got.Name != "out.png" || got.MediaType != "image/png" ||
		got.Size != int64(len(png)) || got.URL != "/doc/image?session=01DOC&path=out.png" ||
		got.SHA != wantSHA || got.Path != "out.png" {
		t.Fatalf("descriptor=%+v, want source/name/media/size/url/sha/path for out.png", got)
	}
}

func TestResolveOutputImageFileRejectsInvalidCandidates(t *testing.T) {
	cwd := t.TempDir()
	outside := filepath.Join(filepath.Dir(cwd), "outside.png")
	if err := os.WriteFile(outside, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := resolveOutputImageFile("01DOC", cwd, "../"+filepath.Base(outside), "shell-path"); ok {
		t.Fatalf("resolveOutputImageFile accepted traversal candidate")
	}
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := resolveOutputImageFile("01DOC", cwd, "notes.txt", "shell-path"); ok {
		t.Fatalf("resolveOutputImageFile accepted non-image candidate")
	}
}
