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
func TestInitializeTellsAnOlderClientToUpgrade(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "evener serve", Version: "test", SourceID: "local"})
	_, err := server.initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: "evener-appwire-v5"})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("initialize(v5) error = %v, want a wire error", err)
	}
	data, _ := wireErr.Data.(appwire.ErrorData)
	if wireErr.Code != appwire.CodeInvalidRequest || data.EvenerErrorInfo != appwire.ErrorUpgradeRequired {
		t.Fatalf("initialize(v5) = %+v, want invalid request with evenerErrorInfo %q", wireErr, appwire.ErrorUpgradeRequired)
	}
	for _, version := range []string{"evener-appwire-v5", appwire.ProtocolVersion} {
		if !strings.Contains(wireErr.Message, version) {
			t.Fatalf("initialize(v5) message %q does not name %s", wireErr.Message, version)
		}
	}
}

func TestInitializeAcceptsProtocolV6(t *testing.T) {
	if appwire.ProtocolVersion != "evener-appwire-v6" {
		t.Fatalf("ProtocolVersion = %q, want evener-appwire-v6", appwire.ProtocolVersion)
	}
	server := NewServer(ServerConfig{ServerName: "evener serve", Version: "test", SourceID: "local"})
	response, err := server.initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: "evener-appwire-v6"})
	if err != nil {
		t.Fatalf("initialize(v6): %v", err)
	}
	if response.ProtocolVersion != "evener-appwire-v6" {
		t.Fatalf("initialize(v6) protocol = %q", response.ProtocolVersion)
	}
}

// A version this server does not know as older (missing, newer, or not an
// AppWire version at all) stays a plain incompatibility.
func TestInitializeRejectsAnUnknownVersionWithoutUpgradeRequired(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "evener serve", Version: "test", SourceID: "local"})
	for _, version := range []string{"", "wrong", "evener-appwire-v7", "evener-appwire-vx"} {
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
