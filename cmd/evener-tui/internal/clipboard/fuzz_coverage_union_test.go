//go:build serffuzz

package clipboard

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

var fuzzCoverageMu sync.Mutex
var fuzzDependencyDefaults sync.Once

func init() {
	fuzzCoverageUnion = testFuzzCoverageUnion
}

func configureFuzzDeps(t *testing.T) {
	t.Helper()
	clipboardGOOS = "linux"
	clipboardLookPath = func(name string) (string, error) { return name, nil }
	clipboardCreateTemp = os.CreateTemp
	clipboardReadFile = os.ReadFile
	clipboardRemove = os.Remove
	clipboardStat = os.Stat
	clipboardWrite = func(f *os.File, b []byte) (int, error) { return f.Write(b) }
	clipboardClose = func(f *os.File) error { return f.Close() }
	clipboardOutput = func(string, ...string) ([]byte, error) { return nil, nil }
	clipboardPowerShellOutput = clipboardOutput
}

func testFuzzCoverageUnion(t *testing.T) {
	fuzzCoverageMu.Lock()
	defer fuzzCoverageMu.Unlock()
	fuzzDependencyDefaults.Do(func() { testFuzzDependencyDefaults(t) })
	configureFuzzDeps(t)
	TestIsImageFile(t)
	TestIsProbablyWSL(t)
	TestNormalizePastedPath(t)
	TestConvertWindowsPathToWSL(t)
	TestMediaTypeForPath(t)
	TestIsWindowsPath(t)
	TestIsProbablyWSLFromSource(t)
	TestFileURIToPath(t)
	TestParseURIList(t)
	TestTryWSLClipboardFallback_PriorErrorPropagates(t)
	TestTryWSLClipboardFallback_NilSource(t)
	TestWriteTempPNG_WritesBytes(t)
	TestNewSystemClipboardSource(t)
	TestIsWaylandSession(t)
	testPasteCoverage(t)
	testSystemCoverage(t)
	testTempFailureCoverage(t)
}

func testFuzzDependencyDefaults(t *testing.T) {
	if _, err := clipboardLookPath("disabled"); err == nil {
		t.Fatal("fuzz clipboard lookup unexpectedly enabled")
	}
	f, err := os.CreateTemp(t.TempDir(), "clipboard-defaults-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clipboardWrite(f, nil); err != nil {
		t.Fatal(err)
	}
	if err := clipboardClose(f); err != nil {
		t.Fatal(err)
	}
	if _, err := clipboardOutput("disabled"); err == nil {
		t.Fatal("fuzz clipboard command unexpectedly enabled")
	}
}

func testPasteCoverage(t *testing.T) {
	TestPasteClipboardImage_PrefersFileList(t)
	TestPasteClipboardImage_SkipsNonImageFiles(t)
	TestPasteClipboardImage_FallsBackToImageBytes(t)
	TestPasteClipboardImage_DefaultsMediaTypeWhenEmpty(t)
	TestPasteClipboardImage_NoImageReturnsError(t)
	TestPasteClipboardImage_NilSource(t)
	TestPasteClipboardImage_WSLErrorWhenConvertedPathMissing(t)
	TestTryWSLClipboardFallback_NoWindowsImage(t)
	TestTryWSLClipboardFallback_RejectsUNC(t)

	missing := filepath.Join(t.TempDir(), "missing.png")
	_, _ = PasteClipboardImage(&fakeClipboard{files: []string{missing}, imageErr: ErrNoClipboardImage})
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clipboardStat = func(string) (os.FileInfo, error) { return info, nil }
	_, _ = PasteClipboardImage(&fakeClipboard{procVersion: "WSL", imageErr: ErrNoClipboardImage, winPath: `C:\clip.png`})
	clipboardStat = func(string) (os.FileInfo, error) { return nil, errors.New("stat") }
	_, _ = PasteClipboardImage(&fakeClipboard{procVersion: "WSL", imageErr: errors.New("image"), winPath: `C:\missing.png`})
	configureFuzzDeps(t)
	clipboardCreateTemp = func(string, string) (*os.File, error) { return nil, errors.New("temp") }
	_, _ = PasteClipboardImage(&fakeClipboard{imageBytes: []byte("png")})
	configureFuzzDeps(t)
	_, _ = PasteClipboardImage(&fakeClipboard{})
	_ = NormalizePastedPath("nope\npath")
	_ = FileURIToPath("file:///%zz")
}

