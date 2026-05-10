package main

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/agent"
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

type hubSendMsg struct {
	text string
	err  error
}

type hubTasksMsg struct {
	tasks []agent.Task
	err   error
}

type hubActionMsg struct {
	action string
	err    error
}

type hubClearMsg struct {
	resp hubapi.RefResponse
	err  error
}

type hubForkMsg struct {
	resp hubapi.RefResponse
	err  error
}

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

func sendHubInput(client *hubapi.Client, ref hubapi.Ref, text string) tea.Cmd {
	return func() tea.Msg {
		return hubSendMsg{text: text, err: client.Send(context.Background(), ref, text)}
	}
}

func fetchHubTasks(client *hubapi.Client, ref hubapi.Ref) tea.Cmd {
	return func() tea.Msg {
		tasks, err := client.Tasks(context.Background(), ref)
		return hubTasksMsg{tasks: tasks, err: err}
	}
}

func sendHubAction(client *hubapi.Client, ref hubapi.Ref, action string) tea.Cmd {
	return func() tea.Msg {
		var err error
		switch action {
		case "interrupt":
			err = client.Interrupt(context.Background(), ref)
		case "compact":
			err = client.Compact(context.Background(), ref)
		default:
			err = client.SetModel(context.Background(), ref, action)
			action = "model"
		}
		return hubActionMsg{action: action, err: err}
	}
}

func sendHubClear(client *hubapi.Client, ref hubapi.Ref) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.Clear(context.Background(), ref)
		return hubClearMsg{resp: resp, err: err}
	}
}

func sendHubFork(client *hubapi.Client, ref hubapi.Ref, req hubapi.ForkRequest) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.Fork(context.Background(), ref, req)
		return hubForkMsg{resp: resp, err: err}
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
