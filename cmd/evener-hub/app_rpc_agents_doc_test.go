package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubRPCAgentsDocGetReportsAMissingFile(t *testing.T) {
	root := t.TempDir()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{LaunchConfigRoot: root})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var got appwire.AgentsDocResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsAgentsDocGet, appwire.EmptyParams{}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	want := appwire.AgentsDocResponse{Path: filepath.Join(root, "AGENTS.md")}
	if got != want {
		t.Fatalf("get = %+v, want %+v", got, want)
	}
}

func TestHubRPCAgentsDocGetReadsAnExistingFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{LaunchConfigRoot: root})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var got appwire.AgentsDocResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsAgentsDocGet, appwire.EmptyParams{}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	want := appwire.AgentsDocResponse{Path: filepath.Join(root, "AGENTS.md"), Exists: true, Content: "# mine\n"}
	if got != want {
		t.Fatalf("get = %+v, want %+v", got, want)
	}
}

// A blank file is Exists=true with its actual content. agent.LoadUserDoc
// treats a whitespace-only AGENTS.md as absent, but that is a judgement about
// what is worth putting in a prompt: the editor edits the file on disk, and
// telling it the file is missing would make "clear the box and save" look
// like it never happened.
func TestHubRPCAgentsDocGetReportsABlankFileAsExisting(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{LaunchConfigRoot: root})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var got appwire.AgentsDocResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsAgentsDocGet, appwire.EmptyParams{}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	want := appwire.AgentsDocResponse{Path: filepath.Join(root, "AGENTS.md"), Exists: true, Content: "  \n"}
	if got != want {
		t.Fatalf("get = %+v, want %+v", got, want)
	}
}

func TestHubRPCAgentsDocSetWritesVerbatimAndBroadcasts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "evener") // the config root itself may not exist yet
	hub := newHubRPCTestServer(t, hubcore.WebConfig{LaunchConfigRoot: root})
	defer hub.Close()
	clientA := dialHubRPC(t, hub)
	defer clientA.Close()
	clientB := dialHubRPC(t, hub)
	defer clientB.Close()
	for _, client := range []*appwire.Client{clientA, clientB} {
		if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
	}

	const content = "  leading space, no trailing newline"
	var result appwire.AgentsDocResponse
	if err := clientA.Request(context.Background(), appwire.MethodEvenerSettingsAgentsDocSet, appwire.AgentsDocSetParams{Content: content}, &result); err != nil {
		t.Fatalf("set: %v", err)
	}
	want := appwire.AgentsDocResponse{Path: filepath.Join(root, "AGENTS.md"), Exists: true, Content: content}
	if result != want {
		t.Fatalf("set = %+v, want %+v", result, want)
	}

	onDisk, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(onDisk) != content {
		t.Fatalf("on disk = %q, want the content byte for byte", onDisk)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(root, "AGENTS.md"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Fatalf("mode = %o, want 0644", info.Mode().Perm())
		}
	}
	requireNoAgentsDocTempFiles(t, root)

	for _, client := range []*appwire.Client{clientA, clientB} {
		notification := receiveAgentsDocChanged(t, client)
		if notification != want {
			t.Fatalf("notification = %+v, want %+v", notification, want)
		}
	}
}

func TestHubRPCAgentsDocSetReplacesThePreviousContent(t *testing.T) {
	root := t.TempDir()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{LaunchConfigRoot: root})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"first\n", "second\n", ""} {
		var result appwire.AgentsDocResponse
		if err := client.Request(context.Background(), appwire.MethodEvenerSettingsAgentsDocSet, appwire.AgentsDocSetParams{Content: content}, &result); err != nil {
			t.Fatalf("set %q: %v", content, err)
		}
		onDisk, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(onDisk) != content {
			t.Fatalf("on disk = %q after set %q", onDisk, content)
		}
		receiveAgentsDocChanged(t, client)
	}
}

