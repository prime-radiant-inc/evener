# Hub Launch Config — TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface the launch-config and credentials RPCs in the TUI: `:settings` command opens a per-layer browser/editor, `:credentials` command opens a provider list with set/clear/OAuth actions, `Ctrl-L` in the composer opens a per-launch override modal applied to the next spawn. SSE notifications refresh open panels.

**Architecture:** New `tea.Model`-shaped panels following the existing `pickerPanel` pattern in `cmd/serf-tui/picker_panel.go`. Each panel is constructed from RPC data, edited via key handlers, and persists back via `serf/launch/setLayer` / `serf/auth/apiKey/set`. The composer gains a small text-prompt sub-model for `Ctrl-L`.

**Tech Stack:** Bubble Tea (existing TUI framework), Lipgloss styles (existing). RPC via the existing `appwire.Client` already wired into the TUI.

**Prerequisite:** Backend plan landed. Web UI plan is **not** a prerequisite — TUI ships independently against the same RPCs.

**Spec:** `docs/superpowers/specs/2026-05-16-hub-serf-launch-config-design.md`

---

## File Structure

**New files**

- `cmd/serf-tui/launchconfig_client.go` — thin wrappers `resolveLaunchConfig`, `getLayer`, `setLayer`, `trustRepo`, `authList`, `authApiKeySet`, `authLogout` returning `tea.Cmd`s for async calls
- `cmd/serf-tui/credentials_panel.go` — `:credentials` panel (list view + actions)
- `cmd/serf-tui/credentials_panel_test.go`
- `cmd/serf-tui/launch_settings_panel.go` — `:settings` panel (tabbed: global, project, in-repo)
- `cmd/serf-tui/launch_settings_panel_test.go`
- `cmd/serf-tui/launch_overrides_modal.go` — `Ctrl-L` composer modal
- `cmd/serf-tui/launch_overrides_modal_test.go`
- `cmd/serf-tui/text_input_modal.go` — generic single-field text input (reused by credential set and override fields)

**Modified files**

- `cmd/serf-tui/hub_command_registry.go` — register `:credentials` and `:settings`
- `cmd/serf-tui/composer_panel.go` — handle `Ctrl-L`, surface override state in submit
- `cmd/serf-tui/hub_appshell_test.go` and `tmux_e2e_test.go` — extend existing TUI tests
- `cmd/serf-tui/sse_client.go` — subscribe to `serf/auth/updated`, `serf/launch/updated` and forward as tea.Msg

---

## Task 1 — RPC client wrappers as `tea.Cmd`

**Files:**
- Create: `cmd/serf-tui/launchconfig_client.go`

- [ ] **Step 1: Inspect how the TUI currently calls RPCs**

```bash
grep -n "hub\.Client\|appwire\.Client\|client\.Request\|client\.Call" cmd/serf-tui/sse_client.go cmd/serf-tui/hub_model.go | head -15
```

You'll see an `appwireClient` or similar field on the `model`/`appShell`. Use it.

- [ ] **Step 2: Write the wrappers**

`cmd/serf-tui/launchconfig_client.go`:

```go
package main

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

type launchResolveResultMsg struct {
	Resolved appwire.LaunchConfigResolved
	Err      error
}
type launchLayerResultMsg struct {
	Layer string
	CWD   string
	Data  appwire.LaunchConfigLayer
	Err   error
}
type launchSetLayerResultMsg struct {
	Layer    string
	CWD      string
	Resolved appwire.LaunchConfigResolved
	Err      error
}
type launchTrustResultMsg struct {
	CWD      string
	Resolved appwire.LaunchConfigResolved
	Err      error
}
type authListResultMsg struct {
	List appwire.AuthListResponse
	Err  error
}
type authStatusResultMsg struct {
	Status appwire.AuthStatusResponse
	Err    error
}
type authApiKeySetResultMsg struct {
	Status appwire.AuthStatusResponse
	Err    error
}

func cmdResolveLaunch(client *appwire.Client, cwd string, overrides *appwire.LaunchConfigLayer) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.LaunchConfigResolved
		err := client.Request(ctx, appwire.MethodSerfLaunchResolve, appwire.LaunchConfigResolveParams{CWD: cwd, LaunchOverrides: overrides}, &resp)
		return launchResolveResultMsg{Resolved: resp, Err: err}
	}
}

func cmdGetLayer(client *appwire.Client, cwd, layer string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.LaunchConfigLayer
		err := client.Request(ctx, appwire.MethodSerfLaunchGetLayer, appwire.LaunchConfigGetLayerParams{CWD: cwd, Layer: layer}, &resp)
		return launchLayerResultMsg{Layer: layer, CWD: cwd, Data: resp, Err: err}
	}
}

func cmdSetLayer(client *appwire.Client, cwd, layer string, config appwire.LaunchConfigLayer) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.LaunchConfigResolved
		err := client.Request(ctx, appwire.MethodSerfLaunchSetLayer, appwire.LaunchConfigSetLayerParams{CWD: cwd, Layer: layer, Config: config}, &resp)
		return launchSetLayerResultMsg{Layer: layer, CWD: cwd, Resolved: resp, Err: err}
	}
}

func cmdTrustRepo(client *appwire.Client, cwd, hash string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.LaunchConfigResolved
		err := client.Request(ctx, appwire.MethodSerfLaunchTrustRepo, appwire.LaunchConfigTrustRepoParams{CWD: cwd, Hash: hash}, &resp)
		return launchTrustResultMsg{CWD: cwd, Resolved: resp, Err: err}
	}
}

func cmdAuthList(client *appwire.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.AuthListResponse
		err := client.Request(ctx, appwire.MethodSerfAuthList, appwire.EmptyParams{}, &resp)
		return authListResultMsg{List: resp, Err: err}
	}
}

func cmdAuthApiKeySet(client *appwire.Client, provider, value string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.AuthStatusResponse
		err := client.Request(ctx, appwire.MethodSerfAuthApiKeySet, appwire.AuthApiKeySetParams{Provider: provider, Value: value}, &resp)
		return authApiKeySetResultMsg{Status: resp, Err: err}
	}
}

func cmdAuthLogout(client *appwire.Client, provider string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.AuthLogoutResponse
		err := client.Request(ctx, appwire.MethodSerfAuthLogout, appwire.AuthLogoutParams{Provider: provider}, &resp)
		return authApiKeySetResultMsg{Status: resp.Status, Err: err}
	}
}
```

