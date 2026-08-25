package launchconfig

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
)

type pluginCommandCase struct {
	name            string
	wantMethod      string
	wantParams      string
	run             func(*appwire.Client) any
	errOf           func(any) error
	successResponse any
	wantSuccess     any
}

func pluginCommandCases() []pluginCommandCase {
	marketplaceList := appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{{
		Name:            "acme",
		Source:          appwire.MarketplaceSourceInput{Kind: "url", URL: "https://example.test/catalog.git"},
		InstallLocation: "/marketplaces/acme",
		LastUpdated:     42,
	}}}
	browseResponse := appwire.MarketplaceBrowseResponse{
		Name:        "acme",
		Description: "Acme tools",
		Plugins:     []appwire.MarketplaceCatalogPlugin{{Name: "fmt", Description: "Formatter", Category: "dev"}},
	}
	pluginList := appwire.PluginListResponse{Plugins: []appwire.PluginEntry{{
		Plugin:      "fmt",
		Marketplace: "acme",
		Version:     "1.2.3",
		Enabled:     true,
		AutoUpgrade: true,
	}}}
	addParams := appwire.MarketplaceAddParams{
		Name:   "team",
		Source: appwire.MarketplaceSourceInput{Kind: "url", URL: "https://example.test/catalog.git"},
	}
	return []pluginCommandCase{
		{
			name: "marketplace list", wantMethod: appwire.MethodEvenerMarketplaceList, wantParams: `{}`,
			run:             func(c *appwire.Client) any { return CmdMarketplaceList(c)() },
			errOf:           func(m any) error { return m.(MarketplaceListResultMsg).Err },
			successResponse: marketplaceList,
			wantSuccess:     MarketplaceListResultMsg{List: marketplaceList},
		},
		{
			name: "marketplace add", wantMethod: appwire.MethodEvenerMarketplaceAdd,
			wantParams:      `{"name":"team","source":{"kind":"url","url":"https://example.test/catalog.git"}}`,
			run:             func(c *appwire.Client) any { return CmdMarketplaceAdd(c, addParams)() },
			errOf:           func(m any) error { return m.(MarketplaceMutateResultMsg).Err },
			successResponse: marketplaceList,
			wantSuccess:     MarketplaceMutateResultMsg{List: marketplaceList},
		},
		{
			name: "marketplace remove", wantMethod: appwire.MethodEvenerMarketplaceRemove, wantParams: `{"name":"acme"}`,
			run:             func(c *appwire.Client) any { return CmdMarketplaceRemove(c, "acme")() },
			errOf:           func(m any) error { return m.(MarketplaceMutateResultMsg).Err },
			successResponse: marketplaceList,
			wantSuccess:     MarketplaceMutateResultMsg{List: marketplaceList},
		},
		{
			name: "marketplace refresh", wantMethod: appwire.MethodEvenerMarketplaceRefresh, wantParams: `{"name":"acme"}`,
			run:             func(c *appwire.Client) any { return CmdMarketplaceRefresh(c, "acme")() },
			errOf:           func(m any) error { return m.(MarketplaceMutateResultMsg).Err },
			successResponse: marketplaceList,
			wantSuccess:     MarketplaceMutateResultMsg{List: marketplaceList},
		},
		{
			name: "marketplace browse", wantMethod: appwire.MethodEvenerMarketplaceBrowse, wantParams: `{"name":"acme"}`,
			run:             func(c *appwire.Client) any { return CmdMarketplaceBrowse(c, "acme")() },
			errOf:           func(m any) error { return m.(MarketplaceBrowseResultMsg).Err },
			successResponse: browseResponse,
			wantSuccess:     MarketplaceBrowseResultMsg{Name: "acme", Response: browseResponse},
		},
		{
			name: "plugin list", wantMethod: appwire.MethodEvenerPluginList, wantParams: `{}`,
			run:             func(c *appwire.Client) any { return CmdPluginList(c)() },
			errOf:           func(m any) error { return m.(PluginListResultMsg).Err },
			successResponse: pluginList,
			wantSuccess:     PluginListResultMsg{List: pluginList},
		},
		{
			name: "plugin install", wantMethod: appwire.MethodEvenerPluginInstall, wantParams: `{"plugin":"fmt","marketplace":"acme"}`,
			run:             func(c *appwire.Client) any { return CmdPluginInstall(c, "fmt", "acme")() },
			errOf:           func(m any) error { return m.(PluginMutateResultMsg).Err },
			successResponse: pluginList,
			wantSuccess:     PluginMutateResultMsg{List: pluginList},
		},
		{
			name: "plugin upgrade", wantMethod: appwire.MethodEvenerPluginUpgrade, wantParams: `{"plugin":"fmt","marketplace":"acme"}`,
			run:             func(c *appwire.Client) any { return CmdPluginUpgrade(c, "fmt", "acme")() },
			errOf:           func(m any) error { return m.(PluginMutateResultMsg).Err },
			successResponse: pluginList,
			wantSuccess:     PluginMutateResultMsg{List: pluginList},
		},
		{
			name: "plugin remove", wantMethod: appwire.MethodEvenerPluginRemove, wantParams: `{"plugin":"fmt","marketplace":"acme"}`,
			run:             func(c *appwire.Client) any { return CmdPluginRemove(c, "fmt", "acme")() },
			errOf:           func(m any) error { return m.(PluginMutateResultMsg).Err },
			successResponse: pluginList,
			wantSuccess:     PluginMutateResultMsg{List: pluginList},
		},
		{
			name: "plugin enable", wantMethod: appwire.MethodEvenerPluginEnable, wantParams: `{"plugin":"fmt","marketplace":"acme"}`,
			run:             func(c *appwire.Client) any { return CmdPluginEnable(c, "fmt", "acme")() },
			errOf:           func(m any) error { return m.(PluginMutateResultMsg).Err },
			successResponse: pluginList,
			wantSuccess:     PluginMutateResultMsg{List: pluginList},
		},
		{
			name: "plugin disable", wantMethod: appwire.MethodEvenerPluginDisable, wantParams: `{"plugin":"fmt","marketplace":"acme"}`,
			run:             func(c *appwire.Client) any { return CmdPluginDisable(c, "fmt", "acme")() },
			errOf:           func(m any) error { return m.(PluginMutateResultMsg).Err },
			successResponse: pluginList,
			wantSuccess:     PluginMutateResultMsg{List: pluginList},
		},
		{
			name: "plugin set auto-upgrade", wantMethod: appwire.MethodEvenerPluginSetAutoUpgrade,
			wantParams:      `{"plugin":"fmt","marketplace":"acme","autoUpgrade":true}`,
			run:             func(c *appwire.Client) any { return CmdPluginSetAutoUpgrade(c, "fmt", "acme", true)() },
			errOf:           func(m any) error { return m.(PluginMutateResultMsg).Err },
			successResponse: pluginList,
			wantSuccess:     PluginMutateResultMsg{List: pluginList},
		},
	}
}