// A re-read that fails after the rename must not be reported as a rejected
// save. The rename already landed, so telling the requester its write failed
// would show an error over content that is on disk and leave every other
// client stale; the byte-for-byte contract means the content just written is
// what the file holds. A long symlink chain makes that happen for real: the
// save resolves the chain through filepath.EvalSymlinks and lands on the file
// at the end of it, and the read back walks the same chain through the kernel,
// which gives up long before EvalSymlinks does (see linkChainBeyondTheKernel).
func TestHubRPCAgentsDocSetReportsTheSaveWhenTheReadBackFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privileged on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root")
	realDir := filepath.Join(base, "real")
	for _, dir := range []string{root, realDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	realFile := filepath.Join(realDir, "AGENTS.md")
	if err := os.WriteFile(realFile, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := agentsDocPath(root)
	linkChainBeyondTheKernel(t, path, realFile)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{LaunchConfigRoot: root})
	defer hub.Close()
	clientA := dialHubRPC(t, hub)
	defer clientA.Close()
	clientB := dialHubRPC(t, hub)
	defer clientB.Close()
	for _, client := range []*appwire.Client{clientA, clientB} {
		if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
	}

	const content = "saved even though the read back cannot see it\n"
	var result appwire.AgentsDocResponse
	if err := clientA.Request(context.Background(), appwire.MethodEvenerSettingsAgentsDocSet, appwire.AgentsDocSetParams{Content: content}, &result); err != nil {
		t.Fatalf("set: %v", err)
	}
	want := appwire.AgentsDocResponse{Path: path, Exists: true, Content: content}
	if result != want {
		t.Fatalf("set = %+v, want %+v", result, want)
	}
	for _, client := range []*appwire.Client{clientA, clientB} {
		if notification := receiveAgentsDocChanged(t, client); notification != want {
			t.Fatalf("notification = %+v, want %+v", notification, want)
		}
	}

	// Guards the premise: both branches return the same response, so unless
	// the read back is genuinely denied this test proves nothing.
	if _, err := os.ReadFile(path); err == nil {
		t.Fatal("the read back was never actually denied")
	}
	onDisk, err := os.ReadFile(realFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != content {
		t.Fatalf("on disk = %q, want the content the save reported", onDisk)
	}
}

// linkChainBeyondTheKernel points head at target through more symlinks than
// the kernel will follow - Linux stops at 40 hops and macOS at 32, while
// filepath.EvalSymlinks walks up to 255 of them in userspace. A path built
// this way is one writeAgentsDoc can resolve and os.ReadFile cannot.
func linkChainBeyondTheKernel(t *testing.T, head, target string) {
	t.Helper()
	hops := t.TempDir()
	next := target
	for i := range 44 {
		hop := filepath.Join(hops, fmt.Sprintf("hop%02d", i))
		if err := os.Symlink(next, hop); err != nil {
			t.Fatal(err)
		}
		next = hop
	}
	if err := os.Symlink(next, head); err != nil {
		t.Fatal(err)
	}
}

func TestWriteAgentsDocFailureLeavesThePreviousFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("relies on a directory the process cannot write")
	}
	root := t.TempDir()
	path := agentsDocPath(root)
	if err := os.WriteFile(path, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	if err := writeAgentsDoc(path, "new"); err == nil {
		t.Fatal("expected the write to fail in a read-only directory")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != "keep me\n" {
		t.Fatalf("a failed write changed the file: %q", onDisk)
	}
}

// A save that dies partway must not leave a temp file sitting in the user's
// config root. Nothing can occupy the temp path itself any more, so the
// portable way to fail after the temp file exists is a directory where
// AGENTS.md goes: the content lands, the rename cannot.
func TestWriteAgentsDocFailureRemovesTheTempFile(t *testing.T) {
	root := t.TempDir()
	path := agentsDocPath(root)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}

	err := writeAgentsDoc(path, "new")
	if err == nil {
		t.Fatal("expected the save to fail with a directory where AGENTS.md goes")
	}
	if !strings.Contains(err.Error(), "AGENTS.md: rename:") {
		t.Fatalf("err = %v, want the failure to come from the rename step", err)
	}
	requireNoAgentsDocTempFiles(t, root)
}

