package hub

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTutorialBrowserCacheEnvironment(t *testing.T) {
	if os.Getenv("EVENER_TUTORIAL_TOURS") != "1" {
		t.Skip("opt-in installed Playwright lookup")
	}
	// Not parallel: each case sets the caller environment before capturing it.
	for _, mode := range []string{"default", "explicit", "relative", "hermetic", "npm-config", "relative-xdg"} {
		t.Run(mode, func(t *testing.T) {
			caller := t.TempDir()
			settings := map[string]string{
				"PATH": os.Getenv("PATH"), "HOME": caller, "USERPROFILE": caller,
				"LOCALAPPDATA": filepath.Join(caller, "local"), "INIT_CWD": caller,
			}
			for _, key := range []string{"PLAYWRIGHT_BROWSERS_PATH", "npm_config_playwright_browsers_path", "npm_package_config_playwright_browsers_path", "XDG_CACHE_HOME", "npm_config_init_cwd", "npm_package_config_init_cwd"} {
				t.Setenv(key, "")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "explicit":
				settings["PLAYWRIGHT_BROWSERS_PATH"] = filepath.Join(caller, "browser cache")
			case "relative":
				settings["PLAYWRIGHT_BROWSERS_PATH"] = "relative cache"
			case "hermetic":
				settings["PLAYWRIGHT_BROWSERS_PATH"] = "0"
			case "npm-config":
				settings["npm_config_playwright_browsers_path"] = "npm cache"
			case "relative-xdg":
				settings["XDG_CACHE_HOME"] = "relative xdg"
			}
			var callerEnv []string
			for key, value := range settings {
				t.Setenv(key, value)
				callerEnv = append(callerEnv, key+"="+value)
			}
			for _, key := range []string{"OPENAI_API_KEY", "EVENER_CREDENTIALS_CONFIG", "HTTPS_PROXY", "NODE_OPTIONS"} {
				t.Setenv(key, "browser-environment-canary")
			}
			// The public API is an independent consumer, not the registry export
			// used to carry the cache across the isolation boundary.
			executable := func(env []string) string {
				t.Helper()
				cmd := exec.CommandContext(context.Background(), "node", "-e", `process.stdout.write(require('../../docs-site/node_modules/playwright').chromium.executablePath())`)
				cmd.Env = env
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("query Playwright executable: %v", err)
				}
				return string(out)
			}
			want := executable(callerEnv)
			root := t.TempDir()
			env := tutorialDriverEnvironment(context.Background(), t, root, tutorialBrowserLookupEnvironment())
			if got := executable(env); got != want {
				t.Fatalf("isolated executable = %q, caller executable = %q", got, want)
			}
			for _, entry := range env {
				key, value, _ := strings.Cut(entry, "=")
				if strings.Contains(value, "browser-environment-canary") {
					t.Fatalf("caller variable leaked: %s", key)
				}
				if key == "HOME" || strings.HasPrefix(key, "XDG_") || key == "TMPDIR" {
					rel, err := filepath.Rel(root, value)
					if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
						t.Fatalf("unowned child root: %s", key)
					}
				}
			}
		})
	}
}
