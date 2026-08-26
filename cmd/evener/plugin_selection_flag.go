package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"primeradiant.com/evener/internal/plugins"
)

// pluginSelectionFlag preserves whether --enabled-plugins was present. This is
// distinct from an empty selection: an omitted flag enables the normal plugin
// inventory, while --enabled-plugins= explicitly selects nothing.
type pluginSelectionFlag struct {
	set   bool
	names []string
}

func (f *pluginSelectionFlag) Set(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		f.set = true
		if f.names == nil {
			f.names = make([]string, 0)
		}
		return nil
	}

	parts := strings.Split(raw, ",")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			return errors.New("enabled plugin selection contains an empty name")
		}
		if !isKebabCasePluginName(name) {
			return fmt.Errorf("invalid plugin name %q: must be kebab-case", name)
		}
		names = append(names, name)
	}
	f.set = true
	f.names = append(f.names, names...)
	return nil
}

func (f *pluginSelectionFlag) String() string {
	return strings.Join(f.names, ",")
}

func (f *pluginSelectionFlag) Value() *[]string {
	if !f.set {
		return nil
	}
	copyOfNames := make([]string, len(f.names))
	copy(copyOfNames, f.names)
	return &copyOfNames
}

func isKebabCasePluginName(name string) bool {
	if name == "" || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	previousHyphen := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			previousHyphen = false
		case r == '-':
			if previousHyphen {
				return false
			}
			previousHyphen = true
		default:
			return false
		}
	}
	return true
}

func rejectPluginSelectionWithResume(enabledPlugins *[]string, resume string, resumeLast bool) error {
	if enabledPlugins == nil {
		return nil
	}
	if resume != "" && resumeLast {
		return errors.New("--enabled-plugins cannot be used with --resume or --resume-last")
	}
	if resume != "" {
		return errors.New("--enabled-plugins cannot be used with --resume")
	}
	if resumeLast {
		return errors.New("--enabled-plugins cannot be used with --resume-last")
	}
	return nil
}

// effectivePluginListJSON is intentionally a CLI-local envelope. It exposes
// the resolver's safe display metadata without coupling the CLI wire format to
// appwire types.
type effectivePluginListJSON struct {
	Plugins     []effectivePluginJSON            `json:"plugins"`
	Diagnostics []plugins.LaunchPluginDiagnostic `json:"diagnostics,omitempty"`
}

type effectivePluginJSON struct {
	Name         string                     `json:"name"`
	Version      string                     `json:"version,omitempty"`
	Description  string                     `json:"description,omitempty"`
	Source       plugins.LaunchPluginSource `json:"source"`
	Marketplace  string                     `json:"marketplace,omitempty"`
	Path         string                     `json:"path,omitempty"`
	SkillCount   int                        `json:"skillCount"`   //nolint:tagliatelle // stable CLI JSON uses camelCase
	AgentCount   int                        `json:"agentCount"`   //nolint:tagliatelle // stable CLI JSON uses camelCase
	CommandCount int                        `json:"commandCount"` //nolint:tagliatelle // stable CLI JSON uses camelCase
	HookCount    int                        `json:"hookCount"`    //nolint:tagliatelle // stable CLI JSON uses camelCase
	MCPCount     int                        `json:"mcpCount"`     //nolint:tagliatelle // stable CLI JSON uses camelCase
}

func renderLaunchPluginDiagnostics(w io.Writer, diagnostics []plugins.LaunchPluginDiagnostic) {
	for _, diagnostic := range diagnostics {
		if diagnostic.Path != "" {
			_, _ = fmt.Fprintf(w, "warning: plugin %s: %s\n", diagnostic.Path, diagnostic.Message)
		} else if diagnostic.Name != "" {
			_, _ = fmt.Fprintf(w, "warning: plugin %q: %s\n", diagnostic.Name, diagnostic.Message)
		} else {
			_, _ = fmt.Fprintf(w, "warning: plugin: %s\n", diagnostic.Message)
		}
	}
}
