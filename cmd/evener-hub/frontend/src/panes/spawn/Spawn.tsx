// The spawn pane: starts a new agent.
//
// This page IS the composer, not a form that happens to contain one - it renders
// the same widgets/promptcard the session composer does, with its own primary
// verb ("Start") in the card's corner. The prompt leads and takes the page's
// vertical slack; beneath it sits ONE compact configuration row (working
// directory widest, since it is the only field that changes often, then model
// and effort), with the branch riding the directory row as a read-only HEAD
// readout rather than a peer field. Harness lives in Advanced options: most
// installs have exactly one, and a field whose answer is always "evener" should
// not lead the page.
//
// Behind that: sticky-default layering + stale-model cleanup, the schema-driven
// advanced options (which also host the access-mode field, 9ct0 §3.3), image
// attachments, working-dir preflight, and ?dir=/?prompt= URL prefill (floor §1,
// parity-m6-surfaces.md). The recent-prompts row is a decided parity drop
// (Jesse 2026-07-22).
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { friendlyLaunchErrorMessage } from "../../protocol/errors";
import type {
  HarnessDescriptor,
  LaunchConfigLayer,
  LaunchOption,
  ModelListResponse,
  PluginSelectionError,
} from "../../protocol/types.gen";
import { useClient } from "../../shell/clientContext";
import type { PaneProps } from "../../shell/paneRegistry";
import { effortLabel } from "../../shell/reasoningEffort";
import { navigate, paneToURL } from "../../shell/routing";
import { useExtensionsStore } from "../../stores/extensions";
import {
  Button,
  ConfirmDialog,
  chordLabel,
  Dropzone,
  FormRow,
  IconButton,
  Loader,
  PaneScaffold,
  PathField,
  PromptCard,
  Select,
  SendIcon,
  Textarea,
  Tooltip,
  useToasts,
} from "../../widgets";
import { CloseIcon } from "../../widgets/dialog/CloseIcon";
import { Disclosure } from "../../widgets/disclosure";
import { requireClass } from "../../widgets/internal/requireClass";
import type { ModelCatalog, ModelCatalogEntry } from "../../widgets/modelCatalog";
import { modelListToCatalog } from "../../widgets/modelCatalog/catalogClient";
import { mergeCatalogEntry, mergeCatalogSnapshot } from "../../widgets/modelCatalog/scopedCatalog";
import { ModelSwitchTrigger } from "../session/chrome/ModelSwitchTrigger";
import { AttachmentTile } from "../session/composer/AttachmentTile";
import { AttachIcon } from "../session/composer/attachments/AttachIcon";
import { imageFilesFromClipboard } from "../session/composer/attachments/clipboard";
import { type TextEditor, useAttachments } from "../session/composer/attachments/useAttachments";
import { ConnectProviderDialog } from "../settings/sections/credentials/ConnectProviderDialog";
import { AdvancedOptions } from "./AdvancedOptions";
import { ACCESS_MODE_OPTIONS, accessModeDefaultLabel } from "./accessMode";
import { resolveHeadBranch } from "./branch";
import { harnessSupportsPluginSelection, harnessUsesEvenerModels } from "./harnessModels";
import { MobileSettingRows } from "./MobileSettingRows";
import { ModelField } from "./ModelField";
import { PluginSelectionPanel } from "./PluginSelectionPanel";
import pluginSelectionStyles from "./pluginSelection.module.css";
import {
  type PluginSelectionState,
  pluginSelectionIssues,
  reconcilePluginSelection,
  selectedPluginNames,
  withPluginSelection,
} from "./pluginSelectionState";
import { createDir, preflightDir } from "./preflight";
import { perLaunchEvenerOptions, resolveScalars } from "./schema";
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
import { usePluginPreview } from "./usePluginPreview";
import { useProviderSetup } from "./useProviderSetup";

// No route params: /new resolves to spawn with an empty param object; the
// ?dir=/?prompt= prefill is read from window.location.search, not params.
export type SpawnPaneParams = Record<string, never>;

// Fallback effort ladder for a model whose own ladder the hub does not
// enumerate - the same fallback the session status row uses (StatusRow.tsx's
// DEFAULT_EFFORT_LEVELS), so both surfaces agree on the unknown case. The
// select's real ladder comes from the selected model's catalog entry
// (reasoningEffortLevels/supportsReasoning, served by model/list);
// "(default)" + an explicit "none" ride every ladder.
const FALLBACK_EFFORT_LEVELS = ["minimal", "low", "medium", "high"];
// Shared empty-ladder constant so the derived value keeps a stable identity
// across renders (the stale-effort effect below keys off it).
const NO_EFFORT_LEVELS: string[] = [];

// How long the working-directory field must be quiet before the pane reloads
// the model catalog behind the Effort ladder. Long enough to collapse a typed
// path into one load, short enough that the ladder is right by the time anyone
// opens the select.
const CATALOG_SETTLE_MS = 250;

// The effort levels a catalog entry authorizes: the model's own named ladder
// when it has one, an EMPTY list when the catalog says the model cannot
// reason at all, and null when the hub can't say (enrichment failed, or the
// entry names neither levels nor a reasoning capability). The caller
// substitutes FALLBACK_EFFORT_LEVELS for null, so a missing catalog never
// empties or disables the field.
function catalogEffortLevels(entry: ModelCatalogEntry | undefined): string[] | null {
  if (entry === undefined) return null;
  if (entry.reasoningEffortLevels !== undefined && entry.reasoningEffortLevels.length > 0) {
    return entry.reasoningEffortLevels;
  }
  if (entry.supportsReasoning === false) return NO_EFFORT_LEVELS;
  return null;
}

