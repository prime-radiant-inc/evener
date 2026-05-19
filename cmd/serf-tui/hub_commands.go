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
	detail               hubSessionDetail
	messages             []chatMessage
	ref                  string
	expectedState        string
	expectedRefreshToken int
	err                  error
}

type hubNotificationMsg struct {
	notification appwire.Notification
	ok           bool
}

type hubSendMsg struct {
	text                    string
	draft                   string
	turnID                  string
	trackedAttachmentSubmit bool
	submittedAttachments    []*PastedImage
	err                     error
}

type hubTasksMsg struct {
	tasks []agent.Task
	err   error
}

type hubStatusMsg struct {
	detail  hubSessionDetail
	tasks   []agent.Task
	auth    appwire.AuthStatusResponse
	taskErr error
	authErr error
	err     error
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
	harness string
	models  []modelPickerItem
	err     error
}

type hubSessionModelsMsg struct {
	models []modelPickerItem
	err    error
}

type hubSpawnOptionsMsg struct {
	harnesses                   []string
	harnessKinds                map[string]string
	emptyTaskUnsupportedReasons map[string]string
	emptyTaskUnsupportedNext    map[string]string
	models                      []modelPickerItem
	err                         error
	modelErr                    error
}

type hubAuthStatusMsg struct {
	status appwire.AuthStatusResponse
	err    error
}

type hubAuthLoginStartMsg struct {
	resp appwire.AuthLoginStartResponse
	err  error
}

type hubAuthLoginCompleteMsg struct {
	resp appwire.AuthLoginCompleteResponse
	err  error
}

type hubAuthLogoutMsg struct {
	resp appwire.AuthLogoutResponse
	err  error
}

type hubTranscriptTargetsMsg struct {
	targets []appwire.ThreadTranscriptTarget
	err     error
}

type hubTranscriptMsg struct {
	target   appwire.ThreadTranscriptTarget
	messages []chatMessage
	err      error
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
	return fetchHubSessionExpectingState(client, ref, "")
}

func fetchHubSessionExpectingState(client *appwire.Client, ref appwire.Ref, expectedState string) tea.Cmd {
	return fetchHubSessionExpectingStateToken(client, ref, expectedState, 0)
}

func fetchHubSessionExpectingStateToken(client *appwire.Client, ref appwire.Ref, expectedState string, expectedRefreshToken int) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: ref.String(), IncludeTurns: true, ItemsView: "full", Subscribe: true, ReplaceSubscription: true})
		if err != nil {
			return hubSessionMsg{ref: ref.String(), expectedState: expectedState, expectedRefreshToken: expectedRefreshToken, err: err}
		}
		return hubSessionMsg{detail: hubDetailFromThread(resp.Thread), messages: messagesFromThread(resp.Thread), expectedState: expectedState, expectedRefreshToken: expectedRefreshToken}
	}
}

func fetchHubStatus(client *appwire.Client, ref appwire.Ref) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: ref.String(), IncludeTurns: true, ItemsView: "full"})
		if err != nil {
			return hubStatusMsg{err: err}
		}
		detail := hubDetailFromThread(resp.Thread)
		tasks, taskErr := fetchHubTasksSync(context.Background(), client, ref)
		auth, authErr := client.AuthStatus(context.Background(), appwire.AuthStatusParams{Provider: authProviderForStatus(detail)})
		return hubStatusMsg{detail: detail, tasks: tasks, auth: auth, taskErr: taskErr, authErr: authErr}
	}
}

func fetchHubTranscriptTargets(client *appwire.Client, ref appwire.Ref) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ThreadTranscriptList(context.Background(), appwire.ThreadTranscriptListParams{Ref: ref.String()})
		if err != nil {
			return hubTranscriptTargetsMsg{err: err}
		}
		return hubTranscriptTargetsMsg{targets: resp.Data}
	}
}

func fetchHubTranscript(client *appwire.Client, target appwire.ThreadTranscriptTarget) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: target.Ref, IncludeTurns: true, ItemsView: "full"})
		if err != nil {
			return hubTranscriptMsg{target: target, err: err}
		}
		return hubTranscriptMsg{target: target, messages: messagesFromThread(resp.Thread)}
	}
}

func sendHubSpawn(client *appwire.Client, req hubSpawnRequest) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
			Harness:         req.Harness,
			CWD:             req.WorkingDir,
			Prompt:          req.Prompt,
			Model:           strings.TrimSpace(req.Model),
			LaunchOverrides: req.LaunchOverrides,
		})
		return hubSpawnMsg{resp: hubSpawnResponse{Ref: resp.Thread.Serf.Ref}, err: err}
	}
}

