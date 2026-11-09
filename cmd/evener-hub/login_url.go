package hub

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/internal/hubendpoint"
)

var (
	ErrLoginURLUnavailable = errors.New("hub login URL unavailable")
	ErrLoginURLToken       = errors.New("hub login token unavailable")
)

// runLoginURL deliberately discloses one login link for a verified running
// instance. Unlike server startup, stdout redirection is an explicit choice.
func runLoginURL(args []string, stdout, stderr io.Writer, deps mainDeps) error {
	opts := endpointOptions{configPath: DefaultConfigPath()}
	fs := flag.NewFlagSet("evener hub login-url", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.configPath, "config", opts.configPath, "hub.toml path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("login-url accepts only --config PATH")
	}
	opts.configExplicit = setFlagNames(fs)["config"]
	report, err := resolveHubEndpoint(context.Background(), opts, deps)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] login-url: %v\n", err)
		return err
	}
	if report.State != "running" || report.Running == nil {
		_, _ = fmt.Fprintln(stderr, "[hub] no verified running instance for login-url")
		return ErrLoginURLUnavailable
	}
	token, err := readAuthToken(report.Running.HubStateRoot)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "[hub] cannot read running hub token")
		return ErrLoginURLToken
	}
	link, err := browserLoginURL(*report.Running, hubHostname, token)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "[hub] running hub has no valid advertised origin")
		return fmt.Errorf("%w: invalid running origin", ErrLoginURLUnavailable)
	}
	_, err = fmt.Fprintln(stdout, link)
	return err
}

func browserLoginOrigin(record hubendpoint.Record, hostname func() (string, error)) (string, error) {
	raw := record.PublicURL
	if raw == "" {
		raw = "http://" + advertisedHubHost(record.BoundAddr, hostname)
	}
	origin, err := hubendpoint.ParseOrigin(raw)
	if err != nil {
		return "", fmt.Errorf("invalid running hub origin: %w", err)
	}
	return origin.String(), nil
}

func browserLoginURL(record hubendpoint.Record, hostname func() (string, error), token string) (string, error) {
	origin, err := browserLoginOrigin(record, hostname)
	if err != nil {
		return "", err
	}
	return hubedge.AuthURLFor(origin, token), nil
}

func isTerminalWriter(w io.Writer) bool {
	file, ok := w.(*os.File)
	return ok && file != nil && term.IsTerminal(int(file.Fd()))
}
