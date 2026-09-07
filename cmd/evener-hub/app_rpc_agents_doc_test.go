package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
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
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md.tmp")); !os.IsNotExist(err) {
		t.Fatal("the temp file survived the rename")
	}

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
// what the file holds. A leftover write-only AGENTS.md.tmp makes that happen
// for real: os.WriteFile keeps the mode of a file it did not create, so the
// write and the rename both succeed and the file that lands cannot be read.
func TestHubRPCAgentsDocSetReportsTheSaveWhenTheReadBackFails(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("relies on a file the process cannot read")
	}
	root := t.TempDir()
	path := agentsDocPath(root)
	if err := os.WriteFile(path+".tmp", nil, 0o200); err != nil {
		t.Fatal(err)
	}
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
	// the file that landed is genuinely unreadable this test proves nothing.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o200 {
		t.Fatalf("mode = %o, want 0200 - the read back was never actually denied", info.Mode().Perm())
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != content {
		t.Fatalf("on disk = %q, want the content the save reported", onDisk)
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

// A write that dies partway must not leave AGENTS.md.tmp sitting in the user's
// config root. A directory at the temp path is the portable way to fail the
// write itself - the open cannot succeed, and whatever is at that path is what
// the cleanup has to clear.
func TestWriteAgentsDocFailureRemovesTheTempFile(t *testing.T) {
	root := t.TempDir()
	path := agentsDocPath(root)
	if err := os.WriteFile(path, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := path + ".tmp"
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatal(err)
	}

	err := writeAgentsDoc(path, "new")
	if err == nil {
		t.Fatal("expected the write to fail with a directory at the temp path")
	}
	if !strings.Contains(err.Error(), "AGENTS.md: write:") {
		t.Fatalf("err = %v, want the failure to come from the write step", err)
	}
	if _, statErr := os.Stat(tmp); !os.IsNotExist(statErr) {
		t.Fatalf("the temp path survived a failed write: stat = %v", statErr)
	}
	onDisk, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(onDisk) != "keep me\n" {
		t.Fatalf("a failed write changed the file: %q", onDisk)
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
