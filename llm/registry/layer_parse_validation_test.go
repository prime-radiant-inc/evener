package registry

import (
	"strings"
	"testing"
)

// One table per parse-time refusal the vocabularies own. Each asserts the
// load fails; the refusal text is copy and stays unasserted.
func TestParseConfigTomlSyntaxError(t *testing.T) {
	t.Parallel()
	if _, err := ParseConfig([]byte("not = = toml")); err == nil {
		t.Fatal("ParseConfig accepted invalid TOML")
	}
}

func TestParseOverlayTransportRules(t *testing.T) {
	t.Parallel()
	t.Run("preset cannot name another preset", func(t *testing.T) {
		t.Parallel()
		if _, err := ParseOverlay(overlayWith("[transports.bad]\ntransport = \"other\"\n")); err == nil {
			t.Fatal("ParseOverlay accepted a preset naming another preset")
		}
	})
	t.Run("unknown auth", func(t *testing.T) {
		t.Parallel()
		if _, err := ParseOverlay(overlayWith("[transports.bad]\nauth = \"nope\"\n")); err == nil {
			t.Fatal("ParseOverlay accepted an unknown auth scheme")
		}
	})
}

func TestParseConfigTopLevelModelGlobs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		toml string
	}{
		{"protocol on a glob row", "[models.\"g*\"]\nprotocol = \"openai\"\n"},
		{"transport preset on a glob row", "[models.\"g*\"]\ntransport = \"x\"\n"},
		{"unknown surface on a glob row", "[models.\"g*\"]\nsurface = \"bogus\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseConfig([]byte(tc.toml)); err == nil {
				t.Fatalf("ParseConfig accepted %q", strings.TrimSpace(tc.toml))
			}
		})
	}
	t.Run("chat template kwargs are not unknown keys", func(t *testing.T) {
		t.Parallel()
		l, err := ParseConfig([]byte("[models.\"g*\"]\n[models.\"g*\".chat_template_kwargs]\nmarker = \"v\"\n"))
		if err != nil {
			t.Fatal(err)
		}
		row, ok := l.TopGlobs["g*"]
		if !ok {
			t.Fatal("glob row missing")
		}
		kwargs := row.Caps.ChatTemplateKwargs
		if kwargs == nil || kwargs["marker"] != "v" {
			t.Fatalf("chat_template_kwargs = %#v, want marker=v", row.Caps.ChatTemplateKwargs)
		}
	})
}

func TestParseConfigProviderAndModelValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		toml string
	}{
		{"provider header with malformed $ expression", "[providers.p]\nheaders = { H = \"${unclosed\" }\n"},
		{"model protocol unknown", "[providers.p.models.m]\nprotocol = \"bogus\"\n"},
		{"model surface unknown", "[providers.p.models.m]\nsurface = \"bogus\"\n"},
		{"model header with malformed $ expression", "[providers.p.models.m]\nheaders = { H = \"${unclosed\" }\n"},
		{"model transport var with malformed $ expression", "[providers.p.models.m]\nvars = { V = \"${unclosed\" }\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseConfig([]byte(tc.toml)); err == nil {
				t.Fatalf("ParseConfig accepted %q", strings.TrimSpace(tc.toml))
			}
		})
	}
}