- [ ] **Step 3: Build**

```bash
go build ./cmd/serf-tui/...
```

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-tui/launchconfig_client.go
git commit -m "tui: launch-config RPC client wrappers"
```

---

## Task 2 — Generic text-input modal

**Files:**
- Create: `cmd/serf-tui/text_input_modal.go`
- Create: `cmd/serf-tui/text_input_modal_test.go`

A small modal we'll reuse: shows a prompt, captures one line of input, returns it via a result message.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTextInputModal_CapturesAndSubmits(t *testing.T) {
	m := newTextInputModal("API key for anthropic", "")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sk-ant-X")})
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should produce a cmd")
	}
	msg := cmd()
	res, ok := msg.(textInputResultMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want textInputResultMsg", msg)
	}
	if res.Cancelled {
		t.Errorf("should not be cancelled")
	}
	if res.Value != "sk-ant-X" {
		t.Errorf("Value = %q", res.Value)
	}
}

func TestTextInputModal_EscapeCancels(t *testing.T) {
	m := newTextInputModal("x", "")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	msg := cmd()
	res := msg.(textInputResultMsg)
	if !res.Cancelled {
		t.Errorf("Esc should cancel")
	}
}
```

- [ ] **Step 2: Run (fails)**

```bash
go test ./cmd/serf-tui/ -run TestTextInputModal -v
```

- [ ] **Step 3: Implement**

```go
package main

import (
	tea "github.com/charmbracelet/bubbletea"
)

type textInputResultMsg struct {
	Tag       string // caller-supplied identifier
	Value     string
	Cancelled bool
}

type textInputModal struct {
	tag    string
	prompt string
	input  string
	mask   bool
	done   bool
}

func newTextInputModal(prompt, tag string) textInputModal {
	return textInputModal{prompt: prompt, tag: tag}
}

func newTextInputModalMasked(prompt, tag string) textInputModal {
	return textInputModal{prompt: prompt, tag: tag, mask: true}
}

func (m textInputModal) Init() tea.Cmd { return nil }

func (m textInputModal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.KeyMsg:
		switch v.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			m.done = true
			return m, func() tea.Msg { return textInputResultMsg{Tag: m.tag, Cancelled: true} }
		case tea.KeyEnter:
			m.done = true
			return m, func() tea.Msg { return textInputResultMsg{Tag: m.tag, Value: m.input} }
		case tea.KeyBackspace:
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
		case tea.KeyRunes:
			m.input += string(v.Runes)
		}
	}
	return m, nil
}

func (m textInputModal) View() string {
	display := m.input
	if m.mask {
		display = ""
		for range m.input {
			display += "•"
		}
	}
	return m.prompt + "\n" + "> " + display + "_\n[Enter] confirm  [Esc] cancel"
}
```

- [ ] **Step 4: Tests pass**

```bash
go test ./cmd/serf-tui/ -run TestTextInputModal -v
```

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/text_input_modal.go cmd/serf-tui/text_input_modal_test.go
git commit -m "tui: generic text input modal"
```

---

## Task 3 — `:credentials` panel

**Files:**
- Create: `cmd/serf-tui/credentials_panel.go`
- Create: `cmd/serf-tui/credentials_panel_test.go`

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

func TestCredentialsPanel_RendersList(t *testing.T) {
	m := newCredentialsPanel()
	m, _ = m.Update(authListResultMsg{List: appwire.AuthListResponse{Providers: []appwire.AuthStatusResponse{
		{Provider: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey", "oauth"}},
		{Provider: "anthropic", ActiveSource: "absent", AuthModes: []string{"apiKey"}},
	}}})
	view := m.View()
	for _, want := range []string{"openai", "anthropic", "oauth", "absent"} {
		if !contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestCredentialsPanel_EnterTriggersSet(t *testing.T) {
	m := newCredentialsPanel()
	m, _ = m.Update(authListResultMsg{List: appwire.AuthListResponse{Providers: []appwire.AuthStatusResponse{
		{Provider: "anthropic", ActiveSource: "absent", AuthModes: []string{"apiKey"}},
	}}})
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should produce a cmd")
	}
	if msg, ok := cmd().(credentialsActionMsg); !ok {
		t.Errorf("cmd msg = %T", cmd())
	} else if msg.Action != "set" || msg.Provider != "anthropic" {
		t.Errorf("msg = %+v", msg)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: Run (fails)**

```bash
go test ./cmd/serf-tui/ -run TestCredentialsPanel -v
```

- [ ] **Step 3: Implement**

`cmd/serf-tui/credentials_panel.go`:

```go
package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

