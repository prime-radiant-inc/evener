package main

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/hubapi"
)

type hubTreeMsg struct {
	tree hubapi.TreeResponse
	err  error
}

type hubSessionMsg struct {
	detail hubapi.SessionDetail
	err    error
}

type hubStreamStartedMsg struct {
	ch     <-chan tea.Msg
	cancel context.CancelFunc
}

type hubStreamMsg struct {
	ch  <-chan tea.Msg
	msg tea.Msg
}

type hubStreamClosedMsg struct{}

func fetchHubTree(client *hubapi.Client) tea.Cmd {
	return func() tea.Msg {
		tree, err := client.Tree(context.Background())
		return hubTreeMsg{tree: tree, err: err}
	}
}

func fetchHubSession(client *hubapi.Client, ref hubapi.Ref) tea.Cmd {
	return func() tea.Msg {
		detail, err := client.Session(context.Background(), ref)
		return hubSessionMsg{detail: detail, err: err}
	}
}

func startHubStream(streamURL, lastEventID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		ch := make(chan tea.Msg)
		go func() {
			defer close(ch)
			streamSSEURL(ctx, streamURL, lastEventID, func(msg tea.Msg) {
				select {
				case <-ctx.Done():
				case ch <- msg:
				}
			})
		}()
		return hubStreamStartedMsg{ch: ch, cancel: cancel}
	}
}

func waitHubStream(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return hubStreamClosedMsg{}
		}
		return hubStreamMsg{ch: ch, msg: msg}
	}
}
