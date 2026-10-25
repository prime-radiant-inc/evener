package launchconfig

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/spf13/afero"
)

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
	_, diags := decodeTrustedRepoLayer("/repo", []byte("model = \"x\""), func([]byte, any) (toml.MetaData, error) {
		return toml.MetaData{}, sentinel
	})
	if len(diags) != 1 || diags[0].Layer != LayerRepo || diags[0].Field != ".evener/launch.toml" || diags[0].Message != sentinel.Error() {
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
	meta := Meta{Trust: MetaTrust{Hashes: []string{"hash"}, Decision: "pending"}}
	if got := ComputeTrustState("hash", meta); got != TrustUntrusted {
		t.Fatalf("ComputeTrustState = %q, want %q", got, TrustUntrusted)
	}
}
