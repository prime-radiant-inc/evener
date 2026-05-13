package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/buildinfo"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/llm"
)

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
		if _, err := cmdutil.SelectProfile(ref.Provider, ref.Model, ""); err != nil {
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

func launchCheckModels() ([]launchCheckModel, []appwire.ModelListDiagnostic, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	client, err := llm.NewFromEnv()
	if err != nil {
		return nil, nil, err
	}
	cat := llm.EmbeddedModelCatalog()
	providers := client.ProviderNames()
	sort.Strings(providers)
	out := []launchCheckModel{}
	diagnostics := []appwire.ModelListDiagnostic{}
	for _, provider := range providers {
		if provider == "openrouter-anthropic" {
			continue
		}
		models, err := client.ListModels(ctx, provider)
		if err != nil {
			diagnostics = append(diagnostics, launchCheckModelDiagnostic(provider, err))
			continue
		}
		sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
		for _, model := range models {
			if !launchCheckModelVisible(provider, model.ID, cat) {
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

	client, err := llm.NewFromEnv()
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
	for _, model := range models {
		if model.ID == ref.Model && launchCheckModelVisible(ref.Provider, model.ID, cat) {
			return nil
		}
	}
	return fmt.Errorf("model %s is not available from provider %s", ref.Qualified(), ref.Provider)
}

func launchCheckModelListUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "http 403") ||
		strings.Contains(msg, "http 404") ||
		strings.Contains(msg, "does not support listing models")
}

func launchCheckModelVisible(provider, modelID string, cat *llm.ModelCatalog) bool {
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
	if provider == "openrouter" {
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