// A stale AGENTS.md.tmp - an older evener's leftover, or a symlink planted
// where one used to sit - is no longer part of a save at all: the temp file
// is created exclusively under a name of its own, so the link is neither
// written through nor renamed into place.
func TestWriteAgentsDocIgnoresAStaleTempSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privileged on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(base, "elsewhere")
	if err := os.WriteFile(elsewhere, []byte("not yours\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := agentsDocPath(root)
	stale := path + ".tmp"
	if err := os.Symlink(elsewhere, stale); err != nil {
		t.Fatal(err)
	}

	if err := writeAgentsDoc(path, "mine\n"); err != nil {
		t.Fatalf("writeAgentsDoc: %v", err)
	}

	untouched, err := os.ReadFile(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if string(untouched) != "not yours\n" {
		t.Fatalf("the save wrote through the stale link: %q", untouched)
	}
	info, err := os.Lstat(stale)
	if err != nil {
		t.Fatalf("the stale link did not survive the save: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("mode = %v, want the stale link left as it was", info.Mode())
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != "mine\n" {
		t.Fatalf("on disk = %q, want the saved content", onDisk)
	}
}

// requireNoAgentsDocTempFiles asserts that no temp file writeAgentsDoc could
// have created survives in dir.
func requireNoAgentsDocTempFiles(t *testing.T, dir string) {
	t.Helper()
	leftovers, err := filepath.Glob(filepath.Join(dir, ".AGENTS.md-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temp files survived: %v", leftovers)
	}
}

// A dotfiles-managed ~/.config keeps AGENTS.md as a symlink into the dotfiles
// repo. Renaming over the link would replace it with a regular file and take
// the dotfiles copy out of the loop without saying so, so the save follows it.
func TestWriteAgentsDocFollowsASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privileged on Windows")
	}
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	root := filepath.Join(base, "root")
	for _, dir := range []string{realDir, root} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	realFile := filepath.Join(realDir, "AGENTS.md")
	if err := os.WriteFile(realFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := agentsDocPath(root)
	if err := os.Symlink(realFile, link); err != nil {
		t.Fatal(err)
	}

	if err := writeAgentsDoc(link, "new"); err != nil {
		t.Fatalf("writeAgentsDoc: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("mode = %v, want the symlink itself to survive the save", info.Mode())
	}
	onDisk, err := os.ReadFile(realFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != "new" {
		t.Fatalf("linked file = %q, want the saved content", onDisk)
	}
	requireNoAgentsDocTempFiles(t, root)
	requireNoAgentsDocTempFiles(t, realDir)
}

// A link with no target has nothing to follow: EvalSymlinks fails and the
// ordinary temp-and-rename applies, so the save lands as a regular file where
// the broken link was.
func TestWriteAgentsDocDanglingSymlinkIsReplaced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privileged on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	link := agentsDocPath(root)
	if err := os.Symlink(filepath.Join(base, "gone", "AGENTS.md"), link); err != nil {
		t.Fatal(err)
	}

	if err := writeAgentsDoc(link, "new"); err != nil {
		t.Fatalf("writeAgentsDoc: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("mode = %v, want a regular file where the broken link was", info.Mode())
	}
	onDisk, err := os.ReadFile(link)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != "new" {
		t.Fatalf("on disk = %q, want the saved content", onDisk)
	}
}

func receiveAgentsDocChanged(t *testing.T, client *appwire.Client) appwire.AgentsDocResponse {
	t.Helper()
	select {
	case notification := <-client.Notifications():
		if notification.Method != appwire.NotifyEvenerSettingsAgentsDocChanged {
			t.Fatalf("notification method = %q, want %q", notification.Method, appwire.NotifyEvenerSettingsAgentsDocChanged)
		}
		var params appwire.AgentsDocResponse
		if err := json.Unmarshal(notification.Params, &params); err != nil {
			t.Fatalf("decode notification: %v", err)
		}
		return params
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the agentsDoc notification")
		return appwire.AgentsDocResponse{}
	}
}

// Settings edits the hub's AGENTS.md and the sessions the hub spawns have to
// read that same file. Both come from one expression over the hub's config
// root; this pins them together so neither can move on its own.
func TestThreadStartHandsSpawnedSessionsThePathSettingsEdits(t *testing.T) {
	spawner := &recordingSpawner{}
	cfg := hubcore.WebConfig{LaunchConfigRoot: t.TempDir(), PluginRoot: t.TempDir(), Spawner: spawner}

	if _, err := hubThreadStart(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadStartParams{
		CWD:   t.TempDir(),
		Model: "openai/gpt-5",
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	spawns := spawner.Spawns()
	if len(spawns) != 1 {
		t.Fatalf("spawn calls = %d, want 1", len(spawns))
	}

	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var settings appwire.AgentsDocResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsAgentsDocGet, appwire.EmptyParams{}, &settings); err != nil {
		t.Fatalf("get: %v", err)
	}

	if spawns[0].AgentsDocPath != settings.Path {
		t.Fatalf("spawn AgentsDocPath = %q, want the file Settings edits %q", spawns[0].AgentsDocPath, settings.Path)
	}
	args := buildSpawnArgs(spawns[0])
	if !slicesContainOrderedFlag(args, "--agents-doc", settings.Path) {
		t.Fatalf("spawn args = %v, want --agents-doc %q", args, settings.Path)
	}
}

// The hub's own path derivation has to be as strict as the daemon's
// (agent.personalDocPath): a root that resolves to nothing, or to a relative
// directory, yields no path at all.
func TestHubAgentsDocPath_RefusesARelativeOrUnresolvableRoot(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	if got := hubAgentsDocPath(hubcore.WebConfig{}); got != "" {
		t.Fatalf("hubAgentsDocPath = %q, want no path when no user config root resolves", got)
	}
	relative := filepath.Join(".", ".config", "evener")
	if got := hubAgentsDocPath(hubcore.WebConfig{LaunchConfigRoot: relative}); got != "" {
		t.Fatalf("hubAgentsDocPath = %q, want no path for the relative root %q", got, relative)
	}
	root := t.TempDir()
	want := filepath.Join(root, "AGENTS.md")
	if got := hubAgentsDocPath(hubcore.WebConfig{LaunchConfigRoot: root}); got != want {
		t.Fatalf("hubAgentsDocPath = %q, want %q", got, want)
	}
}

// Without a path there is no file to serve, and the handlers must say so
// rather than fall back to a repository-relative AGENTS.md.
func TestHubRPCAgentsDocRefusesWithoutAConfigRoot(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	hub := newHubRPCTestServer(t, hubcore.WebConfig{HubStateRoot: t.TempDir(), PluginRoot: t.TempDir()})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var got appwire.AgentsDocResponse
	err := client.Request(context.Background(), appwire.MethodEvenerSettingsAgentsDocGet, appwire.EmptyParams{}, &got)
	if err == nil || !strings.Contains(err.Error(), "no user config root") {
		t.Fatalf("get error = %v, want one naming the unresolved user config root", err)
	}
	err = client.Request(context.Background(), appwire.MethodEvenerSettingsAgentsDocSet, appwire.AgentsDocSetParams{Content: "# mine\n"}, &got)
	if err == nil || !strings.Contains(err.Error(), "no user config root") {
		t.Fatalf("set error = %v, want one naming the unresolved user config root", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".config", "evener", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("stat of a working-directory AGENTS.md = %v, want it never created", err)
	}
}

// A spawn with nothing to point at hands the child no --agents-doc rather
// than a relative one; the daemon then resolves (or refuses) on its own.
func TestThreadStartOmitsTheAgentsDocFlagWithoutAConfigRoot(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	spawner := &recordingSpawner{}
	cfg := hubcore.WebConfig{PluginRoot: t.TempDir(), Spawner: spawner}

	if _, err := hubThreadStart(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadStartParams{
		CWD:   t.TempDir(),
		Model: "openai/gpt-5",
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	spawns := spawner.Spawns()
	if len(spawns) != 1 {
		t.Fatalf("spawn calls = %d, want 1", len(spawns))
	}
	if spawns[0].AgentsDocPath != "" {
		t.Fatalf("spawn AgentsDocPath = %q, want none when no user config root resolves", spawns[0].AgentsDocPath)
	}
	if args := buildSpawnArgs(spawns[0]); slices.Contains(args, "--agents-doc") {
		t.Fatalf("spawn args = %v, want no --agents-doc", args)
	}
}