type credentialsActionMsg struct {
	Action   string // "set" | "logout" | "oauth"
	Provider string
}

type credentialsPanel struct {
	providers []appwire.AuthStatusResponse
	cursor    int
	err       error
	loading   bool
	done      bool
	cancelled bool
}

func newCredentialsPanel() credentialsPanel {
	return credentialsPanel{loading: true}
}

func (p credentialsPanel) Init() tea.Cmd { return nil }

func (p credentialsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case authListResultMsg:
		p.loading = false
		p.err = m.Err
		p.providers = m.List.Providers
		if p.cursor >= len(p.providers) {
			p.cursor = 0
		}
	case tea.KeyMsg:
		switch m.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			p.cancelled = true
			p.done = true
			return p, nil
		case tea.KeyUp:
			if p.cursor > 0 {
				p.cursor--
			}
		case tea.KeyDown:
			if p.cursor < len(p.providers)-1 {
				p.cursor++
			}
		case tea.KeyEnter:
			if len(p.providers) == 0 {
				return p, nil
			}
			cur := p.providers[p.cursor]
			modes := cur.AuthModes
			if contains(strings.Join(modes, ","), "apiKey") {
				return p, func() tea.Msg { return credentialsActionMsg{Action: "set", Provider: cur.Provider} }
			}
			if contains(strings.Join(modes, ","), "oauth") {
				return p, func() tea.Msg { return credentialsActionMsg{Action: "oauth", Provider: cur.Provider} }
			}
		case tea.KeyRunes:
			s := string(m.Runes)
			if s == "c" || s == "C" {
				if len(p.providers) == 0 {
					return p, nil
				}
				cur := p.providers[p.cursor]
				return p, func() tea.Msg { return credentialsActionMsg{Action: "logout", Provider: cur.Provider} }
			}
			if s == "o" || s == "O" {
				if len(p.providers) == 0 {
					return p, nil
				}
				cur := p.providers[p.cursor]
				return p, func() tea.Msg { return credentialsActionMsg{Action: "oauth", Provider: cur.Provider} }
			}
		}
	}
	return p, nil
}

func (p credentialsPanel) View() string {
	if p.loading {
		return "Loading credentials…"
	}
	if p.err != nil {
		return fmt.Sprintf("Error: %v\n[Esc] close", p.err)
	}
	var b strings.Builder
	b.WriteString("Credentials\n\n")
	for i, pv := range p.providers {
		cursor := "  "
		if i == p.cursor {
			cursor = "> "
		}
		fmt.Fprintf(&b, "%s%-22s  source: %-10s  modes: %s\n", cursor, pv.Provider, pv.ActiveSource, strings.Join(pv.AuthModes, ","))
	}
	b.WriteString("\n[Enter] set api key  [O] OAuth sign-in  [C] clear  [Esc] close")
	return b.String()
}
```

- [ ] **Step 4: Tests pass**

```bash
go test ./cmd/serf-tui/ -run TestCredentialsPanel -v
```

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/credentials_panel.go cmd/serf-tui/credentials_panel_test.go
git commit -m "tui: credentials panel"
```

---

## Task 4 — Wire `:credentials` command into the appshell

**Files:**
- Modify: `cmd/serf-tui/hub_command_registry.go`
- Modify: `cmd/serf-tui/app_shell.go`

- [ ] **Step 1: Register the command**

In `cmd/serf-tui/hub_command_registry.go`, append to the registry:

```go
{
	ID:    "credentials",
	Label: "Credentials",
	Help:  "Manage provider API keys and OAuth sign-in",
},
{
	ID:    "settings",
	Label: "Launch settings",
	Help:  "Edit hub launch configuration layers",
},
```

- [ ] **Step 2: Handle the command in the appshell**

In `cmd/serf-tui/app_shell.go`, find the existing command dispatch (search for `case commandPaletteCommand:`). Add:

```go
case "credentials":
	s.activeModal = newCredentialsPanel()
	return s, cmdAuthList(s.client)
case "settings":
	s.activeModal = newLaunchSettingsPanel(s.client, s.currentCWD())
	return s, s.activeModal.(launchSettingsPanel).initialCmd()
```

(`s.currentCWD()` returns the working dir of the focused thread; if none, "" — the settings panel handles empty by prompting for one.)

Also handle `credentialsActionMsg`:

```go
case credentialsActionMsg:
	switch m.Action {
	case "set":
		s.followupModal = newTextInputModalMasked(fmt.Sprintf("API key for %s:", m.Provider), "credential-set:"+m.Provider)
		return s, nil
	case "logout":
		return s, cmdAuthLogout(s.client, m.Provider)
	case "oauth":
		return s, cmdAuthLoginStart(s.client, m.Provider)
	}
case textInputResultMsg:
	if strings.HasPrefix(m.Tag, "credential-set:") {
		provider := strings.TrimPrefix(m.Tag, "credential-set:")
		s.followupModal = nil
		if m.Cancelled || m.Value == "" {
			return s, nil
		}
		return s, cmdAuthApiKeySet(s.client, provider, m.Value)
	}
	// Other tags handled later by override modal
```

Add `followupModal` and `activeModal` fields on the appShell type if not already present.

- [ ] **Step 3: Add `cmdAuthLoginStart`**

In `cmd/serf-tui/launchconfig_client.go`, add:

