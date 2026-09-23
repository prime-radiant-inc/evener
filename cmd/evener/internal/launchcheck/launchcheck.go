package launchcheck

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
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
	// Warnings carries the resolved row's registry notes so the hub picker can
	// flag rows the resolver itself warns about (e.g. a global-only model under
	// a regional Vertex location).
	Warnings []string `json:"warnings,omitempty"`
}

// supportedLaunchFlags advertises the serve/run flags this binary accepts
// that a launcher (the hub) passes on the command line. Keep in sync with
// the flag definitions in cmd/evener's run/serve commands: a launcher gates
// on this list before passing a flag, so a flag listed here must parse.
var supportedLaunchFlags = []string{"api-log"}

type launchCheckResponse struct {
	// LaunchFlags is the CLI-capability half of the launch contract: a
	// binary predating a flag omits it, which is itself the signal a
	// launcher needs to reject the pairing up front instead of dying on an
	// unknown flag at spawn time.
	LaunchFlags []string                      `json:"launch_flags,omitempty"`
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
		LaunchFlags: supportedLaunchFlags,
		Version:     buildinfo.Version(),
		Protocol:    appwire.ProtocolVersion,
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
// registry — MINT-FREE: it resolves the row's facts, never the
// credential (spec §10.1), because the check is a read the hub triggers
// and the child's first request owns the mint. It stays network-free
// the same way: no live /models lookup happens here.
func validateLaunchCheckProfile(ref cmdutil.ModelRef) error {
	client, err := launchCheckLoadClient("")
	if err != nil {
		return err
	}
	pr := registry.ParseRef(ref.Qualified())
	if pr.Model == "" {
		return fmt.Errorf("%q: empty model reference", ref.Qualified())
	}
	_, err = client.Registry().ResolveInstanceModelFacts(pr.Instance, pr.Model)
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
		if client.Registry().LaunchMintsCredentialCommand(inst.Name) {
			// The hub never executes a credential command (spec
			// §10.1): a command-credentialed instance's live listing
			// is the child's to make; the contract serves its
			// registry rows, resolved at facts depth — every
			// advertised fact, no credential materialized — under the
			// same §5 visibility filter the child's own listing
			// applies.
			rows, err := client.Registry().InstanceModels(inst.Name)
			if err != nil {
				continue
			}
			for _, row := range rows {
				if row.Disabled {
					continue
				}
				res, err := client.Registry().ResolveInstanceModelFacts(inst.Name, row.ID)
				if err != nil || res.Model.Hidden || llm.LiveSaysNoTools(res) {
					continue
				}
				out = append(out, launchCheckModel{Provider: inst.Name, Model: row.ID, Warnings: append([]string(nil), res.Warnings...)})
			}
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
			out = append(out, launchCheckModel{Provider: inst.Name, Model: m.ModelID, Warnings: append([]string(nil), m.Warnings...)})
		}
	}
	return out, diagnostics, nil
}

func launchCheckModelDiagnostic(provider string, err error) appwire.ModelListDiagnostic {
	message := launchCheckDiagnosticMessage(err)
	info := diagnostic.FromFields(string(diagnostic.SourceProvider), "", "", message)
	return appwire.ModelListDiagnostic{
		Provider: provider,
		Source:   string(info.Source),
		Title:    info.Title,
		Message:  message,
		Hint:     info.Hint,
	}
}

// launchCheckDiagnosticMessage renders the one-line reason a provider's model
// listing failed. The picker prints it inline under the model list
// ("provider — reason"), so it must stop at the failure's class rather than
// quote the failure: an endpoint that answers 404 with an HTML page puts that
// page in the message otherwise, and a missing credential quotes the
// registry's whole remediation warning. The classes carry machine-readable
// facts (the spent-allowance category, the status code, the sentinel), so
// the line stays one short reason; every other failure keeps its own —
// redacted — text.
func launchCheckDiagnosticMessage(err error) string {
	// An exhausted allowance keeps its class ahead of the bare status: it
	// arrives as 429 (or a provider's 403 billing-cycle exhaustion), and the
	// category on the typed error is the specific fact; the status alone
	// would read as a transient throttle.
	if llm.Kind(err) == llm.KindQuotaExceeded {
		return "usage limit reached"
	}
	if status := launchCheckHTTPStatus(err); status != 0 {
		return "HTTP " + strconv.Itoa(status)
	}
	if errors.Is(err, llm.ErrNoCredential) {
		return "no credential"
	}
	return redactLaunchCheckDiagnostic(err.Error())
}

// launchCheckHTTPStatus reports the first provider HTTP status along the
// error's unwrap spine, seeing through the configuration wrappers (the google
// protocol's regional-Vertex remap) that bury the classified HTTP error under
// a ConfigurationError whose own status is zero: errors.As alone would stop at
// that wrapper, whose cause holds the status whose prose the line must not
// quote. The spine can branch — an errors.Join node answers only
// Unwrap() []error, which errors.Unwrap cannot descend into, and errors.As
// stops at the first llm.Error in branch order — so every branch is visited,
// in chain order, until a nonzero status is found.
func launchCheckHTTPStatus(err error) int {
	var llmErr llm.Error
	if errors.As(err, &llmErr) && llmErr.StatusCode() != 0 {
		return llmErr.StatusCode()
	}
	if u, ok := err.(interface{ Unwrap() error }); ok {
		if status := launchCheckHTTPStatus(u.Unwrap()); status != 0 {
			return status
		}
	}
	if branches, ok := err.(interface{ Unwrap() []error }); ok {
		for _, branch := range branches.Unwrap() {
			if status := launchCheckHTTPStatus(branch); status != 0 {
				return status
			}
		}
	}
	return 0
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
