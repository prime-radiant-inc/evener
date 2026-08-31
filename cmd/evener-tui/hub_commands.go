package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/clipboard"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuipick"
	"primeradiant.com/evener/llm"
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
	// beforeCut carries the frames the connection delivered ahead of this
	// read's response, and capture holds the ones it delivered after. The
	// response is an exact cut, so the two go on opposite sides of the
	// snapshot: beforeCut is folded first, capture is released once the
	// snapshot is applied.
	beforeCut []appwire.Notification
	capture   *hubReadCapture
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

// hubForkMsg reports the result of a thread/fork call. aside distinguishes the
// /aside tip-fork (side thread) from the divergent fork-from-turn flow so
// failures are attributed to the right command.
type hubForkMsg struct {
	resp  hubRefResponse
	err   error
	aside bool
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

type hubVisionModelsMsg struct {
	models []tuipick.ModelPickerItem
	err    error
}

type hubSpawnOptionsMsg struct {
	harnesses                   []string
	harnessKinds                map[string]string
	emptyTaskUnsupportedReasons map[string]string
	emptyTaskUnsupportedNext    map[string]string
	models                      []tuipick.ModelPickerItem
	// recentDirs carries the hub's most recently used project dirs (the Dir
	// field's prepopulated dropdown options, issue #35). Best-effort: a hub
	// too old for evener/projects/recent leaves it nil.
	recentDirs []string
	err        error
	modelErr   error
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

func fetchHubSession(feed *hubFrameFeed, client *appwire.Client, ref appwire.Ref) tea.Cmd {
	return fetchHubSessionRead(feed, client, ref, "", 0, true, true)
}

// fetchHubSessionExpectingStateToken takes no cut: a status refresh keeps the
// transcript it found, so no frame can be lost under a snapshot that replaces
// it.
func fetchHubSessionExpectingStateToken(client *appwire.Client, ref appwire.Ref, expectedState string, expectedRefreshToken int) tea.Cmd {
	return fetchHubSessionRead(nil, client, ref, expectedState, expectedRefreshToken, false, false)
}

// resyncHubSession re-reads the viewed thread after evener/thread/resync, so the
// transcript comes from the daemon now behind the relay instead of the one that
// died. The re-subscribe is ADDITIVE, unlike session entry's: this connection
// also carries the subagent rail's child-transcript subscriptions, and nothing
// on the resync path re-issues them, so replacing them would leave every
// watched child's activity dead until the user re-entered the session.
func resyncHubSession(feed *hubFrameFeed, client *appwire.Client, ref appwire.Ref) tea.Cmd {
	return fetchHubSessionRead(feed, client, ref, "", 0, true, false)
}

// fetchHubSessionRead issues thread/read. A feed makes the read a cut: the
// connection's frames are held around it and handed back on the side of the
// snapshot the source committed them on (kata 0vk2). Callers whose response
// does not replace the transcript pass none.
func fetchHubSessionRead(feed *hubFrameFeed, client *appwire.Client, ref appwire.Ref, expectedState string, expectedRefreshToken int, subscribe bool, replaceSubscription bool) tea.Cmd {
	return func() tea.Msg {
		capture := feed.BeginCapture()
		ctx := appwire.WithRequestIDObserver(context.Background(), capture.CutOn)
		resp, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref.String(), IncludeTurns: true, ItemsView: "full", Subscribe: subscribe, ReplaceSubscription: replaceSubscription})
		if err != nil {
			capture.Abandon()
			return hubSessionMsg{ref: ref.String(), expectedState: expectedState, expectedRefreshToken: expectedRefreshToken, err: err}
		}
		return hubSessionMsg{detail: hubDetailFromThread(resp.Thread), messages: transcript.MessagesFromThread(resp.Thread), ref: ref.String(), expectedState: expectedState, expectedRefreshToken: expectedRefreshToken, beforeCut: capture.BeforeCut(), capture: capture}
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
		return hubSpawnMsg{resp: hubSpawnResponse{Ref: resp.Thread.Evener.Ref}, err: err}
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

// fetchHubVisionSessionModels loads the session's launchable models and filters
// them to the vision-capable ones, prepending the two pseudo-entries of the
// vision setting: current-model and off.
func fetchHubVisionSessionModels(client *appwire.Client, workingDir string) tea.Cmd {
	workingDir = strings.TrimSpace(workingDir)
	return func() tea.Msg {
		resp, err := client.ModelList(context.Background(), appwire.ModelListParams{CWD: workingDir})
		if err != nil {
			return hubVisionModelsMsg{err: err}
		}
		// Recent descriptors are the same rows the picker's Recent group is
		// built from, so both halves of the item list can be matched.
		descriptors := append(append([]appwire.ModelDescriptor(nil), resp.Recent...), resp.Data...)
		return hubVisionModelsMsg{models: visionModelPickerItems(descriptors, modelPickerItemsFromResponse(resp, false))}
	}
}

// visionModelPickerItems keeps the picker items whose descriptor reports vision
// support, prepending the two pseudo-entries of the vision setting. A
// descriptor that says nothing about vision is not vision-capable: the picker
// only offers a model the registry vouches for.
func visionModelPickerItems(models []appwire.ModelDescriptor, items []tuipick.ModelPickerItem) []tuipick.ModelPickerItem {
	capable := make(map[string]bool, len(models))
	for _, descriptor := range models {
		if descriptor.SupportsVision == nil || !*descriptor.SupportsVision {
			continue
		}
		capable[strings.TrimSpace(descriptor.Provider)+"/"+strings.TrimSpace(descriptor.Model)] = true
	}
	out := []tuipick.ModelPickerItem{
		{ID: "", Display: "Current model"},
		{ID: "off", Display: "Off"},
	}
	for _, item := range items {
		if capable[item.ID] {
			out = append(out, item)
		}
	}
	return out
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
				kind = "evener"
			}
			harnessKinds[option.ID] = kind
			if reason := strings.TrimSpace(option.EmptyTaskUnsupportedReason); reason != "" {
				emptyTaskUnsupportedReasons[option.ID] = reason
			}
			if next := strings.TrimSpace(option.EmptyTaskUnsupportedNextAction); next != "" {
				emptyTaskUnsupportedNext[option.ID] = next
			}
		}
		// Recent project dirs are best-effort: a failure (e.g. an older hub
		// without evener/projects/recent) must not break spawning.
		var recentDirs []string
		if recentResp, err := client.ProjectsRecent(context.Background(), appwire.ProjectsRecentParams{}); err == nil {
			recentDirs = recentResp.Data
		}
		modelResp, err := client.ModelList(context.Background(), appwire.ModelListParams{CWD: workingDir})
		if err != nil {
			return hubSpawnOptionsMsg{harnesses: harnesses, harnessKinds: harnessKinds, emptyTaskUnsupportedReasons: emptyTaskUnsupportedReasons, emptyTaskUnsupportedNext: emptyTaskUnsupportedNext, recentDirs: recentDirs, modelErr: err}
		}
		models := modelPickerItemsFromResponse(modelResp, false)
		return hubSpawnOptionsMsg{harnesses: harnesses, harnessKinds: harnessKinds, emptyTaskUnsupportedReasons: emptyTaskUnsupportedReasons, emptyTaskUnsupportedNext: emptyTaskUnsupportedNext, models: models, recentDirs: recentDirs}
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

// datedSnapshotSuffix and prettifyModelDisplayName are duplicated from the hub
// model-picker implementation in cmd/evener-hub/app_models.go because the TUI
// and hub are separate binaries.
var datedSnapshotSuffix = regexp.MustCompile(`-\d{8}(-v\d+)?$`)

func isDatedSnapshotModelID(ref string) bool {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	return datedSnapshotSuffix.MatchString(ref)
}

func prettifyModelDisplayName(id string) string {
	base := datedSnapshotSuffix.ReplaceAllString(id, "")
	segments := strings.Split(base, "-")
	for idx, seg := range segments {
		if seg == "" {
			continue
		}
		r := []rune(seg)
		r[0] = unicode.ToUpper(r[0])
		segments[idx] = string(r)
	}
	return strings.Join(segments, " ")
}

// formatModelContextWindow renders a token count compactly ("1M", "128K").
func formatModelContextWindow(n int) string {
	switch {
	case n >= 1_000_000:
		return strconv.Itoa(n/1_000_000) + "M"
	case n >= 1000:
		return strconv.Itoa(n/1000) + "K"
	default:
		return strconv.Itoa(n)
	}
}

// modelInfoMetaTail builds the model picker row's compact caps/ctx/price tail
// from the descriptor the hub delivered, whose fields come from the registry's
// resolved row (spec §7.5). A field the row does not carry is simply left out:
// a descriptor with no metadata at all yields "", the uncatalogued-model rule
// (still render name+provider+id, no badges), and a priceless row renders no
// cost rather than a fabricated "$0.00/$0.00".
func modelInfoMetaTail(descriptor appwire.ModelDescriptor) string {
	var parts []string
	if descriptor.ContextWindow != nil && *descriptor.ContextWindow > 0 {
		parts = append(parts, formatModelContextWindow(*descriptor.ContextWindow)+" ctx")
	}
	if descriptor.InputCostPerMillion != nil && descriptor.OutputCostPerMillion != nil {
		parts = append(parts, fmt.Sprintf("$%.2f/$%.2f", *descriptor.InputCostPerMillion, *descriptor.OutputCostPerMillion))
	}
	var caps []string
	if boolValue(descriptor.SupportsTools) {
		caps = append(caps, "tools")
	}
	if boolValue(descriptor.SupportsVision) {
		caps = append(caps, "vision")
	}
	if boolValue(descriptor.SupportsReasoning) {
		caps = append(caps, "reasoning")
	}
	if len(caps) > 0 {
		parts = append(parts, strings.Join(caps, ","))
	}
	return strings.Join(parts, " · ")
}

// boolValue reads an optional descriptor capability: nil and false both mean
// the model does not have it.
func boolValue(p *bool) bool { return p != nil && *p }

// buildModelPickerItems enriches raw model descriptors into picker items
// (display name, ID, descriptor meta, provider group) without reordering them.
// Callers that need the provider-grouped, dated-snapshot-last presentation
// order should use modelPickerItems instead; callers that must preserve the
// input order (e.g. the server's recency-ordered Recent list) should call
// this directly.
func buildModelPickerItems(models []appwire.ModelDescriptor, rawModelID bool) []tuipick.ModelPickerItem {
	items := make([]tuipick.ModelPickerItem, 0, len(models))
	for _, option := range models {
		model := strings.TrimSpace(option.Model)
		provider := strings.TrimSpace(option.Provider)
		if model == "" || (!rawModelID && provider == "") {
			continue
		}
		display := prettifyModelDisplayName(model)
		id := provider + "/" + model
		if rawModelID {
			id = model
		}
		items = append(items, tuipick.ModelPickerItem{ID: id, Display: display, Group: provider, Meta: modelInfoMetaTail(option)})
	}
	return items
}

func modelPickerItems(models []appwire.ModelDescriptor, rawModelID bool) []tuipick.ModelPickerItem {
	items := buildModelPickerItems(models, rawModelID)
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].Group != items[b].Group {
			return items[a].Group < items[b].Group
		}
		da, db := isDatedSnapshotModelID(items[a].ID), isDatedSnapshotModelID(items[b].ID)
		if da != db {
			return !da
		}
		return false
	})
	return items
}