```go
type authLoginStartResultMsg struct {
	Provider string
	URL      string
	FlowID   string
	Err      error
}

func cmdAuthLoginStart(client *appwire.Client, provider string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.AuthLoginStartResponse
		err := client.Request(ctx, appwire.MethodSerfAuthLoginStart, appwire.AuthLoginStartParams{Provider: provider}, &resp)
		return authLoginStartResultMsg{Provider: provider, URL: resp.URL, FlowID: resp.FlowID, Err: err}
	}
}
```

In `app_shell.go` handle `authLoginStartResultMsg` by displaying the URL and prompting for the redirect URL:

```go
case authLoginStartResultMsg:
	if m.Err != nil {
		s.errorBanner = m.Err.Error()
		return s, nil
	}
	s.errorBanner = "Open in browser: " + m.URL
	s.followupModal = newTextInputModal("Paste full redirect URL after sign-in:", "oauth-redirect:"+m.Provider+":"+m.FlowID)
	return s, nil
```

And complete the OAuth flow in the existing `textInputResultMsg` handler:

```go
if strings.HasPrefix(m.Tag, "oauth-redirect:") {
	parts := strings.SplitN(strings.TrimPrefix(m.Tag, "oauth-redirect:"), ":", 2)
	if len(parts) == 2 {
		s.followupModal = nil
		if m.Cancelled || m.Value == "" {
			return s, nil
		}
		return s, cmdAuthLoginComplete(s.client, parts[0], parts[1], m.Value)
	}
}
```

(Add `cmdAuthLoginComplete` symmetric to `cmdAuthLoginStart`.)

- [ ] **Step 4: Add reload-on-update**

When an `authApiKeySetResultMsg` arrives, re-issue `cmdAuthList`:

```go
case authApiKeySetResultMsg:
	if m.Err != nil {
		s.errorBanner = m.Err.Error()
		return s, nil
	}
	return s, cmdAuthList(s.client)
```

- [ ] **Step 5: Manual test**

Build and run TUI:
```bash
go run ./cmd/serf-tui --hub-addr http://127.0.0.1:9180
```

In TUI:
1. Type `:` → `credentials` → Enter
2. Cursor on `anthropic`, press Enter → text input modal appears
3. Paste key, Enter
4. Status bar updates, panel re-renders with `source: file`

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/hub_command_registry.go cmd/serf-tui/app_shell.go cmd/serf-tui/launchconfig_client.go
git commit -m "tui: :credentials command wires panel + actions"
```

---

## Task 5 — `:settings` launch settings panel (skeleton + tabs)

**Files:**
- Create: `cmd/serf-tui/launch_settings_panel.go`
- Create: `cmd/serf-tui/launch_settings_panel_test.go`

The panel has three tabs: Global, Project, In-Repo. Tab navigation with `←`/`→`, item navigation with `↑`/`↓`, edit with Enter, save with `Ctrl-S`, escape with Esc.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

func TestLaunchSettingsPanel_TabSwitch(t *testing.T) {
	p := newLaunchSettingsPanel(nil, "/cwd")
	m, _ := p.Update(launchLayerResultMsg{Layer: "global", Data: appwire.LaunchConfigLayer{Model: "openai/gpt-5"}})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	v := m.View()
	if !contains(v, "Project") {
		t.Errorf("view should show Project tab after Right:\n%s", v)
	}
}

func TestLaunchSettingsPanel_LoadsGlobalFirst(t *testing.T) {
	p := newLaunchSettingsPanel(nil, "/cwd")
	cmd := p.initialCmd()
	if cmd == nil {
		t.Fatal("initialCmd nil")
	}
	// Calling cmd() would do a real RPC; we just want to know the panel
	// is in loading state.
	if !p.loadingGlobal {
		t.Errorf("expected loadingGlobal")
	}
}
```

- [ ] **Step 2: Implement (failure first)**

```bash
go test ./cmd/serf-tui/ -run TestLaunchSettingsPanel -v
```

- [ ] **Step 3: Write the panel**

`cmd/serf-tui/launch_settings_panel.go`:

