package appserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// A client announcing an older AppWire protocol is told to upgrade, with an
// error it can recognize by discriminant, naming both versions.
//
// v7 belongs in this list because it predates thread/model/set's required
// authority fields: such a daemon accepts the method, applies the pick and
// answers an empty success, so the pair must fail at the handshake instead of
// at a mutation the daemon could not fence.
func TestInitializeTellsAnOlderClientToUpgrade(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "evener serve", Version: "test", SourceID: "local"})
	for _, version := range []string{"evener-appwire-v5", "evener-appwire-v6", "evener-appwire-v7"} {
		t.Run(version, func(t *testing.T) {
			_, err := server.initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: version})
			var wireErr appwire.WireError
			if !errors.As(err, &wireErr) {
				t.Fatalf("initialize(%s) error = %v, want a wire error", version, err)
			}
			data, _ := wireErr.Data.(appwire.ErrorData)
			if wireErr.Code != appwire.CodeInvalidRequest || data.EvenerErrorInfo != appwire.ErrorUpgradeRequired {
				t.Fatalf("initialize(%s) = %+v, want invalid request with evenerErrorInfo %q", version, wireErr, appwire.ErrorUpgradeRequired)
			}
			for _, named := range []string{version, appwire.ProtocolVersion} {
				if !strings.Contains(wireErr.Message, named) {
					t.Fatalf("initialize(%s) message %q does not name %s", version, wireErr.Message, named)
				}
			}
		})
	}
}

func TestInitializeAcceptsProtocolV8(t *testing.T) {
	if appwire.ProtocolVersion != "evener-appwire-v8" {
		t.Fatalf("ProtocolVersion = %q, want evener-appwire-v8", appwire.ProtocolVersion)
	}
	server := NewServer(ServerConfig{ServerName: "evener serve", Version: "test", SourceID: "local"})
	response, err := server.initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: "evener-appwire-v8"})
	if err != nil {
		t.Fatalf("initialize(v8): %v", err)
	}
	if response.ProtocolVersion != "evener-appwire-v8" {
		t.Fatalf("initialize(v8) protocol = %q", response.ProtocolVersion)
	}
}

// A version this server does not know as older (missing, newer, or not an
// AppWire version at all) stays a plain incompatibility.
func TestInitializeRejectsAnUnknownVersionWithoutUpgradeRequired(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "evener serve", Version: "test", SourceID: "local"})
	for _, version := range []string{"", "wrong", "evener-appwire-v9", "evener-appwire-vx"} {
		_, err := server.initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: version})
		var wireErr appwire.WireError
		if !errors.As(err, &wireErr) {
			t.Fatalf("initialize(%q) error = %v, want a wire error", version, err)
		}
		if data, _ := wireErr.Data.(appwire.ErrorData); data.EvenerErrorInfo == appwire.ErrorUpgradeRequired {
			t.Fatalf("initialize(%q) = upgradeRequired, want a plain incompatibility", version)
		}
	}
}
