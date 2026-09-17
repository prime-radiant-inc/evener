package launchconfig

import (
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
)

// TestLaunchConfigCommandsCallTheirWireMethodAndCarryFailureIntoTheirMessage
// covers every tea.Cmd constructor in launchconfig_client.go at the transport
// boundary. TestCmdAuthTestUsesSharedMethodAndInstanceName next door proves one
// command's params and success decoding in depth; this proves the two
// properties that must hold for ALL of them.
//
// Each constructor is a one-line binding of a UI action to an appwire method,
// and both ways it can be wrong are silent. A mistyped or copy-pasted method
// name still compiles and still returns a well-formed message; the hub simply
// never does what the user asked. And a request error dropped instead of
// carried into the message's Err field leaves the panel rendering a success it
// never got.
//
// The fake stops at the transport, so the real appwire.Client encodes, matches
// the response to the request ID, and decodes on the way back.
func TestLaunchConfigCommandsCallTheirWireMethodAndCarryFailureIntoTheirMessage(t *testing.T) {
	const (
		cwd      = "/repo"
		provider = "anthropic"
	)

	for _, tc := range []struct {
		name       string
		wantMethod string
		run        func(c *appwire.Client) any
		errOf      func(msg any) error
	}{
		{"resolve launch", appwire.MethodEvenerLaunchResolve,
			func(c *appwire.Client) any { return CmdResolveLaunch(c, cwd, nil)() },
			func(m any) error { return m.(LaunchResolveResultMsg).Err }},
		{"get layer", appwire.MethodEvenerLaunchGetLayer,
			func(c *appwire.Client) any { return CmdGetLayer(c, cwd, "project")() },
			func(m any) error { return m.(LaunchLayerResultMsg).Err }},
		{"set layer", appwire.MethodEvenerLaunchSetLayer,
			func(c *appwire.Client) any { return CmdSetLayer(c, cwd, "project", appwire.LaunchConfigLayer{})() },
			func(m any) error { return m.(LaunchSetLayerResultMsg).Err }},
		{"trust repo", appwire.MethodEvenerLaunchTrustRepo,
			func(c *appwire.Client) any { return CmdTrustRepo(c, cwd, "hash")() }, nil},
		{"launch schema", appwire.MethodEvenerLaunchSchema,
			func(c *appwire.Client) any { return CmdLaunchSchema(c)() }, nil},
		{"auth api key set", appwire.MethodEvenerAuthApiKeySet,
			func(c *appwire.Client) any { return CmdAuthApiKeySet(c, provider, "sk-test")() },
			func(m any) error { return m.(AuthApiKeySetResultMsg).Err }},
		{"auth logout", appwire.MethodEvenerAuthLogout,
			func(c *appwire.Client) any { return CmdAuthLogout(c, provider)() }, nil},
		{"auth login start", appwire.MethodEvenerAuthLoginStart,
			func(c *appwire.Client) any { return CmdAuthLoginStart(c, provider)() },
			func(m any) error { return m.(AuthLoginStartResultMsg).Err }},
		{"auth test", appwire.MethodEvenerAuthTest,
			func(c *appwire.Client) any { return CmdAuthTest(c, provider, 1)() },
			func(m any) error { return m.(AuthTestResultMsg).Err }},
		{"auth login complete", appwire.MethodEvenerAuthLoginComplete,
			func(c *appwire.Client) any {
				return CmdAuthLoginComplete(c, provider, "flow", "http://localhost/cb")()
			},
			func(m any) error { return m.(AuthLoginCompleteResultMsg).Err }},
		{"instance list", appwire.MethodEvenerInstanceList,
			func(c *appwire.Client) any { return CmdInstanceList(c)() },
			func(m any) error { return m.(InstanceListResultMsg).Err }},
		{"instance create", appwire.MethodEvenerInstanceCreate,
			func(c *appwire.Client) any { return CmdInstanceCreate(c, appwire.InstanceCreateParams{})() },
			func(m any) error { return m.(InstanceMutateResultMsg).Err }},
		{"instance edit", appwire.MethodEvenerInstanceEdit,
			func(c *appwire.Client) any { return CmdInstanceEdit(c, appwire.InstanceEditParams{})() },
			func(m any) error { return m.(InstanceMutateResultMsg).Err }},
		{"instance remove", appwire.MethodEvenerInstanceRemove,
			func(c *appwire.Client) any { return CmdInstanceRemove(c, "inst")() },
			func(m any) error { return m.(InstanceMutateResultMsg).Err }},
		{"instance set default", appwire.MethodEvenerInstanceSetDefault,
			func(c *appwire.Client) any { return CmdInstanceSetDefault(c, "inst")() },
			func(m any) error { return m.(InstanceMutateResultMsg).Err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := appwiretest.NewScriptedTransport()
			client := appwire.NewClient(transport)
			client.Start(t.Context())

			gotMethod := make(chan string, 1)
			go func() {
				req := <-transport.Sent()
				gotMethod <- req.Request.Method
				// Failing the request drives the Err mapping and reads the method
				// off the same exchange.
				transport.DeliverError(req.Request.ID, -32000, "hub refused")
			}()

			msg := tc.run(client)

			select {
			case method := <-gotMethod:
				if method != tc.wantMethod {
					t.Fatalf("sent method %q, want %q", method, tc.wantMethod)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("command issued no request")
			}

			if msg == nil {
				t.Fatal("command produced no message")
			}
			if tc.errOf != nil {
				if err := tc.errOf(msg); err == nil {
					t.Fatalf("message %T carried no error despite a failed request", msg)
				}
			}
		})
	}
}