func modelPickerItemsFromResponse(resp appwire.ModelListResponse, rawModelID bool) []tuipick.ModelPickerItem {
	items := modelPickerItems(resp.Data, rawModelID)
	if len(resp.Diagnostics) > 0 {
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
		if len(reasons) > 0 {
			for i := range items {
				provider := modelPickerItemProvider(items[i])
				if provider == "" {
					continue
				}
				if reason := reasons[provider]; reason != "" {
					items[i].DisabledReason = reason
				}
			}
		}
	}
	if len(resp.Recent) == 0 {
		return items
	}
	recentItems := buildModelPickerItems(resp.Recent, rawModelID)
	for i := range recentItems {
		recentItems[i].Group = "Recent"
	}
	return append(recentItems, items...)
}

func modelPickerItemProvider(item tuipick.ModelPickerItem) string {
	if g := strings.TrimSpace(item.Group); g != "" {
		return g
	}
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

func sendHubInput(client *appwire.Client, ref appwire.Ref, text string, draft string, attachments []*clipboard.PastedImage, expectedInstanceIDs ...string) tea.Cmd {
	trackedAttachmentSubmit := len(attachments) > 0
	// Minted here rather than inside the closure: one user action is one
	// mutation, whatever happens to the command afterwards.
	mutationID, idErr := newClientMutationID()
	expectedInstanceID := mutationInstanceID(ref, expectedInstanceIDs...)
	return func() tea.Msg {
		if idErr != nil {
			return hubSendMsg{ref: ref.String(), text: text, draft: draft, trackedAttachmentSubmit: trackedAttachmentSubmit, submittedAttachments: attachments, err: idErr}
		}
		items, err := buildAttachmentItems(attachments)
		if err != nil {
			return hubSendMsg{ref: ref.String(), text: text, draft: draft, trackedAttachmentSubmit: trackedAttachmentSubmit, submittedAttachments: attachments, err: err}
		}
		resp, err := client.TurnStart(context.Background(), appwire.TurnStartParams{
			Ref:                ref.String(),
			ClientMutationID:   mutationID,
			ExpectedInstanceID: expectedInstanceID,
			Input:              appendTextInput(text, items),
		})
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

func sendHubAction(client *appwire.Client, ref appwire.Ref, action string, expectedInstanceIDs ...string) tea.Cmd {
	// Only the interrupt branch is a retry-safe turn mutation; the rest are
	// thread-level calls the guard does not cover. Minted unconditionally so the
	// identity is fixed to the action, not to the branch taken at run time.
	mutationID, idErr := newClientMutationID()
	expectedInstanceID := mutationInstanceID(ref, expectedInstanceIDs...)
	return func() tea.Msg {
		var err error
		switch action {
		case "interrupt":
			if idErr != nil {
				return hubActionMsg{action: action, err: idErr}
			}
			err = client.TurnInterrupt(context.Background(), appwire.TurnInterruptParams{
				Ref:                ref.String(),
				ClientMutationID:   mutationID,
				ExpectedInstanceID: expectedInstanceID,
			})
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

// sessionEffortLevels is the ladder /effort works from: the model's own when
// it states one, else the canonical vocabulary. A reasoning model with no
// stated ladder still takes an effort — the session sends its default on
// every request and the request builder passes any level through unclamped —
// so the picker offers the tiers instead of denying they exist. Callers gate
// on SupportsReasoning first; a model that does not reason has no ladder at
// all, not an unstated one.
func sessionEffortLevels(levels []string) []string {
	if len(levels) > 0 {
		return append([]string(nil), levels...)
	}
	return llm.ReasoningEffortVocabulary()
}

// effortChoices lists what /effort accepts for a reasoning session: the
// model's ladder plus the always-settable explicit off. The session stores
// "none" and the request builder sends it only where the model's ladder
// lists an off level, omitting the field otherwise.
func effortChoices(levels []string) []string {
	for _, l := range levels {
		if strings.EqualFold(l, string(llm.ReasoningEffortNone)) {
			return append([]string(nil), levels...)
		}
	}
	return append(append([]string(nil), levels...), llm.ReasoningEffortNone)
}

// effortDisplay labels a picker choice the way the hub surfaces do: an off
// level says "off" only where the model's ladder can express one, and
// "provider default" where an explicit none merely omits the field.
func effortDisplay(level string, levels []string) string {
	if llm.NormalizeReasoningEffort(level) != llm.ReasoningEffortNone {
		return level
	}
	for _, l := range levels {
		if strings.EqualFold(l, llm.ReasoningEffortNone) {
			return "none (off)"
		}
	}
	return "none (provider default)"
}

// reasoningEffortLevelSettable reports whether level (case-insensitively,
// with disable aliases normalized) is one of effortChoices, so
// /effort <level> can be rejected client-side without a wire round trip.
func reasoningEffortLevelSettable(levels []string, level string) bool {
	normalized := llm.NormalizeReasoningEffort(level)
	for _, l := range effortChoices(levels) {
		if strings.EqualFold(l, normalized) {
			return true
		}
	}
	return false
}

// visionModelRefKnown reports whether ref parses as a vision-model setting —
// "", "off", a bare model, or "provider/model" — so /vision-model can reject
// a malformed ref client-side without a wire round trip.
func visionModelRefKnown(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.EqualFold(ref, "off") {
		return true
	}
	prov, model, ok := strings.Cut(ref, "/")
	return !ok || (prov != "" && model != "")
}

func sendHubVisionModelAction(client *appwire.Client, ref appwire.Ref, visionModel string) tea.Cmd {
	return func() tea.Msg {
		err := client.ThreadVisionModelSet(context.Background(), appwire.ThreadVisionModelSetParams{Ref: ref.String(), VisionModel: visionModel})
		return hubActionMsg{action: "vision-model", err: err}
	}
}

func sendHubEffortAction(client *appwire.Client, ref appwire.Ref, level string) tea.Cmd {
	return func() tea.Msg {
		err := client.ThreadReasoningEffortSet(context.Background(), appwire.ThreadReasoningEffortSetParams{Ref: ref.String(), ReasoningEffort: level})
		return hubActionMsg{action: "effort", err: err}
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
	lines := []string{fmt.Sprintf("Evener upgraded to %s.", target)}
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

func sendHubClear(client *appwire.Client, ref appwire.Ref, expectedInstanceID string) tea.Cmd {
	mutationID, idErr := newClientMutationID()
	return func() tea.Msg {
		if idErr != nil {
			return hubClearMsg{resp: hubRefResponse{Ref: ref.String()}, err: idErr}
		}
		resp, err := client.ThreadClear(context.Background(), appwire.ThreadClearParams{
			Ref:                ref.String(),
			ClientMutationID:   mutationID,
			ExpectedInstanceID: expectedInstanceID,
		})
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
			SourceTurnID: strconv.Itoa(req.EntryIndex),
			EditedInput:  req.EditedMessage,
			Label:        req.Label,
		})
		return hubForkMsg{resp: hubRefResponse{Ref: resp.Thread.Evener.Ref}, err: err}
	}
}

// sendHubAside issues the /aside fork: thread/fork in aside mode forks the
// session at its tip into a side thread that inherits this session's
// permissions and config. The success path mirrors fork: the update loop opens
// the returned child thread.
func sendHubAside(client *appwire.Client, ref appwire.Ref) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{
			Ref:   ref.String(),
			Aside: true,
		})
		return hubForkMsg{resp: hubRefResponse{Ref: resp.Thread.Evener.Ref}, err: err, aside: true}
	}
}

func waitHubNotification(feed *hubFrameFeed) tea.Cmd {
	return func() tea.Msg {
		notification, ok := <-feed.Notifications()
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
