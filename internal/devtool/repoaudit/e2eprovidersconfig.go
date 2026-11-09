package repoaudit

import (
	"os"
	"strings"
)

// providersPinnedScripts are the disposable-hub e2e harnesses that write a
// providers.toml with the one writer in test/e2e/providers (#4445).
var providersPinnedScripts = []string{
	"scripts/e2e/e2e-webui-turn-controls.sh",
	"scripts/e2e/e2e-ratelimited-provider.sh",
}

// providersWriteLine is what every shell harness invokes: the built
// test/e2e/providers/cmd binary, pointed at the isolated hub's own file.
const providersWriteLine = `"$run/providers-toml" \`

// e2eScriptsPinProvidersConfig keeps each disposable-hub script immune to
// an ambient EVENER_PROVIDERS_CONFIG after isolation: e2e_isolate_home unsets
// it (and TestDisposableHubRecipeIsolatesTheHubEnvironment requires it to stay
// unset), so each script names its own isolated providers file before starting
// the hub. Without that pin the hub rides the HOME-derived default, which a
// wrapper re-exporting the operator's path would silently outrank — the hub
// then has no providers while every liveness check still passes, the silent
// failure #4411 shaped this check around.
func e2eScriptsPinProvidersConfig(a *audit) {
	for _, source := range providersPinnedScripts {
		scriptPinsProvidersConfig(a, source)
	}
}

// scriptPinsProvidersConfig holds one script to the pin, reporting the first
// thing it finds wrong so each script is judged on its own.
func scriptPinsProvidersConfig(a *audit, source string) {
	body, err := os.ReadFile(source)
	if err != nil {
		a.Errorf("read %s: %v", source, err)
		return
	}
	lines := strings.Split(string(body), "\n")
	writeAt, hubAt, exportAt := -1, -1, -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if writeAt < 0 && trimmed == providersWriteLine {
			writeAt = i
			continue
		}
		if exportAt < 0 && trimmed == `export EVENER_PROVIDERS_CONFIG="$HOME/.config/evener/providers.toml"` {
			exportAt = i
			continue
		}
		if hubAt < 0 && strings.Contains(line, `"$run/evener" hub`) {
			hubAt = i
		}
	}
	switch {
	case writeAt < 0:
		a.Errorf("%s no longer writes its providers.toml with the one writer (%q)", source, providersWriteLine)
	case hubAt < 0:
		a.Errorf("%s no longer starts its hub via \"$run/evener\" hub", source)
	case exportAt < 0:
		a.Errorf("%s never exports EVENER_PROVIDERS_CONFIG at the isolated providers.toml path", source)
	case writeAt >= exportAt || exportAt >= hubAt:
		a.Errorf("%s must export EVENER_PROVIDERS_CONFIG between the providers.toml write (line %d) and the hub start (line %d); found at line %d",
			source, writeAt+1, hubAt+1, exportAt+1)
	}
}
