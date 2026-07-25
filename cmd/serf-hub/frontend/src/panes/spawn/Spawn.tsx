// The spawn pane: starts a new agent.
//
// This page IS the composer, not a form that happens to contain one - it renders
// the same widgets/promptcard the session composer does, with its own primary
// verb ("Start") in the card's corner. The prompt leads and takes the page's
// vertical slack; beneath it sits ONE compact configuration row (working
// directory widest, since it is the only field that changes often, then model
// and effort), with the branch riding the directory row as a read-only HEAD
// readout rather than a peer field. Harness lives in Advanced options: most
// installs have exactly one, and a field whose answer is always "serf" should
// not lead the page.
//
// Behind that: sticky-default layering + stale-model cleanup, the schema-driven
// advanced options (which also host the access-mode field, 9ct0 §3.3), image
// attachments, working-dir preflight, and ?dir=/?prompt= URL prefill (floor §1,
// parity-m6-surfaces.md). The recent-prompts row is a decided parity drop
// (Jesse 2026-07-22).
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { errorText } from "../../protocol/errors";
import type { HarnessDescriptor, LaunchConfigLayer, LaunchOption } from "../../protocol/types.gen";
import { useClient } from "../../shell/clientContext";
import type { PaneProps } from "../../shell/paneRegistry";
import { navigate, paneToURL } from "../../shell/routing";
import {
  Button,
  Chip,
  ConfirmDialog,
  chordLabel,
  Dropzone,
  FormRow,
  IconButton,
  PaneScaffold,
  PathField,
  PromptCard,
  Select,
  Textarea,
  Tooltip,
  useToasts,
} from "../../widgets";
import { CloseIcon } from "../../widgets/dialog/CloseIcon";
import { requireClass } from "../../widgets/internal/requireClass";
import { fetchModelCatalog } from "../../widgets/modelCatalog/catalogClient";
import { mergeScopedCatalog } from "../../widgets/modelCatalog/scopedCatalog";
import { AttachIcon } from "../session/composer/attachments/AttachIcon";
import { imageFilesFromClipboard } from "../session/composer/attachments/clipboard";
import { type TextEditor, useAttachments } from "../session/composer/attachments/useAttachments";
import { AdvancedOptions } from "./AdvancedOptions";
import { ACCESS_MODE_OPTIONS } from "./accessMode";
import { resolveHeadBranch } from "./branch";
import { harnessUsesSerfModels } from "./harnessModels";
import { ModelField } from "./ModelField";
import { createDir, preflightDir } from "./preflight";
import { perLaunchSerfOptions, resolveScalars } from "./schema";
import styles from "./spawn.module.css";
import {
  getGlobalLastWorkingDir,
  resolveInitialDefaults,
  saveDefaults,
  setGlobalLastWorkingDir,
  sweepStaleModels,
} from "./spawnDefaults";
import { startThread } from "./startThread";
import { readUrlPrefill } from "./urlPrefill";

// No route params: /new resolves to spawn with an empty param object; the
// ?dir=/?prompt= prefill is read from window.location.search, not params.
export type SpawnPaneParams = Record<string, never>;

// Interim reasoning-effort ladder (floor §1.5, the rich per-model ladder is
// Wave 8): "(default)" + the standard levels + an explicit "none".
const REASONING_LEVELS = ["minimal", "low", "medium", "high"];
const REASONING_OPTIONS = [
  { value: "", label: "(default)" },
  ...REASONING_LEVELS.map((level) => ({ value: level, label: level })),
  { value: "none", label: "none" },
];

const CLASS = {
  form: requireClass(styles.form, "spawn.module.css", "form"),
  cfg: requireClass(styles.cfg, "spawn.module.css", "cfg"),
  cfgDir: requireClass(styles.cfgDir, "spawn.module.css", "cfgDir"),
  cfgModel: requireClass(styles.cfgModel, "spawn.module.css", "cfgModel"),
  branch: requireClass(styles.branch, "spawn.module.css", "branch"),
  branchSeparator: requireClass(styles.branchSeparator, "spawn.module.css", "branchSeparator"),
  notice: requireClass(styles.notice, "spawn.module.css", "notice"),
  chips: requireClass(styles.chips, "spawn.module.css", "chips"),
  fieldLabel: requireClass(styles.fieldLabel, "spawn.module.css", "fieldLabel"),
};

