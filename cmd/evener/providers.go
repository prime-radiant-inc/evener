package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// providerNetworkTimeout bounds each call `evener providers` makes: one model
// listing or one probe request per instance and protocol.
const providerNetworkTimeout = 10 * time.Second

func printProvidersUsage(w io.Writer) {
	_, _ = fmt.Fprintf(w, "Usage: evener providers <list|probe|add> [flags]\n\n")
	_, _ = fmt.Fprintf(w, "Inspect and author provider instances (docs/superpowers/specs/2026-08-28-provider-registry-design.md §11.2).\n\n")
	_, _ = fmt.Fprintf(w, "Commands:\n")
	_, _ = fmt.Fprintf(w, "  list [--check]                Every instance with its base, protocol, endpoint, and credential source; --check lists each one's models live\n")
	_, _ = fmt.Fprintf(w, "  probe <instance> [--write]    Which protocol the endpoint accepts; --write records it in providers.toml\n")
	_, _ = fmt.Fprintf(w, "  add <name> --base X [flags]   Write a new instance entry, then probe it\n\n")
	_, _ = fmt.Fprintf(w, "Keys are never passed on the command line: a credential header references a $VARIABLE,\n")
	_, _ = fmt.Fprintf(w, "and a key itself goes in credentials.toml or the hub's credentials pane.\n")
}

// runProviders dispatches `evener providers` (spec §11.2).
func runProviders(args []string, _ io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printProvidersUsage(stderr)
		return nil
	}
	switch args[0] {
	case "list":
		return runProvidersList(args[1:], stdout, stderr)
	case "probe":
		return runProvidersProbe(args[1:], stdout, stderr)
	case "add":
		return runProvidersAdd(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printProvidersUsage(stderr)
		return nil
	default:
		printProvidersUsage(stderr)
		return fmt.Errorf("unknown providers command %q", args[0])
	}
}

// parseNamedCommand parses `<name> [flags]`. The name comes first, so the
// flags after it must be parsed separately: Go's flag package stops at the
// first positional argument. An argument list that starts with a flag names
// no instance and gets the usage line, after the flag set has had its own
// chance at --help.
func parseNamedCommand(fs *flag.FlagSet, args []string, usage string) (string, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		return "", errors.New(usage)
	}
	name := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return "", err
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return name, nil
}

func runProvidersList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("providers list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "list each instance's models live and report reachability")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	r, store, err := loadCLIRegistryWithNotices(stderr)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, r.UserLayerNote())
	instances := r.Instances()
	reportStrayCredentials(r, store.Names(), instances, stderr)

	var client *llm.Client
	if *check {
		client = cmdutil.NewRegistryClient(r, "")
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	header := "NAME\tBASE\tPROTOCOL\tENDPOINT\tCREDENTIAL\tNOTES"
	if *check {
		header += "\tLIVE"
	}
	_, _ = fmt.Fprintln(tw, header)
	for _, inst := range instances {
		base := inst.Base
		if base == "" {
			base = inst.ProviderID
		}
		notes := inst.Warnings
		if inst.Default {
			notes = append(append([]string(nil), notes...), "default")
		}
		// The credential SOURCE is what a listing may show; the value it names
		// never crosses this boundary (spec §11.2) — and neither does the
		// userinfo an endpoint URL may carry, which is credential material
		// too.
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s", inst.Name, base, inst.Protocol, registry.RedactURL(inst.BaseURL), inst.CredentialSource, strings.Join(notes, "; "))
		if *check {
			row += "\t" + liveColumn(client, inst.Name)
		}
		_, _ = fmt.Fprintln(tw, row)
	}
	return tw.Flush()
}

// reportStrayCredentials names credentials.toml entries that are neither an
// instance nor a registry provider id: after the cut-over a key keyed by an
// old instance name is read by nothing (spec §14.1).
func reportStrayCredentials(r *registry.Registry, names []string, instances []registry.Instance, stderr io.Writer) {
	known := make(map[string]bool, len(instances))
	for _, inst := range instances {
		known[inst.Name] = true
	}
	for _, name := range names {
		if known[name] {
			continue
		}
		if _, curated := r.Provider(name); curated {
			continue
		}
		_, _ = fmt.Fprintf(stderr, "warning: credentials.toml entry %q names no instance; re-enter it under the new instance name or delete it (spec §14.1)\n", name)
	}
}

// liveColumn is one instance's --check verdict: what its transport answered
// when asked for its models. An endpoint the registry knows but cannot list
// is registry-only, not an error (spec §8.1).
func liveColumn(client *llm.Client, name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), providerNetworkTimeout)
	defer cancel()
	listing, err := client.Models(ctx, name)
	switch {
	case err != nil:
		return "error: " + err.Error()
	case !listing.Live:
		return fmt.Sprintf("registry-only (%d models)", len(listing.Models))
	default:
		return fmt.Sprintf("ok (%d models)", len(listing.Models))
	}
}

