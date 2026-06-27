package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/clipboard"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

type hubTreeMsg struct {
	tree hubTreeResponse
	err  error
}

type hubSessionMsg struct {
	detail               hubSessionDetail
	messages             []transcript.ChatMessage
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
	ref                     string
	text                    string
	draft                   string
	turnID                  string
	trackedAttachmentSubmit bool
	submittedAttachments    []*clipboard.PastedImage
	err                     error
}

type hubTasksMsg struct {
	tasks []task.Task
	err   error
}

type hubStatusMsg struct {
	detail  hubSessionDetail
	tasks   []task.Task
	auth    appwire.AuthStatusResponse
	taskErr error
	authErr error
	err     error
}

type hubActionMsg struct {
	action string
	err    error
}

type hubUpgradeMsg struct {
	resp appwire.UpgradeResponse
	err  error
}

type hubClearMsg struct {
	resp hubRefResponse
	err  error
}

// hubGoalMsg reports the result of a goal/set call. cleared distinguishes the
// clear path (empty objective) from setting an objective; started reports
// whether the goal loop began immediately versus after the current turn.
type hubGoalMsg struct {
	cleared bool
	started bool
	err     error
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
	models  []tuipick.ModelPickerItem
	err     error
}

type hubSessionModelsMsg struct {
	models []tuipick.ModelPickerItem
	err    error
}

type hubSpawnOptionsMsg struct {
	harnesses                   []string
	harnessKinds                map[string]string
	emptyTaskUnsupportedReasons map[string]string
	emptyTaskUnsupportedNext    map[string]string
	models                      []tuipick.ModelPickerItem
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
	messages []transcript.ChatMessage
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
	return fetchHubSessionRead(client, ref, "", 0, true, true)
}

func fetchHubSessionExpectingStateToken(client *appwire.Client, ref appwire.Ref, expectedState string, expectedRefreshToken int) tea.Cmd {
	return fetchHubSessionRead(client, ref, expectedState, expectedRefreshToken, false, false)
}

func fetchHubSessionRead(client *appwire.Client, ref appwire.Ref, expectedState string, expectedRefreshToken int, subscribe bool, replaceSubscription bool) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: ref.String(), IncludeTurns: true, ItemsView: "full", Subscribe: subscribe, ReplaceSubscription: replaceSubscription})
		if err != nil {
			return hubSessionMsg{ref: ref.String(), expectedState: expectedState, expectedRefreshToken: expectedRefreshToken, err: err}
		}
		return hubSessionMsg{detail: hubDetailFromThread(resp.Thread), messages: transcript.MessagesFromThread(resp.Thread), ref: ref.String(), expectedState: expectedState, expectedRefreshToken: expectedRefreshToken}
	}
}

// subscribeChildActivity subscribes (additively, no turns) to a subagent
// child's transcript thread so its live frames push to this connection; the
// child-activity handler routes them to the matching rail row. Fire-and-forget.
func subscribeChildActivity(client *appwire.Client, ref string) tea.Cmd {
	return func() tea.Msg {
		_, _ = client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: ref, IncludeTurns: false, Subscribe: true, ReplaceSubscription: false})
		return nil
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
		return hubTranscriptMsg{target: target, messages: transcript.MessagesFromThread(resp.Thread)}
	}
}

func sendHubSpawn(client *appwire.Client, req hubSpawnRequest) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
			Harness:         req.Harness,
			CWD:             req.WorkingDir,
			Input:           textInput(req.Prompt),
			Model:           strings.TrimSpace(req.Model),
			LaunchOverrides: req.LaunchOverrides,
		})
		return hubSpawnMsg{resp: hubSpawnResponse{Ref: resp.Thread.Serf.Ref}, err: err}
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

func modelPickerItems(models []appwire.ModelDescriptor, rawModelID bool) []tuipick.ModelPickerItem {
	items := make([]tuipick.ModelPickerItem, 0, len(models))
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
		items = append(items, tuipick.ModelPickerItem{ID: id, Display: display})
	}
	return items
}

func modelPickerItemsFromResponse(resp appwire.ModelListResponse, rawModelID bool) []tuipick.ModelPickerItem {
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
			items[i].DisabledReason = reason
		}
	}
	return items
}