```go
package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

type launchTab int

const (
	launchTabGlobal launchTab = iota
	launchTabProject
	launchTabRepo
)

type launchSettingsPanel struct {
	client        *appwire.Client
	cwd           string
	tab           launchTab
	global        appwire.LaunchConfigLayer
	project       appwire.LaunchConfigLayer
	resolved      appwire.LaunchConfigResolved
	loadingGlobal bool
	loadingProj   bool
	loadingResolve bool
	cursor        int
	statusMessage string
	done          bool
	cancelled     bool
}

func newLaunchSettingsPanel(client *appwire.Client, cwd string) launchSettingsPanel {
	return launchSettingsPanel{client: client, cwd: cwd, loadingGlobal: true, loadingProj: true, loadingResolve: true}
}

func (p launchSettingsPanel) initialCmd() tea.Cmd {
	return tea.Batch(
		cmdGetLayer(p.client, p.cwd, "global"),
		cmdGetLayer(p.client, p.cwd, "project"),
		cmdResolveLaunch(p.client, p.cwd, nil),
	)
}

func (p launchSettingsPanel) Init() tea.Cmd { return nil }

func (p launchSettingsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case launchLayerResultMsg:
		if m.Err != nil {
			p.statusMessage = "load error: " + m.Err.Error()
			return p, nil
		}
		switch m.Layer {
		case "global":
			p.global = m.Data
			p.loadingGlobal = false
		case "project":
			p.project = m.Data
			p.loadingProj = false
		}
	case launchResolveResultMsg:
		p.resolved = m.Resolved
		p.loadingResolve = false
		if m.Err != nil {
			p.statusMessage = "resolve error: " + m.Err.Error()
		}
	case launchSetLayerResultMsg:
		p.statusMessage = "saved " + m.Layer
		p.resolved = m.Resolved
	case launchTrustResultMsg:
		p.resolved = m.Resolved
		p.statusMessage = "trust recorded"
	case tea.KeyMsg:
		switch m.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			p.cancelled = true
			p.done = true
			return p, nil
		case tea.KeyLeft:
			if p.tab > 0 {
				p.tab--
				p.cursor = 0
			}
		case tea.KeyRight:
			if p.tab < launchTabRepo {
				p.tab++
				p.cursor = 0
			}
		case tea.KeyUp:
			if p.cursor > 0 {
				p.cursor--
			}
		case tea.KeyDown:
			p.cursor++
		case tea.KeyEnter:
			return p.editCurrent()
		}
	}
	return p, nil
}

func (p launchSettingsPanel) View() string {
	var b strings.Builder
	tabs := []string{"Global", "Project", "In-Repo"}
	for i, name := range tabs {
		if launchTab(i) == p.tab {
			fmt.Fprintf(&b, "[%s] ", strings.ToUpper(name))
		} else {
			fmt.Fprintf(&b, " %s  ", name)
		}
	}
	b.WriteString("\n\n")
	switch p.tab {
	case launchTabGlobal:
		b.WriteString(renderLayerView("global", p.global, p.cursor))
	case launchTabProject:
		b.WriteString("cwd: " + p.cwd + "\n")
		b.WriteString(renderLayerView("project", p.project, p.cursor))
	case launchTabRepo:
		b.WriteString(renderRepoView(p.resolved.Repo))
	}
	if p.statusMessage != "" {
		fmt.Fprintf(&b, "\n%s", p.statusMessage)
	}
	b.WriteString("\n[←/→] tab  [↑/↓] field  [Enter] edit  [Esc] close")
	return b.String()
}

func renderLayerView(label string, l appwire.LaunchConfigLayer, cursor int) string {
	var b strings.Builder
	rows := layerRows(l)
	for i, r := range rows {
		c := "  "
		if i == cursor {
			c = "> "
		}
		fmt.Fprintf(&b, "%s%-22s %s\n", c, r.label, r.value)
	}
	return b.String()
}

type layerRow struct {
	field string
	label string
	value string
}

func layerRows(l appwire.LaunchConfigLayer) []layerRow {
	ptrIntStr := func(p *int) string {
		if p == nil {
			return "(default)"
		}
		return fmt.Sprintf("%d", *p)
	}
	ptrBoolStr := func(p *bool) string {
		if p == nil {
			return "(default)"
		}
		if *p {
			return "true"
		}
		return "false"
	}
	return []layerRow{
		{"model", "model", l.Model},
		{"agent", "agent", l.Agent},
		{"reasoning_effort", "reasoning_effort", l.ReasoningEffort},
		{"context_strategy", "context_strategy", l.ContextStrategy},
		{"max_rounds", "max_rounds", ptrIntStr(l.MaxRounds)},
		{"max_subagent_depth", "max_subagent_depth", ptrIntStr(l.MaxSubagentDepth)},
		{"no_project_prompts", "no_project_prompts", ptrBoolStr(l.NoProjectPrompts)},
		{"skills_dirs", "skills_dirs", fmt.Sprintf("%d entries", len(l.SkillsDirs))},
		{"plugin_dirs", "plugin_dirs", fmt.Sprintf("%d entries", len(l.PluginDirs))},
		{"mcp_configs", "mcp_configs", fmt.Sprintf("%d entries", len(l.MCPConfigs))},
		{"mcps", "mcps", fmt.Sprintf("%d entries", len(l.MCPs))},
		{"env", "env", fmt.Sprintf("%d entries", len(l.Env))},
	}
}

func renderRepoView(r *appwire.RepoLaunchConfigStatus) string {
	if r == nil {
		return "no in-repo file"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "path:  %s\ntrust: %s\nhash:  %s\n\n", r.Path, r.Trust, r.Hash)
	if r.Preview != "" {
		fmt.Fprintf(&b, "preview:\n%s\n", r.Preview)
	}
	if r.Trust == "untrusted" || r.Trust == "changed" {
		b.WriteString("\n[T] trust this file")
	}
	return b.String()
}

func (p launchSettingsPanel) editCurrent() (tea.Model, tea.Cmd) {
	// Edit handling defers to Task 6 — for now, just record cursor pos.
	p.statusMessage = "(editor not yet wired)"
	return p, nil
}
```

(The `editCurrent()` stub is filled in by the next task; the panel here is the chassis.)

- [ ] **Step 4: Tests pass**