// probe verdicts for one protocol (spec §11.2).
const (
	probeOK           = "ok"
	probeInconclusive = "inconclusive"
	probeUnsupported  = "unsupported"
	probeFailed       = "error"
)

// probeResult is what one protocol's minimal request came back as. detail is
// the provider's own message, which names no credential.
type probeResult struct {
	status string
	detail string
}

func (p probeResult) String() string {
	if p.detail == "" {
		return p.status
	}
	return p.status + " (" + p.detail + ")"
}

func runProvidersProbe(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("providers probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	write := fs.Bool("write", false, "record the protocol that succeeded in providers.toml")
	name, err := parseNamedCommand(fs, args, "usage: evener providers probe <instance> [--write]")
	if err != nil {
		return err
	}
	r, _, err := loadCLIRegistryWithNotices(stderr)
	if err != nil {
		return err
	}
	return probeInstance(r, name, *write, stdout)
}

// probeInstance is `providers probe` against a registry the caller already
// loaded: `providers add` probes what its own write reloaded, so the notices
// are announced once and the entry is read once.
func probeInstance(r *registry.Registry, name string, write bool, stdout io.Writer) error {
	res, err := r.ResolveInstance(name)
	if err != nil {
		return err
	}

	discovered := listProbeModels(r, name, res, stdout)
	model := res.DefaultModel
	if model == "" && len(discovered) > 0 {
		model = discovered[0]
	}
	if model == "" {
		return fmt.Errorf("probe %s: no model to send: the instance has no default_model and its endpoint listed none", name)
	}
	entry, err := probeEntry(name)
	if err != nil {
		return err
	}

	protocols := probeProtocols(res.Protocol)
	results := make(map[string]probeResult, len(protocols))
	for _, proto := range protocols {
		results[proto] = probeProtocol(name, proto, entry, model)
		_, _ = fmt.Fprintf(stdout, "%s: %s\n", proto, results[proto])
	}
	if !write {
		return nil
	}
	return writeProbedProtocol(name, res.Protocol, protocols, results, stdout)
}

// listProbeModels prints the instance's live model ids and returns them.
// Discovered models are printed, never written (spec §11.2).
func listProbeModels(r *registry.Registry, name string, res registry.Resolved, stdout io.Writer) []string {
	if res.Transport.ModelsEndpoint == registry.EndpointUnsupported {
		_, _ = fmt.Fprintf(stdout, "models: %s has no model listing endpoint\n", name)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), providerNetworkTimeout)
	defer cancel()
	listing, err := cmdutil.NewRegistryClient(r, "").Models(ctx, name)
	if err != nil {
		_, _ = fmt.Fprintf(stdout, "models: error: %v\n", err)
		return nil
	}
	ids := make([]string, 0, len(listing.Models))
	for _, m := range listing.Models {
		ids = append(ids, m.ModelID)
	}
	_, _ = fmt.Fprintf(stdout, "models: %d discovered: %s\n", len(ids), strings.Join(ids, ", "))
	return ids
}

// probeProtocols is what to probe for an instance on protocol p: the two
// OpenAI protocols are interchangeable enough that a gateway may speak either,
// so both are tried; every other protocol is only itself (spec §11.2).
func probeProtocols(p string) []string {
	if p == registry.ProtocolOpenAIChat || p == registry.ProtocolOpenAIResponses {
		return []string{registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses}
	}
	return []string{p}
}

