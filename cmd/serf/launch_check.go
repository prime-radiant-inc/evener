package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/buildinfo"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/internal/providerconfig"
	"primeradiant.com/serf/llm"
)

// launchCheckLoadClient is the injectable hook for tests. Production code calls
// cmdutil.LoadClient; tests may replace this to inject a stub client or config.
var launchCheckLoadClient = func(opts ...llm.EnvOption) (*llm.Client, providerconfig.Config, bool, error) {
	return cmdutil.LoadClient(opts...)
}

// launchCheckLoadConfig resolves the providers config path (same logic as
// LoadClient) and parses it. Returns (cfg, true, nil) when the file exists and
// is valid, (cfg{}, false, nil) when absent, or (cfg{}, _, err) on parse error.
var launchCheckLoadConfig = func() (providerconfig.Config, bool, error) {
	path := os.Getenv("SERF_PROVIDERS_CONFIG")
	if path == "" {
		path = filepath.Join(providerconfig.DefaultStateRoot(), "providers.toml")
	}
	return providerconfig.LoadFile(path)
}

type launchCheckModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type launchCheckResponse struct {
	Version     string                        `json:"version"`
	Protocol    string                        `json:"protocol"`
	Provider    string                        `json:"provider,omitempty"`
	Model       string                        `json:"model,omitempty"`
	Models      []launchCheckModel            `json:"models,omitempty"`
	Diagnostics []appwire.ModelListDiagnostic `json:"diagnostics,omitempty"`
}

func runLaunchCheck(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("launch-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	model := fs.String("model", "", "provider/model to validate")
	protocol := fs.String("protocol", appwire.ProtocolVersion, "required appwire protocol")
	jsonOut := fs.Bool("json", false, "write machine-readable launch contract")
	modelsOut := fs.Bool("models", false, "include launchable models in the contract")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*protocol) != appwire.ProtocolVersion {
		return fmt.Errorf("unsupported appwire protocol %q (supported %q)", *protocol, appwire.ProtocolVersion)
	}

	resp := launchCheckResponse{
		Version:  buildinfo.Version(),
		Protocol: appwire.ProtocolVersion,
	}
	if *modelsOut {
		models, diagnostics, err := launchCheckModels()
		if err != nil {
			return err
		}
		resp.Models = models
		resp.Diagnostics = diagnostics
	}
	if strings.TrimSpace(*model) != "" {
		ref, err := cmdutil.ParseModelRef(*model)
		if err != nil {
			return err
		}
		if err := validateLaunchCheckProfile(ref); err != nil {
			return err
		}
		if err := validateLaunchCheckModel(ref); err != nil {
			return err
		}
		resp.Provider = ref.Provider
		resp.Model = ref.Model
	}

	if *jsonOut {
		enc := json.NewEncoder(stdout)
		return enc.Encode(resp)
	}
	if resp.Provider != "" {
		fmt.Fprintf(stdout, "ok protocol=%s provider=%s model=%s\n", resp.Protocol, resp.Provider, resp.Model)
	} else {
		fmt.Fprintf(stdout, "ok protocol=%s\n", resp.Protocol)
	}
	return nil
}

// validateLaunchCheckProfile checks that the model ref names a known provider
// or config instance. When a providers.toml exists (hasConfig=true),
// it resolves via ResolveProfileFromConfig so custom instance names are valid;
// otherwise it falls back to SelectProfile for the env-variable path.
//
// Profile validation only needs the config file, not a live client, so it uses
// launchCheckLoadConfig rather than launchCheckLoadClient. This avoids
// credential errors when no API keys are present (the launch contract must
// resolve without credentials).
func validateLaunchCheckProfile(ref cmdutil.ModelRef) error {
	cfg, hasConfig, err := launchCheckLoadConfig()
	if err != nil {
		return err
	}
	if hasConfig {
		_, err := agent.ResolveProfileFromConfig(cfg, ref.Qualified())
		return err
	}
	_, err = cmdutil.SelectProfile(ref.Provider, ref.Model, "")
	return err
}

