// Session creation keeps the project directory above the prompt and the
// less frequently changed launch settings below it. The directory picker
// commits once, so browsing does not churn directory-dependent configuration.
import {
  Component,
  type JSX,
  type LazyExoticComponent,
  lazy,
  memo,
  type ReactNode,
  Suspense,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { friendlyLaunchErrorMessage } from "../../protocol/errors";
import type {
  HarnessDescriptor,
  LaunchConfigLayer,
  LaunchOption,
  ModelListResponse,
  PluginSelectionError,
} from "../../protocol/types.gen";
import { useClient } from "../../shell/clientContext";
import { splitModelId } from "../../shell/palette/commands";
import type { PaneProps } from "../../shell/paneRegistry";
import { effortLabel } from "../../shell/reasoningEffort";
import { navigate, paneToURL } from "../../shell/routing";
import { useExtensionsStore } from "../../stores/extensions";
import {
  Button,
  Chevron,
  ConfirmDialog,
  chordLabel,
  Dialog,
  Dropzone,
  FormRow,
  IconButton,
  Loader,
  PaneScaffold,
  PromptCard,
  Select,
  SendIcon,
  Textarea,
  Tooltip,
  useToasts,
} from "../../widgets";
import { CloseIcon } from "../../widgets/dialog/CloseIcon";
import { DirectoryIcon, DirectoryPicker } from "../../widgets/directorypicker";
import { Disclosure } from "../../widgets/disclosure";
import { requireClass } from "../../widgets/internal/requireClass";
import type { ModelCatalog, ModelCatalogEntry } from "../../widgets/modelCatalog";
import { modelListToCatalog } from "../../widgets/modelCatalog/catalogClient";
import { mergeCatalogEntry, mergeCatalogSnapshot } from "../../widgets/modelCatalog/scopedCatalog";
import { basename } from "../../widgets/pathfield/pathRows";
import { ModelSwitchTrigger } from "../session/chrome/ModelSwitchTrigger";
import { AttachmentTile } from "../session/composer/AttachmentTile";
import { AttachIcon } from "../session/composer/attachments/AttachIcon";
import { imageFilesFromClipboard } from "../session/composer/attachments/clipboard";
import { type TextEditor, useAttachments } from "../session/composer/attachments/useAttachments";
import { matchBuiltinInvocation } from "../session/composer/builtinCommand";
import { SlashCompletionMenu, optionId as slashOptionId } from "../session/composer/SlashCompletionMenu";
import {
  filterSlashMenuItems,
  mergeSlashCommands,
  parseSlashToken,
  type SlashMenuItem,
  type SlashToken,
  spliceSlashCommand,
} from "../session/composer/slashCompletion";
import { AdvancedOptions } from "./AdvancedOptions";
import { ACCESS_MODE_OPTIONS, accessModeDefaultLabel } from "./accessMode";
import { resolveHeadBranch } from "./branch";
import { isStaleConnectDialogChunkError, loadConnectDialog } from "./connectDialogChunk";
import { harnessSupportsPluginSelection, harnessUsesEvenerModels } from "./harnessModels";
import { MobileSettingRows } from "./MobileSettingRows";
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
import {
  resolveSpawnEffortItems,
  resolveSpawnModelItems,
  runSpawnBuiltinAfterStart,
  spawnBuiltinCommands,
} from "./spawnSlashMenu";
import { startThread } from "./startThread";
import { readUrlPrefill } from "./urlPrefill";
import { usePluginPreview } from "./usePluginPreview";
import { useProviderSetup } from "./useProviderSetup";
import { useSpawnSlashCatalog } from "./useSpawnSlashCatalog";

// Below-the-fold dialog: mounted only after the user clicks "Connect
// provider" (connectingProvider state), never on first paint. The chunk -
// the dialog plus its instance-credential editors (instanceDialogs,
// oauthDialogs, oauthFlow) - stays out of the spawn pane's initial bundle
// and loads on first open.
//
// A rejected chunk lands on ConnectProviderDialogBoundary below, scoped to
// the dialog: without it the lazy() rethrow would bubble into whatever
// boundary happens to sit above this pane (on desktop the dock's
// workspace-failure state - misleading, the workspace is fine - and on
// mobile StackHost mounts panes with no boundary at all, so the whole app).
// Retry swaps in a fresh lazy component - a rejected payload rethrows
// forever, so re-rendering the old one could never recover. The Suspense
// fallback is a real dialog reading "Loading…": a null fallback would leave
// the click that opened the dialog with no visible response while the chunk
// fetches, and the boundary below renders the same Dialog shell on failure
// so the pending/failure states share one frame.
//
// The fresh component loads over loadConnectDialog's cache-busted URL -
// Chrome retains a failed module fetch by URL (see connectDialogChunk.ts),
// so a same-URL retry would replay the cached failure instead of reaching
// the network.
interface ConnectProviderDialogBoundaryProps {
  // Swaps in a fresh lazy component to load the chunk again. The boundary
  // clears its own failure state alongside it - both halves are needed, and
  // neither is any use without the other.
  onRetry: () => void;
  // True once a cache-busted retry has already failed: a deploy that replaced
  // the hashed chunk filename 404s forever under a new query param, so the
  // second strike offers a page reload instead of another same-file fetch
  // (the DockRegion chunk-boundary pattern).
  reloadAvailable: boolean;
  onClose: () => void;
  children: ReactNode;
}

interface ConnectProviderDialogBoundaryState {
  // The failed chunk's own message ("Failed to fetch dynamically imported
  // module: ..."), shown verbatim - a stated failure is worth more to
  // whoever hits it than a generic apology.
  failure: string | null;
}

class ConnectProviderDialogBoundary extends Component<
  ConnectProviderDialogBoundaryProps,
  ConnectProviderDialogBoundaryState
> {
  state: ConnectProviderDialogBoundaryState = { failure: null };

  static getDerivedStateFromError(error: unknown): ConnectProviderDialogBoundaryState {
    // A logic bug inside the resolved dialog (module init, render) surfaces
    // through this same boundary as a chunk-fetch failure does. Only a stale
    // hashed-asset URL (JS or CSS) is a chunk-load failure worth a retry:
    // anything else keeps unwinding to the next boundary above instead of
    // being misreported - and retried - as a network fetch.
    if (!isStaleConnectDialogChunkError(error)) throw error;
    return { failure: error instanceof Error ? error.message : String(error) };
  }

  private retry = () => {
    this.setState({ failure: null });
    this.props.onRetry();
  };

  render(): ReactNode {
    if (this.state.failure === null) return this.props.children;
    return (
      <Dialog
        open
        onClose={this.props.onClose}
        title="Couldn't load the connect dialog"
        footer={
          <>
            <Button variant="quiet" onClick={this.props.onClose}>
              Close
            </Button>
            <Button variant="primary" onClick={this.retry}>
              Retry
            </Button>
            {/* reloadAvailable alone counts retries, so a logic bug that rode
                in on a stale chunk URL would earn a page reload that cannot
                fix it. The failed retry must itself name a stale hashed
                asset (the DockRegion chunk-boundary pattern). */}
            {this.props.reloadAvailable && isStaleConnectDialogChunkError(this.state.failure) && (
              <Button variant="quiet" onClick={() => window.location.reload()}>
                Reload page
              </Button>
            )}
          </>
        }
      >
        {this.state.failure}
      </Dialog>
    );
  }
}

type ConnectProviderDialogComponent = (props: { onClose(): void; onConnected(): void }) => JSX.Element;
type ConnectProviderDialogChunk = LazyExoticComponent<ConnectProviderDialogComponent>;

function lazyConnectProviderDialog(cacheBust = false): ConnectProviderDialogChunk {
  // ConnectProviderDialog is a named export, so the import() promise is
  // adapted the same way App.tsx's own DevHarnessRoute does for
  // dev/DevHarness.tsx.
  return lazy(() => loadConnectDialog(cacheBust).then((m) => ({ default: m.ConnectProviderDialog })));
}

// Module scope, not per mount: a lazy() component caches its resolved
// module on its own payload, so one shared component means the chunk is
// fetched once per page load and every later open renders the dialog
// straight away instead of suspending again.
let connectProviderDialog = lazyConnectProviderDialog();

// A payload caches its outcome for the life of the module, success or
// failure, so one test's failed chunk would otherwise be every later test's
// failed chunk. Mirrors the resetXForTests precedent every other module
// singleton here follows (stores/navigation/store.ts's own note); no production code
// should ever call it.
export function resetConnectDialogChunkForTests(): void {
  connectProviderDialog = lazyConnectProviderDialog();
}

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
  cfgDir: requireClass(styles.cfgDir, "spawn.module.css", "cfgDir"),
  branch: requireClass(styles.branch, "spawn.module.css", "branch"),
  directoryButton: requireClass(styles.directoryButton, "spawn.module.css", "directoryButton"),
  directoryText: requireClass(styles.directoryText, "spawn.module.css", "directoryText"),
  directoryPath: requireClass(styles.directoryPath, "spawn.module.css", "directoryPath"),
  notice: requireClass(styles.notice, "spawn.module.css", "notice"),
  attachments: requireClass(styles.attachments, "spawn.module.css", "attachments"),
  leading: requireClass(styles.leading, "spawn.module.css", "leading"),
  modelTrigger: requireClass(styles.modelTrigger, "spawn.module.css", "modelTrigger"),
  effortTrigger: requireClass(styles.effortTrigger, "spawn.module.css", "effortTrigger"),
  effortSeparator: requireClass(styles.effortSeparator, "spawn.module.css", "effortSeparator"),
  effortValue: requireClass(styles.effortValue, "spawn.module.css", "effortValue"),
  effortChevron: requireClass(styles.effortChevron, "spawn.module.css", "effortChevron"),
  effortSelect: requireClass(styles.effortSelect, "spawn.module.css", "effortSelect"),
  srOnly: requireClass(styles.srOnly, "spawn.module.css", "srOnly"),
  mobileConfig: requireClass(styles.mobileConfig, "spawn.module.css", "mobileConfig"),
  promptIntro: requireClass(styles.promptIntro, "spawn.module.css", "promptIntro"),
  promptHeading: requireClass(styles.promptHeading, "spawn.module.css", "promptHeading"),
  promptSubtitle: requireClass(styles.promptSubtitle, "spawn.module.css", "promptSubtitle"),
  promptAnchor: requireClass(styles.promptAnchor, "spawn.module.css", "promptAnchor"),
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

// StartingLoader owns the busy elapsed clock: it ticks its own `now` once a
// second while mounted and renders the SAME Loader the pane rendered inline
// (label "Starting", startedAt as stamped by the submit). Mounted only while
// `busy`, so the interval exists exactly as long as the old pane-level effect
// ran it - but a tick re-renders this leaf alone, never the whole pane.
const StartingLoader = memo(function StartingLoader({ startedAt }: { startedAt: number }): JSX.Element {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, []);
  return <Loader label="Starting" startedAt={startedAt} now={now} />;
});