// probeEntry is the instance's own providers.toml entry, which the probe
// re-loads under each candidate protocol. An instance that is authored
// nowhere is an implicit provider, whose bare entry inherits the curated
// record of the same id (spec §5.1).
func probeEntry(name string) (registry.Provider, error) {
	path, noUserLayer := cmdutil.ProvidersConfigPath()
	if noUserLayer {
		return registry.Provider{ID: name}, nil
	}
	l, _, err := registry.ReadConfigFile(path)
	if err != nil {
		return registry.Provider{}, err
	}
	if p, ok := l.Providers[name]; ok {
		return p, nil
	}
	return registry.Provider{ID: name}, nil
}

// probeProtocol sends spec §11.2's minimal request under one protocol. The
// candidate protocol is applied by loading the registry again with the
// instance's own entry carrying it, so the endpoint, the pruner, and the caps
// are the ones that protocol would really run with.
func probeProtocol(name, proto string, entry registry.Provider, model string) probeResult {
	entry.ID = name
	entry.Protocol = proto
	r, _, err := loadCLIRegistry(registry.WithInstances(map[string]registry.Provider{name: entry}))
	if err != nil {
		return probeResult{status: probeFailed, detail: err.Error()}
	}
	res, err := r.Resolve(name + "/" + model)
	if err != nil {
		return probeResult{status: probeFailed, detail: err.Error()}
	}
	p, ok := llm.ProtocolFor(proto)
	if !ok {
		return probeResult{status: probeFailed, detail: fmt.Sprintf("protocol %q is not registered", proto)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), providerNetworkTimeout)
	defer cancel()
	if _, err := p.Complete(ctx, probeRequest(model), res); err != nil {
		// "unsupported" is a verdict the endpoint gave; anything the endpoint
		// never answered is the probe's own error. A rejection normally
		// carries an HTTP status, with one exception: a 200 that served
		// nothing the protocol recognizes, which the adapter reports as an
		// UnsupportedEndpointError and which is exactly this verdict without
		// a status. A refused connection, a DNS failure, and a deadline all
		// also carry no status, and calling those "unsupported" would claim
		// something about the endpoint from evidence about the network.
		e, answered := errors.AsType[llm.Error](err)
		_, servedNothing := errors.AsType[*llm.UnsupportedEndpointError](err)
		switch {
		case servedNothing:
			return probeResult{status: probeUnsupported, detail: err.Error()}
		case !answered || e.StatusCode() == 0:
			return probeResult{status: probeFailed, detail: err.Error()}
		case namesMaxTokensField(e.Error()):
			// The endpoint rejected the max-tokens field this row spells, not
			// the protocol: it may well speak it (spec §11.2).
			return probeResult{status: probeInconclusive, detail: e.Error()}
		default:
			return probeResult{status: probeUnsupported, detail: e.Error()}
		}
	}
	return probeResult{status: probeOK}
}

// maxTokensFields are the three spellings of the output cap. A rejection
// naming one is about the field, not the protocol.
var maxTokensFields = []string{"max_output_tokens", "max_completion_tokens", "max_tokens"}

func namesMaxTokensField(message string) bool {
	for _, field := range maxTokensFields {
		if strings.Contains(message, field) {
			return true
		}
	}
	return false
}

// probeRequest is spec §11.2's minimal request: a one-word prompt, the
// smallest output cap OpenAI's Responses endpoint accepts, one trivial tool
// with an optional parameter, and an explicit text format — the shapes the
// runtime sends, small enough that a working endpoint answers cheaply.
func probeRequest(model string) llm.Request {
	return llm.Request{
		Model: model, Messages: []llm.Message{llm.User("ping")}, MaxTokens: new(16),
		Tools: []llm.ToolDefinition{{
			Name: "noop", Description: "does nothing",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"note": map[string]any{"type": "string"}}},
		}},
		ResponseFormat: &llm.ResponseFormat{Type: "text"},
	}
}