func fetchHubModels(client *appwire.Client, workingDir string) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ModelList(context.Background(), appwire.ModelListParams{CWD: strings.TrimSpace(workingDir)})
		if err != nil {
			return hubModelsMsg{err: err}
		}
		return hubModelsMsg{models: modelPickerItemsFromResponse(resp, false)}
	}
}

func fetchHubModelsForHarness(client *appwire.Client, harness string, workingDir string) tea.Cmd {
	harness = strings.TrimSpace(harness)
	workingDir = strings.TrimSpace(workingDir)
	return func() tea.Msg {
		resp, err := client.ModelList(context.Background(), appwire.ModelListParams{Harness: harness, CWD: workingDir})
		if err != nil {
			return hubModelsMsg{harness: harness, err: err}
		}
		return hubModelsMsg{harness: harness, models: modelPickerItemsFromResponse(resp, harness != "")}
	}
}

func fetchHubSessionModels(client *appwire.Client, workingDir string) tea.Cmd {
	workingDir = strings.TrimSpace(workingDir)
	return func() tea.Msg {
		resp, err := client.ModelList(context.Background(), appwire.ModelListParams{CWD: workingDir})
		if err != nil {
			return hubSessionModelsMsg{err: err}
		}
		return hubSessionModelsMsg{models: modelPickerItemsFromResponse(resp, false)}
	}
}

func fetchHubSpawnOptions(client *appwire.Client, workingDir string) tea.Cmd {
	workingDir = strings.TrimSpace(workingDir)
	return func() tea.Msg {
		harnessResp, err := client.HarnessList(context.Background(), appwire.HarnessListParams{})
		if err != nil {
			return hubSpawnOptionsMsg{err: err}
		}
		harnesses := make([]string, 0, len(harnessResp.Data))
		harnessKinds := map[string]string{}
		emptyTaskUnsupportedReasons := map[string]string{}
		emptyTaskUnsupportedNext := map[string]string{}
		for _, option := range harnessResp.Data {
			if option.ID == "" {
				continue
			}
			harnesses = append(harnesses, option.ID)
			kind := strings.TrimSpace(option.Kind)
			if kind == "" {
				kind = "serf"
			}
			harnessKinds[option.ID] = kind
			if reason := strings.TrimSpace(option.EmptyTaskUnsupportedReason); reason != "" {
				emptyTaskUnsupportedReasons[option.ID] = reason
			}
			if next := strings.TrimSpace(option.EmptyTaskUnsupportedNextAction); next != "" {
				emptyTaskUnsupportedNext[option.ID] = next
			}
		}
		modelResp, err := client.ModelList(context.Background(), appwire.ModelListParams{CWD: workingDir})
		if err != nil {
			return hubSpawnOptionsMsg{harnesses: harnesses, harnessKinds: harnessKinds, emptyTaskUnsupportedReasons: emptyTaskUnsupportedReasons, emptyTaskUnsupportedNext: emptyTaskUnsupportedNext, modelErr: err}
		}
		models := modelPickerItemsFromResponse(modelResp, false)
		return hubSpawnOptionsMsg{harnesses: harnesses, harnessKinds: harnessKinds, emptyTaskUnsupportedReasons: emptyTaskUnsupportedReasons, emptyTaskUnsupportedNext: emptyTaskUnsupportedNext, models: models}
	}
}

func fetchHubAuthStatus(client *appwire.Client, provider string) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.AuthStatus(context.Background(), appwire.AuthStatusParams{Provider: provider})
		return hubAuthStatusMsg{status: resp, err: err}
	}
}

func startHubAuthLogin(client *appwire.Client, provider string) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.AuthLoginStart(context.Background(), appwire.AuthLoginStartParams{Provider: provider})
		return hubAuthLoginStartMsg{resp: resp, err: err}
	}
}

func completeHubAuthLogin(client *appwire.Client, provider, flowID, redirectURL string) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.AuthLoginComplete(context.Background(), appwire.AuthLoginCompleteParams{
			Provider:    provider,
			FlowID:      flowID,
			RedirectURL: redirectURL,
		})
		return hubAuthLoginCompleteMsg{resp: resp, err: err}
	}
}

func logoutHubAuth(client *appwire.Client, provider string) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.AuthLogout(context.Background(), appwire.AuthLogoutParams{Provider: provider})
		return hubAuthLogoutMsg{resp: resp, err: err}
	}
}

func modelPickerItems(models []appwire.ModelDescriptor, rawModelID bool) []modelPickerItem {
	items := make([]modelPickerItem, 0, len(models))
	for _, option := range models {
		model := strings.TrimSpace(option.Model)
		provider := strings.TrimSpace(option.Provider)
		if model == "" || (!rawModelID && provider == "") {
			continue
		}
		display := model
		if provider != "" {
			display = provider + "/" + model
		}
		id := display
		if rawModelID {
			id = model
		}
		items = append(items, modelPickerItem{id: id, display: display})
	}
	return items
}

