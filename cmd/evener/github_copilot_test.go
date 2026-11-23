package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/auth/githubcopilot"
)

// copilotGitHubScript serves the GitHub endpoints the Copilot service calls.
// exchangeStatus is set before the server starts and never written after, so
// the test goroutine and the handler goroutines share it without locking.
type copilotGitHubScript struct {
	exchangeStatus int
}

func newCopilotGitHubScript(t *testing.T) (*copilotGitHubScript, *httptest.Server) {
	t.Helper()
	script := &copilotGitHubScript{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/device/code":
			writeCopilotJSON(w, map[string]any{
				"device_code":      "device-secret",
				"user_code":        "ABCD-1234",
				"verification_uri": "https://github.com/login/device",
				"expires_in":       900,
				"interval":         5,
			})
		case "/login/oauth/access_token":
			writeCopilotJSON(w, map[string]any{
				"access_token":             "gho_secret_access",
				"refresh_token":            "ghr_secret_refresh",
				"refresh_token_expires_in": 86400,
				"token_type":               "bearer",
				"scope":                    "read:user",
			})
		case "/user":
			writeCopilotJSON(w, map[string]any{"id": 4242, "login": "octocat", "email": "octocat@example.com"})
		case "/copilot_internal/v2/token":
			if script.exchangeStatus != 0 {
				w.WriteHeader(script.exchangeStatus)
				writeCopilotJSON(w, map[string]string{"message": "Bad credentials"})
				return
			}
			writeCopilotJSON(w, map[string]any{"token": "copilot_secret_session", "expires_at": time.Now().Add(time.Hour).Unix()})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return script, server
}

func writeCopilotJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// useScriptedGitHubCopilot points the command group at the scripted server
// while the real service, command, and filesystem code run.
func useScriptedGitHubCopilot(t *testing.T, server *httptest.Server) {
	t.Helper()
	cfg := githubcopilot.DefaultConfig()
	cfg.GitHubBaseURL = server.URL
	cfg.APIBaseURL = server.URL
	cfg.HTTPTimeout = 2 * time.Second
	service := githubcopilot.NewService(cfg, server.Client())
	previous := gitHubCopilotServiceFactory
	gitHubCopilotServiceFactory = func() *githubcopilot.Service { return service }
	t.Cleanup(func() { gitHubCopilotServiceFactory = previous })
}

// parseCopilotFields flattens the command's key=value stdout into a map.
func parseCopilotFields(t *testing.T, out string) map[string]string {
	t.Helper()
	fields := make(map[string]string)
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		for token := range strings.FieldsSeq(line) {
			key, value, ok := strings.Cut(token, "=")
			if !ok {
				t.Fatalf("stdout token %q is not key=value\n%s", token, out)
			}
			fields[key] = value
		}
	}
	return fields
}

func TestGitHubCopilotDispatchAndHelp(t *testing.T) {
	stateDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	handled, label, err := dispatchCLICommand(
		[]string{"github-copilot", "status", "--state-dir", stateDir},
		strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("dispatchCLICommand() error = %v", err)
	}
	if !handled || label != "evener github-copilot" {
		t.Fatalf("handled = %t, label = %q; want the github-copilot group", handled, label)
	}
	fields := parseCopilotFields(t, stdout.String())
	if fields["state"] != "signed-out" || fields["source"] != "signed-out" {
		t.Fatalf("status = %v, want signed out", fields)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"--help"}} {
		var out, errOut bytes.Buffer
		if err := runGitHubCopilot(args, strings.NewReader(""), &out, &errOut); err != nil {
			t.Fatalf("runGitHubCopilot(%v) error = %v", args, err)
		}
		if out.Len() != 0 || !strings.Contains(errOut.String(), "Usage: evener github-copilot <command>") {
			t.Fatalf("runGitHubCopilot(%v) stdout = %q stderr = %q", args, out.String(), errOut.String())
		}
	}

	for _, command := range []string{"login", "logout", "status"} {
		var out, errOut bytes.Buffer
		err := runGitHubCopilot([]string{command, "--help"}, strings.NewReader(""), &out, &errOut)
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("runGitHubCopilot(%s --help) error = %v, want flag.ErrHelp", command, err)
		}
		if errOut.Len() == 0 {
			t.Fatalf("%s --help produced no usage output", command)
		}
	}

	var unknownOut, unknownErr bytes.Buffer
	err = runGitHubCopilot([]string{"frobnicate"}, strings.NewReader(""), &unknownOut, &unknownErr)
	var unknown *unknownSubcommandError
	if !errors.As(err, &unknown) || unknown.name != "frobnicate" {
		t.Fatalf("error = %v, want an unknownSubcommandError naming frobnicate", err)
	}

	var help bytes.Buffer
	printRunCommands(&help)
	if !strings.Contains(help.String(), "  github-copilot ") {
		t.Fatalf("top-level command help does not list github-copilot:\n%s", help.String())
	}
}