func testSystemCoverage(t *testing.T) {
	s := NewSystemClipboardSource()
	for _, goos := range []string{"darwin", "linux", "other"} {
		clipboardGOOS = goos
		if goos == "linux" {
			t.Setenv("WAYLAND_DISPLAY", "")
		}
		_, _ = s.ReadFilePaths()
		_, _, _ = s.ReadImageBytes()
	}
	clipboardGOOS = "linux"
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	_, _ = s.ReadFilePaths()
	_, _, _ = s.ReadImageBytes()

	clipboardReadFile = func(string) ([]byte, error) { return nil, errors.New("read") }
	_ = s.ProcVersion()
	clipboardReadFile = func(string) ([]byte, error) { return []byte("WSL"), nil }
	_ = s.ProcVersion()

	clipboardLookPath = func(string) (string, error) { return "", errors.New("missing") }
	_, _ = readFilePathsMacOS()
	_, _, _ = readImageBytesMacOS()
	_, _ = readFilePathsX11()
	_, _, _ = readImageBytesX11()
	_, _ = readFilePathsWayland()
	_, _, _ = readImageBytesWayland()
	_, _ = s.ReadWindowsClipboardViaPowerShell()

	clipboardLookPath = func(name string) (string, error) { return name, nil }
	outputs := [][]byte{nil, []byte("text/plain"), []byte("text/uri-list"), []byte("file:///tmp/a.png"), []byte("image/png"), []byte("png")}
	for _, out := range outputs {
		clipboardOutput = func(string, ...string) ([]byte, error) { return out, nil }
		_, _ = readFilePathsMacOS()
		_, _ = readFilePathsX11()
		_, _, _ = readImageBytesX11()
		_, _ = readFilePathsWayland()
		_, _, _ = readImageBytesWayland()
	}
	clipboardOutput = func(string, ...string) ([]byte, error) { return nil, errors.New("command") }
	_, _ = readFilePathsMacOS()
	_, _ = readFilePathsX11()
	_, _, _ = readImageBytesX11()
	_, _ = readFilePathsWayland()
	_, _, _ = readImageBytesWayland()

	sequence := func(values ...struct {
		out []byte
		err error
	}) func(string, ...string) ([]byte, error) {
		i := 0
		return func(string, ...string) ([]byte, error) {
			v := values[i]
			i++
			return v.out, v.err
		}
	}
	clipboardOutput = sequence(
		struct {
			out []byte
			err error
		}{[]byte("text/uri-list"), nil},
		struct {
			out []byte
			err error
		}{nil, errors.New("convert")},
	)
	_, _ = readFilePathsX11()
	clipboardOutput = sequence(
		struct {
			out []byte
			err error
		}{[]byte("image/png"), nil},
		struct {
			out []byte
			err error
		}{nil, errors.New("convert")},
	)
	_, _, _ = readImageBytesX11()
	clipboardOutput = sequence(
		struct {
			out []byte
			err error
		}{[]byte("image/png"), nil},
		struct {
			out []byte
			err error
		}{nil, nil},
	)
	_, _, _ = readImageBytesX11()
	clipboardOutput = sequence(
		struct {
			out []byte
			err error
		}{[]byte("text/uri-list"), nil},
		struct {
			out []byte
			err error
		}{nil, errors.New("convert")},
	)
	_, _ = readFilePathsWayland()
	clipboardOutput = sequence(
		struct {
			out []byte
			err error
		}{[]byte("image/png"), nil},
		struct {
			out []byte
			err error
		}{nil, errors.New("convert")},
	)
	_, _, _ = readImageBytesWayland()
	clipboardOutput = sequence(
		struct {
			out []byte
			err error
		}{[]byte("image/png"), nil},
		struct {
			out []byte
			err error
		}{nil, nil},
	)
	_, _, _ = readImageBytesWayland()

	clipboardCreateTemp = func(string, string) (*os.File, error) { return nil, errors.New("temp") }
	_, _, _ = readImageBytesMacOS()
	clipboardCreateTemp = os.CreateTemp
	clipboardOutput = func(string, ...string) ([]byte, error) { return []byte("noimage"), nil }
	_, _, _ = readImageBytesMacOS()
	clipboardOutput = func(string, ...string) ([]byte, error) { return nil, errors.New("command") }
	_, _, _ = readImageBytesMacOS()
	clipboardOutput = func(string, ...string) ([]byte, error) { return []byte("ok"), nil }
	clipboardReadFile = func(string) ([]byte, error) { return nil, errors.New("read") }
	_, _, _ = readImageBytesMacOS()
	clipboardReadFile = func(string) ([]byte, error) { return nil, nil }
	_, _, _ = readImageBytesMacOS()
	clipboardReadFile = func(string) ([]byte, error) { return []byte("png"), nil }
	_, _, _ = readImageBytesMacOS()

	clipboardPowerShellOutput = func(string, ...string) ([]byte, error) { return nil, &exec.ExitError{ProcessState: exitState(t, 1)} }
	_, _ = s.ReadWindowsClipboardViaPowerShell()
	clipboardPowerShellOutput = func(string, ...string) ([]byte, error) { return nil, errors.New("run") }
	_, _ = s.ReadWindowsClipboardViaPowerShell()
	clipboardPowerShellOutput = func(string, ...string) ([]byte, error) { return nil, nil }
	_, _ = s.ReadWindowsClipboardViaPowerShell()
	clipboardPowerShellOutput = func(string, ...string) ([]byte, error) { return []byte(" C:\\clip.png \n"), nil }
	_, _ = s.ReadWindowsClipboardViaPowerShell()
}

func exitState(t *testing.T, code int) *os.ProcessState {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit "+string(rune('0'+code)))
	_ = cmd.Run()
	return cmd.ProcessState
}

func testTempFailureCoverage(t *testing.T) {
	configureFuzzDeps(t)
	clipboardCreateTemp = func(string, string) (*os.File, error) { return nil, errors.New("temp") }
	_, _ = WriteTempPNG(nil)
	clipboardCreateTemp = os.CreateTemp
	clipboardWrite = func(*os.File, []byte) (int, error) { return 0, errors.New("write") }
	_, _ = WriteTempPNG(nil)
	clipboardWrite = func(f *os.File, b []byte) (int, error) { return f.Write(b) }
	clipboardClose = func(*os.File) error { return errors.New("close") }
	_, _ = WriteTempPNG(nil)
}