const CLASS = {
  form: requireClass(styles.form, "spawn.module.css", "form"),
  cfg: requireClass(styles.cfg, "spawn.module.css", "cfg"),
  cfgDir: requireClass(styles.cfgDir, "spawn.module.css", "cfgDir"),
  cfgModel: requireClass(styles.cfgModel, "spawn.module.css", "cfgModel"),
  branch: requireClass(styles.branch, "spawn.module.css", "branch"),
  branchSeparator: requireClass(styles.branchSeparator, "spawn.module.css", "branchSeparator"),
  notice: requireClass(styles.notice, "spawn.module.css", "notice"),
  attachments: requireClass(styles.attachments, "spawn.module.css", "attachments"),
  leading: requireClass(styles.leading, "spawn.module.css", "leading"),
  modelTrigger: requireClass(styles.modelTrigger, "spawn.module.css", "modelTrigger"),
  mobileConfig: requireClass(styles.mobileConfig, "spawn.module.css", "mobileConfig"),
  mobilePromptIntro: requireClass(styles.mobilePromptIntro, "spawn.module.css", "mobilePromptIntro"),
  mobilePromptHeading: requireClass(styles.mobilePromptHeading, "spawn.module.css", "mobilePromptHeading"),
  mobilePromptSubtitle: requireClass(styles.mobilePromptSubtitle, "spawn.module.css", "mobilePromptSubtitle"),
  fieldLabel: requireClass(styles.fieldLabel, "spawn.module.css", "fieldLabel"),
  modelNote: requireClass(styles.modelNote, "spawn.module.css", "modelNote"),
  submitLabel: requireClass(styles.submitLabel, "spawn.module.css", "submitLabel"),
  pluginDesktop: requireClass(pluginSelectionStyles.desktopSurface, "pluginSelection.module.css", "desktopSurface"),
  pluginSummary: requireClass(pluginSelectionStyles.summary, "pluginSelection.module.css", "summary"),
};

// kata xgk8: the empty-value label Model shows when the hub has confirmed it
// has no default to fall back to - never "(default)", which reads exactly
// like Effort's own working default and invites a submit the daemon refuses
// ("model is required", app_threadlifecycle.go).
const MODEL_CHOOSE_LABEL = "Choose a model";