```bash
go test ./cmd/serf-tui/ -run TestLaunchSettingsPanel -v
```

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/launch_settings_panel.go cmd/serf-tui/launch_settings_panel_test.go
git commit -m "tui: launch settings panel chassis with tabs"
```

---

## Task 6 — `:settings` editing actions

**Files:**
- Modify: `cmd/serf-tui/launch_settings_panel.go`
- Modify: `cmd/serf-tui/launch_settings_panel_test.go`
- Modify: `cmd/serf-tui/app_shell.go`

The panel's `editCurrent` returns a text-input modal scoped by the field being edited. The appshell handles the `textInputResultMsg` to update the layer and call `cmdSetLayer`.

- [ ] **Step 1: Write the failing test**

```go
func TestLaunchSettingsPanel_EditEmitsModalRequest(t *testing.T) {
	p := newLaunchSettingsPanel(nil, "/cwd")
	m, _ := p.Update(launchLayerResultMsg{Layer: "global", Data: appwire.LaunchConfigLayer{Model: "openai/gpt-5"}})
	// cursor starts at 0, which is "model"
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should produce a cmd requesting a modal")
	}
	msg := cmd()
	req, ok := msg.(launchSettingsEditRequestMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	if req.Layer != "global" || req.Field != "model" {
		t.Errorf("req = %+v", req)
	}
	if req.CurrentValue != "openai/gpt-5" {
		t.Errorf("req.CurrentValue = %q", req.CurrentValue)
	}
}
```

- [ ] **Step 2: Implement editCurrent**

Update `editCurrent` in `launch_settings_panel.go`:

```go
type launchSettingsEditRequestMsg struct {
	Layer        string
	Field        string
	CurrentValue string
}

func (p launchSettingsPanel) editCurrent() (tea.Model, tea.Cmd) {
	if p.tab == launchTabRepo {
		// In repo tab, Enter applies trust.
		if p.resolved.Repo == nil || p.resolved.Repo.Hash == "" {
			return p, nil
		}
		if p.resolved.Repo.Trust == "untrusted" || p.resolved.Repo.Trust == "changed" {
			return p, cmdTrustRepo(p.client, p.cwd, p.resolved.Repo.Hash)
		}
		return p, nil
	}
	rows := layerRows(p.currentLayer())
	if p.cursor >= len(rows) {
		return p, nil
	}
	row := rows[p.cursor]
	return p, func() tea.Msg {
		return launchSettingsEditRequestMsg{
			Layer:        p.tabName(),
			Field:        row.field,
			CurrentValue: row.value,
		}
	}
}

func (p launchSettingsPanel) tabName() string {
	switch p.tab {
	case launchTabProject:
		return "project"
	default:
		return "global"
	}
}

func (p launchSettingsPanel) currentLayer() appwire.LaunchConfigLayer {
	if p.tab == launchTabProject {
		return p.project
	}
	return p.global
}
```

Add an `applyEdit` helper that takes a field name + new value (string) + the existing layer, parses the value into the right type, and returns the updated layer:

```go
func applyEdit(layer appwire.LaunchConfigLayer, field, value string) (appwire.LaunchConfigLayer, error) {
	switch field {
	case "model":
		layer.Model = strings.TrimSpace(value)
	case "agent":
		layer.Agent = strings.TrimSpace(value)
	case "reasoning_effort":
		layer.ReasoningEffort = strings.TrimSpace(value)
	case "context_strategy":
		layer.ContextStrategy = strings.TrimSpace(value)
	case "max_rounds":
		v, err := parseOptionalInt(value)
		if err != nil {
			return layer, err
		}
		layer.MaxRounds = v
	case "max_subagent_depth":
		v, err := parseOptionalInt(value)
		if err != nil {
			return layer, err
		}
		layer.MaxSubagentDepth = v
	case "no_project_prompts":
		switch strings.TrimSpace(value) {
		case "", "(default)":
			layer.NoProjectPrompts = nil
		case "true", "yes", "1":
			t := true
			layer.NoProjectPrompts = &t
		case "false", "no", "0":
			f := false
			layer.NoProjectPrompts = &f
		default:
			return layer, fmt.Errorf("bool required, got %q", value)
		}
	case "skills_dirs", "plugin_dirs", "mcp_configs", "system_prompt_append":
		// Comma-separated list edit replaces the list.
		entries := splitTrim(value, ",")
		switch field {
		case "skills_dirs":
			layer.SkillsDirs = entries
		case "plugin_dirs":
			layer.PluginDirs = entries
		case "mcp_configs":
			layer.MCPConfigs = entries
		case "system_prompt_append":
			layer.SystemPromptAppend = entries
		}
	default:
		return layer, fmt.Errorf("editing %q in TUI not yet supported; use the web UI", field)
	}
	return layer, nil
}