export default function Spawn(_props: PaneProps<SpawnPaneParams>) {
  const client = useClient();
  const toasts = useToasts();

  const [prompt, setPrompt] = useState("");
  const [harness, setHarness] = useState("");
  const [model, setModel] = useState(""); // qualified "provider/model", or "" for the harness default
  const [reasoningEffort, setReasoningEffort] = useState("");
  const [cwd, setCwd] = useState("");
  const [branch, setBranch] = useState(""); // display-only (floor §1.7)
  const [accessMode, setAccessMode] = useState("");
  const [harnesses, setHarnesses] = useState<HarnessDescriptor[]>([]);
  const [schemaOptions, setSchemaOptions] = useState<LaunchOption[]>([]);
  const [advancedOverrides, setAdvancedOverrides] = useState<LaunchConfigLayer>({});
  const [staleNotice, setStaleNotice] = useState<string | null>(null);
  const [createDialogPath, setCreateDialogPath] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Attachments reuse the composer's staged-image pipeline via a TextEditor
  // bridge over the prompt textarea (see Composer.tsx's own bridge for the
  // React controlled-input rationale). textRef mirrors `prompt` synchronously
  // so a late decode-failure callback never reverts newer typing.
  const textRef = useRef(prompt);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const cursorRef = useRef<number | null>(null);

  function updatePrompt(next: string): void {
    textRef.current = next;
    setPrompt(next);
  }

  useLayoutEffect(() => {
    if (cursorRef.current !== null && textareaRef.current) {
      textareaRef.current.setSelectionRange(cursorRef.current, cursorRef.current);
      cursorRef.current = null;
    }
  });

  const textEditor: TextEditor = {
    read: () => ({
      text: textRef.current,
      cursor: cursorRef.current ?? textareaRef.current?.selectionStart ?? textRef.current.length,
    }),
    write: (next, cursor) => {
      updatePrompt(next);
      cursorRef.current = cursor;
    },
  };
  const attachments = useAttachments(textEditor);

  const usesSerfModels = harnessUsesSerfModels(harness, harnesses);

  const loadModels = useCallback(
    () => client.request("model/list", { harness: harness || undefined, cwd: cwd || undefined }).then((r) => r.data),
    [client, harness, cwd],
  );
  // The advanced panel's model-valued fields use the same scoped catalog the
  // top-level Model field does: model/list is the authoritative launchable SET
  // for this harness+cwd, enriched best-effort with /api/models metadata (a
  // failed enrichment degrades to the plain scoped list, never an empty
  // picker) - the identical composition ModelField.tsx documents.
  const loadCatalog = useCallback(
    () =>
      Promise.all([
        loadModels(),
        // A failed enrichment degrades to the plain scoped list
        // (mergeScopedCatalog tolerates null); a failed model/list still
        // rejects, so the picker surfaces the real error.
        fetchModelCatalog({ harness: harness || undefined, cwd: cwd || undefined }).catch(() => null),
      ]).then(([scoped, enrichment]) => mergeScopedCatalog(scoped, enrichment)),
    [loadModels, harness, cwd],
  );
  // Both path RPCs answer with a Go slice, and an EMPTY one marshals as JSON
  // null rather than [] - a hub with no remembered projects, or a directory with
  // no children. types.gen.ts declares `data: string[]`, so the compiler is no
  // help here; these coalesce so a consumer counting entries never sees null.
  const listRecents = useCallback(() => client.request("serf/projects/recent", {}).then((r) => r.data ?? []), [client]);
  // Injected into every PathField on this pane (the working directory here and
  // the advanced panel's path/pathList fields): the widget derives includeFiles
  // from its own kind, so this just forwards it.
  const complete = useCallback(
    (prefix: string, includeFiles: boolean) =>
      client.request("serf/paths/complete", { prefix, includeFiles }).then((r) => r.data ?? []),
    [client],
  );
  const validatePath = useCallback(
    (path: string, kind: string) =>
      // `path` is the server-canonicalized spelling, which a pathList add stores
      // in place of the raw input (matching the settings-side pathList field).
      client
        .request("serf/path/validate", { path, kind })
        .then((r) => ({ valid: r.valid, error: r.error, path: r.path })),
    [client],
  );
  const resolveConfig = useCallback(
    (overrides: LaunchConfigLayer) => client.request("serf/launch/resolve", { cwd, launchOverrides: overrides }),
    [client, cwd],
  );

  // Mount: URL prefill + sticky defaults (synchronous), then the async catalogs
  // (harnesses, advanced schema) and the stale-model sweep.
  // biome-ignore lint/correctness/useExhaustiveDependencies: mount-only initialization; the closures it calls are stable for the first paint
  useEffect(() => {
    const urlPrefill = readUrlPrefill(window.location.search);
    const defaults = resolveInitialDefaults({ serverPrefillDir: urlPrefill.dir });
    if (urlPrefill.prompt) updatePrompt(urlPrefill.prompt);
    if (defaults.harness) setHarness(defaults.harness);
    if (defaults.model) setModel(defaults.model);
    if (defaults.workingDir) setCwd(defaults.workingDir);
    if (defaults.accessMode) setAccessMode(defaults.accessMode);
    if (defaults.reasoningEffort) setReasoningEffort(defaults.reasoningEffort);
    // The persisted branch is a fast first paint only - the HEAD resolution
    // below always overrides it once it lands. Now that the readout is
    // read-only, HEAD is the sole authority for what it says; a remembered
    // value outliving the branch it named would be a lie in a field that
    // presents itself as fact.
    if (defaults.branch) setBranch(defaults.branch);
    // Writing the prompt is what starting an agent IS, so the caret starts
    // there rather than on whichever field happens to be first in the DOM.
    textareaRef.current?.focus();

    let active = true;
    client.request("serf/harnesses/list", {}).then(
      (r) => {
        if (active) setHarnesses(r.data);
      },
      () => {},
    );
    client.request("serf/launch/schema", {}).then(
      (r) => {
        if (active) setSchemaOptions(perLaunchSerfOptions(r));
      },
      () => {},
    );
    // Stale-model cleanup (floor §1.10): sweep the persisted defaults against the
    // live model list; if this project's prefilled model was discarded, clear it
    // and surface the inline notice.
    client.request("model/list", {}).then(
      (r) => {
        if (!active) return;
        const { discarded } = sweepStaleModels(r.data);
        if (defaults.model && discarded.includes(defaults.model)) {
          setModel("");
          setStaleNotice(defaults.model);
        }
      },
      () => {},
    );
    return () => {
      active = false;
    };
  }, []);

  // kata 11ee: the spawn pane is a dockview singleton (index.tsx) - a second
  // /new?dir=/?prompt= navigation while this pane is already open refocuses
  // this SAME mounted instance instead of remounting it, so the mount-only
  // effect above (deps []) never reruns and the new prefill is silently
  // dropped. A popstate listener re-applies whatever of readUrlPrefill IS
  // present on every subsequent in-app navigation - routing.ts's navigate()
  // dispatches popstate on every push, the same signal AppShell's own
  // routing glue and settings/sections/project.tsx's useQueryCwd both key
  // off - without touching the sticky-defaults layering above, which is
  // mount-only initialization, not a navigation param. A URL with neither
  // param present (e.g. an unrelated navigation elsewhere and back) yields
  // no entries from readUrlPrefill and so leaves both fields untouched,
  // matching that function's own "absent param -> no entry" contract.
  // biome-ignore lint/correctness/useExhaustiveDependencies: install once - setCwd is a stable setter and updatePrompt closes only over the stable textRef, so the mount-time closure stays correct for every later popstate
  useEffect(() => {
    function onPopState(): void {
      const urlPrefill = readUrlPrefill(window.location.search);
      if (urlPrefill.dir) setCwd(urlPrefill.dir);
      if (urlPrefill.prompt) updatePrompt(urlPrefill.prompt);
    }
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  // Branch HEAD resolution (floor §1.7): the readout is read-only, so HEAD is
  // its ONLY source - re-resolved on every working-dir change with no
  // user-edited escape hatch to respect. `active` still guards a late response
  // from a directory the user has already navigated away from.
  useEffect(() => {
    if (cwd.trim() === "") return undefined;
    let active = true;
    resolveHeadBranch(cwd).then((head) => {
      if (active) setBranch(head);
    });
    return () => {
      active = false;
    };
  }, [cwd]);

  function handleHarnessChange(next: string): void {
    setHarness(next);
    // Switching to a non-serf harness always blanks the model; switching to a
    // serf-model harness only blanks a value that isn't already provider/model
    // shaped (floor §1.10, spawn.js:395-402).
    if (!harnessUsesSerfModels(next, harnesses)) setModel("");
    else if (model !== "" && !model.includes("/")) setModel("");
  }

  function handleModelChange(next: string): void {
    setModel(next);
    if (next !== "") setStaleNotice(null); // any new model clears the discard notice (floor §1.10)
  }

  function handlePromptKeyDown(event: React.KeyboardEvent<HTMLTextAreaElement>): void {
    // ⌘/Ctrl+Enter submits (floor §1.12, spawn.js:1204-1211).
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
      event.preventDefault();
      void handleSpawn();
    }
  }

  function handlePaste(event: React.ClipboardEvent<HTMLTextAreaElement>): void {
    const files = imageFilesFromClipboard(event.clipboardData);
    if (files.length > 0) attachments.ingestFiles(files, (message) => toasts.push("error", message));
  }

  function handleFilePicker(event: React.ChangeEvent<HTMLInputElement>): void {
    const files = Array.from(event.target.files ?? []);
    if (files.length > 0) attachments.ingestFiles(files, (message) => toasts.push("error", message));
    event.target.value = ""; // re-picking the identical file must re-fire change
  }

  async function doSpawn(): Promise<void> {
    // The advanced schema's sandbox wins over the access-mode chip (floor §1.8);
    // its model/reasoningEffort win over the chips (floor §1.11) - resolveScalars
    // hoists them into the top-level fields the daemon prefers over overrides.
    const overrides = advancedOverrides;
    const scalars = resolveScalars({ model, reasoningEffort }, overrides);
    // Snapshot before the await (mirrors Composer.tsx's submitAction) so an
    // attachment staged WHILE this request is in flight isn't in the set
    // clearSubmitted removes below - it survives untouched, same contract
    // useAttachments.ts documents for the composer.
    const submittedMarkers = new Set(attachments.items.map((item) => item.marker));
    const { ref } = await startThread(client, {
      cwd,
      prompt,
      attachments: attachments.toInputAttachments(),
      harness: harness || undefined,
      modelProvider: scalars.modelProvider,
      model: scalars.model,
      reasoningEffort: scalars.reasoningEffort,
      accessMode,
      launchOverrides: Object.keys(overrides).length > 0 ? overrides : undefined,
    });
    saveDefaults({ cwd, harness, model, branch, accessMode, reasoningEffort, harnessUsesSerfModels: usesSerfModels });
    // Reset transient form state on success, before navigating away (floor
    // §1.14 L186: the pending-attachment bag is cleared and the paste
    // marker-counter reset). The spawn pane is a dockview singleton that can
    // still be mounted behind the session pane this navigates to, so without
    // this an already-sent prompt/image stays staged and re-sendable if the
    // user returns to it. Sticky defaults (harness/model/cwd/branch/access
    // mode, floor §1.9-§1.10) are deliberately left untouched - only the
    // one-shot prompt/attachments reset.
    updatePrompt("");
    attachments.clearSubmitted(submittedMarkers);
    // Same defect class: both callers set busy=true before awaiting this
    // function but only their OWN catch blocks ever reset it back to false,
    // so a success fell through with the button stuck disabled/"Spawning…"
    // forever on a pane that can outlive the navigation below.
    setBusy(false);
    const url = paneToURL("session", { ref });
    if (url) navigate(url);
  }

  async function handleSpawn(): Promise<void> {
    if (busy) return;
    if (attachments.hasPending) {
      toasts.push("error", "Image attachment is still processing.");
      return;
    }
    // A blank prompt is NOT an error: it starts a dormant session, which is
    // what the prompt placeholder promises. buildInput drops the empty text
    // item, and hubThreadStart starts a turn only for a non-empty input
    // (cmd/serf-hub/app_threadlifecycle.go), so the session is created and
    // simply waits for its first prompt in the session composer.
    setBusy(true);
    try {
      const outcome = await preflightDir(client, cwd);
      if (outcome.kind === "abort") {
        toasts.push("error", outcome.message);
        setBusy(false);
        return;
      }
      if (outcome.kind === "offer-create") {
        setCreateDialogPath(outcome.path);
        setBusy(false);
        return;
      }
      await doSpawn();
    } catch (err) {
      toasts.push("error", `Spawn failed: ${errorText(err)}`);
      setBusy(false);
    }
  }

  async function handleCreateConfirm(): Promise<void> {
    const path = createDialogPath;
    if (path === null) return;
    setBusy(true);
    try {
      await createDir(path);
      await doSpawn();
    } catch (err) {
      toasts.push("error", `Spawn failed: ${errorText(err)}`);
      setBusy(false);
    } finally {
      setCreateDialogPath(null);
    }
  }

  const harnessOptions =
    harnesses.length > 0 ? harnesses.map((h) => ({ value: h.id, label: h.label })) : [{ value: "serf", label: "serf" }];

  return (
    <PaneScaffold title="Start an agent">
      <div className={CLASS.form}>
        {staleNotice !== null && (
          <div className={CLASS.notice} role="status">
            <span>Discarded last-used model {staleNotice} — no longer offered by this hub.</span>
            <IconButton
              label="Dismiss notice"
              icon={<CloseIcon />}
              variant="quiet"
              size="sm"
              onClick={() => setStaleNotice(null)}
            />
          </div>
        )}

        {/* The prompt comes FIRST and takes the page's slack: writing the
            prompt is what starting an agent IS, and everything below it is
            configuration that mostly stays where it was last left. The card is
            the same widgets/promptcard the session composer renders. */}
        <Dropzone onFiles={(files) => attachments.ingestFiles(files, (message) => toasts.push("error", message))}>
          <PromptCard
            data-testid="spawn-prompt-card"
            field={
              <Textarea
                ref={textareaRef}
                value={prompt}
                onChange={(e) => updatePrompt(e.target.value)}
                onKeyDown={handlePromptKeyDown}
                onPaste={handlePaste}
                // The dormant-start rule rides in the placeholder rather than a
                // separate instruction line above the form: it is a fact about
                // THIS field, and a sentence of chrome explaining a field is
                // worse than the field explaining itself.
                placeholder="What should the agent work on? Leave blank to start it dormant."
                aria-label="Prompt"
                autoGrow
                // The PromptCard around it draws the one border this field
                // needs and owns the focus ring - without this the field drew
                // its own box inside the card's, and its resize grabber floated
                // loose in the corner between them.
                seamless
                // The page's primary input, so it opens at a size worth writing
                // in rather than growing into one. This is also what absorbs
                // the slack that used to sit dead below the button.
                minLines={6}
              />
            }
            leading={
              <IconButton
                label="Attach image"
                icon={<AttachIcon />}
                variant="quiet"
                size="xs"
                type="button"
                data-testid="spawn-attach"
                onClick={() => fileInputRef.current?.click()}
              />
            }
            actions={
              <Tooltip label={`Start the agent · ${chordLabel(["Mod", "Enter"])}`}>
                {/* "Start", not "Spawn": spawn is implementation vocabulary, and
                    the rail's own button already says "New session". */}
                <Button
                  variant="primary"
                  size="xs"
                  data-testid="spawn-submit"
                  onClick={() => void handleSpawn()}
                  disabled={busy}
                >
                  {busy ? "Starting…" : "Start"}
                </Button>
              </Tooltip>
            }
          />
        </Dropzone>
        <input ref={fileInputRef} type="file" accept="image/*" multiple hidden onChange={handleFilePicker} />

        {attachments.items.length > 0 && (
          <div className={CLASS.chips}>
            {attachments.items.map((item) => (
              <Chip key={item.marker} tone="neutral" onRemove={() => attachments.removeItem(item.marker)}>
                {`${item.name}${item.pending ? " (processing…)" : ""}`}
              </Chip>
            ))}
          </div>
        )}

        {/* ONE compact row of configuration beneath the card, widest field
            first: the working directory is the only one that changes often.
            Harness moved into Advanced options - most installs have exactly one,
            and a field whose answer is always "serf" should not lead the page. */}
        <div className={CLASS.cfg}>
          <div className={CLASS.cfgDir}>
            <FormRow label="Working directory" htmlFor="spawn-cwd">
              <PathField
                id="spawn-cwd"
                value={cwd}
                onChange={setCwd}
                kind="dir"
                listRecents={listRecents}
                complete={complete}
                // With no ?dir= prefill and no per-project blob the field starts
                // empty; the panel then opens on the last directory a session was
                // launched in rather than on $HOME (spec 3.4). Read here rather
                // than captured at mount so it reflects the latest stamp.
                fallbackDir={getGlobalLastWorkingDir()}
                placeholder="Working directory"
                // Browsing writes the field on every step, so the last-used
                // directory is recorded once the panel closes rather than
                // continuously (spec 3.7).
                onPanelClose={setGlobalLastWorkingDir}
              />
            </FormRow>
            {/* Branch is a read-only HEAD readout, not a peer field: it rides
                the directory row as a mono suffix ("~/code/serf · main") because
                it is a PROPERTY of that directory, and the wire has nowhere to
                send it anyway (startThread.ts's own branch comment). */}
            {branch !== "" && (
              <span className={CLASS.branch} data-testid="spawn-branch" title={`HEAD ${branch}`}>
                <span className={CLASS.branchSeparator} aria-hidden="true">
                  ·
                </span>
                {branch}
              </span>
            )}
          </div>

          <div className={CLASS.cfgModel}>
            <span className={CLASS.fieldLabel} id="spawn-model-label">
              Model
            </span>
            <ModelField
              value={model}
              onChange={handleModelChange}
              loadModels={loadModels}
              harness={harness || undefined}
              cwd={cwd || undefined}
            />
          </div>

          <FormRow label="Effort" htmlFor="spawn-reasoning">
            <Select
              id="spawn-reasoning"
              value={reasoningEffort}
              onChange={(e) => setReasoningEffort(e.target.value)}
              options={REASONING_OPTIONS}
              disabled={!usesSerfModels}
            />
          </FormRow>
        </div>

        <AdvancedOptions
          options={schemaOptions}
          onOverridesChange={setAdvancedOverrides}
          validatePath={validatePath}
          resolveConfig={resolveConfig}
          loadCatalog={loadCatalog}
          complete={complete}
        >
          <FormRow label="Harness" htmlFor="spawn-harness">
            <Select
              id="spawn-harness"
              value={harness || "serf"}
              onChange={(e) => handleHarnessChange(e.target.value)}
              options={harnessOptions}
            />
          </FormRow>
          <FormRow label="Access mode" htmlFor="spawn-access">
            <Select
              id="spawn-access"
              value={accessMode}
              onChange={(e) => setAccessMode(e.target.value)}
              options={[{ value: "", label: "(default)" }, ...ACCESS_MODE_OPTIONS]}
            />
          </FormRow>
        </AdvancedOptions>
      </div>

      <ConfirmDialog
        open={createDialogPath !== null}
        title="Create directory?"
        confirmLabel="Create & start"
        destructive={false}
        busy={busy}
        onConfirm={() => void handleCreateConfirm()}
        onCancel={() => setCreateDialogPath(null)}
      >
        The directory {createDialogPath} doesn't exist yet. Create it and start the session?
      </ConfirmDialog>
    </PaneScaffold>
  );
}
