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

// storeCredentialSource exposes the credentials.toml file layer to the
// registry (spec §10: store entries are looked up by instance name only).
type storeCredentialSource struct{ store *credentials.Store }

func (s storeCredentialSource) Lookup(name string) (string, bool) {
	if s.store == nil {
		return "", false
	}
	if hasFile, _ := s.store.Layers(name); !hasFile {
		return "", false
	}
	v, _ := s.store.Get(name)
	return v, v != ""
}

// credentialsPath is credentials.toml's location: the sibling of the
// providers.toml in use, else <config-root>/credentials.toml.
func credentialsPath() string {
	if p, ok := envvars.EVENERProvidersConfig.LookupEnv(); ok && strings.TrimSpace(p) != "" {
		return filepath.Join(filepath.Dir(p), "credentials.toml")
	}
	return filepath.Join(cmdutil.DefaultConfigRoot(), "credentials.toml")
}

// loadRegistryForCLI loads the registry with the credentials store. During
// steps 1–2 an old-schema providers.toml is ignored with a note (spec §14).
func loadRegistryForCLI(stderr io.Writer) (*registry.Registry, error) {
	store, err := credentials.LoadStore(credentialsPath())
	if err != nil {
		return nil, fmt.Errorf("credentials: %w", err)
	}
	opts := []registry.Option{registry.WithCredentials(storeCredentialSource{store}), registry.WithStateRoot(cmdutil.DefaultStateRoot())}
	r, err := registry.Load(opts...)
	if errors.Is(err, registry.ErrOldSchema) {
		_, _ = fmt.Fprintf(stderr, "note: %v; ignored until the cut-over\n", err)
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
		for _, inst := range r.Instances() {
			if !seen[inst.Name] {
				names = append(names, inst.Name)
			}
		}
		sort.Strings(names)
	default:
		for _, inst := range r.Instances() {
			names = append(names, inst.Name)
		}
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "REF\tPROTOCOL\tSURFACE\tCONTEXT\tOUTPUT\tCOST $/M\tEFFORT\tNOTES")
	for _, name := range names {
		if p, ok := r.Provider(name); ok && p.Hidden {
			if *all {
				_, _ = fmt.Fprintf(tw, "%s/\t%s\t\t\t\t\t\tneeds base_url (hidden)\n", name, p.Protocol)
			}
			continue
		}
		ids, err := r.ModelIDs(name)
		if err != nil {
			return err
		}
		for _, id := range ids {
			res, err := r.Resolve(name + "/" + id)
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
	headers := map[string]string{}
	maps.Copy(headers, res.Headers)
	for k := range res.CredentialHeaders {
		headers[k] = "***"
	}
	view := inspectView{
		Resolved: res, CredentialSource: res.Credential.Source, PrunedFields: pruned,
		Request: inspectRequest{
			Method:  "POST",
			URL:     res.Transport.BaseURL + strings.ReplaceAll(res.Transport.Endpoint, "{model}", res.WireID),
			Auth:    res.Transport.Auth,
			Headers: headers,
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
