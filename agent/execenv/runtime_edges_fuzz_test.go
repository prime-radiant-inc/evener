//go:build serffuzz && linux

package execenv

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"primeradiant.com/serf/agent/sandbox"
)

// FuzzRuntimeBoundaryEdges drives deterministic error paths at the process,
// environment, and descriptor boundaries. It constructs commands but never
// starts them; invalid descriptors and missing paths cannot affect host state.
func FuzzRuntimeBoundaryEdges(f *testing.F) {
	f.Add([]byte(nil))
	f.Add([]byte("PATH=/fuzz/bin"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64 {
			raw = raw[:64]
		}
		token := string(raw)
		originalGOOS, originalExec, originalOSOutput, originalShellStat := runtimeGOOS, execCommandContext, osVersionOutput, shellStat
		t.Cleanup(func() {
			runtimeGOOS, execCommandContext, osVersionOutput, shellStat = originalGOOS, originalExec, originalOSOutput, originalShellStat
		})
		for _, goos := range []string{"linux", "darwin", "windows", "plan9"} {
			runtimeGOOS = goos
			if NewLocalExecutionEnvironment(t.TempDir()).Platform() == "" {
				t.Fatal("empty platform")
			}
			_ = shellCommand("exit 0").Args
		}
		shellStat = func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist }
		runtimeGOOS = "linux"
		_ = shellCommand("exit 0").Args
		shellStat = originalShellStat
		execCommandContext = func(context.Context, string, ...string) *exec.Cmd {
			return exec.Command("/definitely/missing/serf-fuzz-os-probe")
		}
		for _, goos := range []string{"linux", "darwin", "windows", "plan9"} {
			runtimeGOOS = goos
			if resolveOSVersion() == "" {
				t.Fatal("empty OS version fallback")
			}
		}
		osVersionOutput = func(context.Context, string, ...string) ([]byte, error) { return []byte(" scripted version \n"), nil }
		for _, goos := range []string{"linux", "darwin", "windows"} {
			runtimeGOOS = goos
			_ = resolveOSVersion()
		}
		runtimeGOOS = originalGOOS
		factory := systemCommandRuntimeFactory{}
		shell := factory.Shell("printf %s " + token)
		if len(shell.Args()) == 0 || shell.PID() != 0 {
			t.Fatal("unstarted shell runtime has invalid shape")
		}
		if _, ok := shell.ExitCode(errors.New("not an exit error")); ok {
			t.Fatal("plain error unexpectedly carried an exit code")
		}
		exitCodeOrig := processExitCode
		processExitCode = func(*exec.ExitError) int { return 9 }
		if code, ok := shell.ExitCode(&exec.ExitError{}); !ok || code != 9 {
			t.Fatalf("scripted exit code = %d, %v", code, ok)
		}
		processExitCode = exitCodeOrig
		argv := factory.Argv("missing-fuzz-command", token)
		argv.Configure(commandRuntimeConfig{Dir: t.TempDir(), ExecutablePath: filepath.Join(t.TempDir(), "missing")})
		if argv.PID() != 0 {
			t.Fatal("unstarted argv runtime has a pid")
		}
		systemArgv := argv.(*systemCommandRuntime)
		systemArgv.cmd.Process = &os.Process{Pid: 1 << 30}
		_ = systemArgv.PID()

		extra := map[string]string{"FUZZ_VALUE": token}
		_ = filteredEnvWithPolicy(EnvPolicyDefault, extra)
		_ = filteredEnv(extra)
		cleanupEnv := NewLocalExecutionEnvironment(t.TempDir())
		cleanupEnv.runningPIDs.Store("not-a-pid", true)
		cleanupEnv.Cleanup()
		terminateProcessGroup(0)
		killProcessGroup(0)
		terminateProcessGroup(1 << 30)
		killProcessGroup(1 << 30)
		venvRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(venvRoot, ".venv", "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = injectLocalVenvPath([]string{"PATH=/base"}, []string{venvRoot, venvRoot})
		windowsRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(windowsRoot, ".venv", "Scripts"), 0o755); err != nil {
			t.Fatal(err)
		}
		runtimeGOOS = "windows"
		_ = injectLocalVenvPath([]string{"PATH=/base"}, []string{windowsRoot})
		runtimeGOOS = originalGOOS
		candidateOrig := venvCandidateDirs
		venvCandidateDirs = func(string, string) []string { return []string{filepath.Join(venvRoot, ".venv", "bin")} }
		_ = injectLocalVenvPath([]string{"PATH=/base"}, []string{"root-a", "root-b"})
		venvCandidateDirs = candidateOrig
		splitEditOrig := splitEditLines
		splitEditLines = func(string, string) []string { return nil }
		_ = nearestFileRegion("content", "old")
		splitEditLines = splitEditOrig
		splitPathOrig := splitPathComponents
		splitPathComponents = func(string, string) []string { return []string{"."} }
		_ = DirsFromRootToCwd("/root", "/root/child")
		splitPathComponents = splitPathOrig

		zeroGrace := time.Duration(0)
		timeoutCommand := &processRuntimeCommand{plan: processRuntimePlan{waitForTerminate: true}, pid: 70001, terminated: make(chan struct{})}
		timeoutEnv := NewLocalExecutionEnvironment(t.TempDir())
		timeoutEnv.commandFactory = oneRuntimeFactory{command: timeoutCommand}
		timeoutEnv.terminationGrace = &zeroGrace
		result, err := timeoutEnv.ExecArgv(context.Background(), "scripted", nil, 1, "", nil)
		if err == nil || !result.TimedOut || timeoutCommand.terminate == 0 {
			t.Fatalf("scripted timeout result=%+v err=%v terminate=%d", result, err, timeoutCommand.terminate)
		}
		streamCommand := &processRuntimeCommand{plan: processRuntimePlan{waitForTerminate: true, ignoreTerminate: true}, pid: 70002, terminated: make(chan struct{})}
		streamEnv := NewLocalExecutionEnvironment(t.TempDir())
		streamEnv.commandFactory = oneRuntimeFactory{command: streamCommand}
		streamEnv.terminationGrace = &zeroGrace
		streamCtx, cancelStream := context.WithCancel(context.Background())
		waitStream, err := streamEnv.StreamCommand(streamCtx, "scripted", "", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		cancelStream()
		if _, err := waitStream.Wait(); err != nil {
			t.Fatal(err)
		}
		if streamCommand.terminate == 0 || streamCommand.kill == 0 {
			t.Fatalf("stream signals terminate=%d kill=%d", streamCommand.terminate, streamCommand.kill)
		}
		for _, afterTimer := range []bool{false, true} {
			cmd := &processRuntimeCommand{plan: processRuntimePlan{waitForTerminate: true, ignoreTerminate: true}, pid: 71000, terminated: make(chan struct{})}
			env := NewLocalExecutionEnvironment(t.TempDir())
			env.commandFactory = oneRuntimeFactory{command: cmd}
			env.terminationGrace = &zeroGrace
			h, err := env.StreamCommand(context.Background(), "scripted", "", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if afterTimer {
				orig := streamAfterTimer
				called := make(chan struct{})
				streamAfterTimer = func(closeDone func()) { closeDone(); close(called) }
				h.Signal()
				<-called
				streamAfterTimer = orig
			} else {
				orig := streamBeforeSignalOnce
				streamBeforeSignalOnce = func(closeDone func()) { closeDone() }
				h.Signal()
				streamBeforeSignalOnce = orig
			}
			cmd.doneOnce.Do(func() { close(cmd.terminated) })
			_, _ = h.Wait()
		}

		policyForWrapper := sandbox.ResolvedPolicy{Mode: sandbox.ModeWorkspaceWrite, Backend: sandbox.BackendBwrap}
		wrapper, err := sandbox.NewWrapper(policyForWrapper, "/fixture/bwrap", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		wrapperOrig := wrapperWithPolicy
		wrapperWithPolicy = func(*sandbox.Wrapper, sandbox.ResolvedPolicy) (*sandbox.Wrapper, error) {
			return nil, errors.New("scripted wrapper failure")
		}
		_, _ = applyWrapperPolicy(wrapper, policyForWrapper)
		controlEnv := NewLocalExecutionEnvironment(t.TempDir())
		controlEnv.Sandbox, controlEnv.Wrapper = &policyForWrapper, wrapper
		_ = controlEnv.UseControlPolicy(controlEnv.RootDir)
		controlNoWrapper := NewLocalExecutionEnvironment(t.TempDir())
		resolvedRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(resolvedRoot, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		resolvedPolicy := sandboxLifecyclePolicy(t, sandbox.ModeWorkspaceWrite, sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapCapable: true, BwrapPath: "/fixture/bwrap", OverlaySupported: true}, resolvedRoot)
		controlNoWrapper.RootDir = resolvedRoot
		controlNoWrapper.Sandbox = resolvedPolicy
		_ = controlNoWrapper.UseControlPolicy(controlNoWrapper.RootDir)
		wrapperWithPolicy = wrapperOrig

		if err := atomicWriteAt(-1, "leaf", raw, 0o600); err == nil {
			t.Fatal("atomic write on invalid descriptor succeeded")
		}
		if err := writeAllFd(-1, rawOrOne(raw)); err == nil {
			t.Fatal("write on invalid descriptor succeeded")
		}
		policy := sandbox.ResolvedPolicy{FileTool: sandbox.AccessScope{Read: sandbox.ReadAnywhere}}
		sfs := newSandboxFS(&policy, "")
		defer sfs.close()
		if err := sfs.walkDirFd(-1, "", t.TempDir(), 2, new([]DirEntry)); err == nil {
			t.Fatal("directory walk on invalid descriptor succeeded")
		}
		_ = sfs.recheckMaskedFd("read_file", filepath.Join(t.TempDir(), "x"), -1)
		_ = sfs.recheckWriteTargetFd("write_file", filepath.Join(t.TempDir(), "x"), -1, "x")
		if _, err := sfs.openAnywhereMinusMasked("read_file", filepath.Join(t.TempDir(), "missing"), os.O_RDONLY); err == nil {
			t.Fatal("missing anywhere path unexpectedly opened")
		}

		root := t.TempDir()
		file := filepath.Join(root, "file")
		if err := os.WriteFile(file, rawOrOne(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		canonicalOrig, requiredOrig := canonicalPathForFd, canonicalRecheckRequired
		canonicalRecheckRequired = true
		canonicalPathForFd = func(int) (string, error) { return "", errors.New("scripted canonical failure") }
		if _, err := sfs.openAnywhereMinusMasked("read_file", file, os.O_RDONLY); err == nil {
			t.Fatal("canonical recheck failure did not deny anywhere read")
		}
		rootPolicy := sandbox.ResolvedPolicy{Mode: sandbox.ModeRestricted, FileTool: sandbox.AccessScope{Read: sandbox.ReadWorktreeOnly, ReadRoots: []string{root}, WriteRoots: []string{root}}}
		rootFS := newSandboxFS(&rootPolicy, "")
		defer rootFS.close()
		if err := os.Chmod(file, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := rootFS.openInRoot("read_file", file, root, "file", os.O_RDONLY); err == nil {
			t.Fatal("canonical recheck failure did not deny rooted read")
		}
		_ = rootFS.recheckWriteTargetFd("write_file", file, -1, "file")
		if _, _, err := rootFS.openWriteParent("write_file", filepath.Join(root, "file"), false); err == nil {
			t.Fatal("canonical recheck failure did not deny rooted write")
		}
		grantPolicy := sandbox.ResolvedPolicy{FileTool: sandbox.AccessScope{Read: sandbox.ReadAnywhere}}
		grantFS := newSandboxFS(&grantPolicy, "")
		grantFS.grant = file
		if _, _, err := grantFS.grantedWriteParent("write_file", file); err == nil {
			t.Fatal("canonical recheck failure did not deny granted write")
		}
		canonicalRecheckRequired = false
		masked := filepath.Join(root, "masked")
		rootPolicy.MaskedPaths = []string{masked}
		rootPolicy.Git.ProtectedPaths = []string{filepath.Join(root, "protected")}
		canonicalPathForFd = func(int) (string, error) { return masked, nil }
		_ = rootFS.recheckMaskedFd("read_file", file, -1)
		_ = rootFS.recheckWriteTargetFd("write_file", file, -1, "leaf")
		canonicalPathForFd = func(int) (string, error) { return root, nil }
		_ = rootFS.recheckWriteTargetFd("write_file", file, -1, "protected")
		canonicalPathForFd, canonicalRecheckRequired = canonicalOrig, requiredOrig
		pathRelOrig := securePathRel
		securePathRel = func(string, string) (string, error) { return "", errors.New("scripted rel failure") }
		_, _, _ = containingRoot([]string{root}, filepath.Join(root, "child"))
		securePathRel = pathRelOrig
		if _, err := rootFS.listDir("list_directory", root, 2); err != nil {
			t.Fatal(err)
		}
		entryInfoOrig := secureEntryInfo
		secureEntryInfo = func(os.DirEntry) (os.FileInfo, error) { return nil, fs.ErrPermission }
		if _, err := rootFS.listDir("list_directory", root, 1); err != nil {
			t.Fatal(err)
		}
		secureEntryInfo = entryInfoOrig
		fakeEntry := fs.FileInfoToDirEntry(runtimeEdgeFileInfo{name: "exec", mode: 0o755})
		readDirForInfo := secureReadDirEntries
		secureReadDirEntries = func(int) ([]os.DirEntry, error) { return []os.DirEntry{fakeEntry}, nil }
		var synthetic []DirEntry
		if err := rootFS.walkDirFd(-1, "", root, 1, &synthetic); err != nil || len(synthetic) != 1 || !synthetic[0].IsExec {
			t.Fatalf("synthetic executable entry=%+v err=%v", synthetic, err)
		}
		secureReadDirEntries = readDirForInfo
		missingRootPolicy := sandbox.ResolvedPolicy{FileTool: sandbox.AccessScope{WriteRoots: []string{filepath.Join(root, "missing-root")}}}
		if _, _, err := newSandboxFS(&missingRootPolicy, "").openWriteParent("write_file", filepath.Join(root, "missing-root", "file"), true); err == nil {
			t.Fatal("missing write root unexpectedly opened")
		}
		if _, _, err := rootFS.openWriteParent("write_file", filepath.Join(root, "missing-dir", "file"), false); err == nil {
			t.Fatal("missing write parent unexpectedly opened")
		}

		readDirOrig := secureReadDirEntries
		secureReadDirEntries = func(int) ([]os.DirEntry, error) { return nil, fs.ErrPermission }
		if _, err := rootFS.listDir("list_directory", root, 2); err == nil {
			t.Fatal("scripted readdir failure unexpectedly succeeded")
		}
		secureReadDirEntries = readDirOrig
		browseWalkOrig, browseReadOrig := secureBrowseWalkDir, secureBrowseReadFile
		secureBrowseWalkDir = func(fsys fs.FS, root string, fn fs.WalkDirFunc) error {
			_ = fn("denied", nil, fs.ErrPermission)
			return nil
		}
		if _, err := rootFS.grepNative("x", root, "", false, 10, "content"); err != nil {
			t.Fatal(err)
		}
		secureBrowseWalkDir = func(fs.FS, string, fs.WalkDirFunc) error { return fs.ErrPermission }
		if _, err := rootFS.grepNative("x", root, "", false, 10, "content"); err == nil {
			t.Fatal("browse walk fault succeeded")
		}
		secureBrowseWalkDir = browseWalkOrig
		secureBrowseReadFile = func(fs.FS, string) ([]byte, error) { return nil, fs.ErrPermission }
		if _, err := rootFS.grepNative("x", root, "", false, 10, "content"); err != nil {
			t.Fatal(err)
		}
		secureBrowseReadFile = browseReadOrig

		grepReadOrig, grepWalkOrig := grepReadFile, grepWalk
		grepReadFile = func(string) ([]byte, error) { return nil, fs.ErrPermission }
		if _, err := NewLocalExecutionEnvironment(root).grepNative("x", root, "", false, 10, "content"); err != nil {
			t.Fatal(err)
		}
		grepReadFile = grepReadOrig
		grepWalk = func(string, fs.WalkDirFunc) error { return fs.ErrPermission }
		if _, err := NewLocalExecutionEnvironment(root).grepNative("x", root, "", false, 10, "content"); err == nil {
			t.Fatal("local walk fault succeeded")
		}
		grepWalk = grepWalkOrig
		if err := os.MkdirAll(filepath.Join(root, "list-child"), 0o755); err != nil {
			t.Fatal(err)
		}
		listReadOrig := listReadDir
		listCalls := 0
		listReadDir = func(name string) ([]os.DirEntry, error) {
			listCalls++
			if listCalls > 1 {
				return nil, fs.ErrPermission
			}
			return listReadOrig(name)
		}
		if _, err := NewLocalExecutionEnvironment(root).ListDirectory(root, 2); err == nil {
			t.Fatal("recursive list fault succeeded")
		}
		listReadDir = listReadOrig
		subdir := filepath.Join(root, "subdir")
		if err := os.Mkdir(subdir, 0o755); err != nil {
			t.Fatal(err)
		}
		openatOrig := secureOpenat
		secureOpenat = func(int, string, int, uint32) (int, error) { return -1, fs.ErrPermission }
		if _, err := rootFS.listDir("list_directory", root, 2); err != nil {
			t.Fatal(err)
		}
		secureOpenat = openatOrig
		readCalls := 0
		secureReadDirEntries = func(fd int) ([]os.DirEntry, error) {
			readCalls++
			if readCalls > 1 {
				return nil, fs.ErrPermission
			}
			return readDirOrig(fd)
		}
		if _, err := rootFS.listDir("list_directory", root, 2); err == nil {
			t.Fatal("recursive readdir failure unexpectedly succeeded")
		}
		secureReadDirEntries = readDirOrig

		openat2Orig := secureOpenat2
		openat2Calls := 0
		secureOpenat2 = func(int, string, *unix.OpenHow) (int, error) {
			openat2Calls++
			if openat2Calls == 1 {
				return -1, unix.EINTR
			}
			return -1, fs.ErrNotExist
		}
		_, _ = openat2Retry(-1, "missing", &unix.OpenHow{})
		secureOpenat2 = openat2Orig

		randOrig := secureRandRead
		secureRandRead = func([]byte) (int, error) { return 0, errors.New("scripted entropy failure") }
		_ = tempName()
		secureRandRead = randOrig
		writeOrig := secureWrite
		secureWrite = func(int, []byte) (int, error) { return 0, fs.ErrPermission }
		if err := os.WriteFile(file, []byte("old"), 0o700); err != nil {
			t.Fatal(err)
		}
		sandboxEnv := NewLocalExecutionEnvironment(root)
		sandboxEnv.Sandbox, sandboxEnv.sbfs = &rootPolicy, rootFS
		if _, err := sandboxEnv.EditFile("file", "old", "new", false); err == nil {
			t.Fatal("sandbox edit fault succeeded")
		}
		if err := atomicWriteAt(openDirFD(t, root), "write-fail", rawOrOne(raw), 0o600); err == nil {
			t.Fatal("scripted write failure unexpectedly succeeded")
		}
		secureWrite = writeOrig
		calls := 0
		secureWrite = func(fd int, p []byte) (int, error) {
			calls++
			if calls == 1 {
				return 0, unix.EINTR
			}
			return writeOrig(fd, p)
		}
		if err := atomicWriteAt(openDirFD(t, root), "eintr", rawOrOne(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		secureWrite = writeOrig
		closeOrig := secureClose
		secureClose = func(fd int) error { _ = unix.Close(fd); return fs.ErrPermission }
		if err := atomicWriteAt(openDirFD(t, root), "close-fail", rawOrOne(raw), 0o600); err == nil {
			t.Fatal("scripted close failure unexpectedly succeeded")
		}
		secureClose = closeOrig
		renameOrig := secureRenameat
		secureRenameat = func(int, string, int, string) error { return fs.ErrPermission }
		if err := atomicWriteAt(openDirFD(t, root), "rename-fail", rawOrOne(raw), 0o600); err == nil {
			t.Fatal("scripted rename failure unexpectedly succeeded")
		}
		secureRenameat = renameOrig
	})
}

type oneRuntimeFactory struct{ command commandRuntime }

type runtimeEdgeFileInfo struct {
	name string
	mode os.FileMode
}

func (i runtimeEdgeFileInfo) Name() string       { return i.name }
func (i runtimeEdgeFileInfo) Size() int64        { return 1 }
func (i runtimeEdgeFileInfo) Mode() os.FileMode  { return i.mode }
func (i runtimeEdgeFileInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (i runtimeEdgeFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i runtimeEdgeFileInfo) Sys() any           { return nil }

func (f oneRuntimeFactory) Shell(string) commandRuntime           { return f.command }
func (f oneRuntimeFactory) Argv(string, ...string) commandRuntime { return f.command }

func openDirFD(t *testing.T, path string) int {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return fd
}

func rawOrOne(raw []byte) []byte {
	if len(raw) == 0 {
		return []byte{0}
	}
	return raw
}
