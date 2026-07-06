package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// writeMCPFixture writes a minimal .mcp.json file with one stdio server and
// one http server, matching mcpprobe's own test fixtures for a
// deterministic available/unreachable pair.
func writeMCPFixture(t *testing.T, dir, stdioCommand, httpURL string) string {
	t.Helper()
	path := filepath.Join(dir, "mcp.json")
	data := `{"mcpServers": {
		"local-tool": {"command": "` + stdioCommand + `"},
		"remote-api": {"type": "http", "url": "` + httpURL + `"}
	}}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("writing mcp.json fixture: %v", err)
	}
	return path
}

// refusedURL returns an http:// URL nobody is listening on: bind a loopback
// listener, then close it immediately, mirroring mcpprobe's own
// TestProbe_ConnectionRefused_Unreachable fixture.
func refusedURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("closing listener: %v", err)
	}
	return "http://" + addr
}

// discoverMCPsForSettings must probe every configured server via mcpprobe
// (real initialize handshake / command-present check), not the retired
// mcpstatus HEAD-then-GET check, and surface both the transport and any
// handshake failure per row.
func TestDiscoverMCPsForSettings_StdioAvailableHTTPUnreachable(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skipf("`true` not found on PATH: %v", err)
	}
	dir := t.TempDir()
	path := writeMCPFixture(t, dir, "true", refusedURL(t))

	s := &WebServer{}
	rows, err := s.discoverMCPsForSettings(context.Background(), path)
	if err != nil {
		t.Fatalf("discoverMCPsForSettings: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2: %+v", len(rows), rows)
	}
	// discoverMCPsForSettings sorts by Name: "local-tool" < "remote-api".
	stdioRow, httpRow := rows[0], rows[1]

	if stdioRow.Name != "local-tool" || stdioRow.Transport != "stdio" {
		t.Errorf("stdio row Name/Transport = %q/%q, want local-tool/stdio", stdioRow.Name, stdioRow.Transport)
	}
	if stdioRow.Status != "available" {
		t.Errorf("stdio row Status = %q, want available (Error=%q)", stdioRow.Status, stdioRow.Error)
	}
	if stdioRow.Error != "" {
		t.Errorf("stdio row Error = %q, want empty on success", stdioRow.Error)
	}

	if httpRow.Name != "remote-api" || httpRow.Transport != "http" {
		t.Errorf("http row Name/Transport = %q/%q, want remote-api/http", httpRow.Name, httpRow.Transport)
	}
	if httpRow.Status != "unreachable" {
		t.Errorf("http row Status = %q, want unreachable", httpRow.Status)
	}
	if httpRow.Error == "" {
		t.Error(`http row Error = "", want a populated connection failure`)
	}
}

// TestSettingsMCPStatus_PopulatedAndEmpty exercises the real
// /_partials/settings/mcp render path end-to-end (renderSettingsPartial ->
// discoverMCPsForSettings -> mcp.html), proving the new status block
// actually executes for both a populated and an empty MCP config.
// template.Must at parse time cannot catch a bad field reference inside a
// {{range}} — only Execute with real, non-empty row data can.
func TestSettingsMCPStatus_PopulatedAndEmpty(t *testing.T) {
	t.Run("populated", func(t *testing.T) {
		if _, err := exec.LookPath("true"); err != nil {
			t.Skipf("`true` not found on PATH: %v", err)
		}
		dir := t.TempDir()
		path := writeMCPFixture(t, dir, "true", refusedURL(t))
		web := NewWebServer(hubcore.WebConfig{
			HubAddr:       "127.0.0.1:9180",
			Roster:        hubcore.NewRoster(t.TempDir(), nil),
			Past:          hubcore.NewPastIndex(""),
			MCPConfigPath: path,
		})
		body := settingsRequest(t, web, "mcp")
		for _, want := range []string{
			"as probed from the hub",
			"local-tool", "remote-api",
			"status-available", "status-unreachable",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("populated mcp settings body missing %q", want)
			}
		}
	})

	t.Run("empty", func(t *testing.T) {
		web := NewWebServer(hubcore.WebConfig{
			HubAddr:       "127.0.0.1:9180",
			Roster:        hubcore.NewRoster(t.TempDir(), nil),
			Past:          hubcore.NewPastIndex(""),
			MCPConfigPath: filepath.Join(t.TempDir(), "does-not-exist.json"),
		})
		body := settingsRequest(t, web, "mcp")
		if !strings.Contains(body, "as probed from the hub") {
			t.Errorf("empty mcp settings body missing the probed-status caption")
		}
		if !strings.Contains(body, "No MCP servers configured") {
			t.Errorf("empty mcp settings body missing the empty-state copy")
		}
	})
}

// TestSettings_DisplaySectionRoutes confirms the "display" settings section
// (Enter-to-send + Show-cost home) is registered and renders its heading. It
// requests just the settings-content partial (HX-Target: settings-content)
// so the assertion is on display.html's own heading, not on the settings
// shell's nav — which also contains the literal word "Display" for its link.
func TestSettings_DisplaySectionRoutes(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/settings/display", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "settings-content")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	const want = `<h2 class="settings-h2">Display</h2>`
	if !strings.Contains(body, want) {
		t.Errorf("display settings partial missing %q heading:\n%s", want, body)
	}
}