// TestCovPluginCommandsCallTheirWireMethodAndCarryFailure verifies the request
// payload at the appwire transport boundary and the exact error returned to the
// Bubble Tea update loop.
func TestCovPluginCommandsCallTheirWireMethodAndCarryFailure(t *testing.T) {
	for _, tc := range pluginCommandCases() {
		t.Run(tc.name, func(t *testing.T) {
			transport := appwiretest.NewScriptedTransport()
			client := appwire.NewClient(transport)
			client.Start(t.Context())
			t.Cleanup(func() { _ = client.Close() })

			result := make(chan any, 1)
			go func() { result <- tc.run(client) }()
			req := <-transport.Sent()
			if req.Request.Method != tc.wantMethod {
				t.Fatalf("method = %q, want %q", req.Request.Method, tc.wantMethod)
			}
			if got := string(req.Request.Params); got != tc.wantParams {
				t.Fatalf("params = %s, want %s", got, tc.wantParams)
			}
			transport.DeliverError(req.Request.ID, -32000, "hub refused")

			msg := <-result
			wantErr := "appwire " + tc.wantMethod + ": hub refused"
			if err := tc.errOf(msg); err == nil || err.Error() != wantErr {
				t.Fatalf("message error = %v, want %q; message=%+v", err, wantErr, msg)
			}
		})
	}
}

// TestCovPluginCommandsDecodeSuccess verifies that every command preserves the
// complete response payload in the concrete message consumed by the panel.
func TestCovPluginCommandsDecodeSuccess(t *testing.T) {
	for _, tc := range pluginCommandCases() {
		t.Run(tc.name, func(t *testing.T) {
			transport := appwiretest.NewScriptedTransport()
			client := appwire.NewClient(transport)
			client.Start(t.Context())
			t.Cleanup(func() { _ = client.Close() })

			result := make(chan any, 1)
			go func() { result <- tc.run(client) }()
			req := <-transport.Sent()
			if req.Request.Method != tc.wantMethod || string(req.Request.Params) != tc.wantParams {
				t.Fatalf("request = %s %s, want %s %s", req.Request.Method, req.Request.Params, tc.wantMethod, tc.wantParams)
			}
			transport.DeliverResponse(req.Request.ID, tc.successResponse)
			msg := <-result
			if !reflect.DeepEqual(msg, tc.wantSuccess) {
				t.Fatalf("message = %#v, want %#v", msg, tc.wantSuccess)
			}
		})
	}
}
