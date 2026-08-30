package launchcheck

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmdutil"
)

// launchCheckLoadClient is the injectable hook for tests. Production code
// calls cmdutil.LoadClient; tests may replace this to inject a stub client.
var launchCheckLoadClient = cmdutil.LoadClient

// launchCheckListTimeout bounds one instance's model listing. It is per
// instance, not per command, so a slow endpoint costs only its own budget.
const launchCheckListTimeout = 8 * time.Second

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

// RunLaunchCheck executes the launch-check command, writing the launch contract
// to stdout and diagnostics to stderr. It validates the requested appwire
// protocol and, when requested, the provider/model ref and launchable models.
func RunLaunchCheck(args []string, stdout, stderr io.Writer) error {
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
		_, _ = fmt.Fprintf(stdout, "ok protocol=%s provider=%s model=%s\n", resp.Protocol, resp.Provider, resp.Model)
	} else {
		_, _ = fmt.Fprintf(stdout, "ok protocol=%s\n", resp.Protocol)
	}
	return nil
}

// validateLaunchCheckProfile checks that the model ref resolves on the
// registry. It is NETWORK-FREE: profile resolution reads the registry alone,
// so the probe needs no credentials and issues no live /models lookup.
func validateLaunchCheckProfile(ref cmdutil.ModelRef) error {
	client, err := launchCheckLoadClient("")
	if err != nil {
		return err
	}
	_, err = cmdutil.ResolveProfile(client, ref.Qualified())
	return err
}

// launchCheckModels lists what every visible instance can launch. Each
// instance is listed on its own, under its own timeout, so one unreachable
// endpoint reports a diagnostic instead of hiding the rest or starving every
// instance after it.
func launchCheckModels() ([]launchCheckModel, []appwire.ModelListDiagnostic, error) {
	client, err := launchCheckLoadClient("")
	if err != nil {
		return nil, nil, err
	}
	out := []launchCheckModel{}
	diagnostics := []appwire.ModelListDiagnostic{}
	for _, inst := range client.Registry().Instances() {
		if inst.Hidden {
			continue
		}
		listCtx, cancel := context.WithTimeout(context.Background(), launchCheckListTimeout)
		listing, err := client.Models(listCtx, inst.Name)
		cancel()
		if err != nil {
			diagnostics = append(diagnostics, launchCheckModelDiagnostic(inst.Name, err))
			continue
		}
		for _, m := range listing.Models {
			out = append(out, launchCheckModel{Provider: inst.Name, Model: m.ModelID})
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

// validateLaunchCheckModel confirms the instance can serve the model: the
// registry's own verdict first, then — only when the instance actually
// answered with a live listing — membership in what it advertised. A
// registry-only listing proves nothing about availability, so it passes.
func validateLaunchCheckModel(ref cmdutil.ModelRef) error {
	ctx, cancel := context.WithTimeout(context.Background(), launchCheckListTimeout)
	defer cancel()

	client, err := launchCheckLoadClient("")
	if err != nil {
		return nil
	}
	if !client.CanServe(ref.Provider, ref.Model) {
		if _, resolveErr := client.Resolve(ref.Qualified()); resolveErr != nil {
			return resolveErr
		}
		return fmt.Errorf("model %s is not available from provider %s", ref.Qualified(), ref.Provider)
	}
	listing, err := client.Models(ctx, ref.Provider)
	if err != nil || !listing.Live {
		return nil
	}
	for _, m := range listing.Models {
		if m.ModelID == ref.Model {
			return nil
		}
	}
	return fmt.Errorf("model %s is not available from provider %s", ref.Qualified(), ref.Provider)
}