func modelPickerItemProvider(item tuipick.ModelPickerItem) string {
	display := strings.TrimSpace(item.Display)
	if display == "" {
		display = strings.TrimSpace(item.ID)
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

func sendHubInput(client *appwire.Client, ref appwire.Ref, text string, draft string, attachments []*clipboard.PastedImage) tea.Cmd {
	trackedAttachmentSubmit := len(attachments) > 0
	return func() tea.Msg {
		items, err := buildAttachmentItems(attachments)
		if err != nil {
			return hubSendMsg{ref: ref.String(), text: text, draft: draft, trackedAttachmentSubmit: trackedAttachmentSubmit, submittedAttachments: attachments, err: err}
		}
		resp, err := client.TurnStart(context.Background(), appwire.TurnStartParams{Ref: ref.String(), Input: appendTextInput(text, items)})
		return hubSendMsg{ref: ref.String(), text: text, draft: draft, turnID: resp.Turn.ID, trackedAttachmentSubmit: trackedAttachmentSubmit, submittedAttachments: attachments, err: err}
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

func fetchHubTasksSync(ctx context.Context, client *appwire.Client, ref appwire.Ref) ([]task.Task, error) {
	resp, err := client.TasksList(ctx, appwire.TaskListParams{Ref: ref.String()})
	if err != nil {
		return nil, err
	}
	var tasks []task.Task
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
			err = client.TurnInterrupt(context.Background(), appwire.TurnInterruptParams{Ref: ref.String(), ExpectedTurnID: turnID})
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

func sendHubUpgrade(client *appwire.Client, requested string) tea.Cmd {
	requested = strings.TrimSpace(requested)
	return func() tea.Msg {
		resp, err := client.Upgrade(context.Background(), appwire.UpgradeParams{Requested: requested})
		return hubUpgradeMsg{resp: resp, err: err}
	}
}

func formatHubUpgradeResult(resp appwire.UpgradeResponse) string {
	target := strings.TrimSpace(resp.Channel)
	if target == "" {
		target = strings.TrimSpace(resp.Release)
	}
	if target == "" {
		target = "requested channel"
	}
	lines := []string{fmt.Sprintf("Serf upgraded to %s.", target)}
	if resp.Archive != "" {
		lines = append(lines, "Archive: "+resp.Archive)
	}
	if resp.ShareBinDir != "" {
		lines = append(lines, "Installed: "+resp.ShareBinDir)
	}
	if resp.BinDir != "" {
		lines = append(lines, "Symlinks: "+resp.BinDir)
	}
	if resp.RestartMessage != "" {
		lines = append(lines, resp.RestartMessage)
	}
	return strings.Join(lines, "\n")
}

func appendTextInput(text string, items []appwire.InputItem) []appwire.InputItem {
	input := textInput(text)
	return append(input, items...)
}

func textInput(text string) []appwire.InputItem {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []appwire.InputItem{{Type: "text", Text: text}}
}

func sendHubClear(client *appwire.Client, ref appwire.Ref) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ThreadClear(context.Background(), appwire.ThreadClearParams{Ref: ref.String()})
		return hubClearMsg{resp: hubRefResponse{Ref: resp.Ref}, err: err}
	}
}

// sendHubGoal issues goal/set to set (empty objective ⇒ clear) the session's
// /goal. It mirrors sendHubQueue: a thin async command that reports its result
// so the update loop can surface a system message.
func sendHubGoal(client *appwire.Client, ref appwire.Ref, objective string) tea.Cmd {
	cleared := strings.TrimSpace(objective) == ""
	return func() tea.Msg {
		resp, err := client.GoalSet(context.Background(), appwire.GoalSetParams{Ref: ref.String(), Objective: objective})
		return hubGoalMsg{cleared: cleared, started: resp.Started, err: err}
	}
}

// runHubGoal dispatches the /goal command: `clear` clears the goal, `status`
// reports the cached goal snapshot, and anything else sets it as the objective.
func (m *hubModel) runHubGoal(args string) tea.Cmd {
	arg := strings.TrimSpace(args)
	if strings.EqualFold(arg, "status") {
		m.addSessionSystem(hubGoalStatusText(m.detail.Goal))
		return nil
	}
	ref, ok := m.currentRef()
	if !ok {
		m.addSessionSystem("Session ref is invalid.")
		return nil
	}
	if strings.EqualFold(arg, "clear") {
		return sendHubGoal(m.client, ref, "")
	}
	return sendHubGoal(m.client, ref, arg)
}

// hubGoalStatusText renders the `/goal status` line from the cached goal
// snapshot. With no goal set it prints a minimal usage hint.
func hubGoalStatusText(goal *appwire.GoalState) string {
	if goal == nil {
		return "No goal set. Use /goal <objective> to set one."
	}
	return fmt.Sprintf("Goal: %s %d", goal.Status, goal.Iterations)
}

func sendHubFork(client *appwire.Client, ref appwire.Ref, req hubForkRequest) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{
			Ref:          ref.String(),
			SourceTurnID: strconv.Itoa(req.Turn),
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
