package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryIdentityPathBytes(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "physical")
	spaced := filepath.Join(root, "physical ")
	alias := filepath.Join(root, "alias")
	for _, dir := range []string{plain, spaced} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(spaced, alias); err != nil {
		t.Fatal(err)
	}
	a, err := os.Stat(plain)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(alias)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(a, b) {
		t.Fatal("different trailing-space directories have the same OS identity")
	}
	spaced, err = filepath.EvalSymlinks(spaced)
	if err != nil {
		t.Fatal(err)
	}
	host, _ := ownerCommands(t)
	fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, _ io.Reader) ([]byte, error) {
		return exec.CommandContext(ctx, "/bin/sh", "-c", strings.Join(argv[12:], " ")).Output()
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	t.Run("exact-physical-path", func(t *testing.T) {
		got, err := m.directoryIdentity(t.Context(), host, alias)
		if err != nil || got != spaced {
			t.Fatalf("identity=%q want=%q err=%v", got, spaced, err)
		}
	})
	for _, tc := range []struct {
		name, target string
		matches      bool
	}{
		{"alias-own-directory", spaced, true},
		{"alias-other-directory", plain, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matches, err := m.sameDirectory(t.Context(), host, alias, tc.target)
			if err != nil || matches != tc.matches {
				t.Fatalf("sameDirectory=%t want=%t err=%v", matches, tc.matches, err)
			}
		})
	}
}

func TestDirectoryIdentityOutputFraming(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
	}{
		{"plain", "/physical\n", "/physical"},
		{"space", "/physical \n", "/physical "},
		{"tab", "/physical\t\n", "/physical\t"},
		{"carriage-return-path-byte", "/physical\r\n", "/physical\r"},
		{"empty", "", ""},
		{"empty-line", "\n", ""},
		{"missing-frame", "/physical", ""},
		{"relative", "physical\n", ""},
		{"interior-newline", "/physical\n/other\n", ""},
		{"extra-newline", "/physical\n\n", ""},
		{"nul", "/physical\x00\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, _ := ownerCommands(t)
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if remote != "CDPATH='' cd -P -- /selected && pwd -P" {
					return nil, fmt.Errorf("unexpected command: %s", remote)
				}
				return []byte(tc.output), nil
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			got, err := m.directoryIdentity(t.Context(), host, "/selected")
			if tc.want == "" {
				if !errors.Is(err, errOwnerEvidence) {
					t.Fatalf("malformed output accepted: identity=%q err=%v", got, err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("identity=%q want=%q err=%v", got, tc.want, err)
			}
		})
	}
}
