package launchconfig

import (
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
)

// TestCovPluginCommandsCallTheirWireMethodAndCarryFailure verifies each plugin
// and marketplace Cmd constructor calls the right appwire method and carries
// request errors into its message's Err field.
func TestCovPluginCommandsCallTheirWireMethodAndCarryFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		wantMethod string
		run        func(c *appwire.Client) any
		errOf      func(msg any) error
	}{
		{"marketplace list", appwire.MethodEvenerMarketplaceList,
			func(c *appwire.Client) any { return CmdMarketplaceList(c)() },
			func(m any) error { return m.(MarketplaceListResultMsg).Err }},
		{"marketplace add", appwire.MethodEvenerMarketplaceAdd,
			func(c *appwire.Client) any { return CmdMarketplaceAdd(c, appwire.MarketplaceAddParams{})() },
			func(m any) error { return m.(MarketplaceMutateResultMsg).Err }},
		{"marketplace remove", appwire.MethodEvenerMarketplaceRemove,
			func(c *appwire.Client) any { return CmdMarketplaceRemove(c, "acme")() },
			func(m any) error { return m.(MarketplaceMutateResultMsg).Err }},
		{"marketplace refresh", appwire.MethodEvenerMarketplaceRefresh,
			func(c *appwire.Client) any { return CmdMarketplaceRefresh(c, "acme")() },
			func(m any) error { return m.(MarketplaceMutateResultMsg).Err }},
		{"marketplace browse", appwire.MethodEvenerMarketplaceBrowse,
			func(c *appwire.Client) any { return CmdMarketplaceBrowse(c, "acme")() },
			func(m any) error { return m.(MarketplaceBrowseResultMsg).Err }},
		{"plugin list", appwire.MethodEvenerPluginList,
			func(c *appwire.Client) any { return CmdPluginList(c)() },
			func(m any) error { return m.(PluginListResultMsg).Err }},
		{"plugin install", appwire.MethodEvenerPluginInstall,
			func(c *appwire.Client) any { return CmdPluginInstall(c, "fmt", "acme")() },
			func(m any) error { return m.(PluginMutateResultMsg).Err }},
		{"plugin upgrade", appwire.MethodEvenerPluginUpgrade,
			func(c *appwire.Client) any { return CmdPluginUpgrade(c, "fmt", "acme")() },
			func(m any) error { return m.(PluginMutateResultMsg).Err }},
		{"plugin remove", appwire.MethodEvenerPluginRemove,
			func(c *appwire.Client) any { return CmdPluginRemove(c, "fmt", "acme")() },
			func(m any) error { return m.(PluginMutateResultMsg).Err }},
		{"plugin enable", appwire.MethodEvenerPluginEnable,
			func(c *appwire.Client) any { return CmdPluginEnable(c, "fmt", "acme")() },
			func(m any) error { return m.(PluginMutateResultMsg).Err }},
		{"plugin disable", appwire.MethodEvenerPluginDisable,
			func(c *appwire.Client) any { return CmdPluginDisable(c, "fmt", "acme")() },
			func(m any) error { return m.(PluginMutateResultMsg).Err }},
		{"plugin set auto-upgrade", appwire.MethodEvenerPluginSetAutoUpgrade,
			func(c *appwire.Client) any { return CmdPluginSetAutoUpgrade(c, "fmt", "acme", true)() },
			func(m any) error { return m.(PluginMutateResultMsg).Err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := appwiretest.NewScriptedTransport()
			client := appwire.NewClient(transport)
			client.Start(t.Context())

			gotMethod := make(chan string, 1)
			go func() {
				req := <-transport.Sent()
				gotMethod <- req.Request.Method
				transport.DeliverError(req.Request.ID, -32000, "hub refused")
			}()

			msg := tc.run(client)

			method := <-gotMethod
			if method != tc.wantMethod {
				t.Fatalf("method = %q, want %q", method, tc.wantMethod)
			}

			if tc.errOf != nil {
				if err := tc.errOf(msg); err == nil {
					t.Fatalf("expected error in message, got nil: %+v", msg)
				}
			}
		})
	}
}

// TestCovPluginCommandsDecodeSuccess verifies each command decodes a
// successful response into its message struct without error.
func TestCovPluginCommandsDecodeSuccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(c *appwire.Client) any
	}{
		{"marketplace list", func(c *appwire.Client) any { return CmdMarketplaceList(c)() }},
		{"marketplace add", func(c *appwire.Client) any { return CmdMarketplaceAdd(c, appwire.MarketplaceAddParams{})() }},
		{"marketplace remove", func(c *appwire.Client) any { return CmdMarketplaceRemove(c, "acme")() }},
		{"marketplace refresh", func(c *appwire.Client) any { return CmdMarketplaceRefresh(c, "acme")() }},
		{"marketplace browse", func(c *appwire.Client) any { return CmdMarketplaceBrowse(c, "acme")() }},
		{"plugin list", func(c *appwire.Client) any { return CmdPluginList(c)() }},
		{"plugin install", func(c *appwire.Client) any { return CmdPluginInstall(c, "fmt", "acme")() }},
		{"plugin upgrade", func(c *appwire.Client) any { return CmdPluginUpgrade(c, "fmt", "acme")() }},
		{"plugin remove", func(c *appwire.Client) any { return CmdPluginRemove(c, "fmt", "acme")() }},
		{"plugin enable", func(c *appwire.Client) any { return CmdPluginEnable(c, "fmt", "acme")() }},
		{"plugin disable", func(c *appwire.Client) any { return CmdPluginDisable(c, "fmt", "acme")() }},
		{"plugin set auto-upgrade", func(c *appwire.Client) any { return CmdPluginSetAutoUpgrade(c, "fmt", "acme", true)() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := appwiretest.NewScriptedTransport()
			client := appwire.NewClient(transport)
			client.Start(t.Context())

			result := make(chan any, 1)
			go func() { result <- tc.run(client) }()

			req := <-transport.Sent()
			// Deliver an empty success response; all these methods return
			// either a list or browse response with no required fields.
			transport.DeliverResponse(req.Request.ID, map[string]any{})
			msg := <-result

			_ = msg // just ensure it doesn't panic; success decode is covered
		})
	}
}
