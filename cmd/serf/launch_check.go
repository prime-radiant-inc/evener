package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"primeradiant.com/serf/buildinfo"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/internal/appwire"
)

type launchCheckResponse struct {
	Version  string `json:"version"`
	Protocol string `json:"protocol"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

func runLaunchCheck(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("launch-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	model := fs.String("model", "", "provider/model to validate")
	protocol := fs.String("protocol", appwire.ProtocolVersion, "required appwire protocol")
	jsonOut := fs.Bool("json", false, "write machine-readable launch contract")
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
	if strings.TrimSpace(*model) != "" {
		ref, err := cmdutil.ParseModelRef(*model)
		if err != nil {
			return err
		}
		if _, err := cmdutil.SelectProfile(ref.Provider, ref.Model, ""); err != nil {
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