func launchCheckModels() ([]launchCheckModel, []appwire.ModelListDiagnostic, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	client, _, _, err := launchCheckLoadClient()
	if err != nil {
		return nil, nil, err
	}
	cat := llm.EmbeddedModelCatalog()
	providers := client.ProviderNames()
	sort.Strings(providers)
	out := []launchCheckModel{}
	diagnostics := []appwire.ModelListDiagnostic{}
	for _, provider := range providers {
		tag := client.BehaviorTagOf(provider)
		if tag == "openrouter-anthropic" {
			continue
		}
		models, err := client.ListModels(ctx, provider)
		if err != nil {
			diagnostics = append(diagnostics, launchCheckModelDiagnostic(provider, err))
			continue
		}
		sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
		for _, model := range models {
			if !launchCheckModelVisible(tag, model.ID, cat) {
				continue
			}
			out = append(out, launchCheckModel{Provider: provider, Model: model.ID})
		}
	}
	return out, diagnostics, nil
}

func launchCheckModelDiagnostic(provider string, err error) appwire.ModelListDiagnostic {
	message := redactLaunchCheckDiagnostic(err.Error())
	info := diagnostic.FromFields(string(diagnostic.SourceProvider), "", "", message)
	return appwire.ModelListDiagnostic{
		Provider: provider,
		Source:   string(info.Source),
		Title:    info.Title,
		Message:  message,
		Hint:     info.Hint,
	}
}

func redactLaunchCheckDiagnostic(text string) string {
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !launchCheckSensitiveEnvKey(key) || len(value) < 8 {
			continue
		}
		text = strings.ReplaceAll(text, value, "[redacted]")
	}
	return text
}

func launchCheckSensitiveEnvKey(key string) bool {
	key = strings.ToUpper(key)
	return strings.Contains(key, "KEY") ||
		strings.Contains(key, "TOKEN") ||
		strings.Contains(key, "SECRET") ||
		strings.Contains(key, "PASSWORD") ||
		strings.Contains(key, "CREDENTIAL")
}

func validateLaunchCheckModel(ref cmdutil.ModelRef) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	client, _, _, err := launchCheckLoadClient()
	if err != nil {
		return nil
	}
	providerConfigured := false
	for _, provider := range client.ProviderNames() {
		if provider == ref.Provider {
			providerConfigured = true
			break
		}
	}
	if !providerConfigured {
		return nil
	}
	models, err := client.ListModels(ctx, ref.Provider)
	if err != nil {
		if launchCheckModelListUnavailable(err) {
			return nil
		}
		return fmt.Errorf("validate model %s: %w", ref.Qualified(), err)
	}
	cat := llm.EmbeddedModelCatalog()
	tag := client.BehaviorTagOf(ref.Provider)
	for _, model := range models {
		if model.ID == ref.Model && launchCheckModelVisible(tag, model.ID, cat) {
			return nil
		}
	}
	return fmt.Errorf("model %s is not available from provider %s", ref.Qualified(), ref.Provider)
}

func launchCheckModelListUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "http 403") ||
		strings.Contains(msg, "http 404") ||
		strings.Contains(msg, "does not support listing models") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "client.timeout exceeded") ||
		strings.Contains(msg, "timeout awaiting response headers")
}

// launchCheckModelVisible reports whether modelID should appear in the launch
// model list for a provider with the given behavior tag. It returns false for
// non-chat model IDs (embedding, media, etc.) and, for the "openrouter" tag,
// for models that are not in the catalog or lack tool support.
func launchCheckModelVisible(behaviorTag, modelID string, cat *llm.ModelCatalog) bool {
	lower := strings.ToLower(modelID)
	if strings.Contains(lower, "embedding") ||
		strings.Contains(lower, "whisper") ||
		strings.Contains(lower, "tts") ||
		strings.Contains(lower, "dall-e") ||
		strings.Contains(lower, "moderation") ||
		strings.Contains(lower, "audio") ||
		strings.Contains(lower, "transcribe") ||
		strings.Contains(lower, "image") {
		return false
	}
	if behaviorTag == "openrouter" {
		mi := launchCheckCatalogModelInfo(cat, modelID)
		return mi != nil && mi.SupportsTools
	}
	return true
}

func launchCheckCatalogModelInfo(cat *llm.ModelCatalog, modelID string) *llm.ModelInfo {
	if cat == nil {
		return nil
	}
	return cat.GetModelInfo(modelID)
}
