package launchconfig

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
)

type LaunchResolveResultMsg struct {
	Resolved appwire.LaunchConfigResolved
	Err      error
}
type LaunchLayerResultMsg struct {
	Layer string
	CWD   string
	Data  appwire.LaunchConfigLayer
	Err   error
}
type LaunchSetLayerResultMsg struct {
	Layer    string
	CWD      string
	Resolved appwire.LaunchConfigResolved
	Err      error
}
type LaunchTrustResultMsg struct {
	CWD      string
	Resolved appwire.LaunchConfigResolved
	Err      error
}
type LaunchSchemaResultMsg struct {
	Schema appwire.LaunchOptionSchemaResponse
	Err    error
}
type AuthListResultMsg struct {
	List appwire.AuthListResponse
	Err  error
}
type authStatusResultMsg struct {
	Status appwire.AuthStatusResponse
	Err    error
}
type AuthApiKeySetResultMsg struct {
	Status appwire.AuthStatusResponse
	Err    error
}
type AuthLoginStartResultMsg struct {
	Provider string
	URL      string
	FlowID   string
	Err      error
}

func CmdResolveLaunch(client *appwire.Client, cwd string, overrides *appwire.LaunchConfigLayer) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.LaunchConfigResolved
		err := client.Request(ctx, appwire.MethodSerfLaunchResolve, appwire.LaunchConfigResolveParams{CWD: cwd, LaunchOverrides: overrides}, &resp)
		return LaunchResolveResultMsg{Resolved: resp, Err: err}
	}
}

func CmdGetLayer(client *appwire.Client, cwd, layer string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.LaunchConfigLayer
		err := client.Request(ctx, appwire.MethodSerfLaunchGetLayer, appwire.LaunchConfigGetLayerParams{CWD: cwd, Layer: layer}, &resp)
		return LaunchLayerResultMsg{Layer: layer, CWD: cwd, Data: resp, Err: err}
	}
}

func CmdSetLayer(client *appwire.Client, cwd, layer string, config appwire.LaunchConfigLayer) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.LaunchConfigResolved
		err := client.Request(ctx, appwire.MethodSerfLaunchSetLayer, appwire.LaunchConfigSetLayerParams{CWD: cwd, Layer: layer, Config: config}, &resp)
		return LaunchSetLayerResultMsg{Layer: layer, CWD: cwd, Resolved: resp, Err: err}
	}
}

func CmdTrustRepo(client *appwire.Client, cwd, hash string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.LaunchConfigResolved
		err := client.Request(ctx, appwire.MethodSerfLaunchTrustRepo, appwire.LaunchConfigTrustRepoParams{CWD: cwd, Hash: hash}, &resp)
		return LaunchTrustResultMsg{CWD: cwd, Resolved: resp, Err: err}
	}
}

func CmdLaunchSchema(client *appwire.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.LaunchOptionSchemaResponse
		err := client.Request(ctx, appwire.MethodSerfLaunchSchema, appwire.EmptyParams{}, &resp)
		return LaunchSchemaResultMsg{Schema: resp, Err: err}
	}
}

func cmdAuthList(client *appwire.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.AuthListResponse
		err := client.Request(ctx, appwire.MethodSerfAuthList, appwire.EmptyParams{}, &resp)
		return AuthListResultMsg{List: resp, Err: err}
	}
}

func CmdAuthApiKeySet(client *appwire.Client, provider, value string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.AuthStatusResponse
		err := client.Request(ctx, appwire.MethodSerfAuthApiKeySet, appwire.AuthApiKeySetParams{Provider: provider, Value: value}, &resp)
		return AuthApiKeySetResultMsg{Status: resp, Err: err}
	}
}

func CmdAuthLogout(client *appwire.Client, provider string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.AuthLogoutResponse
		err := client.Request(ctx, appwire.MethodSerfAuthLogout, appwire.AuthLogoutParams{Provider: provider}, &resp)
		return AuthApiKeySetResultMsg{Status: resp.Status, Err: err}
	}
}

func CmdAuthLoginStart(client *appwire.Client, provider string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.AuthLoginStartResponse
		err := client.Request(ctx, appwire.MethodSerfAuthLoginStart, appwire.AuthLoginStartParams{Provider: provider}, &resp)
		return AuthLoginStartResultMsg{Provider: provider, URL: resp.URL, FlowID: resp.FlowID, Err: err}
	}
}

type AuthLoginCompleteResultMsg struct {
	Provider string
	Status   appwire.AuthStatusResponse
	Err      error
}

type InstanceListResultMsg struct {
	List appwire.InstanceListResponse
	Err  error
}

type InstanceSetDefaultMsg struct {
	Name string
}

type InstanceRemoveMsg struct {
	Name string
}

type InstanceMutateResultMsg struct {
	List appwire.InstanceListResponse
	Err  error
}

func CmdInstanceList(client *appwire.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.InstanceListResponse
		err := client.Request(ctx, appwire.MethodSerfInstanceList, appwire.EmptyParams{}, &resp)
		return InstanceListResultMsg{List: resp, Err: err}
	}
}

func CmdInstanceCreate(client *appwire.Client, params appwire.InstanceCreateParams) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.InstanceListResponse
		err := client.Request(ctx, appwire.MethodSerfInstanceCreate, params, &resp)
		return InstanceMutateResultMsg{List: resp, Err: err}
	}
}

func CmdInstanceEdit(client *appwire.Client, params appwire.InstanceEditParams) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.InstanceListResponse
		err := client.Request(ctx, appwire.MethodSerfInstanceEdit, params, &resp)
		return InstanceMutateResultMsg{List: resp, Err: err}
	}
}

func CmdInstanceRemove(client *appwire.Client, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.InstanceListResponse
		err := client.Request(ctx, appwire.MethodSerfInstanceRemove, appwire.InstanceRemoveParams{Name: name}, &resp)
		return InstanceMutateResultMsg{List: resp, Err: err}
	}
}

func CmdInstanceSetDefault(client *appwire.Client, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.InstanceListResponse
		err := client.Request(ctx, appwire.MethodSerfInstanceSetDefault, appwire.InstanceSetDefaultParams{Name: name}, &resp)
		return InstanceMutateResultMsg{List: resp, Err: err}
	}
}

func CmdAuthLoginComplete(client *appwire.Client, provider, flowID, redirectURL string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var resp appwire.AuthLoginCompleteResponse
		err := client.Request(ctx, appwire.MethodSerfAuthLoginComplete, appwire.AuthLoginCompleteParams{Provider: provider, FlowID: flowID, RedirectURL: redirectURL}, &resp)
		return AuthLoginCompleteResultMsg{Provider: provider, Status: resp.Status, Err: err}
	}
}
