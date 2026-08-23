package main

import (
	"testing"

	"primeradiant.com/evener/internal/appserver"
)

// TestNotifyLaunchUpdated covers notifyLaunchUpdated by verifying it does not
// panic with no connections.
func TestNotifyLaunchUpdated(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{})
	notifyLaunchUpdated(server, "/test/cwd", "user")
}

// TestNotifyMarketplaceUpdated covers notifyMarketplaceUpdated.
func TestNotifyMarketplaceUpdated(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{})
	notifyMarketplaceUpdated(server)
}

// TestNotifyPluginUpdated covers notifyPluginUpdated.
func TestNotifyPluginUpdated(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{})
	notifyPluginUpdated(server)
}
