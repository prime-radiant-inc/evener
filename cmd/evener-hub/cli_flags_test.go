package hub

import (
	"errors"
	"fmt"
	"io"
	"testing"
)

// Every hub command line tells a named --config apart from the implicit
// default path, and the commands that take one more locating flag also report
// whether it was given. The five parsers must agree on that.
func TestHubCommandLinesReportWhichFlagsWereSet(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		args         []string
		wantConfig   bool
		wantLocating bool
	}{
		{name: "neither", args: nil},
		{name: "config only", args: []string{"--config", "/x/hub.toml"}, wantConfig: true},
		{name: "locating only", args: nil, wantLocating: true},
		{name: "both", args: []string{"--config", "/x/hub.toml"}, wantConfig: true, wantLocating: true},
	}
	parsers := []struct {
		name      string
		locating  string // flag name, empty when the command has none that is tracked
		parse     func(args []string) (config, locating bool, err error)
		stdioFlag []string
	}{
		{
			name: "hub", locating: "--addr",
			parse: func(args []string) (bool, bool, error) {
				o, err := parseHubOptions(args, io.Discard)
				return o.configExplicit, o.addrPresent, err
			},
		},
		{
			name: "attach", locating: "--hub-url", stdioFlag: []string{"--stdio"},
			parse: func(args []string) (bool, bool, error) {
				o, err := parseAttachOptions(args, io.Discard)
				return o.configExplicit, o.urlPresent, err
			},
		},
		{
			name: "restart",
			parse: func(args []string) (bool, bool, error) {
				o, err := parseRestartOptions(args, io.Discard)
				return o.configExplicit, false, err
			},
		},
	}
	locatingValue := map[string]string{"--addr": "127.0.0.1:1", "--hub-url": "http://127.0.0.1:1"}
	for _, p := range parsers {
		for _, tc := range cases {
			if p.locating == "" && tc.wantLocating {
				continue
			}
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				args := append([]string{}, p.stdioFlag...)
				args = append(args, tc.args...)
				if tc.wantLocating {
					args = append(args, p.locating, locatingValue[p.locating])
				}
				config, locating, err := p.parse(args)
				if err != nil {
					t.Fatalf("parse %v: %v", args, err)
				}
				if config != tc.wantConfig || locating != tc.wantLocating {
					t.Fatalf("parse %v: config=%v locating=%v, want %v %v", args, config, locating, tc.wantConfig, tc.wantLocating)
				}
			})
		}
	}
}

// endpoint and login-url hand the loader whether --config was named.
func TestEndpointCommandsReportWhetherConfigWasNamed(t *testing.T) {
	t.Parallel()
	errStop := errors.New("stop after load")
	run := map[string]func(args []string, deps mainDeps) error{
		"endpoint": func(args []string, deps mainDeps) error {
			return runEndpoint(append([]string{"--json"}, args...), io.Discard, io.Discard, deps)
		},
		"login-url": func(args []string, deps mainDeps) error {
			return runLoginURL(args, io.Discard, io.Discard, deps)
		},
	}
	for name, call := range run {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/explicit=%v", name, explicit), func(t *testing.T) {
				t.Parallel()
				var gotExplicit bool
				var loaded bool
				deps := defaultMainDeps()
				deps.loadConfig = func(_ string, explicit bool) (Config, error) {
					loaded, gotExplicit = true, explicit
					return Config{}, errStop
				}
				var args []string
				if explicit {
					args = []string{"--config", "/x/hub.toml"}
				}
				_ = call(args, deps)
				if !loaded {
					t.Fatal("the command never loaded its config")
				}
				if gotExplicit != explicit {
					t.Fatalf("config explicit = %v, want %v", gotExplicit, explicit)
				}
			})
		}
	}
}
