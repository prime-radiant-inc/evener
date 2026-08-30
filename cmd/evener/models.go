package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

// modelsRefreshFetcher fetches models.dev for `evener models refresh`; tests
// replace it so the command never touches the network.
var modelsRefreshFetcher = registry.HTTPFetcher(&http.Client{Timeout: 60 * time.Second})

func printModelsUsage(w io.Writer) {
	_, _ = fmt.Fprintf(w, "Usage: evener models <list|inspect|refresh> [flags]\n\n")
	_, _ = fmt.Fprintf(w, "Inspect the provider registry (docs/superpowers/specs/2026-08-28-provider-registry-design.md §11.1).\n\n")
	_, _ = fmt.Fprintf(w, "Commands:\n")
	_, _ = fmt.Fprintf(w, "  list [--provider X] [--all]   Resolved rows: protocol, surface, context, output cap, cost, effort ladder, warnings\n")
	_, _ = fmt.Fprintf(w, "  inspect <instance/model>      The full Resolved record as JSON, with provenance, pruned fields, and the request skeleton\n")
	_, _ = fmt.Fprintf(w, "  refresh [--force]             Fetch models.dev into the runtime cache now\n")
}

// runModels dispatches `evener models` (spec §11.1).
func runModels(args []string, _ io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printModelsUsage(stderr)
		return nil
	}
	switch args[0] {
	case "list":
		return runModelsList(args[1:], stdout, stderr)
	case "inspect":
		return runModelsInspect(args[1:], stdout, stderr)
	case "refresh":
		return runModelsRefresh(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printModelsUsage(stderr)
		return nil
	default:
		printModelsUsage(stderr)
		return fmt.Errorf("unknown models command %q", args[0])
	}
}

// providersPath is the providers.toml the registry reads:
// EVENER_PROVIDERS_CONFIG when it names one, else <config-root>/providers.toml.
func providersPath() string {
	if p, ok := envvars.EVENERProvidersConfig.LookupEnv(); ok && strings.TrimSpace(p) != "" {
		return p
	}
	return filepath.Join(cmdutil.DefaultConfigRoot(), "providers.toml")
}

// credentialsPath is credentials.toml's location: the sibling of the
// providers.toml in use.
func credentialsPath() string {
	return filepath.Join(filepath.Dir(providersPath()), "credentials.toml")
}

// modelsLoadOptions are extra registry options every `evener models` load
// appends; tests set it to inject a catalog fixture.
var modelsLoadOptions []registry.Option

// loadRegistryForCLI loads the registry with the credentials store. During
// steps 1–2 an old-schema providers.toml is ignored with a note (spec §14).
func loadRegistryForCLI(stderr io.Writer) (*registry.Registry, error) {
	store, err := credentials.LoadStore(credentialsPath())
	if err != nil {
		return nil, fmt.Errorf("credentials: %w", err)
	}
	// list and inspect never fetch: `evener models refresh` is the explicit
	// path to the network (spec §6.4, §11.1).
	opts := []registry.Option{registry.WithCredentials(cmdutil.StoreCredentialSource{Store: store}), registry.WithStateRoot(cmdutil.DefaultStateRoot()), registry.WithOffline(true)}
	opts = append(opts, modelsLoadOptions...)
	r, err := registry.Load(opts...)
	if errors.Is(err, registry.ErrOldSchema) {
		_, _ = fmt.Fprintf(stderr, "note: %s uses the pre-registry providers.toml schema; ignored until the cut-over\n", providersPath())
		r, err = registry.Load(append(opts, registry.WithNoUserLayer())...)
	}
	if err != nil {
		return nil, err
	}
	for _, w := range r.Warnings() {
		_, _ = fmt.Fprintln(stderr, "warning:", w)
	}
	return r, nil
}

