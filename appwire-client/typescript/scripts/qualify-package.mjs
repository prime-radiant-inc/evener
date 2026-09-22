import assert from "node:assert/strict";
import { execFile, execFileSync } from "node:child_process";
import { once } from "node:events";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { WebSocketServer } from "ws";
import { consumerPackageUsage, entrySurface } from "./consumer-value-imports.mjs";
import { reachableModules } from "./declaration-reachability.mjs";
import { runInstalledDiscoveryContracts } from "./discovery-contracts.mjs";

const packageDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const consumerDir = mkdtempSync(join(tmpdir(), "evener-appwire-package-"));
const run = (command, args, cwd) =>
  execFileSync(command, args, {
    cwd,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
async function qualify() {
  const packageManifest = JSON.parse(readFileSync(join(packageDir, "package.json"), "utf8"));
  const packed = JSON.parse(run("npm", ["pack", "--json", "--pack-destination", consumerDir], packageDir))[0];
  const tarball = join(consumerDir, packed.filename);
  run(
    "npm",
    ["install", "--offline", "--ignore-scripts", "--no-audit", "--no-fund", "--package-lock=false", tarball],
    consumerDir,
  );
  // Every module the package ships, read off the build's own file list rather
  // than restated here: the two were duplicates of each other, and a module
  // added to the build and forgotten here was shipped without ever being
  // reachability-checked (#1224). docContent.ts is in that list too, and is
  // fine to load: it names no browser global at all - readDocFile takes the
  // host's DocPort - so the module's URL builders, size cap and error type run
  // in a bare Node consumer. The runner never calls readDocFile: a port implies
  // a request, and qualification makes none.
  const buildConfig = JSON.parse(readFileSync(join(packageDir, "tsconfig.build.json"), "utf8"));
  const shippedModules = (buildConfig.files ?? [])
    .filter((file) => file.endsWith(".ts"))
    .map((file) => file.slice(0, -3));
  assert(shippedModules.includes("index"), "tsconfig.build.json must compile index.ts - it is the package root entry");
  assert(shippedModules.length > 1, "tsconfig.build.json lists no modules to qualify");
  // Each specifier's runtime values and exported types are derived below from
  // its entry module, so the qualification surface cannot drift from what the
  // entry point exports; an export added there is qualified without editing
  // this file. The smoke CALLS stay hand-written -- they are behaviour probes,
  // not a surface.
  // One call per shipped module, with a trivial input. Importing alone would
  // pass for a module that needs a browser global at load time; calling proves
  // each module actually evaluates and runs inside a bare Node consumer.
  const rootSmokeCalls = `assert.equal(typeof client.AppwireClient, "function");
assert.deepEqual(client.mergeTurnHistory([], []).turns, []);
assert.equal(client.rpcURLFromLocation({ protocol: "https:", host: "hub.example:9180" }), "wss://hub.example:9180/rpc");
assert.equal(client.composeAskAnswers([]), "[answers]");
const askItem = {
  id: "ask1", turnId: "t1", type: "commandExecution", toolName: "ask_user", status: "completed",
  argumentsJSON: '{"questions":[{"header":"DB","question":"Which store?","options":[{"label":"SQLite","detail":"one file"}]}]}',
};
const askModel = { turns: [{ items: [askItem] }], askPending: true };
assert.equal(client.parseAskUserQuestions(askItem)?.[0].question, "Which store?");
assert.equal(client.parseAskUserQuestions({ argumentsJSON: "not json" }), undefined);
assert.equal(client.liveAskQuestions(askModel)[0].key, "ask1:0");
const counter = client.createFrameworkFreeStore((set) => ({ n: 0, bump: () => set((s) => ({ n: s.n + 1 })) })); counter.getState().bump(); assert.equal(counter.getState().n, 1);
const disclosureStore = client.createDisclosureStore(); disclosureStore.toggle(client.scopedDisclosureId("live", "tool"), false); assert.equal(client.isDisclosureOpenIn(disclosureStore.getState(), client.scopedDisclosureId("live", "tool"), false), true);
const keybindingsStore = client.createKeybindingsStore({ client: { request: async () => { throw new Error("offline"); }, onNotification: () => () => {} } }); keybindingsStore.setSupport(client.keybindingsSupport({ keybindingsSettings: true })); assert.equal(keybindingsStore.getState().hubSupport, "supported"); assert.deepEqual(client.fromWireOverrides({ version: 1, revision: 2, rules: [{ action: "palette.open", chord: null }] }), { version: 1, revision: 2, rules: [{ action: "palette.open", chord: null }] }); assert.equal(client.fromWireOverrides({ version: 2 }), undefined);
const askBatches = client.reconcileBatches([], client.liveAskQuestions(askModel), () => "batch1");
assert.equal(askBatches[0].id, "batch1");
assert.equal(askBatches[0].questions[0].key, "ask1:0");
const askDock = client.createAskDockStore(); askDock.reconcile("ref1", client.liveAskQuestions(askModel)); assert.equal(askDock.beginSend("ref1", askDock.getState().byRef.get("ref1").batches[0].id), true);
const askReply = { id: "u1", turnId: "t1", type: "userMessage", text: '[answers]\\n1. [DB] \u2192 "SQLite"' };
assert.equal(client.answeredAskUserSuffix({ turns: [{ items: [askItem, askReply] }] }, askItem), ' \u2014 answered: "SQLite"');
assert.equal(client.rejectionReason({ type: "image/png", size: client.MAX_ATTACHMENT_BYTES + 1, name: "big.png" }, 0), "big.png (maximum 8 MB)");
assert.deepEqual(client.stripMarker(client.insertMarker("go", 0, 0, client.markerText(1)).value, 9, 1), { value: "go", cursor: 0 });
assert.equal(client.translateAttachmentMarkers("[image 1]go", [{ marker: 1, name: "shot.png" }]), "(attached image 1: shot.png)go");
assert.deepEqual(client.buildInput("hi"), [{ type: "text", text: "hi" }]);
assert.deepEqual(client.buildComposerInput("[image 1]go", [{ marker: 1, mediaType: "image/png", data: "AA", name: "shot.png" }]), [
  { type: "text", text: "(attached image 1: shot.png)go" },
  { type: "image", mediaType: "image/png", data: "AA", name: "shot.png" },
]);
assert.equal(new client.WireError("nope", -32000).code, -32000);
assert.equal(client.errorText(new Error("boom")), "boom");
assert.equal(client.errorKind(new Error("boom")), "unknown");
assert.equal(client.friendlyErrorMessage(new Error("boom")), client.GENERIC_ERROR_MESSAGE);
assert.equal(client.friendlyErrorMessage(new client.ClientNotReadyError("waited")), client.HUB_UNREACHABLE_MESSAGE);
assert.equal(client.friendlyLaunchErrorMessage(new Error("boom")), client.GENERIC_ERROR_MESSAGE);
assert.equal(client.isHubLaunchError(new Error("boom")), false);
assert.equal(client.isStaleCursorError(new Error("boom")), false);
assert.equal(client.sessionActionHeadline("Couldn't rename", new Error("boom")), "Couldn't rename");
assert.equal(client.sessionActionError("Couldn't rename", new Error("boom")), "Couldn't rename: boom");
assert.equal(client.mutationErrorData(new Error("boom")), undefined);
assert(client.METHOD_NAMES.length > 0);
const session = {
  kind: "session", sessionId: "thread", ref: "ref", label: "label", aggregate: "idle",
  counts: { active: 0, failed: 0, completed: 0, complete: true }, entries: [], branch: {},
};
const tree = { revision: 1, root: session };
assert.equal(client.parseActivityTree(null), null);
assert.equal(client.activityNodeID(session), "session:thread");
assert(Array.isArray(client.defaultExpandedIDs(tree)));
assert.equal(client.isActivityFailure("failure", undefined), true);
assert.equal(client.fenceRootSession(session, session).sessionId, "thread");
assert.equal(client.graftContinuationTree(tree, "session:thread", tree).revision, 1);
assert.equal(client.foldRowID("session:thread"), "session:thread:inactive-fold");
assert.deepEqual(client.buildActivityRows(tree, new Set()), []);
assert.equal(client.jobIsFailed({ terminal: true, outcome: "failure" }), true);
assert.equal(client.activityDelegateState({ kind: "delegate", type: "task", child: session }).failed, false);
assert.equal(client.hasItemFailure({ status: "completed", exitCode: 1 }), true);
assert.equal(client.hasFailureStatus({ status: "interrupted" }), true);
assert.equal(client.hasErrorText({ error: "  " }), false);
assert.equal(client.isNonZeroExit({ exitCode: 0 }), false);
assert.equal(client.isInProgressStatus("inProgress"), true);
assert.equal(client.parseJobLogTail(null), null);
assert.equal(client.SYSTEM_PRELUDE_TURN_ID, "turn_system");
assert.equal(client.pendingTextJoined(["a", "b"]), "ab");
assert.equal(client.notificationRoutingKey({ method: "evener/x", params: {} }), null);
assert.equal(client.deriveSendQueueAvailability({ statusType: "restartRequired", capabilities: {} }).canSend, false);
assert.equal(client.isActionUnavailable(new Error("not a wire error")), false);
assert.equal(client.isThreadNotFound(new Error("not a wire error")), false);
assert.equal(client.stableDelegateDisplayStatus({ status: "running" }), "running");
assert.equal(client.delegateTiming({ terminal: true, runStartedAt: "2026-09-07T00:00:00Z", runEndedAt: "2026-09-07T00:00:05Z" }, Number.NaN).durationMs, 5000);
assert.equal(client.docFileRawURL("", "s", "p"), "/doc/file?format=raw&session=s&path=p");
assert.equal(client.decideSubmitRoute({ hasContent: false, availability: { canSend: true, canQueue: false } }), "none");
assert.equal(client.decideSteerRoute({ hasText: true, hasAttachments: false, queueDepth: 0 }), "steer");
assert.equal(client.isTurnActive("active"), true);
assert.equal(client.isTurnActive("idle"), false);
assert.equal(client.canSteer("active", { steer: true }), true);
assert.equal(client.canSteer("idle", { steer: true }), false);
assert.equal(client.canSteer("active", { steer: false }), false);
assert.equal(client.canDrainQueue("active", { steer: true }, 0), true);
assert.equal(client.canDrainQueue("idle", { steer: true }, 1), true);
assert.equal(client.canDrainQueue("idle", { steer: true }, 0), false);
assert.equal(client.canDrainQueue("idle", { steer: false }, 1), false);
assert.deepEqual(
  client.sessionControls("idle", { steer: true, interrupt: true, queue: false, send: true }, 1),
  { stop: false, steer: false, drain: true, drainQueue: true, queue: false, send: true, reason: { stop: "no active turn", steer: "no active turn", queue: "Queue is not available for this session" } },
);
assert.equal(client.formatTokenCount(41200), "41k");
assert.equal(client.formatDurationMs(1500), "1.5s");
assert.equal(client.formatCharCount(2500), "2.5k chars");
assert.equal(client.formatClockTime(undefined), undefined);
assert.equal(client.formatClockTimeSeconds("not a timestamp"), undefined);
assert.equal(client.formatElapsed(65000), "1m05s");
assert.equal(client.firstLine("\\n  hello  \\n", 20), "hello");
assert.deepEqual(client.splitMandate("first\\n\\nrest"), { first: "first", rest: "rest" });
assert.equal(client.plainQuoteLine("# Title\\n**bold** line"), "bold line");
assert.equal(client.slashCommandInvocation({ name: "plan", source: "plugin", pluginName: "acme" }), "/acme:plan");
assert.deepEqual(client.visibleCatalogCommands([{ name: "plan", source: "plugin", pluginName: "acme" }], new Set()), []);
const commandCatalog = client.createCommandCatalog({ request: async () => ({ commands: [{ name: "plan", source: "user" }] }), onNotification: () => () => {} });
const catalogRead = commandCatalog.getState().refresh();
assert.equal(commandCatalog.getState().loading, true);
catalogRead.then(() => assert.deepEqual(commandCatalog.getState().commands.map((c) => c.name), ["plan"])).catch((error) => { console.error(error); process.exit(1); });
assert.deepEqual(client.sessionPluginNames({ plugins: [{ name: "acme" }] }), new Set(["acme"]));
const activity = new client.ActivityList({ request: async () => ({}), onNotification: () => () => {} }, "ref", "thread");
assert.equal(activity.getSnapshot().tree, null);
assert.equal(client.clip("hello", 3), "hel\u2026");
assert.equal(client.clipJobID("job"), "job");
assert.equal(client.tailSlice("hello", 2), "lo");
assert.equal(client.tailFold("hello", 99), "hello");
assert.equal(client.formatToolDuration(0), "1ms");
assert.equal(client.formatByteCount(1), "1 byte");
assert.equal(client.lineCount("a\\nb\\n"), 2);
assert.deepEqual(client.parseArgs("not json"), {});
assert.equal(client.parseJSONObject("[]"), undefined);
assert.equal(client.trailingBracketFooter("done [exit 0]"), "exit 0");
assert.equal(client.str({ path: "/tmp" }, "path"), "/tmp");
const slashToken = client.parseSlashToken("say /rev", 8);
assert.deepEqual(slashToken, { start: 4, end: 8, query: "rev" });
assert.equal(client.parseSlashToken("say /rev\\nthen", 13), null);
const slashItems = client.mergeSlashCommands(
  [{ id: "goal", hint: "sets the session goal" }],
  [{ name: "review", source: "plugin", pluginName: "acme" }],
  // Completion offers only skills that are available AND user-invocable
  // (docs/skills.md), so a skill fixture must carry both flags to appear.
  [{ name: "writing", description: "writing skill", available: true, userInvocable: true, disableModelInvocation: false }],
);
assert.deepEqual(slashItems.map((item) => item.invocation), ["/goal", "/acme:review", "/writing"]);
assert.deepEqual(
  client.filterSlashMenuItems(slashItems, slashToken.query).map((item) => item.label),
  ["review"],
);
assert.equal(client.evaluateSlashLabel("review", "rev").embedding.longestRun, 3);
assert.deepEqual(client.spliceSlashCommand("say /rev", slashToken, "/acme:review"), {
  text: "say /acme:review ",
  caret: 17,
});
assert.equal(client.canReadSharedNotes({ capabilities: { sharedNotes: true } }), true);
assert.equal(client.canReadSharedNotes({ capabilities: { sharedNotes: false } }), false);
assert.equal(client.canReadSharedNotes({ capabilities: {} }), false);
assert.equal(client.canReadSharedNotes(undefined), false);
assert.equal(client.marketplaceSourceLabel({ kind: "github", repo: "acme/plugins" }), "github: acme/plugins");
assert.equal(client.marketplaceSourceLabel({ kind: "git-subdir", url: "https://example.com/x.git", path: "sub" }), "https://example.com/x.git (sub)");
assert.equal(client.humanizeState("awaiting", true), "question waiting");
assert.equal(client.humanizeState("awaiting", false), "your move");
assert.equal(client.humanizeState("notLoaded", false), "idle");
const catalogEntry = { provider: "openai", model: "gpt-5", displayName: "GPT-5", supportsTools: true, contextWindow: 200000 };
const catalogOptions = client.toCatalogOptions([catalogEntry]);
assert.equal(catalogOptions[0].qualified, "openai/gpt-5");
assert.equal(client.filterCatalog(catalogOptions, "anthropic").length, 0);
assert.equal(client.withGroupHeads(catalogOptions)[0].groupHead, "openai");
assert.deepEqual(client.capabilityLabels(catalogEntry), ["tools"]);
assert.equal(client.formatCost(catalogEntry), null);
assert.equal(client.contextWindowLabel(catalogEntry), "200k");
assert.equal(client.rowMeta(catalogEntry, true), "openai \u00b7 tools \u00b7 200k");
assert.equal(client.unavailableLine({ provider: "anthropic", message: "no credentials" }), "anthropic \u2014 no credentials");
const pickerRows = client.buildPickerRows({ models: [catalogEntry], recent: [] }, "");
assert.deepEqual(pickerRows.map((row) => row.kind), ["group", "model"]);
assert.equal(client.pickableModelRows(pickerRows).length, 1);
assert.deepEqual(client.buildPickerRows(null, ""), []);
assert.equal(client.effortLabel("none", ["none", "high"]), "none (off)");
assert.deepEqual(client.effortOptionLevels(["low", "high"], "medium"), ["", "low", "high", "medium"]);
assert.deepEqual(client.sessionEffortLevels(undefined, true), ["minimal", "low", "medium", "high"]);
const envInstance = {
  name: "openai", providerId: "openai", protocol: "openai-chat", auth: "bearer", implicit: true, isDefault: false,
  activeSource: "env:OPENAI_API_KEY", hasStoredFile: true, hasStoredOAuth: false, credentialRequired: true,
};
assert.equal(client.activeSourceLabel(envInstance), "Configured via environment variable (OPENAI_API_KEY)");
assert.deepEqual(client.credentialLayers(envInstance).map((layer) => layer.source), ["env:OPENAI_API_KEY", "store"]);
assert.equal(client.keylessByDesign({ ...envInstance, activeSource: "none", credentialRequired: false }), true);
assert.equal(client.unconfiguredLabel({ ...envInstance, activeSource: "none" }), "Not configured");
assert.equal(client.styleInfoText({ ...envInstance, baseUrl: "https://api.example" }), "openai-chat · base https://api.example");
assert.deepEqual(client.groupByProvider([envInstance]).map((group) => group.providerId), ["openai"]);
assert.equal(client.safeCredentialTestResult("openai", { status: "bogus" }).status, "endpoint_failure");
assert.equal(client.safeCredentialTestMessage("success"), "Credentials verified.");
assert.equal(client.isEndpointConflict(new Error("conflict")), false);
assert.equal(client.fingerprintUnavailable({ ...envInstance, baseUrl: "https://api.example" }), true);
assert.equal(typeof client.ENDPOINT_CHANGED_TEST_MESSAGE, "string");
assert.equal(client.basename("/home/me/proj/"), "proj");
assert.equal(client.parentOf("/home/me"), "/home");
assert.equal(client.childrenPrefix("/home/me"), "/home/me/");
assert.equal(client.isDirEntry("/home/me/src/"), true);
const pathRows = client.buildPathRows({
  kind: "file", currentDir: "/home/me", entries: ["/home/me/src/", "/home/me/notes.md"],
  value: "/home/me/notes.md", recents: ["/home/me/proj"], showRecents: true,
});
assert.deepEqual(pathRows.map((row) => row.kind), ["group", "recent", "group", "parent", "dir", "file"]);
assert.deepEqual(client.pickablePathRows(pathRows).map((row) => row.path), ["/home/me/proj", "/home", "/home/me/src", "/home/me/notes.md"]);
assert.equal(client.parseTaskListData(null), null);
assert.deepEqual(client.parseTaskListData([]), []);
const taskRows = client.parseTaskListData([
  { id: 1, type: "implement", description: "a", prompt: "", status: "done" },
  { id: 2, type: "verify", description: "b", prompt: "", status: "open" },
]);
assert.deepEqual(taskRows.map((row) => row.id), [1, 2]);
assert.equal(client.taskAggregateLabel({ total: 2, done: 1 }), "1 of 2 tasks left");
assert.deepEqual(client.groupTasks(taskRows).settled.map((row) => row.id), [1]);
const tasksPanel = client.createTasksPanelStore(async () => [{ id: 3, type: "verify", description: "c", prompt: "", status: "open" }]);
tasksPanel.refresh("local:smoke", () => false).then((result) => { assert.equal(result.kind, "rows"); assert.deepEqual(tasksPanel.getState().entries.get("local:smoke").rows.map((row) => row.id), [3]); });
assert.equal(client.classifyTasksRejection(new client.WireError("thread not found: x", -32000, { evenerErrorInfo: "sessionUnavailable" }), false).kind, "empty");
assert.equal(client.relativeTime("2026-08-09T12:00:00Z", new Date("2026-08-09T12:37:00Z")), "37m ago");
assert.equal(client.absoluteTime("not-a-date"), "not-a-date");
const launchOption = { field: "skillsDirs", wireField: "skillsDirs", label: "Skill directories", group: "Resources", kind: "pathList" };
assert.deepEqual(client.collectConfig([launchOption], client.buildFormState([launchOption], { skillsDirs: ["/opt/skills"] })), { skillsDirs: ["/opt/skills"] });
assert.deepEqual(client.inheritedItems(["/a", "/b"], ["/a"], (item) => item, client.asStringList), ["/b"]);
client.validatePathListAdd(launchOption, ["/opt/skills"], "/opt/skills", async () => ({ valid: true })).then((outcome) => assert.deepEqual(outcome, { ok: false, error: "Already added." }));
// The launch-config gateway over a two-member client port: the schema read is cached per store, the layer read is not.
const launchCalls = [];
const launchStore = client.createLaunchConfigStore({
  request: async (method, params) => { launchCalls.push(method); return method === "evener/launch/schema" ? { options: [launchOption] } : params; },
  onNotification: () => () => {},
});
Promise.all([launchStore.getState().schema(), launchStore.getState().schema(), launchStore.getState().getLayer("/", "global")]).then(([schema, , layer]) => {
  assert.equal(schema.options[0].wireField, "skillsDirs");
  assert.deepEqual(layer, { cwd: "/", layer: "global" });
  assert.deepEqual(launchCalls, ["evener/launch/schema", "evener/launch/getLayer"]);
});
assert.equal(new client.LaunchSettings(null, "/", "global").getSnapshot().dirty, false);
assert.equal(client.findBuiltinArgument([{ id: "anthropic/claude-x", label: "Claude X" }], " claude x ")?.id, "anthropic/claude-x");
assert.equal(client.matchBuiltinInvocation("/goal fix it", [{ id: "goal", args: { kind: "free" } }])?.argsText, "fix it");
const displayConfig = client.makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }, { tokenCounts: true });
assert.equal(client.advancedEnabledCount(displayConfig), 1);
assert.equal(client.accessibleConfigSummary(client.shippedMobileConfig), "Intent");
assert.equal(client.accessibleConfigSummary(displayConfig), "Tools · 1 advanced");
assert.deepEqual(client.presetContent("chat"), { toolIntent: true, toolCalls: false, reasoning: false, expandByDefault: false });
assert.deepEqual(client.decodeLocalConfig(client.encodeLocalConfig(displayConfig)), displayConfig);
assert.equal(client.resolveEffectiveConfig({ local: null, hub: client.shippedDefault("desktop") }).content.level, "tools");
assert.equal(client.visibleCategoryInventory(displayConfig).visible.includes("tokenCounts"), true);
const projectorTurn = { id: "turn1", status: "completed", items: [{ id: "item1", type: "userMessage", text: "hi" }] };
const projection = client.projectThread({ turns: [projectorTurn] }, displayConfig);
assert.equal(projection.turns[0].entries[0].kind, "item");
assert.equal(projection.turns[0].entries[0].id, "item1");
assert.equal(typeof client.ACTION_SUMMARY_UNAVAILABLE, "string");
assert.equal(client.legacyConfigFromValues({ transcriptHookExitsAll: "1" })?.advanced.hookExits, "all");
assert.deepEqual(client.resolveScalars({ model: "openai/gpt-5", reasoningEffort: "low" }, { model: "anthropic/claude", reasoningEffort: "" }), { model: "anthropic/claude", reasoningEffort: "low" });
assert.deepEqual(client.withPluginSelection({ enabledPlugins: ["old"], model: "m" }, { mode: "explicit", names: ["a", "b"] }), { model: "m", enabledPlugins: ["a", "b"] });
assert.equal(client.harnessUsesEvenerModels("external", [{ id: "external", label: "external", kind: "external" }]), false);
const hubOverview = client.createHubOverviewStore({ request: async () => ({ hub: { pastIndex: { path: "/index" } }, mcpDiscovered: {} }) });
hubOverview.getState().fetch().then(() => assert.deepEqual(hubOverview.getState().data, { hub: { pastIndex: { path: "/index", count: 0, perPage: 0 } }, mcpDiscovered: { servers: [] }, agents: [] }));
// The keybinding group parses through a host-supplied KeybindingParser (tinykeys' parseKeybinding in both apps); this consumer supplies a plain-press one of the port's shape.
const keybindingParser = (keybinding) => keybinding.split(" ").map((press) => { const parts = press.split("+"); return [parts.slice(0, -1), [], parts[parts.length - 1]]; });
assert.equal(client.ACTIONS.paletteOpen, "palette.open");
assert.deepEqual(client.parseChord(keybindingParser, "Control+K"), [{ modifiers: ["Control"], optionalModifiers: [], key: "K" }]);
const keybindingRegistry = client.createKeybindingsRegistry(keybindingParser);
assert.equal(keybindingRegistry.getState().registerBinding({ id: "probe", actionId: client.ACTIONS.sessionNext, chord: "Alt+ArrowRight" }).scope, client.GLOBAL_SCOPE);
assert.deepEqual(client.defaultBindingChordsForAction(keybindingParser, client.ACTIONS.sessionPrevious), [{ id: "session.previous", scope: "global", serialized: "Alt+ArrowLeft" }]);
assert.equal(client.displayBindingFor(keybindingRegistry.getState().bindings, client.ACTIONS.sessionNext)?.id, "probe");
client.rebindAction(keybindingRegistry, client.ACTIONS.sessionNext, "Alt+ArrowUp");
assert.deepEqual(keybindingRegistry.getState().bindings.map((binding) => binding.id), ["session.next#override"]);
assert.equal(client.validateOverrideRules([{ action: "nope", chord: "Control+K" }], keybindingRegistry, "other").warnings[0].reason, "unknown-action");
// The settings-hub generation core over a scripted fence and a settings
// fields store: a defaults read publishes under one generation, a
// replacement generation fences the in-flight read and rewires notifications,
// and ending the generation retires the payload - a write caught mid-flight
// keeps its unknown outcome in writeUncertain.
const settingsFence = client.createReadyGenerationFence(() => true);
let settingsFields = { loaded: false, saving: false, hubLoading: false, writeUncertain: false, revision: 0 };
const wiredGenerations = [];
const settingsGeneration = client.createSettingsHubGeneration({
  fence: settingsFence,
  wireNotifications: (generation) => { wiredGenerations.push(generation); return () => wiredGenerations.pop(); },
  retirePayload: () => { settingsFields = { ...settingsFields, loaded: false, saving: false, hubLoading: false, writeUncertain: settingsFields.writeUncertain || settingsFields.saving, revision: 0 }; },
});
settingsGeneration.beginReadyGeneration();
const firstSettingsGeneration = settingsFence.generation;
const landingRead = settingsFence.claimRead();
settingsFields = { ...settingsFields, loaded: true, revision: 3 };
settingsGeneration.beginReadyGeneration();
assert.equal(settingsFence.readStillMine(firstSettingsGeneration, landingRead), false);
assert.deepEqual(wiredGenerations, [settingsFence.generation]);
settingsFields = { ...settingsFields, saving: true };
settingsGeneration.endReadyGeneration();
assert.equal(settingsFence.generation, -1);
assert.deepEqual(wiredGenerations, []);
assert.deepEqual(settingsFields, { loaded: false, saving: false, hubLoading: false, writeUncertain: true, revision: 0 });
// The checkpointed draft editor over a scripted in-memory checkpoint port:
// an edit persists a fresh checkpoint, the discardable gate refuses a
// mid-write discard, discarding removes the record and clears the draft
// fields, and a port save failure marks storageUnavailable AND draftError.
let editorFields = { saving: false, writeUncertain: false, storageUnavailable: false, draftUnreadable: false, draft: null, draftConflict: false, draftError: null };
const setEditorFields = (partial) => { editorFields = { ...editorFields, ...partial }; };
let storedDraft = null;
let draftSaveFailure = null;
let mintedDraftIds = 0;
const draftRepo = {
  createId: () => "draft-" + (mintedDraftIds += 1),
  save: (checkpoint) => { if (draftSaveFailure) throw draftSaveFailure; storedDraft = checkpoint; return true; },
  discardClassified: () => { const removed = storedDraft !== null; storedDraft = null; return removed; },
};
const savedDraft = client.persistCheckpointedDraft(draftRepo, { value: "new draft" }, () => editorFields, setEditorFields, () => ({}), "draft save failed", "review the draft again");
assert.deepEqual(savedDraft, { id: "draft-1", value: "new draft" });
assert.deepEqual(storedDraft, { id: "draft-1", value: "new draft" });
setEditorFields({ draft: { value: "new draft" }, draftConflict: true });
setEditorFields({ saving: true });
assert.throws(() => client.assertDraftDiscardable({ disposed: false }, () => editorFields, "drafts unavailable"), /drafts unavailable/);
setEditorFields({ saving: false });
client.discardCheckpointedDraft(draftRepo, () => editorFields, setEditorFields, () => ({}), "draft discard failed");
assert.equal(storedDraft, null);
assert.deepEqual(editorFields, { saving: false, writeUncertain: false, storageUnavailable: false, draftUnreadable: false, draft: null, draftConflict: false, draftError: null });
draftSaveFailure = new Error("port unavailable");
assert.throws(() => client.persistCheckpointedDraft(draftRepo, { value: "second draft" }, () => editorFields, setEditorFields, () => ({}), "draft save failed", "review the draft again"), /draft save failed/);
assert.deepEqual(editorFields, { saving: false, writeUncertain: false, storageUnavailable: true, draftUnreadable: false, draft: null, draftConflict: false, draftError: "draft save failed" });
// The transcript display store over a scripted client port: a malformed GET
// publishes the read error and no state, a well-formed GET publishes both
// layouts' confirmed defaults, a change contradicting the confirmed revision
// is fenced while a newer one lands, and dropping hub support retires the
// payload and fences later changes.
assert.equal(client.transcriptDisplaySupport({ transcriptDisplaySettings: true }), "supported");
const transcriptCalls = [];
let transcriptReply = {};
const transcriptStore = client.createTranscriptDisplayStore({
  client: { request: async (method) => { transcriptCalls.push(method); return transcriptReply; }, onNotification: () => () => {} },
});
transcriptStore.setSupport("supported");
transcriptStore.beginReadyGeneration();
transcriptStore.getState().refreshHubDefaults().then(() => {
  assert.equal(transcriptStore.getState().hubError, "Hub returned malformed transcript display defaults");
  assert.equal(transcriptStore.getState().hubLoading, false);
  assert.equal(transcriptStore.getState().loaded, false);
  transcriptReply = {
    desktop: client.toWireDefault({ revision: 3, config: client.makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }) }),
    mobile: client.toWireDefault({ revision: 2, config: client.makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }) }),
  };
  return transcriptStore.getState().refreshHubDefaults();
}).then(() => {
  assert.deepEqual(transcriptCalls, ["evener/settings/transcriptDisplay/get", "evener/settings/transcriptDisplay/get"]);
  assert.equal(transcriptStore.getState().hubError, null);
  assert.deepEqual(transcriptStore.getState().hubErrors, {});
  assert.equal(transcriptStore.getState().loaded, true);
  assert.equal(transcriptStore.getState().hub.desktop.revision, 3);
  assert.equal(transcriptStore.getState().hub.mobile.revision, 2);
  const changedConfig = client.makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  transcriptStore.getState().applyHubChange({ layout: "mobile", revision: 1, config: changedConfig });
  assert.equal(transcriptStore.getState().hub.mobile.revision, 2);
  transcriptStore.getState().applyHubChange({ layout: "mobile", revision: 5, config: changedConfig });
  assert.equal(transcriptStore.getState().hub.mobile.revision, 5);
  transcriptStore.setSupport("unsupported");
  transcriptStore.getState().applyHubChange({ layout: "desktop", revision: 9, config: changedConfig });
  assert.equal(transcriptStore.getState().hubSupport, "unsupported");
  assert.deepEqual(transcriptStore.getState().hub, {});
  assert.equal(transcriptStore.getState().loaded, false);
}).catch((error) => { console.error(error); process.exit(1); });
`;
  // The twelve storage-port methods neither outbox fixture exercises: the
  // type-use program and the smoke script embed this one definition and add the
  // two calls each of them actually makes (enqueueIntent, listTargetRefs).
  const inertOutboxStorageMethods = `  getOutbox: () => Promise.resolve(undefined),
  enqueueInterruptAndCancel: () => Promise.reject(new Error("inert")),
  getOptimistic: () => Promise.resolve(undefined),
  listOptimistic: () => Promise.resolve([]),
  getRecovery: () => Promise.resolve(undefined),
  nextDispatchable: () => Promise.resolve(undefined),
  markAttempted: () => Promise.resolve(false),
  markUnknown: () => Promise.resolve(false),
  settleReceipt: () => Promise.resolve(false),
  settleApplied: () => Promise.resolve(false),
  restoreProvenAbsent: () => Promise.resolve([]),
  transferToRecovery: () => Promise.resolve(undefined),`;
  // The qualification manifest: every specifier package.json publishes, with
  // the hand-written probes run against it; the names it promises are read off
  // its entry module below. A subpath with no entry here is not qualified,
  // whatever the exports map claims, so the two must agree. An in-repo-only
  // path alias is therefore unlistable: nothing in the tarball backs it, and
  // the apps' own typecheck is what validates it.
  const packageExports = {
    ".": {
      // A typed construction for the specifiers that offer one, so the
      // declaration checks prove more than that the names resolve.
      esmTypeUses: `const client: AppwireClient = new AppwireClient({ url: "ws://127.0.0.1:1/rpc" });
const version: string = APPWIRE_PROTOCOL_VERSION; void client; void version;`,
      cjsTypeUses: `const app: client.AppwireClient = new client.AppwireClient({ url: "ws://127.0.0.1:1/rpc" }); void app;`,
      smoke: rootSmokeCalls,
    },
    // The doc-pane data layer, published as its own specifier because
    // readDocFile is not a root export: it needs a fetch, and a consumer that
    // wants to substitute one (or spy on the module) needs a real subpath to
    // import, which a root re-export cannot give it.
    "./docContent": {
      esmTypeUses: `const read: (session: string, path: string, port: DocPort) => Promise<DocFileContent> = readDocFile;
const cap: number = DOC_FILE_MAX_BYTES; void read; void cap;`,
      cjsTypeUses: `const fetchDoc: client.DocFetch = async (url: string) => {
  void url;
  throw new client.DocFileError("error", 500);
};
const port: client.DocPort = { origin: "https://hub.example", fetch: fetchDoc }; void port;`,
      // The URL builders and the size cap are the whole callable surface here:
      // readDocFile needs a port, and qualification makes no requests, so it is
      // checked for presence and its behavior is covered by unit tests. The
      // builders are called with both bases the two adapters supply, so the
      // same-origin web string and the native absolute URL are both qualified.
      smoke: `assert.equal(client.docFileRawURL("", "s", "p"), "/doc/file?format=raw&session=s&path=p");
assert.equal(client.docImageURL("", "s", "p"), "/doc/image?session=s&path=p");
assert.equal(
  client.docFileRawURL("https://hub.example", "s", "p"),
  "https://hub.example/doc/file?format=raw&session=s&path=p",
);
assert.equal(client.docImageURL("https://hub.example", "s", "p"), "https://hub.example/doc/image?session=s&path=p");
assert.equal(client.DOC_FILE_MAX_BYTES, 512 * 1024);
assert.equal(typeof client.readDocFile, "function");
`,
    },
    // The connection state layer: the client-swap safety and
    // notification-following a host's own connection store wraps, over an
    // AppwireClientLike only - no handshake, no view binding, so a bare Node
    // consumer needs no socket. AppwireClientLike and FeatureSet are root
    // exports, not this barrel's own surface, so the type-use program imports
    // them itself rather than relying on the generated import block.
    "./state/connection": {
      esmTypeUses: `import type { AppwireClientLike, FeatureSet } from "@evener/appwire-client";
const fakeClient: AppwireClientLike = {
  connect: () => Promise.resolve({ serverInfo: { name: "q", version: "0" }, protocolVersion: "v", sourceId: "q", features: {} as FeatureSet }),
  request: () => Promise.reject(new Error("offline")),
  forceStop: () => Promise.resolve(),
  resumeThread: () => Promise.reject(new Error("offline")),
  onNotification: () => () => undefined,
  onReady: () => () => undefined,
  onStateChange: () => () => undefined,
  retryNow: () => undefined,
  state: "idle",
  terminalReason: null,
};
const store: ConnectionStore = createConnectionStore();
const state: ConnectionStoreState = store.getState();
store.connect(fakeClient);
void state;
const stop = onConnectionNotification(store, () => undefined);
stop();`,
      cjsTypeUses: `const connectionState: client.ConnectionStoreState = client.createConnectionStore().getState(); void connectionState;`,
      // A store with no client wired starts idle; connect() mirrors an
      // already-"ready" client's state (a reconnect scenario) and wires its
      // client-swap safety; onConnectionNotification follows the client the
      // store holds - all without opening a socket.
      smoke: `let notified = 0;
const readyClient = {
  stateChangeHandlers: new Set(),
  notificationHandlers: new Set(),
  state: "ready",
  terminalReason: null,
  connect: () => Promise.resolve({ serverInfo: { name: "q", version: "0" }, protocolVersion: "v", sourceId: "q", features: {} }),
  request: () => Promise.reject(new Error("offline")),
  forceStop: () => Promise.resolve(),
  resumeThread: () => Promise.reject(new Error("offline")),
  onNotification(cb) {
    this.notificationHandlers.add(cb);
    return () => this.notificationHandlers.delete(cb);
  },
  onReady: () => () => undefined,
  onStateChange(cb) {
    this.stateChangeHandlers.add(cb);
    return () => this.stateChangeHandlers.delete(cb);
  },
  retryNow: () => undefined,
};
const connectionStore = client.createConnectionStore();
assert.equal(connectionStore.getState().state, "idle");
assert.equal("connect" in connectionStore.getState(), false);
connectionStore.connect(readyClient);
assert.equal(connectionStore.getState().state, "ready");
assert.equal(connectionStore.getState().client, readyClient);
const stopNotifications = client.onConnectionNotification(connectionStore, () => {
  notified += 1;
});
for (const cb of readyClient.notificationHandlers) cb({ method: "evener/plugin/updated", params: {} });
assert.equal(notified, 1);
stopNotifications();
`,
    },
    // The navigation state layer, published as one subpath rather than through
    // the root: both apps' navigation stores are built on it, it is not part of
    // the client surface every consumer takes, and a barrel is the seam later
    // state relocations extend rather than multiply.
    "./state/navigation": {
      esmTypeUses: `const key: ResourceKey = { kind: "section", section: "live", offset: 0, limit: 50 };
const graph: NavigationGraph = normalizedGraphFromSnapshot({ metadata: {}, entities: [], containers: [] }); void key; void graph;
const waiter: NavigationInvalidationWaiter | undefined = undefined; void waiter;
const memoryExpansion = new Map<string, boolean>();
const navigationPersistence: NavigationPersistence = { readExpansion: () => new Map(memoryExpansion), writeExpansion: () => undefined };
const navigation: NavigationStore = createNavigationStore({ persistence: navigationPersistence });
const navigationState: NavigationStoreState = navigation.getState();
const navigationSources: ReturnType<typeof selectSources> = selectSources(navigationState); void navigationSources;`,
      cjsTypeUses: `const invalid: client.NavigationBaseInvalidError = new client.NavigationBaseInvalidError(); void invalid;
const navigationStoreState: client.NavigationStoreState = client.createNavigationStore({ persistence: { readExpansion: () => new Map(), writeExpansion: () => undefined } }).getState(); void navigationStoreState;`,
      // One call per module: types (keyID, the offset rule), immutable (the
      // freeze and the equality), codec (a snapshot normalized into a graph),
      // merge (an empty delta onto a manifest that carries no metadata, which
      // the merge refuses as an invalid base), invalidation (the wildcard
      // reaching a project page), revalidator (an instance carries its
      // generation), store (a store built over a memory persistence port
      // reads its expansion at creation, writes a toggle back through the
      // port, and re-reads the port on reset - with no client wired, so
      // nothing opens a socket), selectors (an unloaded store names no launch
      // sources and no needs-you rows, and the row age formatter is pure).
      smoke: `assert.equal(client.keyID({ kind: "section", section: "live", offset: 0, limit: 50 }), '{"kind":"section","limit":50,"offset":0,"section":"live"}');
assert.equal(client.nextNavigationOffset(50, 25), 75);
assert.equal(client.isNavigationUnavailable(new Error("boom")), false);
assert.equal(client.equalJSON({ a: [1, { b: 2 }] }, { a: [1, { b: 2 }] }), true);
assert(Object.isFrozen(client.cloneAndDeepFreezeJSON({ a: [1] }).a));
const navigationGraph = client.normalizedGraphFromSnapshot({ metadata: { revision: 1 }, entities: [], containers: [] });
assert.equal(navigationGraph.metadata.revision, 1);
assert.equal(client.normalizeSnapshot({ metadata: {}, entities: [], containers: [] }).entities.size, 0);
const navigationVersion = { generationId: "g", revision: 1, etag: "e" };
const emptyDelta = { upsertedEntities: [], removedEntityKeys: [], upsertedContainers: [], removedContainerKeys: [] };
assert.throws(
  () => client.applyDelta({ key: { kind: "manifest" }, graph: navigationGraph, version: navigationVersion }, emptyDelta, navigationVersion),
  client.NavigationBaseInvalidError,
);
const projectPage = { kind: "project_page", projectKey: "p", tier: "current", offset: 0, limit: 50 };
assert.equal(client.matchesTarget(projectPage, { kind: "all_loaded_projects" }), true);
const revalidator = new client.NavigationRevalidator("g");
assert.equal(revalidator.generationID, "g");
revalidator.dispose();
assert.equal(client.projectNodeExpansionKey("p"), "projectnode:p");
const seededExpansion = new Map([["projectnode:p", true]]);
const navigationStore = client.createNavigationStore({
  persistence: { readExpansion: () => new Map(seededExpansion), writeExpansion: (m) => seededExpansion.clear() || m.forEach((v, k) => seededExpansion.set(k, v)) },
});
assert.equal(navigationStore.getState().expanded.get("projectnode:p"), true);
assert.equal(navigationStore.getState().mode, "unknown");
navigationStore.getState().toggleExpanded("projectnode:p");
assert.equal(seededExpansion.get("projectnode:p"), false);
navigationStore.reset();
assert.equal(navigationStore.getState().expanded.get("projectnode:p"), false);
assert.deepEqual(client.selectSources(navigationStore.getState()), []);
assert.equal(client.selectNeedsYouCount(navigationStore.getState()), 0);
assert.equal(client.relativeAge(new Date().toISOString()), "now");
`,
    },
    // The credentials state layer: the listing core each app's Providers &
    // credentials store is an adapter over. A store is built and driven
    // without a connection, which is the whole of what qualification can do
    // to it: no request is issued, so the smoke proves the factory, the
    // pure helpers and the refusal type resolve and behave.
    "./state/credentials": {
      esmTypeUses: `const store: CredentialInstancesStore = createCredentialInstancesStore({ ownClientId: () => "qualification" });
const held: boolean = staleListingHeld(store.getState());
const listing: CredentialListing = listingOf(store.getState()); void held; void listing;`,
      cjsTypeUses: `const refusal: client.StaleListingRefusal = new client.StaleListingRefusal(); void refusal;`,
      smoke: `const credentialStore = client.createCredentialInstancesStore({ ownClientId: () => "qualification" });
assert.deepEqual(credentialStore.getState().instances, []);
assert.equal(credentialStore.getState().listingFromPreviousConnection, false);
assert.equal(credentialStore.getState().listingEstablished, false);
assert.equal(
  client.foreignListingChange({ ...credentialStore.getState(), loading: true }, credentialStore.getState()),
  true,
);
assert.equal(typeof credentialStore.getState().setApiKey, "function");
assert.equal(typeof credentialStore.getState().devicePoll, "function");
assert.deepEqual(client.listingOf(credentialStore.getState()), {
  instances: [],
  availableProviders: [],
  diagnostics: [],
  userLayer: "",
  writesRefused: false,
});
credentialStore.connectionChanged(null, "idle");
assert.equal(client.staleListingHeld({ instances: [], availableProviders: [], listingFromPreviousConnection: true }), false);
assert.equal(client.isStaleListingRefusal(new client.StaleListingRefusal()), true);
assert.equal(client.isStaleListingRefusal(new Error("boom")), false);
assert.rejects(credentialStore.getState().fetch(), /no client connected/).catch((err) => {
  console.error(err);
  process.exit(1);
});
Promise.all([
  assert.rejects(credentialStore.getState().fetch(), /no client connected/),
  assert.rejects(credentialStore.getState().authStatus("work"), /no client connected/),
]).catch((err) => {
  console.error(err);
  process.exit(1);
});
`,
    },
    // The extensions state layer - the marketplaces, installed-plugins and
    // launch-layer stores - published as one subpath for the same reason
    // state/navigation is: a layer both apps build their settings surfaces on,
    // not part of the client surface every consumer takes.
    "./state/extensions": {
      esmTypeUses: `const marketplacesClient: MarketplacesClient = { request: () => Promise.reject(new Error("offline")), onNotification: () => () => undefined };
const marketplaces: MarketplacesStore = createMarketplacesStore(marketplacesClient);
const entry: MarketplaceCatalogEntry = { status: "loading" }; void marketplaces; void entry;
const pluginsClient: PluginsClient = marketplacesClient;
const plugins: PluginsStore = createPluginsStore(pluginsClient);
const revision: ListRevision = createListRevision(); void plugins; void revision;
const keyed: KeyedRevision = createKeyedRevision(); void keyed.issue("acme");
const lifecycle: StoreLifecycle<PluginsState> = createStoreLifecycle(pluginsClient, { method: "evener/plugin/updated", debounceMs: 250, store: () => plugins, refetch: (state) => state.fetchPlugins(), wantsList: (state) => state.plugins !== null }); void lifecycle;
const layerClient: LaunchLayerClient = marketplacesClient;
const layer: LaunchLayerStore = createLaunchLayerStore(layerClient); void layer;`,
      cjsTypeUses: `const marketplacesState: client.MarketplacesState = client.createMarketplacesStore({ request: () => Promise.reject(new Error("offline")), onNotification: () => () => undefined }).getState(); void marketplacesState;
const pluginsState: client.PluginsState = client.createPluginsStore({ request: () => Promise.reject(new Error("offline")), onNotification: () => () => undefined }).getState(); void pluginsState;
const layerState: client.LaunchLayerState = client.createLaunchLayerStore({ request: () => Promise.reject(new Error("offline")), onNotification: () => () => undefined }).getState(); void layerState;`,
      // Stores built over a client that rejects everything: each list fetch
      // records the rejection as state and resolves, a mutation rejects, and
      // a browse caches the failure - the two conventions the layer keeps. A
      // promise chain rather than await: the CommonJS consumer has no
      // top-level await, and a failure inside exits the consumer non-zero.
      smoke: `const offline = { request: () => Promise.reject(new Error("offline")), onNotification: () => () => undefined };
const marketplacesStore = client.createMarketplacesStore(offline);
assert.equal(client.MARKETPLACE_REFETCH_DEBOUNCE_MS, 250);
const pluginsStore = client.createPluginsStore(offline);
assert.equal(client.PLUGIN_REFETCH_DEBOUNCE_MS, 250);
assert.equal(pluginsStore.getState().pluginRevision, 0);
pluginsStore.connectionChanged(offline, "ready");
const keyed = client.createKeyedRevision();
const keyedRevision = keyed.issue("acme");
keyed.retire("acme");
assert.equal(keyed.current("acme", keyedRevision), false);
const layerStore = client.createLaunchLayerStore(offline);
assert.equal(client.LAUNCH_LAYER_REFETCH_DEBOUNCE_MS, 250);
assert.equal(layerStore.getState().launchLayer, null);
const listRevision = client.createListRevision();
const first = listRevision.next();
listRevision.fence();
let fencedAnswerPublished = false;
listRevision.publish(first, () => {
  fencedAnswerPublished = true;
});
assert.equal(fencedAnswerPublished, false);
marketplacesStore
  .getState()
  .fetchMarketplaces()
  .then(() => {
    assert.equal(marketplacesStore.getState().marketplacesError, "offline");
    return assert.rejects(marketplacesStore.getState().removeMarketplace("acme"), /offline/);
  })
  .then(() => marketplacesStore.getState().browseMarketplace("acme"))
  .then(() => {
    assert.deepEqual(marketplacesStore.getState().browseCatalogs.get("acme"), { status: "error", error: "offline" });
    marketplacesStore.dispose();
    return pluginsStore.getState().fetchPlugins();
  })
  .then(() => {
    assert.equal(pluginsStore.getState().pluginsError, "offline");
    return assert.rejects(pluginsStore.getState().installPlugin("linter", "acme"), /offline/);
  })
  .then(() => {
    pluginsStore.dispose();
    return layerStore.getState().fetchLaunchLayer();
  })
  .then(() => {
    assert.equal(layerStore.getState().launchLayerError, "offline");
    return assert.rejects(layerStore.getState().setLaunchLayer({ pluginDirs: [] }), /offline/);
  })
  .then(() => layerStore.dispose())
  .catch((err) => {
    console.error(err);
    process.exit(1);
  });
`,
    },
    // The mutation state layer: the durable record shapes both apps' outboxes
    // store, the provenance rule their projections ask (did THIS client
    // submit it), the pure reconciliation that turns durable records plus a
    // live model into the rows a composer's queue renders, and the
    // pending-turns projection store built on that reconciliation. Published
    // as its own subpath because a host's storage and scheduling stay out of
    // the package - this is the shape, the rule, the reconciliation and the
    // store alone. Every call here is synchronous apart from the store's own
    // triple, and the store's ports are fakes: there is no client port to
    // script.
    "./state/mutation": {
      esmTypeUses: `const attachmentRef: MutationAttachmentRef = { presentationId: "p1", marker: 1, name: "shot.png", mediaType: "image/png" };
const intent: MutationIntent = { targetRef: "ref", method: "turn/start", payload: {}, attachments: [attachmentRef], optimisticDisplay: null };
const record: MutationRecord = { ...intent, version: 1, clientMutationId: "cmid", intentSequence: 0, createdAt: 0 };
const outboxRecord: MutationOutboxRecord = { ...record, state: "submitting" };
const optimisticRecord: MutationOptimisticRecord = { ...record, state: "accepted" };
const recoveryRecord: MutationRecoveryRecord = { ...outboxRecord, recoveryKind: "rejected" };
const outboxState: MutationOutboxState = outboxRecord.state;
const recoveryKind: MutationRecoveryKind = recoveryRecord.recoveryKind;
const storage: ClientIdentityStorage = { getItem: () => null, setItem: () => undefined };
const pendingMethod: PendingMethod = "send";
const pendingState: PendingTurnState = "submitting";
const pendingEntry: PendingTurnEntry = { id: "cmid", ref: "ref", method: pendingMethod, text: "hi", imageCount: 0, skillNames: [], state: pendingState, source: "outbox", fromThisClient: true };
const secureRandomSource: SecureRandomSource = { getRandomValues: (array) => array };
const identity: ClientIdentity = createClientIdentity(storage, secureRandomSource);
const threadsPort: PendingTurnsThreadsPort = { getThreadModel: () => undefined };
const draftPort: PendingTurnsDraftPort = {
  readDraftRevision: () => 0,
  readComposerDraft: () => ({ text: "", skillNames: [] }),
  clearDraft: () => undefined,
};
const pendingTurnsStore: PendingTurnsStore = createPendingTurnsStore({ threads: threadsPort, draft: draftPort, identity });
const pendingTurnsState: PendingTurnsState = pendingTurnsStore.getState();
const submittedDraft: SubmittedDraft = { draftRevisionAtStart: 0, text: "hi", skillNames: [] };
const outboxStorage: MutationOutboxStorage = {
  enqueueIntent: () => Promise.resolve(outboxRecord),
  listTargetRefs: () => Promise.resolve([outboxRecord.targetRef]),
${inertOutboxStorageMethods}
};
// A complete ready client, type-checked here: the outbox's lookup must accept
// a full AppwireClientLike, not just a state field. The runtime smoke below
// repeats it in plain JavaScript, where the same object cannot be annotated.
const readyClient: NonNullable<ReturnType<MutationClientLookup>> = {
  connect: () => Promise.resolve({} as never),
  request: () => Promise.resolve({} as never),
  forceStop: () => Promise.resolve(),
  resumeThread: () => Promise.resolve({} as never),
  onNotification: () => () => undefined,
  onReady: () => () => undefined,
  onStateChange: () => () => undefined,
  retryNow: () => undefined,
  state: "ready",
  terminalReason: null,
};
const outboxOptions: MutationOutboxOptions = { getClient: () => readyClient, onDiscover: () => undefined };
const outbox: MutationOutbox = new MutationOutbox(outboxStorage, outboxOptions);
const reason: MutationDiscoveryReason = "enqueue";
const dispatcherOptions: MutationDispatcherOptions = { getClient: () => null };
// The dispatcher always supplies a target ref, so a consumer whose lookup
// requires one must stay assignable to its port; the ref-less outbox lookup
// (above) must not have loosened it.
const requiredRefDispatcherOptions: MutationDispatcherOptions = { getClient: (targetRef: string) => { void targetRef; return null; } };
const dispatcher: MutationDispatcher = new MutationDispatcher(outboxStorage, dispatcherOptions);
void intent; void record; void optimisticRecord; void recoveryRecord; void outboxState; void recoveryKind; void storage;
void pendingEntry; void identity; void pendingTurnsState; void submittedDraft; void secureRandomSource; void outbox; void reason; void dispatcher; void requiredRefDispatcherOptions; void readyClient;`,
      cjsTypeUses: `const storage: client.ClientIdentityStorage = { getItem: () => null, setItem: () => undefined }; void storage;
const pendingEntry: client.PendingTurnEntry = { id: "cmid", ref: "ref", method: "send", text: "hi", imageCount: 0, skillNames: [], state: "submitting", source: "outbox", fromThisClient: true }; void pendingEntry;
const secureRandomSource: client.SecureRandomSource = { getRandomValues: (array) => array }; void secureRandomSource;
const identity: client.ClientIdentity = client.createClientIdentity(storage, secureRandomSource); void identity;
const pendingTurnsState: client.PendingTurnsState = client.createPendingTurnsStore({
  threads: { getThreadModel: () => undefined },
  draft: { readDraftRevision: () => 0, readComposerDraft: () => ({ text: "", skillNames: [] }), clearDraft: () => undefined },
  identity,
}).getState(); void pendingTurnsState;
const channel: client.MutationOutboxChannel = {
  postMessage: () => undefined,
  close: () => undefined,
  addEventListener: () => undefined,
  removeEventListener: () => undefined,
};
void channel;`,
      // createClientIdentity is a factory, not a module singleton: two
      // instances over two storages get two identities, and one instance's
      // identity is memoized across repeated calls. Both take their random
      // source explicitly - no default to globalThis.crypto lives in the
      // package. createSecureUUID is the same helper mutation ids use; a
      // source with no randomUUID proves the getRandomValues fallback runs,
      // and a source with neither proves the non-crypto fallback runs rather
      // than throwing. reconcilePendingEntries needs no model to prove it
      // runs: an absent one is the same "no live projection yet" case a
      // fresh composer starts from. The pending-turns store is built over
      // fake threads/draft ports plus one of the identities below:
      // beginSubmission's guard, its release and the empty-projection read
      // all prove the store runs without a real thread store or composer-
      // draft storage behind it.
      smoke: `const identityStorage = { value: undefined, getItem() { return this.value ?? null; }, setItem(_key, value) { this.value = value; } };
const identityRandomSource = { getRandomValues: (array) => globalThis.crypto.getRandomValues(array) };
const identityA = client.createClientIdentity(identityStorage, identityRandomSource);
const identityB = client.createClientIdentity({ getItem: () => null, setItem: () => undefined }, identityRandomSource);
const firstId = identityA.ownClientId();
assert.equal(identityA.ownClientId(), firstId);
assert.notEqual(identityB.ownClientId(), firstId);
assert.equal(identityA.isOwnMutationRecord({ originClientId: firstId }), true);
assert.equal(identityA.isOwnMutationRecord({ originClientId: "someone-else" }), false);
assert.equal(identityA.isOwnMutationRecord({}), true);
const noRandomSourceIdentity = client.createClientIdentity({ getItem: () => null, setItem: () => undefined }, {});
assert.equal(typeof noRandomSourceIdentity.ownClientId(), "string");
assert.equal(client.createSecureUUID({ randomUUID: () => "native-id", getRandomValues: (array) => array }), "native-id");
const fallbackUUID = client.createSecureUUID({ getRandomValues: (array) => array });
assert.match(fallbackUUID, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
const insecureUUID = client.createSecureUUID({});
assert.match(insecureUUID, /^insecure-/);
assert.deepEqual(
  client.reconcilePendingEntries("ref", [], undefined, new Map(), identityA.isOwnMutationRecord),
  [],
);
const draftState = { revision: 0, text: "", skillNames: [] };
const pendingTurnsStore = client.createPendingTurnsStore({
  threads: { getThreadModel: () => undefined },
  draft: {
    readDraftRevision: () => draftState.revision,
    readComposerDraft: () => ({ text: draftState.text, skillNames: draftState.skillNames }),
    clearDraft: () => {
      draftState.text = "";
    },
  },
  identity: identityA,
});
assert.equal(pendingTurnsStore.beginSubmission("ref-a"), true);
assert.equal(pendingTurnsStore.beginSubmission("ref-a"), false);
pendingTurnsStore.endSubmission("ref-a");
assert.equal(pendingTurnsStore.beginSubmission("ref-a"), true);
assert.deepEqual(pendingTurnsStore.pendingTurnEntries("ref-a"), []);
// The outbox over a memory storage port: no channel, no lifecycle target and
// no timer, which is exactly what a host without them passes. One enqueue
// commits through the port and announces the ref it landed under, and stop()
// awaits the discovery it queued — all of it synchronous in shape (one promise
// chain, no timers), so a CommonJS consumer with no top-level await can prove
// it.
const enqueued = [];
const discovered = [];
const memoryOutboxStorage = {
  enqueueIntent(intent) {
    const record = { ...intent, version: 1, clientMutationId: "cmid-1", intentSequence: 0, createdAt: 0, state: "submitting" };
    enqueued.push(record);
    return Promise.resolve(record);
  },
  listTargetRefs: () => Promise.resolve(enqueued.map((record) => record.targetRef)),
${inertOutboxStorageMethods}
};
// A complete AppwireClientLike: the runtime consumer is plain JavaScript, so
// the double cannot be type-annotated here, but it supplies every member so a
// strict TypeScript consumer copying it would compile. Only its state field is
// read by the outbox below.
const readyClient = {
  connect: () => Promise.resolve({}),
  request: () => Promise.resolve({}),
  forceStop: () => Promise.resolve(),
  resumeThread: () => Promise.resolve({}),
  onNotification: () => () => undefined,
  onReady: () => () => undefined,
  onStateChange: () => () => undefined,
  retryNow: () => undefined,
  state: "ready",
  terminalReason: null,
};
const memoryOutbox = new client.MutationOutbox(
  memoryOutboxStorage,
  {
    getClient: () => readyClient,
    onDiscover: (targetRefs, reason) => {
      discovered.push({ targetRefs, reason });
    },
  },
);
memoryOutbox
  .start()
  .then(() =>
    memoryOutbox.enqueueIntent({
      targetRef: "local:thread-1",
      method: "turn/queue",
      payload: {},
      attachments: [],
      optimisticDisplay: null,
    }),
  )
  .then((record) => {
    assert.equal(record.clientMutationId, "cmid-1");
    return memoryOutbox.stop();
  })
  .then(() => {
    assert.deepEqual(
      discovered.map((entry) => entry.reason),
      ["startup", "enqueue"],
    );
    assert.deepEqual(discovered[1].targetRefs, ["local:thread-1"]);
  })
  .then(() => {
    // MutationDispatcher is a runtime export reachable over the same memory
    // port as the outbox above: with no client wired, nextDispatchable's own
    // inert undefined stops dispatchTargets before any transport attempt,
    // proving the export resolves at all - a real attempt needs a transport,
    // which is the unit suite's job, not qualification's.
    const dispatcher = new client.MutationDispatcher(memoryOutboxStorage, { getClient: () => null });
    return dispatcher.dispatchTargets(["local:thread-1"]);
  })
  .catch((err) => {
    console.error(err);
    process.exit(1);
  });
`,
    },
  };
  const publishedSpecifiers = Object.keys(packageManifest.exports);
  for (const specifier of publishedSpecifiers)
    assert(packageExports[specifier], `published specifier is not qualified: no manifest entry for ${specifier}`);
  for (const specifier of Object.keys(packageExports))
    assert(
      publishedSpecifiers.includes(specifier),
      `qualification manifest names ${specifier}, which package.json does not export`,
    );
  // Each specifier's entry module, from the declarations its exports entry
  // names: the surface the specifier promises is read off that module's source
  // (the root's index.ts and a subpath's barrel alike), and its installed
  // declarations anchor the reachability walk below.
  const installedDist = join(consumerDir, "node_modules", packageManifest.name, "dist");
  const entryDeclarations = publishedSpecifiers.map((specifier) => {
    const declarations = packageManifest.exports[specifier].types;
    const entryModule = shippedModules.find((module) => declarations === `./dist/${module}.d.ts`);
    assert(entryModule, `${specifier} publishes declarations no shipped module emits: ${declarations}`);
    Object.assign(packageExports[specifier], entrySurface(join(packageDir, `${entryModule}.ts`)));
    return join(installedDist, `${entryModule}.d.ts`);
  });
  // A module can be built, packed and listed here and still be unreachable: the
  // files list only decides what tsc emits, and the export checks below name
  // identifiers, not modules. Each published specifier's own installed
  // declarations are the honest record of what it re-exports, so a shipped
  // module qualifies by being some specifier's entry or by being re-exported
  // from one, each re-export resolved relative to the declaration that carries
  // it (declaration-reachability.mjs). A module no published specifier reaches
  // fails right here.
  const reachable = reachableModules(entryDeclarations, installedDist);
  for (const module of shippedModules)
    assert(reachable.has(module), `shipped module unreachable from every published specifier: ${module}`);
  // One ESM declaration consumer, one CommonJS declaration consumer and one
  // runtime presence check in each module form, per published specifier. Each
  // type is named only as an `import type` specifier, the one position that
  // does not instantiate it, so a generic type qualifies without anyone
  // supplying its arguments (an import alias cannot name an `export type`
  // re-export, and a tuple of bare names cannot name a generic).
  const declarationConsumers = [];
  const runtimeConsumers = [];
  const consumerNames = new Set();
  for (const [specifier, surface] of Object.entries(packageExports)) {
    const moduleSpecifier = `${packageManifest.name}${specifier.slice(1)}`;
    // The specifier itself, reversibly encoded: "./foo-bar" and "./foo/bar" are
    // different specifiers and must not write over each other's programs.
    const slug = specifier === "." ? "root" : `sub-${encodeURIComponent(specifier.slice(2))}`;
    const presenceLoop = `for (const name of ${JSON.stringify(surface.values)}) assert(name in client, \`missing export \${name} from ${moduleSpecifier}\`);\n`;
    const esmConsumer = `esm-${slug}.mts`;
    const commonjsConsumer = `commonjs-${slug}.cts`;
    const esmRuntime = `runtime-${slug}.mjs`;
    const commonjsRuntime = `runtime-${slug}.cjs`;
    for (const consumer of [esmConsumer, commonjsConsumer, esmRuntime, commonjsRuntime]) {
      assert(!consumerNames.has(consumer), `two specifiers generate the same consumer program: ${consumer}`);
      consumerNames.add(consumer);
    }
    declarationConsumers.push(esmConsumer, commonjsConsumer);
    runtimeConsumers.push(esmRuntime, commonjsRuntime);
    writeFileSync(
      join(consumerDir, esmConsumer),
      `import {
${surface.values.map((name) => `  ${name},`).join("\n")}
} from "${moduleSpecifier}";
import type {
${surface.types.map((name) => `  ${name},`).join("\n")}
} from "${moduleSpecifier}";
${surface.esmTypeUses ?? ""}
${surface.values.map((name) => `void ${name};`).join("\n")}
`,
    );
    writeFileSync(
      join(consumerDir, commonjsConsumer),
      `import client = require("${moduleSpecifier}");
${surface.cjsTypeUses ?? ""}
import type {
${surface.types.map((name) => `  ${name},`).join("\n")}
} from "${moduleSpecifier}";
${surface.values.map((name) => `void client.${name};`).join("\n")}
`,
    );
    writeFileSync(
      join(consumerDir, esmRuntime),
      `import assert from "node:assert/strict";
import * as client from "${moduleSpecifier}";
${presenceLoop}${surface.smoke ?? ""}`,
    );
    writeFileSync(
      join(consumerDir, commonjsRuntime),
      `const assert = require("node:assert/strict");
const client = require("${moduleSpecifier}");
${presenceLoop}${surface.smoke ?? ""}`,
    );
  }
  run(
    resolve(packageDir, "node_modules/.bin/tsc"),
    [
      "--strict",
      "--noEmit",
      "--module",
      "NodeNext",
      "--moduleResolution",
      "NodeNext",
      "--target",
      "ES2022",
      ...declarationConsumers,
    ],
    consumerDir,
  );
  for (const consumer of runtimeConsumers) run(process.execPath, [join(consumerDir, consumer)], consumerDir);
  runConsumerResolveCheck();
  const listing = run("tar", ["-tzf", tarball], consumerDir);
  for (const expected of [
    ...shippedModules.flatMap((module) => [`package/dist/${module}.js`, `package/dist/${module}.d.ts`]),
    "package/README.md",
    "package/examples/connection.mjs",
    "package/examples/inspect.mjs",
    "package/examples/discovery.mjs",
    "package/examples/discovery-cli.mjs",
    "package/examples/discovery-logic.mjs",
    "package/examples/private-output.mjs",
    // The attribution slashCompletion.ts's port requires. It sits outside dist/
    // and examples/, so it needs its own expectation here and its own clause in
    // the allowlist below.
    "package/LICENSES/beautiful-ui.txt",
  ])
    assert(listing.includes(`${expected}\n`), `missing ${expected}`);
  for (const entry of listing.trim().split("\n")) {
    assert(
      entry === "package/package.json" ||
        entry === "package/README.md" ||
        entry.startsWith("package/dist/") ||
        entry.startsWith("package/examples/") ||
        entry.startsWith("package/LICENSES/"),
      `unexpected shipped path ${entry}`,
    );
    assert(!entry.endsWith(".ts") || entry.endsWith(".d.ts"), `source leak ${entry}`);
  }
  // Run the shipped program from the installed tarball. Only the remote server
  // is scripted; imports, sockets, handshake, client requests and output are real.
  const serverProtocolVersion = "evener-appwire-v5";
  const fixtureCwd = "/fixture/project";
  const responses = new Map([
    ["model/list", { params: { cwd: fixtureCwd }, result: { data: [] } }],
    ["thread/list", { params: { limit: 20 }, result: { data: [], nextCursor: "next-page" } }],
    ["evener/launch/schema", { params: {}, result: { options: [] } }],
    [
      "evener/launch/resolve",
      { params: { cwd: fixtureCwd }, result: { effective: {}, layers: {}, provenance: {}, diagnostics: [] } },
    ],
  ]);
  let discoveryMode;
  const observedMethods = [];
  const controller = new AbortController();
  let serverError;
  const server = new WebSocketServer({ host: "127.0.0.1", port: 0 });
  server.on("connection", (socket) => {
    socket.on("message", async (data) => {
      try {
        const request = JSON.parse(data.toString());
        if (request.method === "initialized" && request.id === undefined) return;
        observedMethods.push(request.method);
        let result;
        if (request.method === "initialize") {
          assert.equal(request.params.clientInfo.name, "appwire-reference");
          assert.equal(request.params.protocolVersion, serverProtocolVersion);
          result = {
            serverInfo: { name: "package-qualification", version: "0.0.0" },
            protocolVersion: serverProtocolVersion,
            sourceId: "qualification-source",
            features: {
              threadList: true,
              threadTurnsList: true,
              turnStart: false,
              turnSteer: false,
              threadClear: false,
              threadShutdown: false,
              forkFromTurn: false,
              tasks: false,
              transcriptList: true,
              modelList: true,
              directoryComplete: true,
              auth: false,
            },
          };
        } else if (discoveryMode) {
          assert.equal(request.method, discoveryMode.method);
          assert.deepEqual(request.params, discoveryMode.params);
          await discoveryMode.beforeResponse?.();
          if (discoveryMode.error) {
            socket.send(JSON.stringify({ jsonrpc: "2.0", id: request.id, error: discoveryMode.error }));
            return;
          }
          if (discoveryMode.close) {
            socket.close();
            return;
          }
          result = discoveryMode.response;
        } else {
          const response = responses.get(request.method);
          assert(response, `unexpected method ${request.method}`);
          assert.deepEqual(request.params, response.params);
          result = response.result;
        }
        socket.send(JSON.stringify({ jsonrpc: "2.0", id: request.id, result }));
      } catch (error) {
        serverError ??= error;
        controller.abort(error);
      }
    });
  });
  server.on("error", (error) => {
    serverError ??= error;
    controller.abort(error);
  });
  try {
    await once(server, "listening", { signal: controller.signal });
    const address = server.address();
    assert(address && typeof address !== "string");
    const { stdout } = await promisify(execFile)(
      process.execPath,
      [join(consumerDir, "node_modules/@evener/appwire-client/examples/inspect.mjs")],
      {
        cwd: consumerDir,
        env: { ...process.env, EVENER_RPC_URL: `ws://127.0.0.1:${address.port}/rpc`, EVENER_CWD: fixtureCwd },
        signal: controller.signal,
        timeout: 15000,
        encoding: "utf8",
      },
    );
    assert.deepEqual(observedMethods, ["initialize", ...responses.keys()]);
    assert.deepEqual(JSON.parse(stdout), {
      protocolVersion: serverProtocolVersion,
      sourceId: "qualification-source",
      models: 0,
      sessionsOnFirstPage: 0,
      hasMoreSessions: true,
      launchOptions: 0,
      resolvedLayers: [],
      repositoryTrust: "absent",
      diagnostics: 0,
    });

    await runInstalledDiscoveryContracts({
      consumerDir,
      rpcURL: `ws://127.0.0.1:${address.port}/rpc`,
      fixtureCwd,
      observedMethods,
      signal: controller.signal,
      setMode: (mode) => {
        discoveryMode = mode;
      },
    });
  } catch (error) {
    throw serverError ?? error;
  } finally {
    for (const socket of server.clients) socket.terminate();
    await new Promise((resolveClose) => server.close(resolveClose));
  }
  console.log(`qualified ${packed.name}@${packed.version}: installed imports, declarations and read-only example`);
}

// Every app tree imports this package by name but declares no dependency on
// it: each resolves the name through a repo alias onto TypeScript source, so
// `make test-web` and `make test-native` can be green while the installed
// tarball is missing an export. This answers that question in the one place it
// can be answered - a real consumer with the tarball in node_modules. The
// program is generated from what the apps actually import, read off their
// graph, so there is no fixture to fall behind: a specifier with values is
// imported by name, one used only as a whole module is imported for effect.
function runConsumerResolveCheck() {
  const programFile = "resolve-imports.mjs";
  const usage = consumerPackageUsage(resolve(packageDir, "..", ".."));
  // Each name is aliased under its specifier's index: docImageURL is a value of
  // both the root and ./docContent, and importing it twice under one name would
  // not compile.
  const program = `${[...usage]
    .map(([specifier, entry], index) => {
      if (entry.values.length === 0) return `import "${specifier}";`;
      const locals = entry.values.map((name) => `v${index}_${name}`);
      const imported = entry.values.map((name, at) => `${name} as ${locals[at]}`);
      return `import { ${imported.join(", ")} } from "${specifier}";\nvoid [${locals.join(", ")}];`;
    })
    .join("\n")}\n`;
  writeFileSync(join(consumerDir, programFile), program);
  run(process.execPath, [join(consumerDir, programFile)], consumerDir);
}

try {
  await qualify();
  // Only this invocation's privately created consumer is reclaimed.
  rmSync(consumerDir, { recursive: true });
} catch (error) {
  if (error?.stdout) process.stderr.write(error.stdout);
  if (error?.stderr) process.stderr.write(error.stderr);
  console.error(`Package qualification fixture retained at ${consumerDir}`);
  throw error;
}
