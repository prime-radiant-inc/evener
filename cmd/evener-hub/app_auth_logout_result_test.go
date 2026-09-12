package hub

import (
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
)

func TestAuth_NonCodexLogoutReportsActualStoredRemoval(t *testing.T) {
	for _, tc := range []struct {
		name       string
		env        map[string]string
		stored     bool
		wantRemove bool
		wantSource string
	}{
		{name: "absent", env: map[string]string{}, wantSource: "none"},
		{name: "environment only", env: map[string]string{"WORK_ANT_KEY": "env-key"}, wantSource: "env:WORK_ANT_KEY"},
		{name: "stored", env: map[string]string{}, stored: true, wantRemove: true, wantSource: "none"},
		{name: "stored shadowed by environment", env: map[string]string{"WORK_ANT_KEY": "env-key"}, stored: true, wantRemove: true, wantSource: "env:WORK_ANT_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oaitest.IsolateOpenAIAuth(t)
			dir := t.TempDir()
			c := newTestAuthController(t, dir, filepath.Join(dir, "state"), writeProvidersToml(t, dir, bearerInstanceToml), tc.env)
			if tc.stored {
				if err := c.creds.Set("work-ant", "stored-key"); err != nil {
					t.Fatalf("Set: %v", err)
				}
				if err := c.reg.Reload(); err != nil {
					t.Fatalf("Reload: %v", err)
				}
			}
			before, err := c.Status(appwire.AuthStatusParams{Provider: "work-ant"})
			if err != nil {
				t.Fatalf("Status before logout: %v", err)
			}
			resp, err := c.Logout(appwire.AuthLogoutParams{Provider: "work-ant"})
			if err != nil {
				t.Fatalf("Logout: %v", err)
			}
			if resp.Removed != tc.wantRemove {
				t.Fatalf("Removed=%v, want %v (before=%+v)", resp.Removed, tc.wantRemove, before)
			}
			if resp.Status.ActiveSource != tc.wantSource {
				t.Fatalf("source after logout=%q, want %q", resp.Status.ActiveSource, tc.wantSource)
			}
		})
	}
}
