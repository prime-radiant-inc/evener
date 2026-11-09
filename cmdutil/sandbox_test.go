package cmdutil

import (
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
)

// TestParseSandboxNet: on/off/empty map correctly (case- and space-insensitive);
// anything else errors.
func TestParseSandboxNet(t *testing.T) {
	cases := []struct {
		in   string
		want bool
		err  bool
	}{
		{"on", true, false},
		{"off", false, false},
		{"", true, false},
		{"ON", true, false},
		{" off ", false, false},
		{"yes", false, true},
	}
	for _, tc := range cases {
		got, err := ParseSandboxNet(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("ParseSandboxNet(%q) should error", tc.in)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseSandboxNet(%q) = %v, %v; want %v, nil", tc.in, got, err, tc.want)
		}
	}
}

// ProvisionSandbox picks the session's ID itself, for the off short-circuit as
// much as for an enforced mode, so every caller hands the sandbox a session ID
// to mint its scratch under.
func TestProvisionSandboxPicksAFreshSessionIDBeforeProvisioning(t *testing.T) {
	cfg := agent.SessionConfig{}
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	if err := ProvisionSandbox(env, &cfg, env.WorkingDirectory(), nil); err != nil {
		t.Fatalf("ProvisionSandbox: %v", err)
	}
	if cfg.SessionID == "" {
		t.Fatal("ProvisionSandbox left the session without an ID")
	}
}
