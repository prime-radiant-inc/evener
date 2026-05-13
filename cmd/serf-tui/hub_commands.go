package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appwire"
)

type hubTreeMsg struct {
	tree hubTreeResponse
	err  error
}

type hubSessionMsg struct {
	detail   hubSessionDetail
	messages []chatMessage
	err      error
}

type hubNotificationMsg struct {
	notification appwire.Notification
	ok           bool
}

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
	resp hubRefResponse
	err  error
}

type hubForkMsg struct {
	resp hubRefResponse
	err  error
}

type hubSpawnMsg struct {
	resp hubSpawnResponse
	err  error
}

type hubModelsMsg struct {
	models []modelPickerItem
	err    error
}

func fetchHubTree(client *appwire.Client) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ThreadList(context.Background(), appwire.ThreadListParams{IncludeSubagents: true})
		if err != nil {
			return hubTreeMsg{err: err}
		}
		return hubTreeMsg{tree: hubTreeFromThreads(resp.Data)}
	}
}

func fetchHubSession(client *appwire.Client, ref appwire.Ref) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: ref.String(), IncludeTurns: true, ItemsView: "full"})
		if err != nil {
			return hubSessionMsg{err: err}
		}
		return hubSessionMsg{detail: hubDetailFromThread(resp.Thread), messages: messagesFromThread(resp.Thread)}
	}
}

func sendHubSpawn(client *appwire.Client, req hubSpawnRequest) tea.Cmd {
	return func() tea.Msg {
		provider, model := splitProviderModel(req.Model)
		resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
			CWD:           req.WorkingDir,
			Prompt:        req.Task,
			ModelProvider: provider,
			Model:         model,
		})
		return hubSpawnMsg{resp: hubSpawnResponse{Ref: resp.Thread.Serf.Ref}, err: err}
	}
}

func fetchHubModels(client *appwire.Client) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ModelList(context.Background(), appwire.ModelListParams{})
		if err != nil {
			return hubModelsMsg{err: err}
		}
		items := make([]modelPickerItem, 0, len(resp.Data))
		for _, option := range resp.Data {
			if option.Provider == "" || option.Model == "" {
				continue
			}
			id := option.Provider + "/" + option.Model
			items = append(items, modelPickerItem{id: id, display: id})
		}
		return hubModelsMsg{models: items}
	}
}

func sendHubInput(client *appwire.Client, ref appwire.Ref, text string) tea.Cmd {
	return func() tea.Msg {
		_, err := client.TurnStart(context.Background(), appwire.TurnStartParams{Ref: ref.String(), Prompt: text})
		return hubSendMsg{text: text, err: err}
	}
}

func fetchHubTasks(client *appwire.Client, ref appwire.Ref) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.TasksList(context.Background(), appwire.TaskListParams{Ref: ref.String()})
		if err != nil {
			return hubTasksMsg{err: err}
		}
		var tasks []agent.Task
		data, _ := json.Marshal(resp.Data)
		if len(data) > 0 {
			_ = json.Unmarshal(data, &tasks)
		}
		return hubTasksMsg{tasks: tasks}
	}
}

func sendHubAction(client *appwire.Client, ref appwire.Ref, action string) tea.Cmd {
	return func() tea.Msg {
		var err error
		switch action {
		case "interrupt":
			err = client.TurnInterrupt(context.Background(), appwire.TurnInterruptParams{Ref: ref.String()})
		case "compact":
			err = client.ThreadCompactStart(context.Background(), appwire.ThreadCompactStartParams{Ref: ref.String()})
		default:
			provider, model := splitProviderModel(action)
			err = client.ThreadModelSet(context.Background(), appwire.ThreadModelSetParams{Ref: ref.String(), ModelProvider: provider, Model: model})
			action = "model"
		}
		return hubActionMsg{action: action, err: err}
	}
}

func sendHubClear(client *appwire.Client, ref appwire.Ref) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ThreadClear(context.Background(), appwire.ThreadClearParams{Ref: ref.String()})
		return hubClearMsg{resp: hubRefResponse{Ref: resp.Ref}, err: err}
	}
}

func sendHubFork(client *appwire.Client, ref appwire.Ref, req hubForkRequest) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{
			Ref:          ref.String(),
			SourceTurnID: fmt.Sprint(req.Turn),
			EditedInput:  req.EditedMessage,
			Label:        req.Label,
		})
		return hubForkMsg{resp: hubRefResponse{Ref: resp.Thread.Serf.Ref}, err: err}
	}
}

func waitHubNotification(client *appwire.Client) tea.Cmd {
	return func() tea.Msg {
		notification, ok := <-client.Notifications()
		return hubNotificationMsg{notification: notification, ok: ok}
	}
}

func splitProviderModel(raw string) (string, string) {
	provider, model, ok := strings.Cut(strings.TrimSpace(raw), "/")
	if !ok {
		return "", strings.TrimSpace(raw)
	}
	return provider, model
}