func parseOptionalInt(value string) (*int, error) {
	v := strings.TrimSpace(value)
	if v == "" || v == "(default)" {
		return nil, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func splitTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
```

- [ ] **Step 3: Handle the edit cycle in app_shell.go**

```go
case launchSettingsEditRequestMsg:
	s.followupModal = newTextInputModal(
		fmt.Sprintf("Edit %s.%s (current: %s):", m.Layer, m.Field, m.CurrentValue),
		fmt.Sprintf("settings-edit:%s:%s", m.Layer, m.Field),
	)
	return s, nil
case textInputResultMsg:
	if strings.HasPrefix(m.Tag, "settings-edit:") {
		parts := strings.SplitN(strings.TrimPrefix(m.Tag, "settings-edit:"), ":", 2)
		if len(parts) != 2 {
			return s, nil
		}
		layer, field := parts[0], parts[1]
		s.followupModal = nil
		if m.Cancelled {
			return s, nil
		}
		// Apply the edit to whichever layer is current.
		if panel, ok := s.activeModal.(launchSettingsPanel); ok {
			target := panel.currentLayer()
			updated, err := applyEdit(target, field, m.Value)
			if err != nil {
				s.errorBanner = err.Error()
				return s, nil
			}
			return s, cmdSetLayer(s.client, panel.cwd, layer, updated)
		}
	}
	// (other tags handled elsewhere)
```

- [ ] **Step 4: Re-load on save success**

When `launchSetLayerResultMsg` arrives, refresh the panel's view:

```go
case launchSetLayerResultMsg:
	if m.Err != nil {
		s.errorBanner = m.Err.Error()
		return s, nil
	}
	return s, cmdGetLayer(s.client, m.CWD, m.Layer)
```

- [ ] **Step 5: Run tests + manual smoke**

```bash
go test ./cmd/serf-tui/ -v
```

Then run TUI, `:settings`, edit `model`, save, reload, confirm.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/launch_settings_panel.go cmd/serf-tui/launch_settings_panel_test.go cmd/serf-tui/app_shell.go
git commit -m "tui: settings panel edits + saves layers"
```

---

## Task 7 — `Ctrl-L` per-launch override modal

**Files:**
- Create: `cmd/serf-tui/launch_overrides_modal.go`
- Create: `cmd/serf-tui/launch_overrides_modal_test.go`
- Modify: `cmd/serf-tui/composer_panel.go`
- Modify: `cmd/serf-tui/app_shell.go`

When the composer is focused, `Ctrl-L` opens a small modal showing the per-launch overrides being prepared for the next spawn. The user toggles whether they want to set max_rounds / context_strategy / add skill+plugin dirs. On dismiss, the overrides are stashed on the composer state and submitted as `launchOverrides` on the next thread/start.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

func TestLaunchOverridesModal_AddsSkillDir(t *testing.T) {
	m := newLaunchOverridesModal(nil)
	// Down to "skills_dirs" row (index depends on layout; assume 2)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should request an edit")
	}
	msg := cmd()
	req, ok := msg.(launchSettingsEditRequestMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	if req.Field == "" {
		t.Errorf("missing field: %+v", req)
	}
}

func TestLaunchOverridesModal_ProducesOverrideOnSubmit(t *testing.T) {
	m := newLaunchOverridesModalWith(appwire.LaunchConfigLayer{MaxRounds: ptrIntCM(50)})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil {
		t.Fatal("Ctrl-S should submit")
	}
	msg := cmd()
	res, ok := msg.(launchOverridesResultMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	if res.Overrides == nil || res.Overrides.MaxRounds == nil || *res.Overrides.MaxRounds != 50 {
		t.Errorf("Overrides = %+v", res.Overrides)
	}
}

func ptrIntCM(v int) *int { return &v }
```

- [ ] **Step 2: Implement**

`cmd/serf-tui/launch_overrides_modal.go`:

```go
package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

type launchOverridesResultMsg struct {
	Overrides *appwire.LaunchConfigLayer
	Cancelled bool
}

type launchOverridesModal struct {
	cur       appwire.LaunchConfigLayer
	cursor    int
	done      bool
	cancelled bool
}

func newLaunchOverridesModal(client *appwire.Client) launchOverridesModal {
	return launchOverridesModal{}
}

func newLaunchOverridesModalWith(initial appwire.LaunchConfigLayer) launchOverridesModal {
	return launchOverridesModal{cur: initial}
}

func (m launchOverridesModal) Init() tea.Cmd { return nil }

func (m launchOverridesModal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.KeyMsg:
		switch v.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			m.cancelled = true
			m.done = true
			return m, func() tea.Msg { return launchOverridesResultMsg{Cancelled: true} }
		case tea.KeyCtrlS:
			m.done = true
			cp := m.cur
			return m, func() tea.Msg { return launchOverridesResultMsg{Overrides: &cp} }
		case tea.KeyUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyDown:
			m.cursor++
		case tea.KeyEnter:
			rows := layerRows(m.cur)
			if m.cursor >= len(rows) {
				return m, nil
			}
			row := rows[m.cursor]
			return m, func() tea.Msg {
				return launchSettingsEditRequestMsg{Layer: "launch", Field: row.field, CurrentValue: row.value}
			}
		}
	}
	return m, nil
}

func (m launchOverridesModal) View() string {
	var b strings.Builder
	b.WriteString("Per-launch overrides for next thread\n\n")
	rows := layerRows(m.cur)
	for i, r := range rows {
		c := "  "
		if i == m.cursor {
			c = "> "
		}
		fmt.Fprintf(&b, "%s%-22s %s\n", c, r.label, r.value)
	}
	b.WriteString("\n[Enter] edit  [Ctrl-S] save  [Esc] cancel")
	return b.String()
}

func (m launchOverridesModal) ApplyEdit(field, value string) (launchOverridesModal, error) {
	updated, err := applyEdit(m.cur, field, value)
	if err != nil {
		return m, err
	}
	m.cur = updated
	return m, nil
}
```

- [ ] **Step 3: Wire `Ctrl-L` in composer**

In `cmd/serf-tui/composer_panel.go`, in the Update handler, intercept `Ctrl-L`:

```go
case tea.KeyMsg:
	if msg.Type == tea.KeyCtrlL {
		return c, func() tea.Msg { return launchOverridesOpenMsg{Initial: c.launchOverrides} }
	}
```

Where `c.launchOverrides *appwire.LaunchConfigLayer` is a new field on the composer panel that stores the current overrides.

In `cmd/serf-tui/app_shell.go`:

```go
case launchOverridesOpenMsg:
	if m.Initial != nil {
		s.followupModal = newLaunchOverridesModalWith(*m.Initial)
	} else {
		s.followupModal = newLaunchOverridesModal(s.client)
	}
	return s, nil
case launchSettingsEditRequestMsg:
	if m.Layer == "launch" {
		s.followupModal2 = newTextInputModal(
			fmt.Sprintf("Edit %s (current %s):", m.Field, m.CurrentValue),
			"launch-override:"+m.Field,
		)
		return s, nil
	}
	// (existing settings-edit handling)
case textInputResultMsg:
	if strings.HasPrefix(m.Tag, "launch-override:") {
		field := strings.TrimPrefix(m.Tag, "launch-override:")
		s.followupModal2 = nil
		if m.Cancelled {
			return s, nil
		}
		if modal, ok := s.followupModal.(launchOverridesModal); ok {
			updated, err := modal.ApplyEdit(field, m.Value)
			if err != nil {
				s.errorBanner = err.Error()
				return s, nil
			}
			s.followupModal = updated
		}
		return s, nil
	}
case launchOverridesResultMsg:
	s.followupModal = nil
	if !m.Cancelled {
		s.composer.launchOverrides = m.Overrides
	}
	return s, nil
```

Add `launchOverridesOpenMsg`:
```go
type launchOverridesOpenMsg struct {
	Initial *appwire.LaunchConfigLayer
}
```

- [ ] **Step 4: Pass overrides at spawn**

Wherever the composer triggers `thread/start` (search `cmdThreadStart` or `MethodThreadStart`), set `LaunchOverrides: c.launchOverrides` on the params. Clear them after a successful spawn.

- [ ] **Step 5: Tests + manual smoke**

```bash
go test ./cmd/serf-tui/ -v
```

Then in a running hub, `:new`, press `Ctrl-L`, set `max_rounds` to 50, `Ctrl-S` to confirm, type a prompt and submit. Verify (via hub logs or `:resolve` if you have such a debug command) that the spawn passed `--max-rounds 50`.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/launch_overrides_modal.go cmd/serf-tui/launch_overrides_modal_test.go cmd/serf-tui/composer_panel.go cmd/serf-tui/app_shell.go
git commit -m "tui: Ctrl-L composer modal for per-launch overrides"
```

---

## Task 8 — SSE notification handling

**Files:**
- Modify: `cmd/serf-tui/sse_client.go`
- Modify: `cmd/serf-tui/app_shell.go`

- [ ] **Step 1: Subscribe to the new notifications**

In `cmd/serf-tui/sse_client.go`, find the existing dispatch (the switch on `notification.Method`). Add:

```go
case appwire.NotifySerfAuthUpdated:
	// Forward as tea.Msg so the appshell can refresh panels.
	emit(authUpdatedNotifMsg{})
case appwire.NotifySerfLaunchUpdated:
	emit(launchUpdatedNotifMsg{})
```

Define the messages:
```go
type authUpdatedNotifMsg struct{}
type launchUpdatedNotifMsg struct {
	CWD   string
	Layer string
}
```

- [ ] **Step 2: Refresh open panels on update**

In `app_shell.go`:

```go
case authUpdatedNotifMsg:
	if _, ok := s.activeModal.(credentialsPanel); ok {
		return s, cmdAuthList(s.client)
	}
case launchUpdatedNotifMsg:
	if p, ok := s.activeModal.(launchSettingsPanel); ok {
		return s, p.initialCmd()
	}
```

- [ ] **Step 3: Test (cross-tab via web→TUI or two TUIs)**

Run one TUI + one web client. Set an API key via web; observe the TUI's `:credentials` panel update without manual reload.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-tui/sse_client.go cmd/serf-tui/app_shell.go
git commit -m "tui: refresh panels on serf/auth/updated and serf/launch/updated"
```

---

## Task 9 — Verification

- [ ] **Step 1: Run the full TUI test suite**

```bash
go test ./cmd/serf-tui/ -v
```

- [ ] **Step 2: Manual smoke checklist**

Use a running hub + TUI. Tick through:

- [ ] `:` palette opens; `credentials` and `settings` commands are visible
- [ ] `:credentials` shows providers; setting an API key persists
- [ ] `:settings` shows three tabs; arrows navigate; Enter on `model` opens text input; Ctrl-S saves (actually it's auto-saved on text-input submit per the design); reloading the tab shows the new value
- [ ] In a working dir with `.serf/launch.toml`, the In-Repo tab shows preview + "T to trust"; trusting the file flips state to `trusted` and the merged config includes the in-repo contributions
- [ ] In the composer panel, `Ctrl-L` opens the override modal; editing `max_rounds` and Ctrl-S records the override; spawning a thread passes the override (verify by inspecting `ps -eo args | grep serf-serve` or via Hub logs)
- [ ] When credentials change via web UI, the TUI panel updates within ~1 second (SSE)

- [ ] **Step 3: Commit any final fixes**

```bash
git add -A
git commit -m "tui: launch-config UI verification fixes"
```

(Skip if no changes.)

---

## Implementation Checklist Summary

- [ ] Task 1 — RPC client wrappers as `tea.Cmd`
- [ ] Task 2 — Generic text-input modal
- [ ] Task 3 — `:credentials` panel
- [ ] Task 4 — Wire `:credentials` command + actions
- [ ] Task 5 — `:settings` launch settings panel chassis
- [ ] Task 6 — `:settings` editing actions
- [ ] Task 7 — `Ctrl-L` per-launch override modal
- [ ] Task 8 — SSE notifications
- [ ] Task 9 — Verification