// writeProbedProtocol records the outcome of a probe in providers.toml: the
// one protocol that worked. When both work the registry's own choice stands,
// and when none does nothing is written (spec §11.2).
func writeProbedProtocol(name, current string, protocols []string, results map[string]probeResult, stdout io.Writer) error {
	var worked []string
	for _, proto := range protocols {
		if results[proto].status == probeOK {
			worked = append(worked, proto)
		}
	}
	switch len(worked) {
	case 0:
		_, _ = fmt.Fprintf(stdout, "no protocol succeeded; %s keeps protocol = %s\n", name, current)
		return nil
	case 1:
		// fall through to the write
	default:
		_, _ = fmt.Fprintf(stdout, "%s work; %s keeps the registry's choice, protocol = %s\n", strings.Join(worked, " and "), name, current)
		return nil
	}
	path, noUserLayer := cmdutil.ProvidersConfigPath()
	if noUserLayer {
		return fmt.Errorf("cannot record protocol = %s: EVENER_PROVIDERS_CONFIG is empty, so there is no user layer to write", worked[0])
	}
	l, _, err := registry.ReadConfigFile(path)
	if err != nil {
		return err
	}
	entry := l.Providers[name]
	entry.ID = name
	entry.Protocol = worked[0]
	l.Providers[name] = entry
	if err := registry.WriteConfigFile(path, l); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "wrote protocol = %s to %s\n", worked[0], path)
	return nil
}

func runProvidersAdd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("providers add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "registry provider this instance is based on (required)")
	baseURL := fs.String("base-url", "", "endpoint base URL, including the version segment")
	protocol := fs.String("protocol", "", "wire protocol: openai-chat|openai-responses|anthropic|google")
	surface := fs.String("surface", "", "agent-facing surface: openai|anthropic|google|generic")
	apiKeyEnv := fs.String("api-key-env", "", "environment variable holding this instance's key")
	noProbe := fs.Bool("no-probe", false, "write the entry without probing the endpoint")
	var vars, credentialHeaders cmdutil.StringSliceFlag
	fs.Var(&vars, "var", "base-URL variable, K=V (repeatable)")
	fs.Var(&credentialHeaders, "credential-header", "credential header, K=$VARIABLE (repeatable)")
	name, err := parseNamedCommand(fs, args, "usage: evener providers add <name> --base <provider> [flags]")
	if err != nil {
		return err
	}
	if !registry.ValidInstanceName(name) {
		return fmt.Errorf("invalid instance name %q (lowercase, no slash)", name)
	}
	if strings.TrimSpace(*base) == "" {
		return errors.New("--base is required: every instance is based on a registry provider (`evener models list --all` names them)")
	}
	entry := registry.Provider{
		ID: name, Base: strings.TrimSpace(*base),
		Protocol: strings.TrimSpace(*protocol), Surface: strings.TrimSpace(*surface),
		Transport: registry.Transport{BaseURL: strings.TrimSpace(*baseURL)},
	}
	varMap, err := parseKeyValues(vars, "--var")
	if err != nil {
		return err
	}
	entry.Transport.Vars = varMap
	headers, err := parseCredentialHeaders(credentialHeaders)
	if err != nil {
		return err
	}
	entry.CredentialHeaders = headers
	if v := strings.TrimSpace(*apiKeyEnv); v != "" {
		entry.APIKeyEnv = []string{v}
	}

	r, _, err := loadCLIRegistryWithNotices(stderr)
	if err != nil {
		return err
	}
	if _, ok := r.Provider(entry.Base); !ok {
		return fmt.Errorf("unknown base provider %q (`evener models list --all` names them)", entry.Base)
	}
	path, noUserLayer := cmdutil.ProvidersConfigPath()
	if noUserLayer {
		return errors.New("cannot add an instance: EVENER_PROVIDERS_CONFIG is empty, so there is no user layer to write")
	}
	l, _, err := registry.ReadConfigFile(path)
	if err != nil {
		return err
	}
	if _, exists := l.Providers[name]; exists {
		return fmt.Errorf("instance %q already exists in %s", name, path)
	}
	l.Providers[name] = entry
	if err := registry.WriteConfigFile(path, l); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "wrote [providers.%s] to %s\n", name, path)

	reloaded, _, err := loadCLIRegistry()
	if err != nil {
		return err
	}
	res, err := reloaded.ResolveInstance(name)
	if err != nil {
		return err
	}
	for _, w := range res.Warnings {
		_, _ = fmt.Fprintf(stdout, "%s: %s\n", name, w)
	}
	if res.Credential.Source == "none" && res.Transport.Auth != registry.AuthNone && res.Transport.Auth != registry.AuthOptionalBearer {
		if credentialPointerHelps(entry, res.Transport.Auth) {
			_, _ = fmt.Fprintf(stdout, "no credential resolves for %s: set %s, add --api-key-env, or enter a key with the hub's credentials pane (credentials.toml [providers.%s]); not probing\n",
				name, registry.InstanceKeyEnvVar(name), name)
		} else {
			_, _ = fmt.Fprintf(stdout, "no credential resolves for %s (the warning above names what to set); not probing\n", name)
		}
		return nil
	}
	if *noProbe {
		return nil
	}
	if err := probeInstance(reloaded, name, true, stdout); err != nil {
		// The entry is on disk and reported; a probe that could not run is a
		// report, not a failed add. Returning it would tell a script the whole
		// command failed after providers.toml changed.
		_, _ = fmt.Fprintf(stderr, "probe skipped: %v\n", err)
	}
	return nil
}