export default function Spawn(_props: PaneProps<SpawnPaneParams>) {
  const client = useClient();
  const toasts = useToasts();
  const providerSetup = useProviderSetup();
  const [connectingProvider, setConnectingProvider] = useState(false);
  const closeProviderSetup = useCallback(() => setConnectingProvider(false), []);
  const providerConnected = useCallback(() => {
    setConnectingProvider(false);
    void providerSetup.retry();
  }, [providerSetup.retry]);
  // A retry needs a NEW lazy component, not a re-render of the old one:
  // React.lazy stores the rejection on its payload and rethrows that same
  // error on every subsequent render, forever.
  const [ProviderDialog, setProviderDialog] = useState<ConnectProviderDialogChunk>(connectProviderDialog);
  // A retry re-fetches the same hashed filename over a cache-busted URL:
  // enough for a transient failure, useless once a deploy has removed the
  // file. Counting retries lets the boundary offer a page reload on the
  // second strike (the DockRegion chunk-boundary pattern).
  const [dialogRetryCount, setDialogRetryCount] = useState(0);
  const retryProviderDialog = useCallback(() => {
    setDialogRetryCount((count) => count + 1);
    const nextDialog = lazyConnectProviderDialog(true);
    // Publish the new payload before it resolves so a remount during the
    // retry shares the in-flight request instead of restoring the rejected
    // payload that caused the boundary.
    connectProviderDialog = nextDialog;
    setProviderDialog(() => nextDialog);
  }, []);

  const [prompt, setPrompt] = useState("");
  const [harness, setHarness] = useState("");
  const [model, setModel] = useState(""); // qualified "provider/model", or "" for the harness default
  const [reasoningEffort, setReasoningEffort] = useState("");
  const [cwd, setCwd] = useState("");
  const [directoryOpen, setDirectoryOpen] = useState(false);
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
  // flips busy true; StartingLoader below owns the 1s tick and mounts only
  // while busy, so no interval runs with nothing on screen reading it.
  const [busyStartedAt, setBusyStartedAt] = useState<number | null>(null);
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
  // The scope the pane catalog was fetched for ("harness + cwd", the
  // model/list key loadCatalog uses). The catalog merges snapshots across
  // scopes for picker display continuity, but /model pre-start validation
  // must only read a snapshot fetched for the CURRENT scope: during the
  // settle window after a cwd/harness change, modelCatalog still holds the
  // previous scope, and a value valid only there must not validate.
  const [modelCatalogScope, setModelCatalogScope] = useState("");
  // The catalog pre-start /model validation may read: the pane catalog only
  // when its stamp matches the current scope, null otherwise. Display
  // surfaces (pickers, effort ladder) keep the merged catalog for
  // continuity; validation fail-closes through the mismatch window instead
  // of accepting a value the new scope never offered.
  const scopedModelCatalog = modelCatalogScope === `${harness}\0${cwd}` ? modelCatalog : null;
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

  // Inline slash-command completion (slashCompletion.ts's own header
  // comment - ported from Beautiful UI's prompt-bar). slashToken is the
  // trailing-token match recomputed on every keystroke (below); null means
  // no menu, regardless of what the prompt's text actually contains -
  // Escape closes the menu by setting this to null directly, and typing
  // further reopens it because the very next keystroke recomputes the
  // match fresh. slashHighlighted is the ArrowUp/Down cursor over whatever
  // the CURRENT filtered list is; reset to 0 whenever the token itself
  // changes (new match, or the query narrowed/widened) rather than
  // persisted across it - an index into a list that just changed shape is
  // not a meaningful position to keep.
  const [slashToken, setSlashToken] = useState<SlashToken | null>(null);
  const [slashHighlighted, setSlashHighlighted] = useState(0);
  // The backend already resolved the selection for this cwd plus overrides
  // (evener/spawn/slashCatalog), so no plugin filtering applies here the
  // way Composer's visibleCatalogCommands filters its global catalog by
  // live session plugins.
  const slashCatalog = useSpawnSlashCatalog({
    client,
    cwd,
    harness,
    launchOverrides: combinedOverrides,
    pluginRevision,
    enabled: pluginSelectionSupported,
  });
  // Fail-soft: loading and error both render from the last (or empty)
  // response, never an empty loading flash or a guessed zero.
  const catalogResponse = slashCatalog.state.response ?? { commands: [], skills: [] };
  const slashMenuCatalog = mergeSlashCommands(
    spawnBuiltinCommands(),
    catalogResponse.commands,
    catalogResponse.skills ?? [],
  );
  // The menu is only ever open when a token matched AND the merged catalog
  // has at least one fuzzy label hit for it - a matched-but-empty token
  // (e.g. "/zzz" against a real catalog) shows no menu at all, same as no
  // token matching. The pluginSelectionSupported gate is load-bearing: the
  // hook reports ready-empty for non-evener harnesses, but
  // spawnBuiltinCommands() merges unconditionally, so without it typing
  // "/goal" on an external harness would still open a one-row builtin menu
  // for a session that loads no plugins.
  const slashItems = slashToken ? filterSlashMenuItems(slashMenuCatalog, slashToken.query) : [];
  const slashOpen = pluginSelectionSupported && slashToken !== null && slashItems.length > 0;
  // Singleton pane - no ref scoping needed, unlike Composer's per-ref id.
  const slashListboxId = "spawn-slash-listbox";
  const slashActiveIndex = slashOpen ? Math.min(slashHighlighted, slashItems.length - 1) : -1;
  const slashActiveId = slashActiveIndex >= 0 ? slashOptionId(slashListboxId, slashActiveIndex) : null;

  // Textarea (widgets/textarea) takes no aria-activedescendant/aria-controls
  // prop - it's a shared widget outside this stream's manifest - so this
  // component sets both directly on the native node it already refs for
  // cursor restoration below, the same imperative-DOM idiom the cursor-
  // restore layout effect already uses on the identical ref.
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    if (slashActiveId) {
      el.setAttribute("aria-controls", slashListboxId);
      el.setAttribute("aria-activedescendant", slashActiveId);
    } else {
      el.removeAttribute("aria-controls");
      el.removeAttribute("aria-activedescendant");
    }
  }, [slashActiveId]);

  // A freshly (re)matched token always starts highlighted at its first
  // option - an index carried over from the PREVIOUS token's list is not a
  // meaningful position once the list itself has changed shape.
  // biome-ignore lint/correctness/useExhaustiveDependencies: slashToken's start/query are deliberate trigger-only deps - the effect body only calls setSlashHighlighted(0), but must still re-run whenever the token identity actually changes (a new match, or the same match with a different query), same idiom as the cursor-restore layout effect below
  useEffect(() => {
    setSlashHighlighted(0);
  }, [slashToken?.start, slashToken?.query]);

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
  const initialModelRef = useRef("");
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

  // commitSlashCompletion is Tab/plain-Enter's (handlePromptKeyDown below)
  // and a mouse click's (SlashCompletionMenu's own onSelect) shared "the
  // user chose this command" path: splices the item's own invocation
  // (slashCompletion.ts's mergeSlashCommands - "/plugin:name" for a plugin
  // command via shell/palette/commands.ts's slashCommandInvocation, bare
  // "/id" for a built-in) in at the token's own start (never the caret,
  // when the caret was left mid-token by an earlier Escape-then-retype -
  // spliceSlashCommand's own doc comment), through the SAME
  // textEditor.write() seam every other programmatic edit in this file
  // uses, then closes the menu and returns focus to the field - mirrors
  // Composer.tsx's own commit shape. This only ever INSERTS the invocation
  // text - whether it goes on to execute as a built-in is Task 6's own
  // submit interception, below.
  function commitSlashCompletion(item: SlashMenuItem): void {
    if (!slashToken) return;
    const spliced = spliceSlashCommand(textRef.current, slashToken, item.invocation);
    textEditor.write(spliced.text, spliced.caret);
    setSlashToken(null);
    textareaRef.current?.focus();
  }

  const usesEvenerModels = harnessUsesEvenerModels(harness, harnesses);
  const providerRequired = usesEvenerModels && providerSetup.status === "missing";
  // kata xgk8: Start cannot succeed while Model is untouched AND the hub has
  // confirmed there is no default to fall back to - see the resolve effect
  // below for how noDefaultModel is set.
  const modelRequired = model === "" && noDefaultModel;

  // A credential change can make models discoverable (a stored Vertex
  // credential JSON enables the publisher-model listing) or take them away,
  // so the scoped cache below is keyed on two signals of it: this generation,
  // which evener/auth/updated advances the moment it arrives, and the
  // instance list's identity, which follows the credentials store's debounced
  // refetch and also covers an instance being added, edited or removed. On
  // either, the loader identities change, and the catalog effect and the
  // pickers reload (the mount-only stale-model sweep does not re-run).
  const [credentialsGeneration, setCredentialsGeneration] = useState(0);
  useEffect(
    () =>
      client.onNotification((n) => {
        if (n.method === "evener/auth/updated") setCredentialsGeneration((generation) => generation + 1);
      }),
    [client],
  );
  const modelListCache = useRef<{
    client: object;
    instances: object;
    generation: number;
    entries: Map<string, Promise<ModelListResponse>>;
  }>({ client, instances: providerSetup.instances, generation: credentialsGeneration, entries: new Map() });
  const loadModelList = useCallback((): Promise<ModelListResponse> => {
    if (
      modelListCache.current.client !== client ||
      modelListCache.current.instances !== providerSetup.instances ||
      modelListCache.current.generation !== credentialsGeneration
    ) {
      modelListCache.current = {
        client,
        instances: providerSetup.instances,
        generation: credentialsGeneration,
        entries: new Map(),
      };
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
  }, [client, harness, cwd, providerSetup.instances, credentialsGeneration]);
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
  const createDirectory = useCallback((path: string) => createDir(client, path), [client]);
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
  // (harnesses, advanced schema).
  // biome-ignore lint/correctness/useExhaustiveDependencies: mount-only initialization; the closures it calls are stable for the first paint
  useEffect(() => {
    const urlPrefill = readUrlPrefill(window.location.search);
    const defaults = resolveInitialDefaults({ serverPrefillDir: urlPrefill.dir });
    if (urlPrefill.prompt) updatePrompt(urlPrefill.prompt);
    if (defaults.harness) setHarness(defaults.harness);
    initialModelRef.current = defaults.model ?? "";
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
    return () => {
      active = false;
    };
  }, []);

  // Sweep persisted defaults using the current provider configuration. A
  // credential refresh cancels older catalogs before they can discard a model.
  // biome-ignore lint/correctness/useExhaustiveDependencies: sweep on provider changes, not on each working-directory keystroke; the request captures the current scope
  useEffect(() => {
    let active = true;
    const initialModel = initialModelRef.current;
    loadModelList().then(
      (r) => {
        if (!active) return;
        const { discarded } = sweepStaleModels(r.data);
        if (initialModel && modelRef.current === initialModel && discarded.includes(initialModel)) {
          setModel("");
          setStaleNotice(initialModel);
        }
      },
      () => {},
    );
    return () => {
      active = false;
    };
  }, [client, providerSetup.instances]);

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
    // The scope this request fetches for, stamped on commit below. A scope
    // change re-runs the effect and retires the previous run via active, so
    // only the latest scope's response commits its stamp.
    // biome-ignore lint/correctness/useExhaustiveDependencies: harness/cwd are trigger-only deps - the effect body only snapshots them into requestScope, but must re-run (and re-stamp) whenever the scope they define changes, same idiom as the cursor-restore layout effect in the composer
    const requestScope = `${harness}\0${cwd}`;
    const settle = setTimeout(() => {
      loadCatalog().then(
        (catalog) => {
          if (active) {
            setModelCatalog((previous) => mergeCatalogSnapshot(previous, catalog));
            setModelCatalogScope(requestScope);
          }
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
    // Inline slash-completion's own keyboard mechanics, ADAPTED for Spawn's
    // submit model (deliberately NOT a verbatim Composer port - Composer's
    // Enter sends, Spawn's plain Enter is a newline and only Mod/Ctrl+Enter
    // submits): ArrowUp/Down move the highlighted option (wrapping at both
    // ends) OVER the caret rather than moving the caret itself, Tab OR
    // unmodified non-composing Enter commits the highlighted option, Escape
    // dismisses without touching the prompt. The committing Enter never
    // steals a submit path - it IS the newline key in Spawn, and
    // Mod/Ctrl+Enter always falls through to the submit branch below even
    // with the menu open. Shift+Enter, Alt+Enter, and composing Enter keep
    // their existing behavior (newline/composition).
    if (slashOpen) {
      if (event.key === "ArrowDown") {
        event.preventDefault();
        setSlashHighlighted((i) => (i + 1) % slashItems.length);
        return;
      }
      if (event.key === "ArrowUp") {
        event.preventDefault();
        setSlashHighlighted((i) => (i - 1 + slashItems.length) % slashItems.length);
        return;
      }
      if (
        event.key === "Tab" ||
        (event.key === "Enter" &&
          !event.metaKey &&
          !event.ctrlKey &&
          !event.shiftKey &&
          !event.altKey &&
          !event.nativeEvent.isComposing)
      ) {
        event.preventDefault();
        event.stopPropagation();
        const chosen = slashItems[slashActiveIndex] ?? slashItems[0];
        if (chosen) commitSlashCompletion(chosen);
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        setSlashToken(null);
        return;
      }
    }
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

  // A valid /model invocation supplies the missing model itself, so it
  // bootstraps past the required-model guard: the value rides thread/start
  // (doSpawn's launch-scalar path below), and neither the button nor
  // handleSpawn may refuse a submit that CAN succeed. Unknown values still
  // fail in doSpawn's own pre-start validation with the blocked toast. The
  // catalog half is load-bearing: an unloaded catalog resolves zero items,
  // so a known value typed before it lands does NOT bootstrap (and doSpawn
  // fail-closes it the same way) - the user picks a model once the list
  // they validated against exists.
  const slashModelBootstrap =
    modelRequired && pluginSelectionSupported && attachments.items.length === 0
      ? (() => {
          const match = matchBuiltinInvocation(prompt, spawnBuiltinCommands());
          if (match?.command.id !== "model" || match.argsText.trim() === "") return null;
          const needle = match.argsText.trim().toLowerCase();
          return resolveSpawnModelItems(scopedModelCatalog).some(
            (item) => item.id.toLowerCase() === needle || item.label.toLowerCase() === needle,
          )
            ? match.argsText.trim()
            : null;
        })()
      : null;

  async function doSpawn(): Promise<void> {
    if (pluginSelectionBlocked) {
      busyRef.current = false;
      setBusy(false);
      setBusyStartedAt(null);
      return;
    }
    // Submit interception for the pre-session builtins (spawnSlashMenu's
    // allowlist: goal, model, reasoning-effort). Composer's own guard, ported:
    // a prompt carrying attachments is never read as a command, and on a
    // non-evener harness there is no menu and no interception - the prompt
    // always spawns verbatim (same pluginSelectionSupported gate as slashOpen).
    // A match still starts the session with the literal prompt text (the
    // daemon expands plugin commands/skills in the first input itself).
    //
    // /goal applies post-start via runSpawnBuiltinAfterStart (goal/set has no
    // processing gate - it queues behind the running turn). /model and
    // /reasoning-effort ride thread/start as launch scalars instead: the
    // daemon refuses thread/model/set while the first input's turn is active
    // (Conflict "session is processing"), and the effort source reads the new
    // thread from threadsStore, which is empty until the session pane
    // hydrates - so neither follow-up mutation can work. Their values are
    // pre-start validated here (there is no cheaper moment to refuse), then
    // folded into the start call under the chips, which keeps floor §1.11
    // precedence (explicit slash value wins over ambient form state).
    const builtinMatch =
      pluginSelectionSupported && attachments.items.length === 0
        ? matchBuiltinInvocation(prompt, spawnBuiltinCommands())
        : null;
    // Launch-scalar overrides carried by a matched /model or /reasoning-effort
    // invocation: resolved during pre-start validation below and folded into
    // the thread/start scalars under the chips.
    let slashScalars: { modelProvider?: string; model?: string; reasoningEffort?: string } | null = null;
    if (builtinMatch && (builtinMatch.command.id === "model" || builtinMatch.command.id === "reasoning-effort")) {
      // Pre-start validation for enum-arg builtins: there is no cheaper
      // moment to refuse than before the session exists. Unknown value ->
      // toast the blocked message and abort WITHOUT thread/start - AND reset
      // the busy guard handleSpawn set above, or Start strands disabled.
      // Empty /model means "(default)": fail-open, no override at all.
      const value = builtinMatch.argsText.trim();
      if (builtinMatch.command.id === "model" && value === "") {
        // Fall through to the ordinary start below with no model override.
      } else {
        const items =
          builtinMatch.command.id === "model"
            ? resolveSpawnModelItems(scopedModelCatalog)
            : resolveSpawnEffortItems(effortLevels, reasoningEffort);
        const needle = value.toLowerCase();
        // Bare /reasoning-effort fails CLOSED pre-start: the "" head of
        // resolveSpawnEffortItems (the "(default)" entry) must not count as
        // known here, so an empty effort value toasts
        // "/reasoning-effort needs a value" and aborts without thread/start
        // (palette parity - in-session bare /reasoning-effort errors with no
        // side effects). Bare /model stays fail-open via the branch above.
        const known =
          builtinMatch.command.id === "reasoning-effort" && value === ""
            ? false
            : items.some((item) => item.id.toLowerCase() === needle || item.label.toLowerCase() === needle);
        if (!known) {
          const message = value
            ? `/${builtinMatch.command.id}: unknown value "${value}"`
            : `/${builtinMatch.command.id} needs a value`;
          toasts.push("error", message);
          busyRef.current = false;
          setBusy(false);
          setBusyStartedAt(null);
          return;
        }
        const matched = items.find((item) => item.id.toLowerCase() === needle || item.label.toLowerCase() === needle);
        if (builtinMatch.command.id === "model" && matched) {
          const { provider, model: modelId } = splitModelId(matched.id);
          slashScalars = { modelProvider: provider, model: modelId };
        } else if (builtinMatch.command.id === "reasoning-effort" && matched) {
          slashScalars = { reasoningEffort: matched.id };
        }
      }
    }
    // The advanced schema's sandbox wins over the access-mode chip (floor §1.8);
    // its model/reasoningEffort win over the chips (floor §1.11) - resolveScalars
    // hoists them into the top-level fields the daemon prefers over overrides.
    // An explicit /model or /reasoning-effort invocation wins over ALL of
    // that: the user typed the value as the submit itself, so it is the most
    // specific intent in the room. The overlay applies AFTER resolveScalars
    // (not as chip input to it) because resolveScalars gives overrides.model /
    // overrides.reasoningEffort precedence - folding the slash value into the
    // chips would let an Advanced Options value silently win instead.
    const overrides = combinedOverrides;
    const resolved = resolveScalars({ model, reasoningEffort }, overrides);
    const scalars: { modelProvider?: string; model?: string; reasoningEffort?: string } = {
      modelProvider: slashScalars?.modelProvider ?? resolved.modelProvider,
      model: slashScalars?.model ?? resolved.model,
      reasoningEffort: slashScalars?.reasoningEffort ?? resolved.reasoningEffort,
    };
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
    if (builtinMatch && builtinMatch.command.id === "goal") {
      // Post-start application failure toasts but does NOT block navigation:
      // the session started fine, only the follow-up setting failed.
      await runSpawnBuiltinAfterStart(builtinMatch.command.id, builtinMatch.argsText, ref, toasts);
    }
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
    // The menu is token-driven, not text-driven: clearing the prompt does not
    // recompute the token, so without this the stale menu stays open over the
    // empty prompt on the still-mounted pane (and Enter would commit the
    // stale completion into the next session's first line).
    setSlashToken(null);
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
    if ((modelRequired && slashModelBootstrap === null) || providerRequired) return;
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
    <PaneScaffold title="New session" mobileTitle="New session">
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

        <div className={CLASS.cfgDir}>
          <button
            type="button"
            id="spawn-cwd"
            className={CLASS.directoryButton}
            aria-label={`Working directory: ${cwd || "Choose a folder"}`}
            aria-haspopup="dialog"
            aria-expanded={directoryOpen}
            onClick={() => setDirectoryOpen(true)}
          >
            <DirectoryIcon />
            <span className={CLASS.directoryText}>
              <strong>{cwd ? basename(cwd) || "/" : "Working directory"}</strong>
              <span className={CLASS.directoryPath}>{cwd || "Choose a folder"}</span>
            </span>
            <span>Change…</span>
          </button>
          {branch !== "" && (
            <span className={CLASS.branch} data-testid="spawn-branch">
              {branch}
            </span>
          )}
        </div>
        {directoryOpen && (
          <DirectoryPicker
            key={cwd}
            value={cwd}
            fallbackDir={getGlobalLastWorkingDir()}
            complete={complete}
            listRecents={listRecents}
            validatePath={validatePath}
            createDirectory={createDirectory}
            onClose={() => setDirectoryOpen(false)}
            onPick={(path) => {
              setCwd(path);
              setGlobalLastWorkingDir(path);
              setDirectoryOpen(false);
            }}
          />
        )}

        <div className={CLASS.promptIntro} data-testid="spawn-prompt-intro">
          <h2 className={CLASS.promptHeading}>What should the agent do?</h2>
          <p className={CLASS.promptSubtitle}>Leave blank to start a dormant session.</p>
        </div>

        {/* The prompt shares its card and attachment controls with the session composer. */}
        {/* The positioned anchor for the inline slash menu above the card -
            PromptCard's props are field/leading/actions only, so the menu
            cannot go inside it (nor is Composer's own menu inside its card
            either - it sits in Composer.tsx's positioned .formAnchor
            wrapper). Rendered as this wrapper's first child, mirroring
            Composer's anchor-above-card placement. */}
        <div className={CLASS.promptAnchor}>
          {slashOpen && (
            <SlashCompletionMenu
              id={slashListboxId}
              items={slashItems}
              highlightedIndex={slashActiveIndex}
              onSelect={commitSlashCompletion}
            />
          )}
          <Dropzone onFiles={(files) => attachments.ingestFiles(files, (message) => toasts.push("error", message))}>
            <PromptCard
              data-testid="spawn-prompt-card"
              controlsTestId="spawn-controls"
              field={
                <Textarea
                  ref={textareaRef}
                  value={prompt}
                  onChange={(e) => {
                    updatePrompt(e.target.value);
                    // Every keystroke re-evaluates the trailing-token match
                    // fresh - a token Escape just closed reopens on the very
                    // next text change rather than staying closed
                    // indefinitely.
                    const caret = e.target.selectionStart ?? e.target.value.length;
                    setSlashToken(parseSlashToken(e.target.value, caret));
                  }}
                  onKeyDown={handlePromptKeyDown}
                  onPaste={handlePaste}
                  // Blur is the slash menu's own "clicked/tabbed away
                  // entirely" close: without it a Tab-away leaves a stale
                  // menu. SlashCompletionMenu's own options preventDefault()
                  // on their mousedown specifically so a MOUSE click on an
                  // option never reaches this handler in the first place -
                  // see that component's own comment - so this only ever
                  // fires for a genuine "focus left the field".
                  onBlur={() => setSlashToken(null)}
                  // Short, because the intro above the card already asks the
                  // question and states the dormant-start rule; a placeholder
                  // that repeats them spends the field's one line on nothing.
                  placeholder="Describe the task…"
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
                   attach, then the model trigger, then effort. All stay INSIDE
                   the card's control row at every width - choosing a model and
                   an effort is the same act wherever it happens, so it is the
                   same component (ModelSwitchTrigger) and the same StatusRow
                   quiet-effort recipe rather than a bespoke boxed variant below
                   the card. */
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
                  {/* The label follows the same rules the old desktop field's
                      did - the required-choice word when the hub has confirmed
                      no default (kata xgk8), otherwise the chosen model, the
                      resolved default model's own "<model> (default)", or
                      plain "(default)" until the resolve lands. */}
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
                  {/* StatusRow's quiet-effort recipe (statusrow.module.css's
                      .effortTrigger): the current value IS the visible control -
                      a real native <select> laid over its own readout at zero
                      opacity - so the row stays one quiet line instead of
                      growing a bordered box. The readout renders the SELECTED
                      option's own label - including the resolved default's
                      ("high (default)"), never the bare value - so what the
                      user sees is what the select holds. Same ladder contract
                      the removed FormRow select kept: the selected model's own
                      levels, the fallback ladder when the catalog can't say,
                      and a disabled control when the model cannot reason at all
                      (effortDisabled) rather than no control - pre-launch the
                      setting is still discoverable beside the model it belongs
                      to. */}
                  <span
                    className={CLASS.effortTrigger}
                    data-testid="spawn-effort"
                    data-disabled={effortDisabled ? "true" : undefined}
                  >
                    <span className={CLASS.effortSeparator} aria-hidden="true">
                      ·
                    </span>
                    <span className={CLASS.effortValue} data-testid="spawn-effort-value" aria-hidden="true">
                      {effortOptions.find((option) => option.value === reasoningEffort)?.label ??
                        effortLabel(reasoningEffort, effortLevels)}
                    </span>
                    <span className={CLASS.effortChevron} aria-hidden="true">
                      <Chevron direction="down" />
                    </span>
                    <label className={CLASS.srOnly} htmlFor="spawn-reasoning-effort">
                      Prompt reasoning effort
                    </label>
                    <select
                      id="spawn-reasoning-effort"
                      className={CLASS.effortSelect}
                      value={reasoningEffort}
                      onChange={(e) => setReasoningEffort(e.target.value)}
                      disabled={effortDisabled}
                    >
                      {effortOptions.map((option) => (
                        <option key={option.value} value={option.value}>
                          {option.label}
                        </option>
                      ))}
                    </select>
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
                    disabled={
                      busy ||
                      (modelRequired && slashModelBootstrap === null) ||
                      providerRequired ||
                      pluginSelectionBlocked
                    }
                  >
                    {busy ? (
                      <StartingLoader startedAt={busyStartedAt ?? Date.now()} />
                    ) : (
                      <span className={CLASS.submitLabel}>Start</span>
                    )}
                  </Button>
                </Tooltip>
              }
            />
          </Dropzone>
        </div>
        {providerRequired && (
          <div className={CLASS.notice} role="status">
            <span>Connect a provider to use a model. Sign in or add an API key here.</span>
            <Button onClick={() => setConnectingProvider(true)}>Connect provider</Button>
            <Button variant="quiet" onClick={() => void providerSetup.retry()}>
              Retry provider check
            </Button>
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
        {connectingProvider && (
          <ConnectProviderDialogBoundary
            onRetry={retryProviderDialog}
            reloadAvailable={dialogRetryCount > 0}
            onClose={closeProviderSetup}
          >
            <Suspense
              fallback={
                <Dialog open onClose={closeProviderSetup} title="Connect provider">
                  <Loader label="Loading…" />
                </Dialog>
              }
            >
              <ProviderDialog onClose={closeProviderSetup} onConnected={providerConnected} />
            </Suspense>
          </ConnectProviderDialogBoundary>
        )}
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

        {/* The modelRequired note lives with the card's own model trigger
            below: it explains why Start is disabled, so it sits beside the
            control it names rather than in a form row that no longer exists. */}
        {modelRequired && (
          <p className={CLASS.modelNote} role="alert">
            This hub has no default model configured — choose one to start.
          </p>
        )}

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
            validatePath={validatePath}
            createDirectory={createDirectory}
            listRecents={listRecents}
            fallbackDir={getGlobalLastWorkingDir()}
            onCwdPanelClose={setGlobalLastWorkingDir}
            branch={branch}
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
          createDirectory={createDirectory}
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