func modelPickerItemsFromResponse(resp appwire.ModelListResponse, rawModelID bool) []modelPickerItem {
	items := modelPickerItems(resp.Data, rawModelID)
	if len(resp.Diagnostics) == 0 {
		return items
	}
	reasons := map[string]string{}
	for _, diagnostic := range resp.Diagnostics {
		provider := strings.TrimSpace(diagnostic.Provider)
		if provider == "" {
			continue
		}
		if _, exists := reasons[provider]; exists {
			continue
		}
		reasons[provider] = modelDiagnosticDisabledReason(diagnostic)
	}
	if len(reasons) == 0 {
		return items
	}
	for i := range items {
		provider := modelPickerItemProvider(items[i])
		if provider == "" {
			continue
		}
		if reason := reasons[provider]; reason != "" {
			items[i].disabledReason = reason
		}
	}
	return items
}

func modelPickerItemProvider(item modelPickerItem) string {
	display := strings.TrimSpace(item.display)
	if display == "" {
		display = strings.TrimSpace(item.id)
	}
	provider, _, ok := strings.Cut(display, "/")
	if !ok {
		return ""
	}
	return strings.TrimSpace(provider)
}

func modelDiagnosticDisabledReason(diagnostic appwire.ModelListDiagnostic) string {
	title := strings.TrimSpace(diagnostic.Title)
	message := strings.TrimSpace(diagnostic.Message)
	hint := strings.TrimSpace(diagnostic.Hint)
	reason := title
	if message != "" && !strings.EqualFold(message, title) {
		if reason != "" {
			reason += ": " + message
		} else {
			reason = message
		}
	}
	if reason == "" {
		reason = "provider unavailable"
	}
	if hint != "" {
		reason += " (" + hint + ")"
	}
	return reason
}

func sendHubInput(client *appwire.Client, ref appwire.Ref, text string, draft string, attachments []*PastedImage) tea.Cmd {
	trackedAttachmentSubmit := len(attachments) > 0
	return func() tea.Msg {
		items, err := buildAttachmentItems(attachments)
		if err != nil {
			return hubSendMsg{text: text, draft: draft, trackedAttachmentSubmit: trackedAttachmentSubmit, submittedAttachments: attachments, err: err}
		}
		resp, err := client.TurnStart(context.Background(), appwire.TurnStartParams{Ref: ref.String(), Prompt: text, Items: items})
		return hubSendMsg{text: text, draft: draft, turnID: resp.Turn.ID, trackedAttachmentSubmit: trackedAttachmentSubmit, submittedAttachments: attachments, err: err}
	}
}

func fetchHubTasks(client *appwire.Client, ref appwire.Ref) tea.Cmd {
	return func() tea.Msg {
		tasks, err := fetchHubTasksSync(context.Background(), client, ref)
		if err != nil {
			return hubTasksMsg{err: err}
		}
		return hubTasksMsg{tasks: tasks}
	}
}

func fetchHubTasksSync(ctx context.Context, client *appwire.Client, ref appwire.Ref) ([]agent.Task, error) {
	resp, err := client.TasksList(ctx, appwire.TaskListParams{Ref: ref.String()})
	if err != nil {
		return nil, err
	}
	var tasks []agent.Task
	data, _ := json.Marshal(resp.Data)
	if len(data) > 0 {
		_ = json.Unmarshal(data, &tasks)
	}
	return tasks, nil
}

func sendHubAction(client *appwire.Client, ref appwire.Ref, action string, turnID string) tea.Cmd {
	return func() tea.Msg {
		var err error
		switch action {
		case "interrupt":
			err = client.TurnInterrupt(context.Background(), appwire.TurnInterruptParams{Ref: ref.String(), TurnID: turnID})
		case "compact":
			err = client.ThreadCompactStart(context.Background(), appwire.ThreadCompactStartParams{Ref: ref.String()})
		case "shutdown":
			err = client.ThreadShutdown(context.Background(), appwire.ThreadShutdownParams{Ref: ref.String()})
		default:
			provider, model := splitProviderModel(action)
			err = client.ThreadModelSet(context.Background(), appwire.ThreadModelSetParams{Ref: ref.String(), ModelProvider: provider, Model: model})
			action = "model"
		}
		return hubActionMsg{action: action, err: err}
	}
}

func sendHubSteer(client *appwire.Client, ref appwire.Ref, turnID string, text string) tea.Cmd {
	return func() tea.Msg {
		err := client.TurnSteer(context.Background(), appwire.TurnSteerParams{Ref: ref.String(), TurnID: turnID, Text: text})
		return hubActionMsg{action: "steer", err: err}
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