func TestGitHubCopilotLoginSavesRecordAndPrintsMachineFields(t *testing.T) {
	_, server := newCopilotGitHubScript(t)
	useScriptedGitHubCopilot(t, server)
	stateDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if err := runGitHubCopilot([]string{"login", "--dir", t.TempDir(), "--state-dir", stateDir}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("login error = %v (%s)", err, stderr.String())
	}
	fields := parseCopilotFields(t, stdout.String())
	for key, want := range map[string]string{
		"auth_mode":       "device",
		"device_code_url": "https://github.com/login/device",
		"device_code":     "ABCD-1234",
		"state":           "signed-in",
		"source":          "oauth",
		"account_id":      "4242",
		"email":           "octocat@example.com",
		"needs_refresh":   "false",
		"needs_login":     "false",
	} {
		if fields[key] != want {
			t.Errorf("%s = %q, want %q", key, fields[key], want)
		}
	}
	if _, ok := fields["expiry"]; !ok {
		t.Error("stdout has no expiry field")
	}

	record, err := githubcopilot.LoadAuth(stateDir, "github-copilot")
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if record.AccessToken != "gho_secret_access" || record.RefreshToken != "ghr_secret_refresh" || record.CopilotToken != "copilot_secret_session" {
		t.Fatal("saved record did not carry the authorized tokens")
	}
	if record.AccountID != "4242" || record.Email != "octocat@example.com" {
		t.Fatalf("saved account = %q / %q", record.AccountID, record.Email)
	}
	path := githubcopilot.AuthFilePath(stateDir, "github-copilot")
	if filepath.Base(path) != "github-copilot.json" {
		t.Fatalf("record path = %q, want auth/github-copilot/github-copilot.json", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("record stat error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("record permissions = %#o, want %#o", got, 0o600)
	}

	for _, secret := range []string{"gho_secret_access", "ghr_secret_refresh", "copilot_secret_session", "device-secret"} {
		if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
			t.Errorf("command output leaked %q", secret)
		}
	}

	// Status is local: it must still read the saved record after the
	// scripted GitHub server is gone.
	server.Close()
	var statusOut, statusErr bytes.Buffer
	if err := runGitHubCopilot([]string{"status", "--state-dir", stateDir}, strings.NewReader(""), &statusOut, &statusErr); err != nil {
		t.Fatalf("status error = %v (%s)", err, statusErr.String())
	}
	statusFields := parseCopilotFields(t, statusOut.String())
	if statusFields["state"] != "signed-in" || statusFields["source"] != "oauth" || statusFields["account_id"] != "4242" {
		t.Fatalf("status = %v, want the saved account", statusFields)
	}
}

func TestGitHubCopilotCustomInstanceAndLogout(t *testing.T) {
	_, server := newCopilotGitHubScript(t)
	useScriptedGitHubCopilot(t, server)
	stateDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if err := runGitHubCopilot([]string{"login", "--state-dir", stateDir}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("default login error = %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if err := runGitHubCopilot([]string{"login", "--state-dir", stateDir, "--instance", "work"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("custom login error = %v", err)
	}
	if _, err := githubcopilot.LoadAuth(stateDir, "work"); err != nil {
		t.Fatalf("custom record missing: %v", err)
	}
	if _, err := githubcopilot.LoadAuth(stateDir, "github-copilot"); err != nil {
		t.Fatalf("default record missing after custom login: %v", err)
	}

	var statusOut, statusErr bytes.Buffer
	if err := runGitHubCopilot([]string{"status", "--state-dir", stateDir, "--instance", "work"}, strings.NewReader(""), &statusOut, &statusErr); err != nil {
		t.Fatalf("custom status error = %v", err)
	}
	if fields := parseCopilotFields(t, statusOut.String()); fields["state"] != "signed-in" || fields["account_id"] != "4242" {
		t.Fatalf("custom status = %v", fields)
	}

	var blankOut, blankErr bytes.Buffer
	if err := runGitHubCopilot([]string{"status", "--state-dir", stateDir, "--instance", " "}, strings.NewReader(""), &blankOut, &blankErr); err != nil {
		t.Fatalf("blank instance status error = %v", err)
	}
	if fields := parseCopilotFields(t, blankOut.String()); fields["state"] != "signed-in" {
		t.Fatalf("blank instance did not fall back to github-copilot: %v", fields)
	}

	stdout.Reset()
	stderr.Reset()
	if err := runGitHubCopilot([]string{"logout", "--state-dir", stateDir, "--instance", "work"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("custom logout error = %v", err)
	}
	fields := parseCopilotFields(t, stdout.String())
	if fields["deleted"] != "true" || fields["state"] != "signed-out" || fields["source"] != "signed-out" {
		t.Fatalf("logout = %v, want a signed-out deleted record", fields)
	}
	if _, err := os.Stat(githubcopilot.AuthFilePath(stateDir, "work")); !os.IsNotExist(err) {
		t.Fatalf("custom record survived logout (stat error = %v)", err)
	}
	if _, err := githubcopilot.LoadAuth(stateDir, "github-copilot"); err != nil {
		t.Fatalf("logout removed the default instance: %v", err)
	}
}

func TestGitHubCopilotStatusMissingAndCorruptRecords(t *testing.T) {
	stateDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := runGitHubCopilot([]string{"status", "--state-dir", stateDir}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("missing-record status error = %v", err)
	}
	if fields := parseCopilotFields(t, stdout.String()); fields["state"] != "signed-out" || fields["source"] != "signed-out" {
		t.Fatalf("missing-record status = %v", fields)
	}

	path := githubcopilot.AuthFilePath(stateDir, "github-copilot")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	err := runGitHubCopilot([]string{"status", "--state-dir", stateDir}, strings.NewReader(""), &stdout, &stderr)
	if !errors.Is(err, githubcopilot.ErrAuthCorrupt) {
		t.Fatalf("corrupt status error = %v, want ErrAuthCorrupt", err)
	}

	stdout.Reset()
	stderr.Reset()
	if err := runGitHubCopilot([]string{"logout", "--state-dir", stateDir}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("corrupt logout error = %v", err)
	}
	fields := parseCopilotFields(t, stdout.String())
	if fields["deleted"] != "true" || fields["state"] != "signed-out" {
		t.Fatalf("corrupt logout = %v, want deleted signed-out", fields)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("corrupt record survived logout (stat error = %v)", err)
	}
}

func TestGitHubCopilotLoginReportsRejectedCredentials(t *testing.T) {
	script, server := newCopilotGitHubScript(t)
	script.exchangeStatus = http.StatusUnauthorized
	useScriptedGitHubCopilot(t, server)
	stateDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	err := runGitHubCopilot([]string{"login", "--state-dir", stateDir}, strings.NewReader(""), &stdout, &stderr)
	if !errors.Is(err, githubcopilot.ErrCopilotTokenRejected) {
		t.Fatalf("login error = %v, want ErrCopilotTokenRejected", err)
	}
	if _, err := os.Stat(githubcopilot.AuthFilePath(stateDir, "github-copilot")); !os.IsNotExist(err) {
		t.Fatalf("rejected login saved a record (stat error = %v)", err)
	}
	for _, secret := range []string{"gho_secret_access", "ghr_secret_refresh", "copilot_secret_session"} {
		if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
			t.Errorf("rejected login printed %q", secret)
		}
	}
}

func TestGitHubCopilotLoginPropagatesCancellation(t *testing.T) {
	_, server := newCopilotGitHubScript(t)
	useScriptedGitHubCopilot(t, server)
	previous := gitHubCopilotLoginContext
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gitHubCopilotLoginContext = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	t.Cleanup(func() { gitHubCopilotLoginContext = previous })

	stateDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := runGitHubCopilot([]string{"login", "--state-dir", stateDir}, strings.NewReader(""), &stdout, &stderr)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("login error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(githubcopilot.AuthFilePath(stateDir, "github-copilot")); !os.IsNotExist(err) {
		t.Fatalf("cancelled login saved a record (stat error = %v)", err)
	}
	if fields := parseCopilotFields(t, stdout.String()); fields["auth_mode"] != "device" {
		t.Fatalf("stdout = %v, want the device auth mode", fields)
	}
}

func TestGitHubCopilotDefaultStateRootAndFlagValidation(t *testing.T) {
	xdgStateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdgStateHome)

	now := time.Now()
	if err := githubcopilot.SaveAuth(filepath.Join(xdgStateHome, "evener"), "github-copilot", githubcopilot.AuthRecord{
		Instance: "github-copilot", Scheme: githubcopilot.AuthScheme, Version: 1,
		AccessToken: "access", CopilotToken: "copilot", AccountID: "4242",
		ObtainedAt: now, CopilotExpiry: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	var stdout, stderr bytes.Buffer
	if err := runGitHubCopilot([]string{"status"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("status with the default state root error = %v", err)
	}
	if fields := parseCopilotFields(t, stdout.String()); fields["state"] != "signed-in" {
		t.Fatalf("status with the default state root = %v", fields)
	}

	for _, command := range []string{"login", "logout", "status"} {
		if err := runGitHubCopilot([]string{command, "extra"}, strings.NewReader(""), io.Discard, io.Discard); !errors.Is(err, errUnexpectedArguments) {
			t.Fatalf("%s accepted a positional argument: %v", command, err)
		}
		if err := runGitHubCopilot([]string{command, "--unknown-flag"}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
			t.Fatalf("%s accepted an unknown flag", command)
		}
	}
}
