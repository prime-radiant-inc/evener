package main

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

type launchResolveResultMsg struct {
	Resolved appwire.LaunchConfigResolved
	Err      error
}
type launchLayerResultMsg struct {
	Layer string
	CWD   string
	Data  appwire.LaunchConfigLayer
	Err   error
}
type launchSetLayerResultMsg struct {
	Layer    string
	CWD      string
	Resolved appwire.LaunchConfigResolved
	Err      error
}
type launchTrustResultMsg struct {
	CWD      string
	Resolved appwire.LaunchConfigResolved
	Err      error
}
type authListResultMsg struct {
	List appwire.AuthListResponse
	Err  error
}
type authStatusResultMsg struct {
	Status appwire.AuthStatusResponse
	Err    error
}
type authApiKeySetResultMsg struct {
	Status appwire.AuthStatusResponse
	Err    error
}
type authLoginStartResultMsg struct {
	Provider string
	URL      string
	FlowID   string
	Err      error
}

func cmdResolveLaunch(client *appwire.Client, cwd string, overrides *appwire.LaunchConfigLayer) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.LaunchConfigResolved
		err := client.Request(ctx, appwire.MethodSerfLaunchResolve, appwire.LaunchConfigResolveParams{CWD: cwd, LaunchOverrides: overrides}, &resp)
		return launchResolveResultMsg{Resolved: resp, Err: err}
	}
}

func cmdGetLayer(client *appwire.Client, cwd, layer string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.LaunchConfigLayer
		err := client.Request(ctx, appwire.MethodSerfLaunchGetLayer, appwire.LaunchConfigGetLayerParams{CWD: cwd, Layer: layer}, &resp)
		return launchLayerResultMsg{Layer: layer, CWD: cwd, Data: resp, Err: err}
	}
}

func cmdSetLayer(client *appwire.Client, cwd, layer string, config appwire.LaunchConfigLayer) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.LaunchConfigResolved
		err := client.Request(ctx, appwire.MethodSerfLaunchSetLayer, appwire.LaunchConfigSetLayerParams{CWD: cwd, Layer: layer, Config: config}, &resp)
		return launchSetLayerResultMsg{Layer: layer, CWD: cwd, Resolved: resp, Err: err}
	}
}

func cmdTrustRepo(client *appwire.Client, cwd, hash string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.LaunchConfigResolved
		err := client.Request(ctx, appwire.MethodSerfLaunchTrustRepo, appwire.LaunchConfigTrustRepoParams{CWD: cwd, Hash: hash}, &resp)
		return launchTrustResultMsg{CWD: cwd, Resolved: resp, Err: err}
	}
}

func cmdAuthList(client *appwire.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.AuthListResponse
		err := client.Request(ctx, appwire.MethodSerfAuthList, appwire.EmptyParams{}, &resp)
		return authListResultMsg{List: resp, Err: err}
	}
}

func cmdAuthApiKeySet(client *appwire.Client, provider, value string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.AuthStatusResponse
		err := client.Request(ctx, appwire.MethodSerfAuthApiKeySet, appwire.AuthApiKeySetParams{Provider: provider, Value: value}, &resp)
		return authApiKeySetResultMsg{Status: resp, Err: err}
	}
}

func cmdAuthLogout(client *appwire.Client, provider string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.AuthLogoutResponse
		err := client.Request(ctx, appwire.MethodSerfAuthLogout, appwire.AuthLogoutParams{Provider: provider}, &resp)
		return authApiKeySetResultMsg{Status: resp.Status, Err: err}
	}
}

func cmdAuthLoginStart(client *appwire.Client, provider string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var resp appwire.AuthLoginStartResponse
		err := client.Request(ctx, appwire.MethodSerfAuthLoginStart, appwire.AuthLoginStartParams{Provider: provider}, &resp)
		return authLoginStartResultMsg{Provider: provider, URL: resp.URL, FlowID: resp.FlowID, Err: err}
	}
}