func runModelsList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("models list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	provider := fs.String("provider", "", "list one instance or registry provider")
	all := fs.Bool("all", false, "include hidden providers, hidden rows, and rows without tool calling")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	r, err := loadRegistryForCLI(stderr)
	if err != nil {
		return err
	}
	ranked := r.Instances()
	instances := make(map[string]registry.Instance, len(ranked))
	for _, inst := range ranked {
		instances[inst.Name] = inst
	}
	var names []string
	switch {
	case *provider != "":
		names = []string{*provider}
	case *all:
		names = r.ProviderIDs()
		seen := map[string]bool{}
		for _, id := range names {
			seen[id] = true
		}
		for _, inst := range ranked {
			if !seen[inst.Name] {
				names = append(names, inst.Name)
			}
		}
		sort.Strings(names)
	default:
		for _, inst := range ranked {
			names = append(names, inst.Name)
		}
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "REF\tPROTOCOL\tSURFACE\tCONTEXT\tOUTPUT\tCOST $/M\tEFFORT\tNOTES")
	for _, name := range names {
		// An instance carries its own hidden flag: its vars may resolve a base
		// URL the curated record of the same name cannot. Names that are not
		// instances list off the curated record instead, which needs no
		// credential and no [providers.<id>] entry (spec §11.1).
		inst, isInstance := instances[name]
		hidden, protocol := inst.Hidden, inst.Protocol
		listIDs, resolve := r.ModelIDs, func(id string) (registry.Resolved, error) { return r.Resolve(name + "/" + id) }
		if !isInstance {
			p, ok := r.Provider(name)
			hidden, protocol = ok && p.Hidden, p.Protocol
			listIDs, resolve = r.CatalogModelIDs, func(id string) (registry.Resolved, error) { return r.ResolveCatalog(name, id) }
		}
		if hidden {
			if *all {
				_, _ = fmt.Fprintf(tw, "%s/\t%s\t\t\t\t\t\tneeds base_url (hidden)\n", name, protocol)
			}
			continue
		}
		ids, err := listIDs(name)
		if err != nil {
			_ = tw.Flush() // keep the rows already buffered
			return err
		}
		for _, id := range ids {
			res, err := resolve(id)
			if err != nil {
				_, _ = fmt.Fprintf(tw, "%s/%s\t\t\t\t\t\t\terror: %v\n", name, id, err)
				continue
			}
			if !*all && (res.Model.Hidden || (res.Caps.Tools != nil && !*res.Caps.Tools)) {
				continue
			}
			cost := ""
			if res.Caps.Cost != nil {
				cost = fmt.Sprintf("%g/%g", res.Caps.Cost.Input, res.Caps.Cost.Output)
			}
			_, _ = fmt.Fprintf(tw, "%s/%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", name, id, res.Protocol, res.Surface,
				intOrDash(res.Caps.ContextWindow), intOrDash(res.Caps.MaxOutputTokens), cost,
				strings.Join(res.Caps.EffortValues, ","), strings.Join(res.Warnings, "; "))
		}
	}
	return tw.Flush()
}

func intOrDash(v *int) string {
	if v == nil {
		return "-"
	}
	return strconv.Itoa(*v)
}

// inspectView is what `evener models inspect` prints: the Resolved record
// plus the credential source, the pruned-field list, and the request
// skeleton (spec §11.1). Secrets never appear.
type inspectView struct {
	registry.Resolved
	CredentialSource string         `json:"credential_source"`
	PrunedFields     []string       `json:"pruned_fields"`
	Request          inspectRequest `json:"request"`
}

// maskedHeaders are the plain `headers` entries inspect masks: a literal
// credential is a credential wherever providers.toml wrote it.
var maskedHeaders = map[string]bool{"Authorization": true, "X-Api-Key": true, "Api-Key": true}

type inspectRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Auth    string            `json:"auth"`
	Headers map[string]string `json:"headers"`
}

func runModelsInspect(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("models inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: evener models inspect <instance/model>")
	}
	r, err := loadRegistryForCLI(stderr)
	if err != nil {
		return err
	}
	res, err := r.Resolve(fs.Arg(0))
	if err != nil {
		return err
	}
	var pruned []string
	for k, v := range res.Caps.Fields {
		if !v {
			pruned = append(pruned, k)
		}
	}
	sort.Strings(pruned)
	// A literal Authorization or x-api-key in `headers` is a credential
	// wherever providers.toml wrote it, so the record and the request
	// skeleton both carry it masked.
	headers := map[string]string{}
	for k, v := range res.Headers {
		if maskedHeaders[http.CanonicalHeaderKey(k)] {
			v = "***"
		}
		headers[k] = v
	}
	res.Headers = headers
	reqHeaders := map[string]string{}
	maps.Copy(reqHeaders, headers)
	for k := range res.CredentialHeaders {
		reqHeaders[k] = "***"
	}
	view := inspectView{
		Resolved: res, CredentialSource: res.Credential.Source, PrunedFields: pruned,
		Request: inspectRequest{
			Method:  "POST",
			URL:     res.Transport.BaseURL + strings.ReplaceAll(res.Transport.Endpoint, "{model}", res.WireID),
			Auth:    res.Transport.Auth,
			Headers: reqHeaders,
		},
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(view)
}

func runModelsRefresh(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("models refresh", flag.ContinueOnError)
	fs.SetOutput(stderr)
	force := fs.Bool("force", false, "fetch even when the cache is under 24h old")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := registry.Refresh(ctx, registry.RefreshOptions{StateRoot: cmdutil.DefaultStateRoot(), Fetcher: modelsRefreshFetcher, Force: *force})
	if err != nil {
		return err
	}
	switch {
	case res.Skipped:
		_, _ = fmt.Fprintf(stdout, "cache is fresh (under 24h); pass --force to fetch anyway: %s\n", res.Path)
	case res.NotModified:
		_, _ = fmt.Fprintf(stdout, "not modified upstream (etag %s); cache kept: %s\n", res.Etag, res.Path)
	default:
		_, _ = fmt.Fprintf(stdout, "updated %s: providers %d -> %d, models %d -> %d (etag %s)\n", res.Path, res.ProvidersBefore, res.ProvidersAfter, res.ModelsBefore, res.ModelsAfter, res.Etag)
	}
	return nil
}
