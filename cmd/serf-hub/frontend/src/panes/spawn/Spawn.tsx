// The spawn pane: starts a new session. Fills T1's minimal skeleton into the
// full form on the startThread/preflight seams - the six-field launch bar
// (harness / model / reasoning effort / working dir / branch / access mode),
// sticky-default layering + stale-model cleanup, the schema-driven advanced
// options, image attachments, working-dir preflight, and ?dir=/?prompt= URL
// prefill (floor §1, parity-m6-surfaces.md). The rich model/reasoning catalog
// is the interim model/list picker (Jesse-decided Wave 8 for the rich version);
// the recent-prompts row is a decided parity drop (Jesse 2026-07-22).
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { HarnessDescriptor, LaunchConfigLayer, LaunchOption } from "../../protocol/types.gen";
import { useClient } from "../../shell/clientContext";
import type { PaneProps } from "../../shell/paneRegistry";
import { navigate, paneToURL } from "../../shell/routing";
import {
  Button,
  Chip,
  ConfirmDialog,
  Dropzone,
  FormRow,
  IconButton,
  Input,
  PaneScaffold,
  Select,
  Textarea,
  useToasts,
} from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { imageFilesFromClipboard } from "../session/composer/attachments/clipboard";
import { type TextEditor, useAttachments } from "../session/composer/attachments/useAttachments";
import { AdvancedOptions } from "./AdvancedOptions";
import { ACCESS_MODE_OPTIONS } from "./accessMode";
import { resolveHeadBranch } from "./branch";
import { DirField } from "./DirField";
import { harnessUsesSerfModels } from "./harnessModels";
import { ModelField } from "./ModelField";
import { createDir, preflightDir } from "./preflight";
import { perLaunchSerfOptions, resolveScalars } from "./schema";
import styles from "./spawn.module.css";
import { resolveInitialDefaults, saveDefaults, sweepStaleModels } from "./spawnDefaults";
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
  bar: requireClass(styles.bar, "spawn.module.css", "bar"),
  notice: requireClass(styles.notice, "spawn.module.css", "notice"),
  promptCard: requireClass(styles.promptCard, "spawn.module.css", "promptCard"),
  controls: requireClass(styles.controls, "spawn.module.css", "controls"),
  chips: requireClass(styles.chips, "spawn.module.css", "chips"),
  actions: requireClass(styles.actions, "spawn.module.css", "actions"),
  fieldLabel: requireClass(styles.fieldLabel, "spawn.module.css", "fieldLabel"),
};

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

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
  const branchEditedRef = useRef(false);

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
  const listRecents = useCallback(() => client.request("serf/projects/recent", {}).then((r) => r.data), [client]);
  const complete = useCallback(
    (prefix: string) => client.request("serf/dirs/complete", { prefix }).then((r) => r.data),
    [client],
  );
  const validatePath = useCallback(
    (path: string, kind: string) =>
      client.request("serf/path/validate", { path, kind }).then((r) => ({ valid: r.valid, error: r.error })),
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
    if (defaults.branch) {
      setBranch(defaults.branch);
      branchEditedRef.current = true;
    }

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

  // Branch HEAD auto-resolution (floor §1.7): fills the display when the working
  // dir changes, unless the user has explicitly set a branch. Checked again on
  // resolve so a late response can't clobber a value picked in the meantime.
  useEffect(() => {
    if (cwd.trim() === "" || branchEditedRef.current) return undefined;
    let active = true;
    resolveHeadBranch(cwd).then((head) => {
      if (active && !branchEditedRef.current) setBranch(head);
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

  function handleBranchChange(next: string): void {
    branchEditedRef.current = true;
    setBranch(next);
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
    if (prompt.trim() === "" && attachments.items.length === 0) {
      toasts.push("error", "Prompt is empty. Type something before spawning.");
      return;
    }
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
      toasts.push("error", `Spawn failed: ${errorMessage(err)}`);
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
      toasts.push("error", `Spawn failed: ${errorMessage(err)}`);
      setBusy(false);
    } finally {
      setCreateDialogPath(null);
    }
  }

  const harnessOptions =
    harnesses.length > 0 ? harnesses.map((h) => ({ value: h.id, label: h.label })) : [{ value: "serf", label: "serf" }];

  return (
    <PaneScaffold title="New session">
      <div className={CLASS.form}>
        <p>Leave the prompt blank to start a dormant session.</p>

        <div className={CLASS.bar}>
          <FormRow label="Harness" htmlFor="spawn-harness">
            <Select
              id="spawn-harness"
              value={harness || "serf"}
              onChange={(e) => handleHarnessChange(e.target.value)}
              options={harnessOptions}
            />
          </FormRow>

          <div>
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

          <FormRow label="Reasoning effort" htmlFor="spawn-reasoning">
            <Select
              id="spawn-reasoning"
              value={reasoningEffort}
              onChange={(e) => setReasoningEffort(e.target.value)}
              options={REASONING_OPTIONS}
              disabled={!usesSerfModels}
            />
          </FormRow>

          <FormRow label="Working directory" htmlFor="spawn-cwd">
            <DirField
              id="spawn-cwd"
              value={cwd}
              onChange={setCwd}
              listRecents={listRecents}
              complete={complete}
              placeholder="Working directory"
            />
          </FormRow>

          <FormRow label="Branch" htmlFor="spawn-branch" help="Shows the working directory's current HEAD.">
            <Input
              id="spawn-branch"
              value={branch}
              onChange={(e) => handleBranchChange(e.target.value)}
              placeholder="(default)"
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
        </div>

        {staleNotice !== null && (
          <div className={CLASS.notice} role="status">
            <span>Discarded last-used model {staleNotice} — no longer offered by this hub.</span>
            <IconButton
              label="Dismiss notice"
              icon="×"
              variant="quiet"
              size="sm"
              onClick={() => setStaleNotice(null)}
            />
          </div>
        )}

        <Dropzone onFiles={(files) => attachments.ingestFiles(files, (message) => toasts.push("error", message))}>
          <div className={CLASS.promptCard}>
            <Textarea
              ref={textareaRef}
              value={prompt}
              onChange={(e) => updatePrompt(e.target.value)}
              onKeyDown={handlePromptKeyDown}
              onPaste={handlePaste}
              placeholder="What should the agent work on?"
              aria-label="Prompt"
              autoGrow
            />
            <div className={CLASS.controls}>
              <IconButton
                label="Attach image"
                icon="+"
                variant="quiet"
                type="button"
                onClick={() => fileInputRef.current?.click()}
              />
            </div>
          </div>
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

        {schemaOptions.length > 0 && (
          <AdvancedOptions
            options={schemaOptions}
            onOverridesChange={setAdvancedOverrides}
            validatePath={validatePath}
            resolveConfig={resolveConfig}
          />
        )}

        <div className={CLASS.actions}>
          <Button variant="primary" onClick={() => void handleSpawn()} disabled={busy}>
            {busy ? "Spawning…" : "Spawn"}
          </Button>
        </div>
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
