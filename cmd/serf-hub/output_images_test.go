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

func TestShellOutputImageCandidatesRejectsEmbeddedURLs(t *testing.T) {
	out := `local before ./before.png
html src=https://example.com/from-attr.png
paren (https://example.com/from-paren.png)
markdown ![alt](https://example.com/from-markdown.png)
local after ./after.webp
`
	got := shellOutputImageCandidates(out)
	want := []string{"./before.png", "./after.webp"}
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

func TestSupportedOutputImageMediaAcceptsV1FormatsAndRejectsSVG(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "out.png", data: []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, want: "image/png"},
		{name: "out.jpg", data: []byte{0xff, 0xd8, 0xff, 0xdb}, want: "image/jpeg"},
		{name: "out.gif", data: []byte("GIF89a"), want: "image/gif"},
		{name: "out.webp", data: []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), want: "image/webp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := supportedOutputImageMedia(tt.data, tt.name)
			if !ok || got != tt.want {
				t.Fatalf("supportedOutputImageMedia(%s)=(%q,%v), want (%q,true)", tt.name, got, ok, tt.want)
			}
		})
	}

	if got, ok := supportedOutputImageMedia([]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), "out.svg"); ok {
		t.Fatalf("supportedOutputImageMedia(svg)=(%q,true), want rejected", got)
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
