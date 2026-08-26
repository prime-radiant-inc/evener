package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestPluginPreviewControllerMapsCandidates(t *testing.T) {
	ctl := newHubPluginsController(t.TempDir(), t.TempDir())
	if _, err := ctl.Preview(context.Background(), appwire.PluginPreviewParams{CWD: "relative"}); err == nil {
		t.Fatal("Preview accepted invalid cwd")
	}
	got, err := ctl.Preview(context.Background(), appwire.PluginPreviewParams{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Plugins == nil || got.Diagnostics == nil || got.SelectionErrors == nil {
		t.Fatalf("preview slices must be non-nil: %+v", got)
	}
}