// credentialPointerHelps reports whether "set <NAME>_API_KEY, add
// --api-key-env, or use the hub's credentials pane" would actually resolve
// this instance. It would not for an entry carrying its own api_key or
// credential_headers — resolution stops there and never reaches the store or
// the environment (spec §10) — nor for a scheme that authenticates some other
// way, whose own warning already names the real fix (spec §5.1, §9.5).
func credentialPointerHelps(entry registry.Provider, auth string) bool {
	if entry.APIKey != "" || len(entry.CredentialHeaders) > 0 {
		return false
	}
	switch auth {
	case registry.AuthBearer, registry.AuthOptionalBearer, registry.AuthHeader:
		return true
	default:
		return false
	}
}

// parseKeyValues splits repeated K=V flag values into a map.
func parseKeyValues(pairs []string, flagName string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		k, v, ok := strings.Cut(pair, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("%s expects KEY=VALUE", flagName)
		}
		out[k] = strings.TrimSpace(v)
	}
	return out, nil
}

// parseCredentialHeaders splits repeated K=V credential headers and enforces
// spec §11.2's boundary: the value carries $VARIABLE references, so no key is
// ever typed on a command line or written into providers.toml. The refusal
// never echoes the value it refused.
func parseCredentialHeaders(pairs []string) (map[string]string, error) {
	headers, err := parseKeyValues(pairs, "--credential-header")
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		if err := checkCredentialHeaderValue(v); err != nil {
			return nil, fmt.Errorf("--credential-header %s: %w (a value is an auth scheme word and $VARIABLE references, as in %s=Bearer $PORTKEY_KEY)", k, err, k)
		}
	}
	return headers, nil
}

// checkCredentialHeaderValue holds the command line's half of the secrets
// boundary: every whitespace-separated token of a credential header is either
// a run of $VARIABLE references or a bare auth scheme word, and at least one
// is a reference. That refuses both a value with no reference at all and a
// key smuggled beside one ("Bearer sk-live-abc$X"), which a bare "contains a
// $" check accepts. References are read with the registry's own grammar
// (spec §10), so this agrees with what the parser will make of the value.
//
// The rule is stricter than providers.toml itself, deliberately: an argv is
// world-readable and lands in shell history, so the file may hold shapes the
// command line will not author.
func checkCredentialHeaderValue(value string) error {
	referenced := false
	for token := range strings.FieldsSeq(value) {
		refs, literal, err := registry.ScanConfigValue(token)
		switch {
		case err != nil:
			return err
		case len(refs) == 0 && isAuthSchemeWord(token):
			// A scheme name carries no secret.
		case len(refs) > 0 && literal == "":
			referenced = true
		default:
			return errors.New("only an auth scheme word may be literal; the value itself must be a $VARIABLE reference, never a literal secret")
		}
	}
	if !referenced {
		return errors.New("the value must reference a $VARIABLE, never a literal secret")
	}
	return nil
}

// isAuthSchemeWord reports whether a literal token is an HTTP auth scheme
// name (Bearer, Basic, Token, ...). Letters only: a token carrying digits,
// dashes, or underscores has the shape of a key, and a key is never literal
// here.
func isAuthSchemeWord(token string) bool {
	if token == "" {
		return false
	}
	for i := range len(token) {
		c := token[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}
