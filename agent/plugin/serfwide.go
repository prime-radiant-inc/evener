package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/envvars"
)

var evenerwideUserHomeDir = os.UserHomeDir

// execSpanPattern matches a !`cmd` execution span in a command body. The
// same shape as command.cmdOrFilePattern's first alternative.
var execSpanPattern = regexp.MustCompile("!`[^`]*`")

// globalCommandsDir resolves the user-global commands directory:
// $XDG_CONFIG_HOME/evener/commands, or ~/.config/evener/commands. Mirrors
// promptpath.globalPromptsDir. Returns "" when no home is resolvable.
func globalCommandsDir() string {
	dir := envvars.XDGConfigHome.Getenv()
	if dir == "" {
		home, err := evenerwideUserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "evener", "commands")
}

// DiscoverEvenerWideCommands scans the user-global commands dir, then walks
// git-root→cwd scanning <dir>/.evener/commands, returning commands keyed by
// bare name. Later scans shadow earlier ones, so the deepest project dir wins
// and every project command shadows the user-global one. A nil env or empty
// cwd skips the project walk but still scans the user-global dir.
//
// Discovery is fail-soft: a missing dir is silent, and per-file problems
// (unreadable dir/file, bad name, malformed frontmatter) skip the file with a
// warning rather than failing the scan.
func DiscoverEvenerWideCommands(env execenv.ExecutionEnvironment) (map[string]Command, []events.WarningData) {
	out := map[string]Command{}
	var warnings []events.WarningData

	if dir := globalCommandsDir(); dir != "" {
		scanSerfwideDir(dir, "user", out, &warnings)
	}

	if env != nil {
		cwd := strings.TrimSpace(env.WorkingDirectory())
		if cwd != "" {
			if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
				cwd = resolved
			}
			root := cwd
			if gr := execenv.GitRootOrEmpty(env, cwd); gr != "" {
				root = gr
			}
			for _, dir := range execenv.DirsFromRootToCwd(root, cwd) {
				scanSerfwideDir(filepath.Join(dir, ".evener", "commands"), "project", out, &warnings)
			}
		}
	}

	return out, warnings
}

// scanSerfwideDir parses every immediate .md file of dir into out, keyed by
// bare filename. Later directories overwrite earlier entries.
func scanSerfwideDir(dir, source string, out map[string]Command, warnings *[]events.WarningData) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			*warnings = append(*warnings, evenerwideWarning("unreadable commands directory",
				fmt.Sprintf("skipping commands directory %s: %v", dir, err)))
		}
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		file := filepath.Join(dir, entry.Name())
		name := strings.TrimSuffix(entry.Name(), ".md")
		if name == "" {
			*warnings = append(*warnings, evenerwideWarning("empty command name",
				fmt.Sprintf("skipping command file %s: a file named exactly .md has no command name", file)))
			continue
		}
		if strings.Contains(name, ":") {
			*warnings = append(*warnings, evenerwideWarning("colon in command name",
				fmt.Sprintf("skipping command file %s: ':' is the plugin namespace separator", file)))
			continue
		}
		if strings.IndexFunc(name, unicode.IsSpace) >= 0 {
			*warnings = append(*warnings, evenerwideWarning("whitespace in command name",
				fmt.Sprintf("skipping command file %s: names with whitespace can never be invoked", file)))
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			*warnings = append(*warnings, evenerwideWarning("unreadable command file",
				fmt.Sprintf("skipping command file %s: %v", file, err)))
			continue
		}
		command, err := ParseCommand(data, name, "")
		if err != nil {
			*warnings = append(*warnings, evenerwideWarning("malformed command file",
				fmt.Sprintf("skipping command file %s: %v", file, err)))
			continue
		}
		command.Source = source
		command.File = file
		if execSpanPattern.MatchString(command.Body) {
			*warnings = append(*warnings, evenerwideWarning("inert execution directive",
				fmt.Sprintf("command file %s contains !` spans: execution directives are inert in evener-wide commands; use a plugin command for executable templates", file)))
		}
		if command.Model != "" || len(command.AllowedTools) > 0 {
			*warnings = append(*warnings, evenerwideWarning("unenforced command frontmatter",
				fmt.Sprintf("command file %s declares model/allowed-tools, which evener does not enforce yet", file)))
		}
		out[name] = command
	}
}

func evenerwideWarning(title, message string) events.WarningData {
	return events.WarningData{Source: "commands", Title: title, Message: message}
}