export default function Spawn(_props: PaneProps<SpawnPaneParams>) {
  const client = useClient();
  const toasts = useToasts();
  const providerSetup = useProviderSetup();
  const [connectingProvider, setConnectingProvider] = useState(false);
  const closeProviderSetup = useCallback(() => setConnectingProvider(false), []);

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
  const [pluginSelection, setPluginSelection] = useState<PluginSelectionState>({ mode: "default" });
  const [knownSelectionIssues, setKnownSelectionIssues] = useState<PluginSelectionError[]>([]);
  const pluginSelectionRef = useRef(pluginSelection);
  pluginSelectionRef.current = pluginSelection;
  const [staleNotice, setStaleNotice] = useState<string | null>(null);
  const [createDialogPath, setCreateDialogPath] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // Loader's elapsed readout is pure-render (widgets/loader's own doc
  // comment - no internal timer, so it can't drift or fake liveness): the
  // caller owns the clock. busyStartedAt is stamped once, at the submit that
  // flips busy true; the 1s ticker effect below feeds it a fresh `now` for
  // as long as busy stays true, and stops the instant it doesn't.
  const [busyStartedAt, setBusyStartedAt] = useState<number | null>(null);
  const [now, setNow] = useState(() => Date.now());
  // kata xgk8: true only once evener/launch/resolve has CONFIRMED the hub has
  // no default model for this cwd (Effective.Model resolves empty with no
  // overrides) - never set on a rejection or before cwd is chosen, so an
  // unconfirmable state never blocks Start (same fail-open shape as
  // preflightDir).
  const [noDefaultModel, setNoDefaultModel] = useState(false);
  // The launchable-model catalog, loaded at pane level so the Effort select can
  // read the selected model's own reasoningEffortLevels without waiting for a
  // picker to open. null = not loaded or the load failed - the select stays on
  // the fallback ladder.
  const [modelCatalog, setModelCatalog] = useState<ModelCatalog | null>(null);
  // The hub's resolved default model for this cwd ("" until resolve confirms
  // one): what the Effort ladder keys off while Model reads "(default)".
  const [resolvedDefaultModel, setResolvedDefaultModel] = useState("");
  // The whole effective layer of the same launch/resolve (null until it
  // lands, or after it fails): every launch-config control whose unset state
  // reads "(default)" prepends its entry here - "high (default)",
  // "On (default)", "anthropic/claude-sonnet-4 (default)" - so the word
  // "(default)" never stands in for an answer the hub actually knows.
  const [resolvedDefaults, setResolvedDefaults] = useState<LaunchConfigLayer | null>(null);
  const pluginRevision = useExtensionsStore((state) => state.pluginRevision);
  const pluginSelectionSupported = harnessSupportsPluginSelection(harness, harnesses);
  const combinedOverrides = pluginSelectionSupported
    ? withPluginSelection(advancedOverrides, pluginSelection)
    : withPluginSelection(advancedOverrides, { mode: "default" });

  // Attachments reuse the composer's staged-image pipeline via a TextEditor
  // bridge over the prompt textarea (see Composer.tsx's own bridge for the
  // React controlled-input rationale). textRef mirrors `prompt` synchronously
  // so a late decode-failure callback never reverts newer typing.
  const textRef = useRef(prompt);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const cursorRef = useRef<number | null>(null);
  // kata 61v2: `busy` state alone is not a re-entrancy guard. Three clicks
  // dispatched before React commits the first one's setBusy(true) all read
  // the SAME stale `busy === false` from their own render's closure, so all
  // three pass `if (busy) return` and all three spawn a session. A plain ref
  // is mutated synchronously, in the SAME tick as the click that set it, so a
  // second click arriving before the next render commits still sees it set.
  // `busy` state stays: it still drives the disabled attribute/"Starting…"
  // label, which is the honest UI reflection of `busyRef` once React catches
  // up - this ref is only the guard of record.
  const busyRef = useRef(false);
  // Mirrors `model` for the default-provider-credential effect below: that
  // effect must read whether Model is CURRENTLY untouched without itself
  // re-running (and re-issuing evener/launch/resolve + model/list) every time
  // the user picks a model - same rationale as busyRef, a ref read at async
  // resolution time rather than a dependency that reruns the effect.
  const modelRef = useRef(model);
  modelRef.current = model;

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

  const usesEvenerModels = harnessUsesEvenerModels(harness, harnesses);
  const providerRequired = usesEvenerModels && providerSetup.status === "missing";
  // kata xgk8: Start cannot succeed while Model is untouched AND the hub has
  // confirmed there is no default to fall back to - see the resolve effect
  // below for how noDefaultModel is set.
  const modelRequired = model === "" && noDefaultModel;

  const modelListCache = useRef<{
    client: object;
    instances: object;
    entries: Map<string, Promise<ModelListResponse>>;
  }>({
    client,
    instances: providerSetup.instances,
    entries: new Map(),
  });
  const loadModelList = useCallback((): Promise<ModelListResponse> => {
    if (modelListCache.current.client !== client || modelListCache.current.instances !== providerSetup.instances) {
      modelListCache.current = { client, instances: providerSetup.instances, entries: new Map() };
    }
    const cache = modelListCache.current.entries;
    const key = `${harness}\0${cwd}`;
    const cached = cache.get(key);
    if (cached) return cached;

    const request = client.request("model/list", { harness: harness || undefined, cwd: cwd || undefined });
    let tracked: Promise<ModelListResponse>;
    tracked = request.catch((error) => {
      if (cache.get(key) === tracked) cache.delete(key);
      throw error;
    });
    cache.set(key, tracked);
    return tracked;
  }, [client, harness, cwd, providerSetup.instances]);
  const loadModels = useCallback(() => loadModelList().then((response) => response.data ?? []), [loadModelList]);
  // Every model-valued control in the spawn pane consumes this one scoped
  // response. The same promise is shared with the default-model preview, so
  // opening a picker and resolving the working directory cannot issue
  // duplicate model/list RPCs for the same harness and cwd.
  const loadCatalog = useCallback(() => loadModelList().then(modelListToCatalog), [loadModelList]);
  // Both path RPCs answer with a Go slice, and an EMPTY one marshals as JSON
  // null rather than [] - a hub with no remembered projects, or a directory with
  // no children. types.gen.ts declares `data: string[]`, so the compiler is no
  // help here; these coalesce so a consumer counting entries never sees null.
  const listRecents = useCallback(
    () => client.request("evener/projects/recent", {}).then((r) => r.data ?? []),
    [client],
  );
  // Injected into every PathField on this pane (the working directory here and
  // the advanced panel's path/pathList fields): the widget derives includeFiles
  // from its own kind, so this just forwards it.
  const complete = useCallback(
    (prefix: string, includeFiles: boolean) =>
      client.request("evener/paths/complete", { prefix, includeFiles }).then((r) => r.data ?? []),
    [client],
  );
  const validatePath = useCallback(
    (path: string, kind: string) =>
      // `path` is the server-canonicalized spelling, which a pathList add stores
      // in place of the raw input (matching the settings-side pathList field).
      client
        .request("evener/path/validate", { path, kind })
        .then((r) => ({ valid: r.valid, error: r.error, path: r.path })),
    [client],
  );
  const resolveConfig = useCallback(
    (overrides: LaunchConfigLayer) =>
      client.request("evener/launch/resolve", {
        cwd,
        launchOverrides: pluginSelectionSupported
          ? withPluginSelection(overrides, pluginSelection)
          : withPluginSelection(overrides, { mode: "default" }),
      }),
    [client, cwd, pluginSelection, pluginSelectionSupported],
  );

  const pluginPreview = usePluginPreview({
    client,
    cwd,
    launchOverrides: combinedOverrides,
    pluginRevision,
    enabled: pluginSelectionSupported,
  });

  useEffect(() => {
    if (!pluginSelectionSupported) return;
    const state = pluginPreview.state;
    if (state.status !== "ready") return;
    const nextSelection = reconcilePluginSelection(pluginSelectionRef.current, state.response);
    setPluginSelection(nextSelection);
    setKnownSelectionIssues(pluginSelectionIssues(nextSelection, state.response));
    // A selection change clears the cached issues until its new preview settles.
    // Re-running this effect for that selection change would restore old issues.
  }, [pluginPreview.state, pluginSelectionSupported]);

  // A refresh triggered by a selection toggle keeps the previous response on
  // the loading state (see usePluginPreview), so the disclosure and its list
  // stay mounted instead of flashing an empty "Inspecting plugins…" panel.
  const previewResponse = pluginSelectionSupported ? (pluginPreview.state.response ?? null) : null;
  const configuredPluginNames = previewResponse ? selectedPluginNames(pluginSelection, previewResponse) : [];
  const currentSelectionIssues =
    pluginPreview.state.status === "ready" ? pluginSelectionIssues(pluginSelection, pluginPreview.state.response) : [];
  const explicitSelectionLoading =
    pluginSelectionSupported && pluginSelection.mode === "explicit" && pluginPreview.state.status === "loading";
  const pluginSelectionBlocked =
    explicitSelectionLoading || knownSelectionIssues.length > 0 || currentSelectionIssues.length > 0;

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
    // Writing the prompt is what starting an agent IS, so the caret starts
    // there rather than on whichever field happens to be first in the DOM.
    textareaRef.current?.focus();

    let active = true;
    client.request("evener/harnesses/list", {}).then(
      (r) => {
        if (active) setHarnesses(r.data);
      },
      () => {},
    );
    client.request("evener/launch/schema", {}).then(
      (r) => {
        if (active) setSchemaOptions(perLaunchEvenerOptions(r));
      },
      () => {},
    );
    // Stale-model cleanup (floor §1.10): sweep the persisted defaults against the
    // live model list; if this project's prefilled model was discarded, clear it
    // and surface the inline notice.
    loadModelList().then(
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

  // Feeds the busy Loader's elapsed readout a fresh `now` once a second -
  // only while busy, so the wait for a resolved cwd/model spawn never runs a
  // timer with nothing on screen reading it.
  useEffect(() => {
    if (!busy) return undefined;
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [busy]);

  // Pane-level merged catalog for the Effort select's per-model ladder: the
  // same model/list catalog the pickers load on demand. Reloads with the
  // harness/cwd scope, exactly like loadCatalog itself. Fail-open: a rejected
  // load leaves modelCatalog null and the select on the fallback ladder.
  // Debounced because cwd updates straight from the path field's onChange. The
  // catalog is scoped by harness+cwd, so it settles with the path instead of
  // chasing every keystroke; model pickers call the same keyed loader on
  // demand.
  useEffect(() => {
    let active = true;
    const settle = setTimeout(() => {
      loadCatalog().then(
        (catalog) => {
          if (active) setModelCatalog((previous) => mergeCatalogSnapshot(previous, catalog));
        },
        () => {},
      );
    }, CATALOG_SETTLE_MS);
    return () => {
      active = false;
      clearTimeout(settle);
    };
  }, [loadCatalog]);

  // Branch HEAD resolution (floor §1.7): the readout is read-only, so HEAD is
  // its ONLY source - re-resolved on every working-dir change with no
  // user-edited escape hatch to respect. `active` still guards a late response
  // from a directory the user has already navigated away from.
  useEffect(() => {
    if (cwd.trim() === "") return undefined;
    let active = true;
    resolveHeadBranch(client, cwd).then((head) => {
      if (active) setBranch(head);
    });
    return () => {
      active = false;
    };
  }, [client, cwd]);

  // Default-model preview (kata xgk8): thread/start resolves Model from the
  // SAME layered launch config this previews (app_threadlifecycle.go -
  // overrides.Model wins when set, otherwise Effective.Model; empty refuses
  // the whole submit with "model is required"). advancedOverrides is passed
  // through rather than {}: the daemon's own schema exposes a SECOND "model"
  // wireField inside Advanced options (schema.go's per-launch modelPicker),
  // and floor §1.11 has that override win at submit time too - a model set
  // ONLY there must satisfy this preview without the top-level chip ever
  // leaving "(default)". Re-run on every cwd or advancedOverrides change,
  // since the resolved default is a property of the directory (project/repo
  // layers) plus whatever the user has already configured. Fail OPEN like
  // preflightDir/branch resolution: no cwd yet, or a rejected preview (RPC
  // down), leaves noDefaultModel false rather than blocking Start on an
  // unconfirmed state.
  //
  // Uncredentialed-default fallback: offering "(default)" is a certain
  // thread/start failure when the resolved default's provider has no
  // credentials configured - the server now says so plainly
  // ("provider credentials missing for openai...", spawn.go), but a UI that
  // still points at that dead end is no better. There is no direct
  // per-provider credential RPC the spawn form can key on, but model/list IS
  // already keyed on it: launchCheckModels() (cmd/evener/internal/launchcheck)
  // only adds a provider's models to the launchable SET once it can actually
  // construct that provider's client, so a provider missing from model/list's
  // result is - as far as this form can honestly tell - not credentialed. If
  // the resolved default's provider is absent from that SET, preselect the
  // first model model/list offers (same order the picker's provider groups
  // render in, scopedCatalog.ts) instead of leaving Model at "" - which also
  // removes "(default)" from the trigger, since that label only ever renders
  // for value === "" (ModelCatalog's own contract). A sticky per-project
  // model (or any value the user already picked) is never touched: the
  // fallback only fires when Model is still untouched, read from modelRef so
  // this effect doesn't itself re-run on every model change.
  useEffect(() => {
    if (cwd.trim() === "") {
      setNoDefaultModel(false);
      setResolvedDefaultModel("");
      setResolvedDefaults(null);
      return undefined;
    }
    let active = true;
    const settle = setTimeout(() => {
      Promise.all([resolveConfig(advancedOverrides), loadModels().catch(() => null)]).then(
        ([result, models]) => {
          if (!active) return;
          setResolvedDefaults(result.effective);
          const defaultModel = (result.effective.model ?? "").trim();
          setNoDefaultModel(defaultModel === "");
          setResolvedDefaultModel(defaultModel);
          if (defaultModel === "" || modelRef.current !== "" || !models || models.length === 0) return;
          const slash = defaultModel.indexOf("/");
          const defaultProvider = slash === -1 ? defaultModel : defaultModel.slice(0, slash);
          const defaultCredentialed = models.some((m) => m.provider === defaultProvider);
          const fallback = models[0];
          if (!defaultCredentialed && fallback) {
            setModel(`${fallback.provider}/${fallback.model}`);
          }
        },
        () => {
          if (active) {
            setNoDefaultModel(false);
            setResolvedDefaultModel("");
            setResolvedDefaults(null);
          }
        },
      );
    }, CATALOG_SETTLE_MS);
    return () => {
      active = false;
      clearTimeout(settle);
    };
  }, [cwd, advancedOverrides, resolveConfig, loadModels]);

  // The Effort ladder belongs to the model that will actually launch, in the
  // same precedence thread/start applies (floor §1.11, schema.ts's
  // resolveScalars): an Advanced-options model override first, then the
  // top-level chip, then the hub's resolved default for this cwd.
  const advancedModel = typeof advancedOverrides.model === "string" ? advancedOverrides.model.trim() : "";
  const effortModel = [advancedModel, model, resolvedDefaultModel].find((candidate) => candidate !== "") ?? "";
  const knownEffortLevels = catalogEffortLevels(
    effortModel === ""
      ? undefined
      : modelCatalog?.models.find((entry) => `${entry.provider}/${entry.model}` === effortModel),
  );
  const effortLevels = knownEffortLevels ?? FALLBACK_EFFORT_LEVELS;
  const effortDisabled = !usesEvenerModels || (knownEffortLevels !== null && knownEffortLevels.length === 0);
  // An effort the ladder doesn't name but state still holds. Only the FALLBACK
  // ladder produces one: the reset effect below deliberately skips when the
  // catalog knows nothing about the model, because clobbering a sticky default
  // on a guessed ladder loses the user's setting.
  //
  // Such a value must still be OFFERED. A native select handed a value with no
  // matching <option> renders its first one instead, so the field would read
  // "(default)" while thread/start receives the preserved level -- the select
  // must never show one effort and submit another.
  const preservedEffort =
    reasoningEffort !== "" && reasoningEffort !== "none" && !effortLevels.includes(reasoningEffort)
      ? reasoningEffort
      : null;
  // The effort a session started now would inherit: prepended onto the empty
  // option's "(default)" once launch/resolve has landed with one.
  const resolvedEffortDefault =
    typeof resolvedDefaults?.reasoningEffort === "string" ? resolvedDefaults.reasoningEffort.trim() : "";
  const effortOptions = [
    { value: "", label: resolvedEffortDefault !== "" ? `${resolvedEffortDefault} (default)` : "(default)" },
    ...effortLevels.filter((level) => level !== "none").map((level) => ({ value: level, label: level })),
    ...(preservedEffort === null ? [] : [{ value: preservedEffort, label: preservedEffort }]),
    { value: "none", label: effortLabel("none", effortLevels) },
  ];
  // Access mode is the chip-level face of the launch-config sandbox field
  // (floor §1.8), so its empty option follows the same rule as Effort's:
  // name the inherited sandbox in the chip's own friendly wording
  // ("Workspace write (default)") once resolve lands, plain "(default)"
  // until then.
  const accessOptions = [
    { value: "", label: accessModeDefaultLabel(resolvedDefaults?.sandbox ?? "") },
    ...ACCESS_MODE_OPTIONS,
  ];

  // A chosen effort the (new) model's ladder doesn't name can't stay selected
  // - the select must never display a value it doesn't offer, so the choice
  // resets to "(default)". Only a KNOWN ladder resets: the fallback ladder is
  // a guess, and clobbering a sticky default on a guess would lose the user's
  // setting (the daemon clamps a level the model doesn't accept).
  useEffect(() => {
    if (knownEffortLevels === null) return;
    if (reasoningEffort !== "" && reasoningEffort !== "none" && !knownEffortLevels.includes(reasoningEffort)) {
      setReasoningEffort("");
    }
  }, [knownEffortLevels, reasoningEffort]);

  function handlePluginSelectionChange(next: PluginSelectionState): void {
    setKnownSelectionIssues((issues) => {
      if (next.mode === "default") return [];
      const selectedNames = new Set(next.names);
      return issues.filter((issue) => selectedNames.has(issue.name));
    });
    setPluginSelection(next);
  }

  function handleHarnessChange(next: string): void {
    setHarness(next);
    if (!harnessSupportsPluginSelection(next, harnesses)) handlePluginSelectionChange({ mode: "default" });
    // Switching to a non-evener harness always blanks the model; switching to a
    // evener-model harness only blanks a value that isn't already provider/model
    // shaped (floor §1.10, spawn.js:395-402).
    if (!harnessUsesEvenerModels(next, harnesses)) setModel("");
    else if (model !== "" && !model.includes("/")) setModel("");
  }

  function handleModelChange(next: string): void {
    setModel(next);
    if (next !== "") setStaleNotice(null); // any new model clears the discard notice (floor §1.10)
  }

  // The picker already loaded the picked entry's catalog (with
  // reasoningEffortLevels / supportsReasoning) when the user selected a model;
  // merge that entry into the pane-level modelCatalog so the Effort ladder is
  // correct immediately. Without this, the Effort select waits for the
  // pane-level debounced catalog load (which may have failed enrichment, or
  // not landed yet) and falls back to the generic ladder instead of the
  // model's own.
  function handleModelPickEntry(entry: ModelCatalogEntry): void {
    handleModelChange(`${entry.provider}/${entry.model}`);
    setModelCatalog((prev) => {
      const models = prev?.models ?? [];
      const recent = prev?.recent ?? [];
      const diagnostics = prev?.diagnostics ?? [];
      const key = `${entry.provider}/${entry.model}`;
      const idx = models.findIndex((m) => `${m.provider}/${m.model}` === key);
      if (idx >= 0) {
        const nextModels = [...models];
        nextModels[idx] = mergeCatalogEntry(nextModels[idx], entry);
        return { models: nextModels, recent, diagnostics };
      }
      return { models: [...models, entry], recent, diagnostics };
    });
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
    if (pluginSelectionBlocked) {
      busyRef.current = false;
      setBusy(false);
      setBusyStartedAt(null);
      return;
    }
    // The advanced schema's sandbox wins over the access-mode chip (floor §1.8);
    // its model/reasoningEffort win over the chips (floor §1.11) - resolveScalars
    // hoists them into the top-level fields the daemon prefers over overrides.
    const overrides = combinedOverrides;
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
    saveDefaults({
      cwd,
      harness,
      model,
      accessMode,
      reasoningEffort,
      harnessUsesEvenerModels: usesEvenerModels,
    });
    // Reset transient form state on success, before navigating away (floor
    // §1.14 L186: the pending-attachment bag is cleared and the paste
    // marker-counter reset). The spawn pane is a dockview singleton that can
    // still be mounted behind the session pane this navigates to, so without
    // this an already-sent prompt/image stays staged and re-sendable if the
    // user returns to it. Sticky defaults (harness/model/cwd/access
    // mode, floor §1.9-§1.10) are deliberately left untouched - only the
    // one-shot prompt/attachments reset.
    updatePrompt("");
    attachments.clearSubmitted(submittedMarkers);
    handlePluginSelectionChange({ mode: "default" });
    // Same defect class: both callers set busy=true before awaiting this
    // function but only their OWN catch blocks ever reset it back to false,
    // so a success fell through with the button stuck disabled/"Starting…"
    // forever on a pane that can outlive the navigation below.
    busyRef.current = false;
    setBusy(false);
    setBusyStartedAt(null);
    const url = paneToURL("session", { ref });
    if (url) navigate(url);
  }

  async function handleSpawn(): Promise<void> {
    // kata 61v2: busyRef, not `busy` state - see its declaration for why.
    if (busyRef.current) return;
    // kata xgk8: the Start button is already disabled in this state, but the
    // ⌘/Ctrl+Enter chord (handlePromptKeyDown) reaches this function directly
    // - a submit that CANNOT succeed must never fire regardless of path in.
    // The field's own inline note already says why, so no toast here.
    if (modelRequired || providerRequired) return;
    if (pluginSelectionBlocked) return;
    if (attachments.hasPending) {
      toasts.push("error", "Image attachment is still processing.");
      return;
    }
    // A blank prompt is NOT an error: it starts a dormant session, which is
    // what the prompt placeholder promises. buildInput drops the empty text
    // item, and hubThreadStart starts a turn only for a non-empty input
    // (cmd/evener-hub/app_threadlifecycle.go), so the session is created and
    // simply waits for its first prompt in the session composer.
    busyRef.current = true;
    setBusy(true);
    setBusyStartedAt(Date.now());
    try {
      const outcome = await preflightDir(client, cwd);
      if (outcome.kind === "abort") {
        toasts.push("error", outcome.message);
        busyRef.current = false;
        setBusy(false);
        setBusyStartedAt(null);
        return;
      }
      if (outcome.kind === "offer-create") {
        setCreateDialogPath(outcome.path);
        busyRef.current = false;
        setBusy(false);
        setBusyStartedAt(null);
        return;
      }
      await doSpawn();
    } catch (err) {
      // friendlyLaunchErrorMessage, not errorText: doSpawn's thread/start call
      // can reject with AppwireClient's own "cannot call ... while state is
      // closed" text if the client tears down mid-submit, which is internal
      // wiring detail, never something to toast at a person - and when the
      // hub answered but no agent daemon could be reached for cwd (the
      // first-run worst moment, T3), the launch-check's own raw text is
      // replaced with actionable copy instead.
      toasts.push("error", `Start failed: ${friendlyLaunchErrorMessage(err)}`);
      busyRef.current = false;
      setBusy(false);
      setBusyStartedAt(null);
    }
  }

  async function handleCreateConfirm(): Promise<void> {
    if (busyRef.current) return; // same re-entrancy guard as handleSpawn (kata 61v2)
    const path = createDialogPath;
    if (path === null) return;
    busyRef.current = true;
    setBusy(true);
    setBusyStartedAt(Date.now());
    try {
      await createDir(client, path);
      await doSpawn();
    } catch (err) {
      // friendlyLaunchErrorMessage, not errorText: doSpawn's thread/start call
      // can reject with AppwireClient's own "cannot call ... while state is
      // closed" text if the client tears down mid-submit, which is internal
      // wiring detail, never something to toast at a person - and when the
      // hub answered but no agent daemon could be reached for cwd (the
      // first-run worst moment, T3), the launch-check's own raw text is
      // replaced with actionable copy instead.
      toasts.push("error", `Start failed: ${friendlyLaunchErrorMessage(err)}`);
      busyRef.current = false;
      setBusy(false);
      setBusyStartedAt(null);
    } finally {
      setCreateDialogPath(null);
    }
  }

  const harnessOptions =
    harnesses.length > 0
      ? harnesses.map((h) => ({ value: h.id, label: h.label }))
      : [{ value: "evener", label: "evener" }];
  return (
    <PaneScaffold title="Start an agent" mobileTitle="new">
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

        <div className={CLASS.mobilePromptIntro} data-testid="spawn-mobile-prompt-intro">
          <h3 className={CLASS.mobilePromptHeading}>What should the agent do?</h3>
          <p className={CLASS.mobilePromptSubtitle}>Leave blank to start a dormant session.</p>
        </div>

        {/* The prompt comes FIRST and takes the page's slack: writing the
            prompt is what starting an agent IS, and everything below it is
            configuration that mostly stays where it was last left. The card is
            the same widgets/promptcard the session composer renders. */}
        <Dropzone onFiles={(files) => attachments.ingestFiles(files, (message) => toasts.push("error", message))}>
          <PromptCard
            data-testid="spawn-prompt-card"
            controlsTestId="spawn-controls"
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
              /* The composer's own leading cluster (Composer.tsx's .leading):
                 attach, then the model trigger. Both stay INSIDE the card's
                 control row at every width, which is what "reuse the
                 composer's UI" means (issue #198) - the pane used to hand
                 this row a class that turned it into a fixed viewport band on
                 a phone, stranding the paperclip at the bottom of the screen,
                 far from the prompt it attaches to. */
              <div className={CLASS.leading}>
                <IconButton
                  label="Attach image"
                  icon={<AttachIcon />}
                  variant="quiet"
                  size="xs"
                  type="button"
                  data-testid="spawn-attach"
                  onClick={() => fileInputRef.current?.click()}
                />
                {/* Phone only (the class is display:none until the 899px
                    block): desktop sets the model in the configuration row
                    below, so an in-card trigger there would be a second
                    control for one setting. The label follows the same rules
                    the desktop field's does - the required-choice word when
                    the hub has confirmed no default (kata xgk8), otherwise
                    the chosen model, the resolved default model's own
                    "<model> (default)", or plain "(default)" until the
                    resolve lands. */}
                <span className={CLASS.modelTrigger} data-testid="spawn-model-slot">
                  <ModelSwitchTrigger
                    label={
                      modelRequired
                        ? MODEL_CHOOSE_LABEL
                        : model || (resolvedDefaultModel !== "" ? `${resolvedDefaultModel} (default)` : "(default)")
                    }
                    value={model}
                    loadCatalog={loadCatalog}
                    onPick={handleModelPickEntry}
                    data-testid="spawn-model-trigger"
                    valueTestId="spawn-model-value"
                  />
                </span>
              </div>
            }
            actions={
              <Tooltip label={`Start the agent · ${chordLabel(["Mod", "Enter"])}`}>
                <Button
                  variant="primary"
                  size="xs"
                  data-testid="spawn-submit"
                  aria-label="Start"
                  icon={busy ? undefined : <SendIcon />}
                  onClick={() => void handleSpawn()}
                  disabled={busy || modelRequired || providerRequired || pluginSelectionBlocked}
                >
                  {busy ? (
                    <Loader label="Starting" startedAt={busyStartedAt ?? now} now={now} />
                  ) : (
                    <span className={CLASS.submitLabel}>Start</span>
                  )}
                </Button>
              </Tooltip>
            }
          />
        </Dropzone>
        {providerRequired && (
          <div className={CLASS.notice} role="status">
            <span>Connect a provider to use a model. Sign in or add an API key here.</span>
            <Button onClick={() => setConnectingProvider(true)}>Connect provider</Button>
          </div>
        )}
        {usesEvenerModels && providerSetup.status === "error" && (
          <div className={CLASS.notice} role="status">
            <span>Could not check provider configuration.</span>
            <Button onClick={() => void providerSetup.retry()}>Retry provider check</Button>
            <Button variant="quiet" onClick={() => setConnectingProvider(true)}>
              Review providers
            </Button>
          </div>
        )}
        {connectingProvider && <ConnectProviderDialog onClose={closeProviderSetup} onConnected={closeProviderSetup} />}
        <input ref={fileInputRef} type="file" accept="image/*" multiple hidden onChange={handleFilePicker} />

        {/* The same AttachmentTile the session composer draws (kata kbg7):
            staging an image is one act, so it looks like one thing whichever
            surface starts it. The tile is also the whole pending signal - it
            deliberately says nothing in words, since a pending attachment
            resolves in a few frames and this UI cannot report progress it
            does not have (widgets/skeleton's honest-liveness rule). */}
        {attachments.items.length > 0 && (
          <div className={CLASS.attachments} data-testid="spawn-attachments">
            {attachments.items.map((item) => (
              <AttachmentTile key={item.marker} item={item} onRemove={() => attachments.removeItem(item.marker)} />
            ))}
          </div>
        )}

        {/* ONE compact row of configuration beneath the card, widest field
            first: the working directory is the only one that changes often.
            Harness moved into Advanced options - most installs have exactly one,
            and a field whose answer is always "evener" should not lead the page. */}
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
                the directory row as a mono suffix ("~/code/evener · main") because
                it is a property of that directory. */}
            {branch !== "" && (
              <span className={CLASS.branch} data-testid="spawn-branch" title={`HEAD ${branch}`}>
                <span className={CLASS.branchSeparator} aria-hidden="true">
                  ·
                </span>
                {branch}
              </span>
            )}
          </div>

          {/* data-testid on the wrapper, not the trigger: the trigger is
              ModelCatalog's own button and has no hook to give, and the pane
              now renders a SECOND "— change model" button (the card's
              phone-only ModelSwitchTrigger). A test or a scenario driving the
              desktop field has to say which one it means. */}
          <div className={CLASS.cfgModel} data-testid="spawn-desktop-model">
            <span className={CLASS.fieldLabel} id="spawn-model-label">
              Model
            </span>
            <ModelField
              value={model}
              onChange={handleModelChange}
              onPickEntry={handleModelPickEntry}
              loadCatalog={loadCatalog}
              emptyLabel={
                modelRequired
                  ? MODEL_CHOOSE_LABEL
                  : resolvedDefaultModel !== ""
                    ? `${resolvedDefaultModel} (default)`
                    : undefined
              }
            />
            {modelRequired && (
              <p className={CLASS.modelNote} role="alert">
                This hub has no default model configured — choose one to start.
              </p>
            )}
          </div>

          <FormRow label="Effort" htmlFor="spawn-reasoning">
            <Select
              id="spawn-reasoning"
              value={reasoningEffort}
              onChange={(e) => setReasoningEffort(e.target.value)}
              options={effortOptions}
              disabled={effortDisabled}
            />
          </FormRow>
        </div>

        {pluginSelectionSupported && (
          <div className={CLASS.pluginDesktop} data-testid="spawn-plugin-desktop">
            {previewResponse === null && (
              <div className={CLASS.pluginSummary} data-testid="spawn-plugin-summary" role="status">
                <strong>Plugins for this session</strong>
                {pluginPreview.state.status === "loading" && <span>Inspecting plugins…</span>}
                {pluginPreview.state.status === "error" && (
                  <>
                    <span title={pluginPreview.state.message}>
                      Couldn't inspect plugins: {pluginPreview.state.message}
                    </span>
                    <Button variant="quiet" size="xs" type="button" onClick={pluginPreview.retry}>
                      Retry
                    </Button>
                  </>
                )}
              </div>
            )}
            {previewResponse !== null && (
              <Disclosure
                id="spawn-plugin-selection"
                data-testid="spawn-plugin-disclosure"
                summary={
                  <div className={CLASS.pluginSummary} data-testid="spawn-plugin-summary">
                    <strong>Plugins for this session</strong>
                    <span>
                      {configuredPluginNames.length > 0
                        ? `Configured plugins: ${configuredPluginNames.join(", ")}`
                        : "Configured plugins: none"}
                    </span>
                  </div>
                }
              >
                <PluginSelectionPanel
                  preview={previewResponse}
                  selection={pluginSelection}
                  removeOnly={pluginPreview.state.status === "error"}
                  onSelectionChange={handlePluginSelectionChange}
                  onRetry={pluginPreview.retry}
                />
              </Disclosure>
            )}
          </div>
        )}

        <div className={CLASS.mobileConfig} data-testid="spawn-mobile-config">
          <MobileSettingRows
            harness={harness || "evener"}
            harnessOptions={harnessOptions}
            onHarnessChange={handleHarnessChange}
            cwd={cwd}
            onCwdChange={setCwd}
            complete={complete}
            listRecents={listRecents}
            fallbackDir={getGlobalLastWorkingDir()}
            onCwdPanelClose={setGlobalLastWorkingDir}
            branch={branch}
            reasoningEffort={reasoningEffort}
            reasoningOptions={effortOptions}
            reasoningDisabled={effortDisabled}
            onReasoningChange={setReasoningEffort}
            accessMode={accessMode}
            accessOptions={accessOptions}
            onAccessChange={setAccessMode}
            pluginPreview={pluginPreview.state}
            pluginSelection={pluginSelection}
            pluginsSupported={pluginSelectionSupported}
            onPluginSelectionChange={handlePluginSelectionChange}
            onPluginRetry={pluginPreview.retry}
          />
        </div>

        <AdvancedOptions
          options={schemaOptions}
          onOverridesChange={setAdvancedOverrides}
          validatePath={validatePath}
          resolveConfig={resolveConfig}
          loadCatalog={loadCatalog}
          complete={complete}
          resolvedDefaults={resolvedDefaults ?? undefined}
        >
          <FormRow label="Harness" htmlFor="spawn-harness">
            <Select
              id="spawn-harness"
              value={harness || "evener"}
              onChange={(e) => handleHarnessChange(e.target.value)}
              options={harnessOptions}
            />
          </FormRow>
          <FormRow label="Access mode" htmlFor="spawn-access">
            <Select
              id="spawn-access"
              value={accessMode}
              onChange={(e) => setAccessMode(e.target.value)}
              options={accessOptions}
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
