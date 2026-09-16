import assert from "node:assert/strict";
import { execFile, execFileSync } from "node:child_process";
import { once } from "node:events";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { WebSocketServer } from "ws";
import { consumerPackageUsage, rootSurface } from "./consumer-value-imports.mjs";
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
  // The root's runtime values and exported types, derived from index.ts so
  // the qualification surface cannot drift from what the entry point exports;
  // an export added there is qualified without editing this file. The smoke
  // CALLS below stay hand-written -- they are behaviour probes, not a surface.
  const { values: rootValues, types: rootTypes } = rootSurface(join(packageDir, "index.ts"));
  // One call per shipped module, with a trivial input. Importing alone would
  // pass for a module that needs a browser global at load time; calling proves
  // each module actually evaluates and runs inside a bare Node consumer.
  const rootSmokeCalls = `assert.equal(typeof client.AppwireClient, "function");
assert.equal(client.rpcURLFromLocation({ protocol: "https:", host: "hub.example:9180" }), "wss://hub.example:9180/rpc");
assert.equal(client.composeAskAnswers([]), "[answers]");
const askItem = {
  id: "ask1", turnId: "t1", type: "commandExecution", toolName: "ask_user", status: "completed",
  argumentsJSON: '{"questions":[{"header":"DB","question":"Which store?","options":[{"label":"SQLite","detail":"one file"}]}]}',
};
assert.equal(client.parseAskUserQuestions(askItem)?.[0].question, "Which store?");
assert.equal(client.parseAskUserQuestions({ argumentsJSON: "not json" }), undefined);
assert.equal(client.liveAskQuestions({ turns: [{ items: [askItem] }] })[0].key, "ask1:0");
const askBatches = client.reconcileBatches([], client.liveAskQuestions({ turns: [{ items: [askItem] }] }), () => "batch1");
assert.equal(askBatches[0].id, "batch1");
assert.equal(askBatches[0].questions[0].key, "ask1:0");
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
assert.deepEqual(client.pickableRows(pathRows).map((row) => row.path), ["/home/me/proj", "/home", "/home/me/src", "/home/me/notes.md"]);
assert.equal(client.parseTaskListData(null), null);
assert.deepEqual(client.parseTaskListData([]), []);
const taskRows = client.parseTaskListData([
  { id: 1, type: "implement", description: "a", prompt: "", status: "done" },
  { id: 2, type: "verify", description: "b", prompt: "", status: "open" },
]);
assert.deepEqual(taskRows.map((row) => row.id), [1, 2]);
assert.equal(client.taskAggregateLabel({ total: 2, done: 1 }), "1 of 2 tasks left");
assert.deepEqual(client.groupTasks(taskRows).settled.map((row) => row.id), [1]);
assert.equal(client.relativeTime("2026-08-09T12:00:00Z", new Date("2026-08-09T12:37:00Z")), "37m ago");
assert.equal(client.absoluteTime("not-a-date"), "not-a-date");
const launchOption = { field: "skillsDirs", wireField: "skillsDirs", label: "Skill directories", group: "Resources", kind: "pathList" };
assert.deepEqual(client.collectConfig([launchOption], client.buildFormState([launchOption], { skillsDirs: ["/opt/skills"] })), { skillsDirs: ["/opt/skills"] });
assert.deepEqual(client.inheritedItems(["/a", "/b"], ["/a"], (item) => item, client.asStringList), ["/b"]);
client.validatePathListAdd(launchOption, ["/opt/skills"], "/opt/skills", async () => ({ valid: true })).then((outcome) => assert.deepEqual(outcome, { ok: false, error: "Already added." }));
assert.equal(client.findBuiltinArgument([{ id: "anthropic/claude-x", label: "Claude X" }], " claude x ")?.id, "anthropic/claude-x");
assert.equal(client.matchBuiltinInvocation("/goal fix it", [{ id: "goal", args: { kind: "free" } }])?.argsText, "fix it");
`;
  // The qualification manifest: every specifier package.json publishes, and the
  // names the package promises at each one. A subpath with no entry here is not
  // qualified, whatever the exports map claims, so the two must agree. An
  // in-repo-only path alias is therefore unlistable: nothing in the tarball
  // backs it, and the apps' own typecheck is what validates it.
  const packageExports = {
    ".": {
      values: rootValues,
      types: rootTypes,
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
      values: ["DOC_FILE_MAX_BYTES", "DocFileError", "docFileRawURL", "docImageURL", "readDocFile"],
      types: ["DocFetch", "DocFileContent", "DocFileErrorKind", "DocPort", "DocResponseLike"],
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
  };
  const publishedSpecifiers = Object.keys(packageManifest.exports);
  for (const specifier of publishedSpecifiers)
    assert(packageExports[specifier], `published specifier is not qualified: no manifest entry for ${specifier}`);
  for (const [specifier, surface] of Object.entries(packageExports)) {
    assert(
      publishedSpecifiers.includes(specifier),
      `qualification manifest names ${specifier}, which package.json does not export`,
    );
    assert(
      Array.isArray(surface.values) && Array.isArray(surface.types),
      `qualification manifest entry ${specifier} needs both a value and a type name list`,
    );
  }
  // A module can be built, packed and listed here and still be unreachable: the
  // files list only decides what tsc emits, and the export checks below name
  // identifiers, not modules. Each published specifier's own installed
  // declarations are the honest record of what it re-exports, so a shipped
  // module qualifies by being some specifier's entry or by being re-exported
  // from one. A module no published specifier reaches fails right here.
  const reachableModules = new Set();
  for (const specifier of publishedSpecifiers) {
    const declarations = packageManifest.exports[specifier].types;
    const entryModule = shippedModules.find((module) => declarations === `./dist/${module}.d.ts`);
    assert(entryModule, `${specifier} publishes declarations no shipped module emits: ${declarations}`);
    reachableModules.add(entryModule);
    const text = readFileSync(join(consumerDir, "node_modules", packageManifest.name, declarations), "utf8");
    for (const module of shippedModules) if (text.includes(`from "./${module}"`)) reachableModules.add(module);
  }
  for (const module of shippedModules)
    assert(reachableModules.has(module), `shipped module unreachable from every published specifier: ${module}`);
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
