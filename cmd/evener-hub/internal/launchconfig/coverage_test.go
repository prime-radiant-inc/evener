package launchconfig

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

type failureFile struct {
	afero.File
	writeErr error
	syncErr  error
	closeErr error
}

func (f *failureFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.File.Write(p)
}

func (f *failureFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.File.Sync()
}

func (f *failureFile) Close() error {
	if f.closeErr != nil {
		return f.closeErr
	}
	return f.File.Close()
}

type atomicFailureFS struct {
	afero.Fs
	writeErr error
	syncErr  error
	closeErr error
}

func (fs *atomicFailureFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &failureFile{File: f, writeErr: fs.writeErr, syncErr: fs.syncErr, closeErr: fs.closeErr}, nil
}

func checkWriteAtomicFSFailuresRemoveTemporaryFile(t *testing.T) {
	tests := []struct {
		name      string
		encodeErr error
		writeErr  error
		syncErr   error
		closeErr  error
		want      string
	}{
		{name: "encode", encodeErr: errors.New("encode failed"), want: "encode"},
		{name: "write", writeErr: errors.New("write failed"), want: "write"},
		{name: "sync", syncErr: errors.New("sync failed"), want: "sync"},
		{name: "close", closeErr: errors.New("close failed"), want: "close"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := tc.encodeErr
			if sentinel == nil {
				sentinel = tc.writeErr
			}
			if sentinel == nil {
				sentinel = tc.syncErr
			}
			if sentinel == nil {
				sentinel = tc.closeErr
			}
			base := afero.NewMemMapFs()
			fs := &atomicFailureFS{Fs: base, writeErr: tc.writeErr, syncErr: tc.syncErr, closeErr: tc.closeErr}
			path := filepath.Join("config", "launch.toml")
			if err := afero.WriteFile(base, path, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := writeAtomicFS(fs, path, func(w io.Writer) error {
				if tc.encodeErr != nil {
					return tc.encodeErr
				}
				_, err := w.Write([]byte("model = \"x\"\n"))
				return err
			})
			if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), tc.want+" "+path+".tmp") {
				t.Fatalf("writeAtomicFS error = %v, want %q failure", err, tc.want)
			}
			got, readErr := afero.ReadFile(base, path)
			if readErr != nil || string(got) != "original" {
				t.Fatalf("destination = %q, err=%v; want unchanged", got, readErr)
			}
			if exists, err := afero.Exists(base, path+".tmp"); err != nil || exists {
				t.Fatalf("temporary file exists=%v, err=%v", exists, err)
			}
		})
	}
}

func checkValidateRepoRelativePathResolutionError(t *testing.T) {
	errSentinel := errors.New("rel failed")
	err := validateRepoRelativePath("/repo", "child", func(string, string) (string, error) {
		return "", errSentinel
	})
	if !errors.Is(err, errSentinel) || !strings.Contains(err.Error(), "path resolution") {
		t.Fatalf("validateRepoRelativePath error = %v", err)
	}
}

func checkDecodeTrustedRepoLayerError(t *testing.T) {
	sentinel := errors.New("decode failed")
	_, diags := decodeTrustedRepoLayer("/repo", []byte("model = \"x\""), func([]byte, any) error {
		return sentinel
	})
	if len(diags) != 1 || diags[0].Layer != LayerRepo || diags[0].Field != ".serf/launch.toml" || diags[0].Message != sentinel.Error() {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func checkCanonicalHashEncodeError(t *testing.T) {
	sentinel := errors.New("encode failed")
	_, err := canonicalHashTOML([]byte("model = \"x\""), func(io.Writer, Layer) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "canonical hash: encode") {
		t.Fatalf("canonicalHashTOML error = %v", err)
	}
}

func checkMergeAdditionalLaunchFields(t *testing.T) {
	verbose := true
	resolved, _ := mergeLayers(map[LayerName]Layer{
		LayerLaunch: {
			Verbose:                   &verbose,
			ExportATIFPath:            "/tmp/session.atif",
			ExportATIFProviderHandles: "resolved",
		},
	})
	if resolved.Effective.Verbose == nil || !*resolved.Effective.Verbose {
		t.Fatalf("verbose = %v", resolved.Effective.Verbose)
	}
	if resolved.Effective.ExportATIFPath != "/tmp/session.atif" {
		t.Fatalf("export ATIF path = %q", resolved.Effective.ExportATIFPath)
	}
	if resolved.Effective.ExportATIFProviderHandles != "resolved" {
		t.Fatalf("export ATIF provider handles = %q", resolved.Effective.ExportATIFProviderHandles)
	}
}

func checkResolveProjectLoadError(t *testing.T) {
	fs := afero.NewMemMapFs()
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	paths, err := PathsFor(stateRoot, cwd)
	if err != nil {
		t.Fatal(err)
	}
	path := paths.ProjectFile
	if err := afero.WriteFile(fs, path, []byte("invalid {{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = resolveFS(fs, stateRoot, cwd, Layer{})
	if err == nil || !strings.Contains(err.Error(), "project: launchconfig: parse") {
		t.Fatalf("Resolve error = %v", err)
	}
}

func checkLoadProjectLayerLegacyStatError(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	paths, err := PathsFor(stateRoot, cwd)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("legacy stat failed")
	fs := &statFailureFS{Fs: afero.NewMemMapFs(), path: paths.LegacyProject, err: sentinel}
	_, _, err = loadProjectLayerFS(fs, paths)
	if !errors.Is(err, sentinel) {
		t.Fatalf("loadProjectLayerFS error = %v, want legacy stat failure", err)
	}
}

type statFailureFS struct {
	afero.Fs
	path string
	err  error
}

func (fs *statFailureFS) Stat(name string) (os.FileInfo, error) {
	if name == fs.path {
		return nil, fs.err
	}
	return fs.Fs.Stat(name)
}

func checkComputeTrustStateUnknownDecision(t *testing.T) {
	meta := Meta{Trust: MetaTrust{Hash: "hash", Decision: "pending"}}
	if got := ComputeTrustState("hash", meta); got != TrustUntrusted {
		t.Fatalf("ComputeTrustState = %q, want %q", got, TrustUntrusted)
	}
}
