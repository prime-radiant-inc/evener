// Session creation keeps the project directory above the prompt and the
// less frequently changed launch settings below it. The directory picker
// commits once, so browsing does not churn directory-dependent configuration.

import type { HarnessDescriptor, LaunchConfigLayer, LaunchOption, ModelListResponse } from "@evener/appwire-client";
import {
  type AdvancedValues,
  basename,
  collectAdvancedOverrides,
  effortLabel,
  filterSlashMenuItems,
  findBuiltinArgument,
  friendlyLaunchErrorMessage,
  harnessSupportsPluginSelection,
  harnessUsesEvenerModels,
  matchBuiltinInvocation,
  mergeSlashCommands,
  type PathValidation,
  type PluginSelectionState,
  parseSlashToken,
  perLaunchEvenerOptions,
  pluginSelectionIssues,
  reconcilePluginSelection,
  resolveScalars,
  type SlashMenuItem,
  type SlashToken,
  schemaPathKind,
  selectedPluginNames,
  slashCommandInvocation,
  spliceSlashCommand,
  withPluginSelection,
} from "@evener/appwire-client";
import { type JSX, memo, Suspense, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useStore } from "zustand";
import { useClient } from "../../shell/clientContext";
import { resolveHeadBranch } from "../../shell/gitLocation";
import { splitModelId } from "../../shell/palette/commands";
import type { PaneProps } from "../../shell/paneRegistry";
import { navigate, paneToURL } from "../../shell/routing";
import { useMountAutofocus } from "../../shell/useMountAutofocus";
import { useExtensionsStore } from "../../stores/extensions";
import { hostRequest, isLocalHost } from "../../stores/hostRouting";
import { selectDisplaySources, selectSources } from "../../stores/navigation/selectors";
import { useNavigationStore } from "../../stores/navigation/store";
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
import selectStyles from "../../widgets/select/select.module.css";
import { ModelSwitchTrigger } from "../session/chrome/ModelSwitchTrigger";
import { AttachmentTile } from "../session/composer/AttachmentTile";
import { AttachIcon } from "../session/composer/attachments/AttachIcon";
import { imageFilesFromClipboard } from "../session/composer/attachments/clipboard";
import { type TextEditor, useAttachments } from "../session/composer/attachments/useAttachments";
import { SlashCompletionMenu, optionId as slashOptionId } from "../session/composer/SlashCompletionMenu";
import {
  ConnectProviderDialogBoundary,
  useConnectProviderDialogChunk,
} from "../settings/sections/credentials/ConnectProviderDialogBoundary";
import { AdvancedOptions } from "./AdvancedOptions";
import { ACCESS_MODE_OPTIONS, accessModeDefaultLabel } from "./accessMode";
import { MobileSettingRows } from "./MobileSettingRows";
import { PluginSelectionPanel } from "./PluginSelectionPanel";
import pluginSelectionStyles from "./pluginSelection.module.css";
import { createDir, preflightDir } from "./preflight";
import styles from "./spawn.module.css";
import {
  getGlobalLastWorkingDir,
  modelValidityAgainstList,
  saveDefaults,
  setGlobalLastWorkingDir,
  sweepStaleModels,
} from "./spawnDefaults";
import { applySpawnURL, type SpawnDraft, selectSpawnDirectory, spawnDraftsStore, useDraftField } from "./spawnDrafts";
import {
  PRE_SESSION_BUILTIN_IDS,
  resolveSpawnEffortItems,
  resolveSpawnModelItems,
  runSpawnBuiltinAfterStart,
  spawnBuiltinCommands,
} from "./spawnSlashMenu";
import { startThread } from "./startThread";
import { usePluginPreview } from "./usePluginPreview";
import { useProviderSetup } from "./useProviderSetup";
import { useSpawnSlashCatalog } from "./useSpawnSlashCatalog";

// Below-the-fold dialog: mounted only after the user clicks "Connect
// provider" (connectingProvider state), never on first paint. The chunk -
// the dialog plus its instance-credential editors (instanceDialogs,
// oauthDialogs, oauthFlow) - stays out of the spawn pane's initial bundle
// and loads on first open.
//
// A rejected chunk lands on ConnectProviderDialogBoundary from
// settings/sections/credentials/ConnectProviderDialogBoundary, scoped
// to the dialog so the lazy() rethrow does not bubble into this pane's own
// workspace-failure boundary. The Suspense fallback is a real dialog reading
// "Loading…": a null fallback would leave the click that opened the dialog
// with no visible response while the chunk fetches, and the boundary renders
// the same Dialog shell on failure so pending and failure share one frame.

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

// CONNECT_ATTACH_TIMEOUT_MS bounds the Connect action's `evener/host/attach`
// RPC. The AppWire client's default request timeout is 30s
// (DEFAULT_REQUEST_TIMEOUT_MS in appwire-client/typescript/client.ts), but the
// Ensure seam behind the handler runs sequential bounded phases far beyond
// that: up to three deployLimit phases (deploy + restart + launch-contract
// refresh, 10 minutes each — deployLimit in
// cmd/evener-hub/internal/sshconn/manager.go), up to three attemptLimit phases
// (preflight, running-hub probe, hub-presence probe; 70s by default: 4x10s
// connect + 30s init), and the attach handshake itself (initTimeout, 30s) —
// about 34 minutes worst case. 35 minutes clears that server bound with
// headroom, the same shape as hubUpdate's APPLY_TIMEOUT_MS over its server
// bound: a slow but valid deploy resolves instead of failing the toast at 30s
// while the server-side attach still succeeds. There is no attach-status RPC
// to poll instead (adding one would touch the router catalog), so the explicit
// long timeout is the whole fix.
export const CONNECT_ATTACH_TIMEOUT_MS = 35 * 60_000;

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
  // The host picker's native <select> reuses the Select widget's own stylesheet
  // class: the widget renders a native <select> too, but its SelectOption has no
  // per-option disabled flag, and an offline host must be RENDERED yet not
  // selectable (Component 06b). The class only borrows the visual treatment.
  hostSelect: requireClass(selectStyles.select, "select.module.css", "select"),
  // The Connect affordance beside the picker: one button per offline remote
  // host, an enabled control so a never-attached host is reachable rather
  // than dead UI (component 06 §"Connecting a configured host").
  hostConnectRow: requireClass(styles.hostConnectRow, "spawn.module.css", "hostConnectRow"),
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

export default function Spawn({ focused }: PaneProps<SpawnPaneParams>) {
  const draft = useStore(spawnDraftsStore, (state) => state.current);
  const prefillRevision = useStore(spawnDraftsStore, (state) => state.prefillRevision);
  const [onNewRoute, setOnNewRoute] = useState(() => window.location.pathname === "/new");
  useLayoutEffect(() => {
    applySpawnURL();
    function onPopState(): void {
      setOnNewRoute(window.location.pathname === "/new");
      if (window.location.pathname === "/new") applySpawnURL(true);
    }
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);
  return draft ? <SpawnForm draft={draft} prefillRevision={prefillRevision} focused={focused && onNewRoute} /> : null;
}

// Keep the singleton's controls mounted while changing their backing draft.
// Store-bound setters and image continuations retain their originating scope.
function SpawnForm({
  draft,
  prefillRevision,
  focused,
}: {
  draft: SpawnDraft;
  prefillRevision: number;
  focused: boolean;
}) {
  const client = useClient();
  const toasts = useToasts();
  const [source, setSource] = useDraftField(draft, "source");
  // The manifest carries every configured launch source (Component 06a). It is
  // empty until it loads, and in the common single-host case holds only
  // "local" - either way no picker renders, so the existing form is unchanged.
  //
  // Two views of it, deliberately (round nine). `sources` is the SETTLED list
  // and owns every decision: the launchable host below, the submitted source,
  // and the write-back; `displaySources` (below) is last-known data for DISPLAY
  // only, so a revalidation cannot make the picker vanish.
  const sources = useNavigationStore(selectSources);
  // A draft may name a host that is no longer launchable: removed from the
  // manifest while the draft lived, or still listed but offline (the hub keeps
  // offline sources in the manifest - only the online flag flips). Both fall
  // back to local rather than submitting a source the hub rejects with
  // "spawn source is not available". The fallback is written back into the
  // draft below so the picker's value and the submitted source never diverge: a
  // stale draft value would otherwise survive the fallback and, when the host
  // came back online, silently flip both the select and the launch target back
  // to it. Affirmatively settling on local cannot rewrite the stale value -
  // selecting "Local" fires no change event while the select already reads
  // local - so the draft, not the remembered host, must carry the choice.
  //
  // An EMPTY manifest is not that evidence (component 07b review, round five).
  // `sources` is empty both while the manifest is in flight (and if its fetch
  // failed) and when the manifest genuinely lists nothing remote, so its
  // emptiness cannot decide the target: reading it as a fallback silently
  // converted a remote draft into a controller launch - the draft kept naming
  // its host while every discovery call and thread/start ran on this one. Until
  // the manifest has named its sources, the draft's own value is the last host
  // that was CONFIRMED (the picker writes it), so it stays the launch AND
  // discovery target until the manifest arrives to confirm or contradict it.
  // This is the same `sources.length > 0` gate component 06b's `submittedSource`
  // reads, so both branches agree on the wire value in every case.
  const chosenSource = sources.find((candidate) => candidate.id === source);
  const hostChoice = chosenSource?.online ? chosenSource.id : "local";
  // `sources` is empty both while the manifest loads (and if it never arrives)
  // and when it genuinely lists no remote host, so its emptiness is not
  // evidence the draft's host is gone. Until the manifest lists the sources,
  // the submission carries the draft's own value; only a settled manifest - the
  // same `sources.length > 0` gate the write-back below uses - can confirm the
  // fallback. Reading the loading window as a fallback would start the session
  // locally with no indication while the draft still names a remote host
  // (Component 06b review).
  const submittedSource = sources.length > 0 ? hostChoice : source;
  useEffect(() => {
    // Guarded on a loaded manifest: while the sources are still in flight
    // hostChoice is a provisional "local", and rewriting the draft then would
    // discard a persisted remote host that is merely still loading.
    if (sources.length > 0 && source !== hostChoice) setSource(hostChoice);
  }, [sources.length, source, hostChoice, setSource]);

  // The explicit attach trigger (component 06's Connect action). A configured
  // host with no live channel is listed offline and its spawn option is
  // disabled, and every implicit path is attached-only by design (the snapshot
  // walk and the non-explicit thread/list fan-out skip an unattached source),
  // so nothing else the picker does can attach it. This is the one shipped
  // client that issues `evener/host/attach`, the browser-reachable method that
  // dials the host through the manager's Ensure seam. It is fire-and-forget:
  // the hub's attach event flips the manifest's online flag (and invalidates
  // navigation), so success needs no local bookkeeping beyond clearing the
  // pending marker; a failure is surfaced rather than leaving a dead row.
  const [connectingHosts, setConnectingHosts] = useState<ReadonlySet<string>>(() => new Set());
  // The in-flight guard is a ref, not the state above: setConnectingHosts is
  // asynchronous, so two activations in the same tick both read the pre-update
  // `connectingHosts` set and double-dial. The ref is mutated synchronously, so
  // the second activation sees the first one already in flight. The state stays
  // for rendering (the Connect button's disabled state and label).
  const connectingHostsRef = useRef<Set<string>>(new Set());
  const connectHost = useCallback(
    (host: string) => {
      if (isLocalHost(host) || connectingHostsRef.current.has(host)) return;
      connectingHostsRef.current.add(host);
      setConnectingHosts((current) => new Set(current).add(host));
      void client
        .request("evener/host/attach", { host }, { timeoutMs: CONNECT_ATTACH_TIMEOUT_MS })
        .catch((error: unknown) => {
          toasts.push("error", `Connect ${host} failed: ${friendlyLaunchErrorMessage(error)}`);
        })
        .finally(() => {
          connectingHostsRef.current.delete(host);
          setConnectingHosts((current) => {
            if (!current.has(host)) return current;
            const next = new Set(current);
            next.delete(host);
            return next;
          });
        });
    },
    [client, toasts],
  );

  // Every host-dependent discovery/validation call below is issued against
  // submittedSource (component 07b): remote hosts read models, harnesses,
  // launch config, paths, projects, the slash catalog, git HEAD, plugin
  // diagnostics, and provider instances through evener/host/request; "local"
  // keeps the plain call. submittedSource is the value the form submits as
  // ThreadStartParams.Source, so discovery describes the machine the launch
  // will use - including while the manifest is still settling, when the
  // draft already names the host.
  const providerSetup = useProviderSetup(submittedSource);
  const [connectingProvider, setConnectingProvider] = useState(false);
  const [modelHandoff, setModelHandoff] = useState<{ name?: string }>();
  // Save/reload can make new models appear before Continue. Once a draft scope
  // enters onboarding, do not substitute the legacy untouched-model fallback
  // for its explicit choice, including after cancellation. Scopes are tracked
  // as a set, not one slot: onboarding draft B must not forget that draft A
  // also made an explicit choice (see openProviderSetup below).
  const providerChoiceScopes = useRef<Set<string>>(new Set());
  const closeProviderSetup = useCallback(() => setConnectingProvider(false), []);
  // ConnectProviderDialog is a CONTROLLER-scoped editor: it reads and writes the
  // package store's top-level fields and issues its auth/test flows on the plain
  // connection, so it can only configure the controller's own hub. A remote
  // host's registry lives in that host's own partition (useProviderSetup) and
  // can only be configured on the host itself, so the connect action is offered
  // for the controller only - opening it while a remote target is selected
  // would "connect" the wrong machine and leave the remote spawn blocked with
  // Start still disabled.
  const providerSetupIsLocal = isLocalHost(submittedSource);
  // A target switch while the dialog is open must not leave it mounted over the
  // newly selected host (or re-open it when the controller is chosen again).
  const providerDialogHostRef = useRef(submittedSource);
  useEffect(() => {
    if (providerDialogHostRef.current === submittedSource) return;
    providerDialogHostRef.current = submittedSource;
    setConnectingProvider(false);
  }, [submittedSource]);
  const providerConnected = useCallback(
    (name?: string) => {
      setConnectingProvider(false);
      modelListCache.current.entries.clear();
      setModelHandoff({ name });
      void providerSetup.retry();
    },
    [providerSetup.retry],
  );
  // The chunk hook owns the lazy payload and its cache-busted retry state;
  // see ConnectProviderDialogBoundary.tsx.
  const {
    Dialog: ProviderDialog,
    retry: retryProviderDialog,
    reloadAvailable: dialogReloadAvailable,
    version: dialogChunkVersion,
  } = useConnectProviderDialogChunk();

  const [prompt, setPrompt] = useDraftField(draft, "prompt");
  const [harness, setHarness] = useDraftField(draft, "harness");
  const [model, setModel] = useDraftField(draft, "model"); // qualified "provider/model", or "" for the harness default
  const [reasoningEffort, setReasoningEffort] = useDraftField(draft, "reasoningEffort");
  // The display-only view of the same manifest, used by the picker alone
  // (round nine): last-known data, so a revalidation (loading/stale) cannot
  // make the picker vanish or the visible host flip to Local while the fresh
  // manifest is in flight.
  const displaySources = useNavigationStore(selectDisplaySources);
  // The select's own value, and its option list: last-known data, so the picker
  // stays visible while the manifest revalidates and keeps showing the host the
  // draft names (only `sources` above may decide the launch target). While the
  // manifest is unsettled a submit carries `source` itself, so the visible
  // choice follows the draft rather than reading the withheld list as "the host
  // is gone" (round nine). Once settled this is exactly hostChoice, preserving
  // the round-eight preselection and offline fallback unchanged.
  const displaySource = displaySources.find((candidate) => candidate.id === source);
  const displayHostChoice = sources.length > 0 ? hostChoice : (displaySource?.id ?? hostChoice);
  const displayRemoteHosts = displaySources.filter((candidate) => candidate.id !== "local");
  const cwd = draft.cwd;
  const setCwd = selectSpawnDirectory;
  // Entering onboarding records the draft's own scope; the fallback below is
  // suppressed for exactly that harness+directory. A later scope in the same
  // pane mount still gets its own default, and a connection-driven re-render -
  // which never changes the draft scope - cannot clear the choice.
  const openProviderSetup = useCallback(() => {
    providerChoiceScopes.current.add(`${harness}\0${cwd}`);
    setConnectingProvider(true);
  }, [harness, cwd]);
  const [directoryOpen, setDirectoryOpen] = useState(false);
  // Scoped by cwd AND host so neither a draft switch nor a host switch can show
  // the previous project's/host's branch while the new evener/git/head request
  // is in flight - or indefinitely after it fails (resolveHeadBranch fails soft
  // to ""). The stamped host is submittedSource, the machine the effect below
  // actually asks, so the readout has to be keyed on that same value: a same-cwd
  // host switch must invalidate the previous host's answer immediately, and
  // while the manifest is unsettled - when hostChoice is a provisional "local"
  // and the draft's own host is what every request is issued against - the
  // readout must keep describing the tree the answer came from rather than
  // blanking until the manifest settles.
  const [branchHead, setBranchHead] = useState<{ cwd: string; host: string; head: string } | null>(null);
  const branch =
    branchHead !== null && branchHead.host === submittedSource && branchHead.cwd === cwd ? branchHead.head : ""; // display-only (floor §1.7)
  const [accessMode, setAccessMode] = useDraftField(draft, "accessMode");
  const [harnesses, setHarnesses] = useState<HarnessDescriptor[]>([]);
  const [schemaOptions, setSchemaOptions] = useState<LaunchOption[]>([]);
  const [advancedOverrides, setAdvancedOverrides] = useDraftField(draft, "advancedOverrides");
  // The model that will actually launch: an Advanced-options override first,
  // then the top-level chip, then the hub's resolved default (spawnSchema's
  // resolveScalars). Derived once here so the requirement check, the effort
  // ladder, and submission all judge the same value.
  const advancedModel = typeof advancedOverrides.model === "string" ? advancedOverrides.model.trim() : "";
  const [advancedValues, setAdvancedValues] = useDraftField(draft, "advancedValues");
  const [advancedErrors, setAdvancedErrors] = useDraftField(draft, "advancedErrors");
  const readAdvancedValues = useCallback(() => draft.fields.getState().advancedValues, [draft]);
  const [pluginSelection, setPluginSelection] = useDraftField(draft, "pluginSelection");
  const [knownSelectionIssues, setKnownSelectionIssues] = useDraftField(draft, "knownSelectionIssues");
  const pluginSelectionRef = useRef(pluginSelection);
  pluginSelectionRef.current = pluginSelection;
  const [staleNotice, setStaleNotice] = useDraftField(draft, "staleModelNotice");
  const [globalModelRequest, setGlobalModelRequest] = useState<{
    active: boolean;
    promise: Promise<ModelListResponse>;
  } | null>(null);
  // The host whose global model list has ANSWERED since the last host change
  // (null while the current host's is outstanding). It is the model half of the
  // submit gate: a non-empty model is host-derived exactly as the harness is -
  // it was chosen from, or persisted against, the catalog of whichever host was
  // selected at the time - so until the SELECTED host's list lands, a submit
  // could carry a model this host never offered (component 07b review, round
  // five: `hostCatalogPending` covered only the harness/schema requests). A
  // LOCAL mount starts settled: nothing about it is host-derived, so the form
  // stays startable exactly as it was before host routing existed. Settlement,
  // not success, is the bar - see the effect below.
  const modelHostRef = useRef(submittedSource);
  const [modelHostSettled, setModelHostSettled] = useState<string | null>(() =>
    isLocalHost(submittedSource) ? submittedSource : null,
  );
  const [createDialogPath, setCreateDialogPath] = useDraftField(draft, "createDialogPath");
  // The host that preflighted createDialogPath. The pair is one action: the
  // dialog offers to create a path ON the host that just validated it, and the
  // draft's launch config was reconciled against that host's catalogs.
  const [createDialogHost, setCreateDialogHost] = useDraftField(draft, "createDialogHost");
  // Opened, confirmed, and dismissed as a unit, so neither half can outlive the
  // other.
  const closeCreateDialog = useCallback(() => {
    setCreateDialogPath(null);
    setCreateDialogHost(null);
  }, [setCreateDialogPath, setCreateDialogHost]);
  // The selection can change while the dialog is open: the manifest's `online`
  // flag is live, and an offline source makes hostChoice fall back to "local"
  // (see its derivation). Left open, "Create & start" would then create THIS
  // path and launch THIS draft on whichever host is selected now - a launch the
  // newly selected host never offered, which is the remote-draft-silently-
  // converted-to-a-local-launch class round five closed for the normal Start
  // path. A host switch dismisses the pending create rather than re-homing it:
  // the user confirmed a path on one machine, so the same click must not act on
  // another. handleCreateConfirm re-checks the binding as well, because a
  // confirm can race this effect (component 07b review, round seven).
  const createDialogHostRef = useRef(submittedSource);
  useEffect(() => {
    if (createDialogHostRef.current === submittedSource) return;
    createDialogHostRef.current = submittedSource;
    closeCreateDialog();
  }, [submittedSource, closeCreateDialog]);
  const [busy, setBusy] = useDraftField(draft, "busy");
  // Loader's elapsed readout is pure-render (widgets/loader's own doc
  // comment - no internal timer, so it can't drift or fake liveness): the
  // caller owns the clock. busyStartedAt is stamped once, at the submit that
  // flips busy true; StartingLoader below owns the 1s tick and mounts only
  // while busy, so no interval runs with nothing on screen reading it.
  const [busyStartedAt, setBusyStartedAt] = useDraftField(draft, "busyStartedAt");
  // Own the effective layer by draft AND host so neither its gate nor inherited
  // labels can describe the previous project or host while the current resolve
  // is pending. A remote host's default model is that host's own
  // (evener/launch/resolve is host-scoped), so a same-cwd host switch must not
  // let the previous host's default satisfy - or fail - the Start model check.
  const [defaultPreview, setDefaultPreview] = useState<{
    draft: SpawnDraft;
    host: string;
    effective: LaunchConfigLayer;
  } | null>(null);
  // Every unset launch-config control names its entry in this effective layer:
  // "high (default)", "On (default)", etc. Unknown defaults remain plain.
  const resolvedDefaults =
    defaultPreview?.draft === draft && defaultPreview.host === submittedSource ? defaultPreview.effective : null;
  // kata xgk8: true only once evener/launch/resolve has CONFIRMED the hub has
  // no default model for this cwd (Effective.Model resolves empty with no
  // overrides) - never set on a rejection or before cwd is chosen, so an
  // unconfirmable state never blocks Start (same fail-open shape as
  // preflightDir).
  const noDefaultModel = resolvedDefaults !== null && (resolvedDefaults.model ?? "").trim() === "";
  // The launchable-model catalog, loaded at pane level so the Effort select can
  // read the selected model's own reasoningEffortLevels without waiting for a
  // picker to open. null = not loaded or the load failed - the select stays on
  // the fallback ladder.
  const [modelCatalog, setModelCatalog] = useState<ModelCatalog | null>(null);
  // Validity stamp for the pane catalog: the scope ("harness + cwd") the
  // committed snapshot was fetched for, plus the loader identity that
  // fetched it. The catalog merges snapshots across scopes for picker
  // display continuity, but /model pre-start validation must only read a
  // snapshot fetched for the CURRENT scope by the CURRENT loader: during
  // the settle window after a cwd/harness change — or while a credential
  // change-triggered refresh is pending — modelCatalog still holds the
  // previous snapshot, and a value valid only there must not validate.
  // null means never successfully loaded (or the last refresh failed): a
  // /model value then cannot be validated against this scope at all, so the
  // pre-start check treats it as "don't know" and forwards a shape-valid value
  // for thread/start to judge (effort falls back to its ladder). Only a
  // scope/loader MISMATCH is proven staleness and still fail-closes: there
  // modelCatalog holds the previous snapshot, and a value valid only in that
  // scope must not validate.
  const [modelCatalogStamp, setModelCatalogStamp] = useState<{
    scope: string;
    loader: () => Promise<ModelCatalog>;
  } | null>(null);
  // The hub's resolved default model for this cwd ("" until resolve confirms
  // one): what the Effort ladder keys off while Model reads "(default)".
  const resolvedDefaultModel = (resolvedDefaults?.model ?? "").trim();
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
  const [slashToken, setSlashToken] = useState<SlashToken | null>(() => parseSlashToken(prompt, prompt.length));
  const [slashHighlighted, setSlashHighlighted] = useState(0);
  // URL writes bypass the keystroke path, including a prefill for the current
  // project. A restored draft should offer the same completion as typed text.
  // biome-ignore lint/correctness/useExhaustiveDependencies: prefillRevision triggers URL-only writes independently of keystrokes
  useEffect(() => {
    const text = draft.fields.getState().prompt;
    setSlashToken(parseSlashToken(text, text.length));
    cursorRef.current = null;
  }, [draft, prefillRevision]);
  // Completion can clear this draft through an older, unmounted form. Retire
  // the token from the current prompt, not from that form's stale continuation.
  useEffect(() => {
    if (prompt === "") setSlashToken(null);
  }, [prompt]);
  // The backend already resolved the selection for this cwd plus overrides
  // (evener/spawn/slashCatalog), so no plugin filtering applies here the
  // way Composer's visibleCatalogCommands filters its global catalog by
  // live session plugins.
  const slashCatalog = useSpawnSlashCatalog({
    client,
    cwd,
    host: submittedSource,
    harness,
    launchOverrides: combinedOverrides,
    pluginRevision,
    enabled: pluginSelectionSupported,
  });
  // Fail-soft: loading and error both render from the last (or empty)
  // response, never an empty loading flash or a guessed zero.
  const catalogResponse = slashCatalog.state.response ?? { commands: [], skills: [] };
  // Interaction honesty: while a same-cwd refresh is in flight — or the last
  // refresh errored — the hook retains the previous response, but the menu
  // must not offer rows the new config may have removed: a picked stale
  // entry would submit as literal text once the session no longer loads it.
  // Only ready rows complete; the pre-session builtins stay offered throughout.
  const slashCatalogResponse = slashCatalog.state.status === "ready" ? catalogResponse : { commands: [], skills: [] };
  // Pre-session builtins reserve their invocations: a catalog command or
  // skill addressing the same "/name" would display as the builtin but always
  // lose to it at submit (matchBuiltinInvocation runs first), so offering it
  // is a lie. Plugin-qualified "/plugin:name" invocations never collide and
  // pass through untouched.
  const builtinInvocations = new Set(PRE_SESSION_BUILTIN_IDS.map((id) => `/${id}`));
  const slashMenuCatalog = mergeSlashCommands(
    spawnBuiltinCommands(),
    slashCatalogResponse.commands.filter((c) => !builtinInvocations.has(slashCommandInvocation(c))),
    (slashCatalogResponse.skills ?? []).filter((s) => !builtinInvocations.has(`/${s.name}`)),
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

  const textareaRef = useRef<HTMLTextAreaElement>(null);
  // Textarea (widgets/textarea) takes no aria-activedescendant/aria-controls
  // prop - it's a shared widget outside this stream's manifest - so this
  // component sets both directly on the native node it already refs for
  // cursor restoration below, the same imperative-DOM idiom the cursor-
  // restore layout effect already uses on the identical ref. Only
  // slashActiveId gates the effect: the ref is stable and slashListboxId is a
  // constant, so neither belongs in the dependency list.
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
  // meaningful position once the list itself has changed shape. The presence
  // flip covers Escape-dismiss→retype of the identical token (same start and
  // query, so those deps alone would keep a stale index): reopening always
  // restarts at the first option. Deliberately stricter than the composer's
  // twin effect, which keeps the index across an identical-token reopen.
  const slashTokenPresent = slashToken !== null;
  // biome-ignore lint/correctness/useExhaustiveDependencies: slashToken's start/query/presence are deliberate trigger-only deps - the effect body only calls setSlashHighlighted(0), but must still re-run whenever the token identity actually changes (a new match, the same match with a different query, or a dismiss→reopen flip), same idiom as the cursor-restore layout effect below
  useEffect(() => {
    setSlashHighlighted(0);
  }, [slashToken?.start, slashToken?.query, slashTokenPresent]);

  // Attachments reuse the composer's staged-image pipeline via a TextEditor
  // bridge over the prompt textarea (see Composer.tsx's own bridge for the
  // React controlled-input rationale). textRef mirrors `prompt` synchronously
  // so a late decode-failure callback never reverts newer typing.
  const textRef = useRef(prompt);
  textRef.current = prompt;
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
  const busyRef = draft.busyRef;
  // Mirrors `model` for the default-provider-credential effect below: that
  // effect must read whether Model is CURRENTLY untouched without itself
  // re-running (and re-issuing evener/launch/resolve + model/list) every time
  // the user picks a model - same rationale as busyRef, a ref read at async
  // resolution time rather than a dependency that reruns the effect.
  const modelRef = useRef(model);
  modelRef.current = model;
  // The model value the uncredentialed-default fallback below installed, or
  // null if the current model did not come from it. The fallback reads THIS
  // controller's catalog (model/list), so a value it installed must not be
  // forwarded to a remote target - the selected host resolves its own default
  // - and is retired when the target becomes remote. Cleared on every
  // user-driven model change, so a value the person chose is never treated as
  // fallback-derived (Component 06b review, round seven).
  //
  // The marker lives in the DRAFT, not a form-local ref (round eight): this
  // component is a singleton reused across drafts and unmounted/remounted with
  // the pane, so a ref-local marker leaked draft A's provenance onto an
  // identical model string draft B legitimately owns (clearing B's user-chosen
  // or sticky-default model when B went remote) and vanished on a remount,
  // letting the controller's fallback model ride a remote launch after all.
  const [defaultModelFallback, setDefaultModelFallback] = useDraftField(draft, "defaultModelFallback");

  function isCurrentDraft(): boolean {
    return spawnDraftsStore.getState().current?.fields === draft.fields;
  }

  // A launch may finish its draft after departure, but it cannot take back the
  // screen. Observe transitions, not just the final URL/current draft: a picker
  // need not change the URL, and A -> B -> A must not revive A's old authority.
  const viewOwnership = useRef({});
  useLayoutEffect(() => {
    const revoke = () => {
      viewOwnership.current = {};
    };
    const unsubscribe = spawnDraftsStore.subscribe((state, previous) => {
      if (state.current?.fields !== previous.current?.fields) revoke();
    });
    // The user's most recent navigation wins: ANY real URL change - even a
    // query-only one that keeps the same /new pathname and draft - retires a
    // pending launch's claim to the screen. A popstate carrying no URL
    // change (a redundant dispatch) does not revoke, and the launch's own
    // programmatic navigate() is safe: ownsLaunchView() runs before it.
    let lastURL = window.location.href;
    const onNavigation = () => {
      if (window.location.href === lastURL) return;
      lastURL = window.location.href;
      revoke();
    };
    window.addEventListener("popstate", onNavigation);
    return () => {
      revoke();
      unsubscribe();
      window.removeEventListener("popstate", onNavigation);
    };
  }, []);
  // Focus can change without routing (for example, a contextual dock pane).
  // Cleanup also permanently retires a launch when this form is unmounted.
  useLayoutEffect(() => {
    if (!focused) viewOwnership.current = {};
    return () => {
      viewOwnership.current = {};
    };
  }, [focused]);

  function captureLaunchView(): () => boolean {
    const owner = viewOwnership.current;
    const active = focused && isCurrentDraft() && window.location.pathname === "/new";
    return () => active && viewOwnership.current === owner && isCurrentDraft() && window.location.pathname === "/new";
  }

  function updatePrompt(next: string): void {
    if (isCurrentDraft()) textRef.current = next;
    setPrompt(next);
  }

  useLayoutEffect(() => {
    if (cursorRef.current !== null && textareaRef.current) {
      textareaRef.current.setSelectionRange(cursorRef.current, cursorRef.current);
      cursorRef.current = null;
    }
  });

  const textEditor: TextEditor = {
    read: () => {
      const text = draft.fields.getState().prompt;
      const cursor = isCurrentDraft()
        ? (cursorRef.current ?? textareaRef.current?.selectionStart ?? draft.fields.getState().prompt.length)
        : draft.fields.getState().prompt.length;
      // Preserve Spawn's existing insertion-at-caret behavior.
      return { text, cursor, selection: { start: cursor, end: cursor } };
    },
    write: (next, cursor) => {
      updatePrompt(next);
      if (isCurrentDraft()) cursorRef.current = cursor;
    },
  };
  const attachments = useAttachments(textEditor, draft.attachments);

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

  // Every catalog and readiness signal this form reads - provider instances,
  // the resolved default model, the harness list, plugin previews, the slash
  // catalog, path validate/browse/create - is fetched from THIS controller. A
  // remote launch instead runs on the selected source's own hub: appsource's
  // RemoteHubSource.StartThread forwards thread/start (clearing the controller's
  // source id) and that hub spawns on ITS host, resolving the model, credentials
  // and plugins from its own configuration. So a controller-local "missing" is
  // not evidence a remote launch cannot succeed, and must not BLOCK it - the
  // remote hub's own thread/start error is the authority. (Source-aware
  // discovery needs the evener/host/request proxy, which is not in this branch.)
  const remoteLaunch = submittedSource !== "" && submittedSource !== "local";
  // The selected target's own label for the notices and disclosures that name
  // it; falls back to the raw source id only while no manifest has ever named
  // the host (the display view keeps the label across a revalidation, when the
  // settled list is empty).
  const remoteSourceLabel =
    displaySources.find((candidate) => candidate.id === submittedSource)?.label ?? submittedSource;
  const usesEvenerModels = harnessUsesEvenerModels(harness, harnesses);
  // The provider verdict is host-scoped (component 07b): providerSetup reads the
  // selected host's own partition, so a remote target with no configured
  // provider on THAT host blocks Start the way a local one does, and the banner
  // below offers the host-side remediation instead of this controller's editor.
  const providerRequired = usesEvenerModels && providerSetup.status === "missing";
  // kata xgk8: Start cannot succeed while Model is untouched AND the hub has
  // confirmed there is no default to fall back to - see the resolve effect
  // below for how noDefaultModel is set. The onboarding scope is a second
  // required case: entering onboarding suppresses the uncredentialed-default
  // fallback (that same effect) in favor of the user's explicit choice from
  // the provider they just connected, so an untouched Model there has no
  // honest fallback - the resolved default may name a provider with no
  // credentials, and submitting it is a certain thread/start failure. A valid
  // /model invocation still bootstraps past this (slashModelBootstrap below).
  // Only a harness whose model comes from Evener's providers has that choice
  // to make: an unmanaged one (kind "external") carries its own model, so the
  // connector its model chip opens neither supplies nor replaces what this
  // pane would submit - requiring a choice there would disable Start, and
  // label the chip "Choose a model", over a model nothing launches with.
  // A remote launch is exempt: the controller's model state says nothing about
  // the host's, whose own thread/start error is the authority.
  const modelRequired =
    !remoteLaunch &&
    usesEvenerModels &&
    model === "" &&
    advancedModel === "" &&
    (noDefaultModel || providerChoiceScopes.current.has(`${harness}\0${cwd}`));
  // The model half of the host gate (component 07b review, round five): a
  // non-empty model belongs to the host whose catalog offered it, so it cannot
  // be submitted until the SELECTED host's model list has settled - see
  // modelHostSettled above for why settlement (not success) is the bar.
  const modelHostPending = model !== "" && modelHostSettled !== submittedSource;

  // A credential change can make models discoverable (a stored Vertex
  // credential JSON enables the publisher-model listing) or take them away,
  // so the scoped cache and global cleanup use two signals of it: this generation,
  // which evener/auth/updated advances the moment it arrives, and the
  // instance list's identity, which follows the credentials store's debounced
  // refetch and also covers an instance being added, edited or removed. On
  // either, the loader identities change, the catalog effect and pickers reload,
  // and global cleanup retires its old authority before requesting a new catalog.
  const [credentialsGeneration, setCredentialsGeneration] = useState(0);
  useEffect(
    () =>
      client.onNotification((n) => {
        if (n.method === "evener/auth/updated") {
          setCredentialsGeneration((generation) => generation + 1);
          return;
        }
        // A remote host's own evener/auth/updated is re-emitted to this browser
        // wrapped in evener/host/notification tagged with the host
        // (cmd/evener-hub/app_host_admin.go's remoteHostConfigNotifications).
        // The selected remote host's catalog must observe it too: otherwise a
        // credential or model change made on that host never invalidates this
        // pane's model/list cache, and the form keeps validating against a
        // stale list (a removed credential still reads "configured", an added
        // one still reads "missing"). Only the host whose catalog this form
        // reads - submittedSource, which every request below is scoped to - has
        // a relevant wrapper; another host's never moves this form. While the
        // manifest is unsettled that is the draft's own host, not hostChoice's
        // provisional "local": keying this listener on hostChoice dropped the
        // wrapper for the length of every revalidation.
        if (
          n.method === "evener/host/notification" &&
          n.params.host === submittedSource &&
          n.params.method === "evener/auth/updated"
        ) {
          setCredentialsGeneration((generation) => generation + 1);
        }
      }),
    [client, submittedSource],
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
    const key = `${submittedSource}\0${harness}\0${cwd}`;
    const cached = cache.get(key);
    if (cached) return cached;

    const request = hostRequest(client, submittedSource, "model/list", {
      harness: harness || undefined,
      cwd: cwd || undefined,
    });
    let tracked: Promise<ModelListResponse>;
    tracked = request.catch((error) => {
      if (cache.get(key) === tracked) cache.delete(key);
      throw error;
    });
    cache.set(key, tracked);
    return tracked;
  }, [client, submittedSource, harness, cwd, providerSetup.instances, credentialsGeneration]);
  const loadModels = useCallback(() => loadModelList().then((response) => response.data ?? []), [loadModelList]);
  // Every model-valued control in the spawn pane consumes this one scoped
  // response. The same promise is shared with the default-model preview, so
  // opening a picker and resolving the working directory cannot issue
  // duplicate model/list RPCs for the same harness and cwd.
  const loadCatalog = useCallback(
    (refresh?: boolean) => {
      // ModelSwitchTrigger's contract: refresh:true must bypass the caller's
      // cache. Without this, the pane's per-scope request cache could answer a
      // post-connection refresh with the listing it fetched before connecting,
      // and only providerConnected's own entries.clear() would be holding that
      // apart - an invisible coupling the loader itself should own.
      if (refresh) modelListCache.current.entries.clear();
      return loadModelList().then(modelListToCatalog);
    },
    [loadModelList],
  );
  // Both path RPCs answer with a Go slice, and an EMPTY one marshals as JSON
  // null rather than [] - a hub with no remembered projects, or a directory with
  // no children. types.gen.ts declares `data: string[]`, so the compiler is no
  // help here; these coalesce so a consumer counting entries never sees null.
  const listRecents = useCallback(
    () => hostRequest(client, submittedSource, "evener/projects/recent", {}).then((r) => r.data ?? []),
    [client, submittedSource],
  );
  // Injected into every PathField on this pane (the working directory here and
  // the advanced panel's path/pathList fields): the widget derives includeFiles
  // from its own kind, so this just forwards it.
  const complete = useCallback(
    (prefix: string, includeFiles: boolean) =>
      hostRequest(client, submittedSource, "evener/paths/complete", { prefix, includeFiles }).then((r) => r.data ?? []),
    [client, submittedSource],
  );
  // Path unfolding is host-scoped (component 07b). evener/path/validate answers
  // for the filesystem of the hub it reaches, so a remote target's paths are
  // validated by the SELECTED HOST through evener/host/request - the same
  // authority model the preflight, provider, plugin and model gates follow on
  // this pane, and the reason a directory that exists only on that host neither
  // reads as invalid here nor gets dropped from launchOverrides. Local targets
  // keep the plain call, byte-for-byte.
  // The host the pane is on RIGHT NOW, for async answers issued for an earlier
  // one. Assigned during render, so a switch is visible to a pending response's
  // microtask before the new host's own effects run - a path check answers for
  // the MACHINE that ran it, never for the one the reader moved to.
  const activeHostRef = useRef(submittedSource);
  activeHostRef.current = submittedSource;
  const validatePath = useCallback(
    (path: string, kind: string): Promise<PathValidation> => {
      // `path` is the server-canonicalized spelling, which a pathList add stores
      // in place of the raw input (matching the settings-side pathList field).
      const ask = (host: string) =>
        hostRequest(client, host, "evener/path/validate", { path, kind }).then((r) => ({
          valid: r.valid,
          error: r.error,
          path: r.path,
        }));
      // A superseded host's answer must not land (component 07b review, round
      // five): the advanced panel accepts a validation result on FIELD identity
      // alone, and a pathList add goes on its "ok" verdict, so a response issued
      // for host A could still mark - or re-add - an override after the reader
      // switched to host B, which then rides thread/start to a machine that
      // never accepted it. The answer is discarded and the question re-asked of
      // the host selected now, so every consumer acts on an answer for the
      // machine it is about to launch on. Only a switch DURING a request can
      // continue the loop; each round is one real RPC.
      const askSelectedHost = (issuedFor: string): Promise<PathValidation> =>
        ask(issuedFor).then((result) => {
          const current = activeHostRef.current;
          return current === issuedFor ? result : askSelectedHost(current);
        });
      return askSelectedHost(submittedSource);
    },
    [client, submittedSource],
  );
  // Creating a folder is a WRITE, and evener/dirs/create MkdirAll's it on the
  // filesystem of the hub it reaches. A remote target's path belongs to the
  // SELECTED HOST, so the picker's "New folder" is issued against that host
  // through evener/host/request (component 07b): the folder is created on the
  // machine the launch will use, never here.
  const createDirectory = useCallback(
    (path: string) => createDir(client, path, submittedSource),
    [client, submittedSource],
  );
  // A path-kind value's verdict belongs to the TARGET it was judged for, but
  // the record the advanced panel keeps carries no target with it: `invalid`
  // in advancedValues (and its message in advancedErrors) says "this target
  // cannot see this path", which is a fact about one host only. Because the
  // verdict is made at the moment the field's value changes, a later switch of
  // the Host picker leaves it asserting a fact about the wrong host: a path
  // typed while local stays marked "no such file or directory" after a remote
  // source is selected, so collectAdvancedOverrides keeps dropping it from
  // launchOverrides even though the disclosure below says the selected host
  // resolves these paths (round ten). Re-validate the stored values whenever
  // the launch target changes - the first run counts, so a remount under a
  // remote target clears a flag a local era left behind - so the flag, the
  // message and the collected overrides always describe the CURRENT target.
  // The write is the same one the panel's own updateScalar makes (values +
  // errors + the collected overrides), so a field the reader is looking at
  // updates in place.
  const pathValidationTarget = useRef<string | null>(null);
  useEffect(() => {
    const previous = pathValidationTarget.current;
    pathValidationTarget.current = submittedSource;
    // Only a target change invalidates a stored verdict; within one target,
    // editing a value is what re-judges it.
    if (previous === submittedSource) return;
    const stored = readAdvancedValues();
    for (const option of schemaOptions) {
      if (!option.pathKind) continue;
      const field = stored[option.wireField];
      if (!field) continue;
      const value = field.value;
      if (typeof value !== "string" || value.trim() === "") continue;
      // Every stored value is re-judged for the new target: a flag this pass
      // must SET is invisible in the record it is reading (the previous
      // target's verdict may have been "valid"), so the pass cannot skip the
      // unflagged ones.
      validatePath(value, schemaPathKind(option.pathKind)).then(
        (result) => {
          // A later edit owns this field now, even if it returned to the same
          // text - and a second target change owns the verdict: its own run has
          // already re-stamped the target this one was registered under, so
          // this response no longer describes the target in front of the
          // reader.
          if (pathValidationTarget.current !== submittedSource) return;
          const current = readAdvancedValues();
          if (current[option.wireField] !== field) return;
          const next: AdvancedValues = { ...current, [option.wireField]: { value, invalid: !result.valid } };
          setAdvancedValues(next);
          setAdvancedOverrides(collectAdvancedOverrides(schemaOptions, next));
          setAdvancedErrors((prev) => ({
            ...prev,
            [option.wireField]: result.valid ? "" : (result.error ?? "invalid path"),
          }));
        },
        // A failing validator never blocks (fail-open), matching the panel.
        () => {},
      );
    }
  }, [
    submittedSource,
    schemaOptions,
    validatePath,
    readAdvancedValues,
    setAdvancedValues,
    setAdvancedOverrides,
    setAdvancedErrors,
  ]);
  // The seed both picker surfaces open from when no directory is committed yet.
  // It is the CONTROLLER's own last accepted directory, so a remote host's
  // picker must not start there: the picker validates its opening path before
  // showing anything, and a controller path usually does not exist on the other
  // machine, so remote browsing began by refusing a stranger's directory
  // (component 07b review, round five). A remote picker opens from the committed
  // cwd, or the selected host's home when there is none.
  const pickerFallbackDir = isLocalHost(submittedSource) ? getGlobalLastWorkingDir() : "";
  const resolveConfig = useCallback(
    (overrides: LaunchConfigLayer) =>
      hostRequest(client, submittedSource, "evener/launch/resolve", {
        cwd,
        launchOverrides: pluginSelectionSupported
          ? withPluginSelection(overrides, pluginSelection)
          : withPluginSelection(overrides, { mode: "default" }),
      }),
    [client, submittedSource, cwd, pluginSelection, pluginSelectionSupported],
  );

  const pluginPreview = usePluginPreview({
    client,
    cwd,
    host: submittedSource,
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
    // Selection edits retain issues only for still-selected names. Only a ready
    // preview can reconcile those issues; a failed refresh cannot forgive them.
  }, [pluginPreview.state, pluginSelectionSupported, setPluginSelection, setKnownSelectionIssues]);

  // A refresh triggered by a selection toggle keeps the previous response on
  // the loading state (see usePluginPreview), so the disclosure and its list
  // stay mounted instead of flashing an empty "Inspecting plugins…" panel.
  const previewResponse = pluginSelectionSupported ? (pluginPreview.state.response ?? null) : null;
  const configuredPluginNames = previewResponse ? selectedPluginNames(pluginSelection, previewResponse) : [];
  const currentSelectionIssues =
    pluginPreview.state.status === "ready" ? pluginSelectionIssues(pluginSelection, pluginPreview.state.response) : [];
  const explicitSelectionLoading =
    pluginSelectionSupported && pluginSelection.mode === "explicit" && pluginPreview.state.status === "loading";
  // The plugin preview is the CONTROLLER's own inspection (evener/plugin/preview
  // answers for this hub's host). A remote launch runs on the selected source's
  // own hub, which resolves its own plugins, so a controller-local "this plugin
  // is missing/loading" is not evidence the remote launch cannot succeed and
  // must not disable Start or refuse the submit - the selection is forwarded and
  // the selected host validates it at start, the same authority model the cwd
  // preflight and provider check already follow. (Source-aware discovery needs
  // the evener/host/request proxy, absent from this branch.) The local path
  // keeps every gate unchanged.
  const pluginSelectionBlocked =
    !remoteLaunch && (explicitSelectionLoading || knownSelectionIssues.length > 0 || currentSelectionIssues.length > 0);

  // Writing the prompt is what starting an agent IS, so the caret starts
  // there rather than on whichever field happens to be first in the DOM. A
  // dedicated effect, not folded into the catalog loading below: an unrelated
  // fetch refactor must never silently change focus behavior.
  useMountAutofocus(textareaRef, focused);

  // Draft defaults and URL prefill are owned above the form's lifetime.
  // Load the host-dependent catalogs and focus the current prompt. Reloaded
  // when the selected host changes (component 07b): harnesses and launch
  // schema describe the host, so a remote selection must not keep showing the
  // controller's lists - neither while the new host's answer is in flight (the
  // switch clears them) nor after it fails, when a retained catalog would let
  // the user pick a harness the selected host does not have.
  const catalogHostRef = useRef(submittedSource);
  // The host whose harness catalog and launch schema are the answers in hand
  // (null until the current host's first load ANSWERS - a failed load settles
  // without being an answer). The reconciliation below must not read a catalog
  // that has not answered yet, or an empty one would read as "the host offers
  // nothing" and wipe a perfectly valid draft. Tracked PER CATALOG because the
  // two loads answer and fail independently: a host that answers one and
  // refuses the other still owns the half it answered, and only that half may
  // be reconciled against it (component 07b review, round six).
  const [harnessesHostSettled, setHarnessesHostSettled] = useState<string | null>(null);
  const [schemaHostSettled, setSchemaHostSettled] = useState<string | null>(null);
  // True while the CURRENT host's answers are outstanding. A switch is pending
  // because the catalogs have just been cleared and the draft's host-derived
  // launch config has not been reconciled against the new host's, so a submit in
  // that window could carry a value the host does not offer. A mount that starts
  // on a REMOTE draft is pending for the same reason: drafts persist at module
  // scope, so the form can come back with a remote source and a launch config
  // chosen from that host's (or another one's) catalogs before this mount has
  // heard an answer from it. A LOCAL mount is deliberately still not pending:
  // nothing about it is host-derived, so the form stays startable exactly as it
  // was before host routing existed.
  const [hostCatalogPending, setHostCatalogPending] = useState(() => !isLocalHost(submittedSource));
  useEffect(() => {
    let active = true;
    // A changed target retires the previous host's catalogs before the new
    // host's answer lands (and when it fails), so a retained list can never let
    // the reader pick a harness the selected host does not have.
    if (catalogHostRef.current !== submittedSource) {
      catalogHostRef.current = submittedSource;
      setHarnesses([]);
      setSchemaOptions([]);
      setHostCatalogPending(true);
      // The cleared catalogs make the "settled" stamps a lie: without this, a
      // rapid A→B→A switch keeps reading settled === A while harnesses/
      // schemaOptions hold the just-cleared EMPTY arrays, and the reconciliation
      // below wipes the draft's harness and every Advanced-options value against
      // them. Only answers that land for the CURRENT host re-stamp them.
      setHarnessesHostSettled(null);
      setSchemaHostSettled(null);
      // A plugin selection is a list of names resolved against the SELECTED
      // host's own preview, and a preview that fails for the new host never
      // reconciles it: pluginSelectionBlocked goes false again (no host that
      // never answered has reported an issue) while the previous host's explicit
      // names still ride combinedOverrides into the new host's preview, slash
      // catalog and thread/start. A selection this host cannot resolve is not a
      // selection, so a host switch drops it back to the host's own default -
      // the same reset handleHarnessChange applies when a harness does not
      // support plugin selection. Blocking until the new host's preview succeeds
      // instead would dead-end: the failed-with-no-list state renders no panel,
      // so no control is left to clear the block but Retry, which may never
      // succeed against the very selection it would retry.
      setPluginSelection({ mode: "default" });
      setKnownSelectionIssues([]);
    }
    // An answer stamps its OWN catalog as settled for this host, and is the
    // only thing that may reconcile that half of the draft: an empty list read
    // as "this host offers nothing" wiped the draft's harness and every
    // Advanced-options override on one transient hub error, with nothing left
    // for a later successful load to restore (component 07b review, round five;
    // the rejection handlers deliberately leave the catalogs as they were, as
    // they did before that round). A rejection is a settlement but not an
    // answer: it must release Start (a host that refuses a catalog can never
    // hold the submit hostage) and must NOT be reconciled against. The stamps
    // are per catalog so one host's refusal does not suppress reconciliation of
    // the catalog it DID answer (round six). `active` is false once this host is
    // superseded, so a previous host's late answer never certifies the current
    // one.
    const harnessesLoad = hostRequest(client, submittedSource, "evener/harnesses/list", {}).then(
      (r) => {
        if (!active) return;
        setHarnesses(r.data);
        setHarnessesHostSettled(submittedSource);
      },
      () => {},
    );
    const schemaLoad = hostRequest(client, submittedSource, "evener/launch/schema", {}).then(
      (r) => {
        if (!active) return;
        setSchemaOptions(perLaunchEvenerOptions(r));
        setSchemaHostSettled(submittedSource);
      },
      () => {},
    );
    void Promise.all([harnessesLoad, schemaLoad]).then(() => {
      // Settlement alone releases Start, even when one or both loads never
      // answered: the host refuses what it cannot serve at launch rather than
      // leaving the submit disabled forever. What the user can submit has been
      // reconciled against every answer that did land.
      if (!active) return;
      setHostCatalogPending(false);
    });
    return () => {
      active = false;
    };
  }, [client, submittedSource, setPluginSelection, setKnownSelectionIssues]);

  // The draft's launch config is chosen from the SELECTED host's catalogs, and
  // the draft store carries it across a host switch (component 07b review, round
  // three). Left alone, a harness or Advanced-options override that exists only
  // on the host that produced it still rides thread/start - and the mismatch is
  // invisible in the form, because an empty harness catalog falls back to the
  // "evener" label and an empty schema renders no Advanced fields at all. So
  // reconcile each half against the answers that just landed for the CURRENT
  // host: anything that host does not offer drops back to the host's own
  // default. Each half is gated by its OWN answer stamp, so a host that
  // answered one catalog and refused the other reconciles the half it answered
  // - and only that half, because the refusal remains no evidence about the
  // other catalog (component 07b review, rounds five and six). The values are
  // read from the store rather than from the rendered state so this effect does
  // not re-run on its own writes.
  useEffect(() => {
    if (harnessesHostSettled === submittedSource) {
      const currentHarness = draft.fields.getState().harness;
      if (currentHarness !== "" && !harnesses.some((candidate) => candidate.id === currentHarness)) {
        // The cleared id is the host's own default ("" reads as evener), which
        // supports plugin selection, so this is not a harness transition
        // handleHarnessChange's plugin-selection reset would fire on - and the
        // model is revalidated against the host's own catalog separately.
        setHarness("");
      }
    }
    // The Advanced-options maps are chosen from the launch schema alone, so
    // without a schema answer for this host there is no authority to filter
    // them.
    if (schemaHostSettled !== submittedSource) return;
    const offered = new Set(schemaOptions.map((option) => option.wireField));
    const overrides = draft.fields.getState().advancedOverrides;
    const keptOverrides = Object.fromEntries(Object.entries(overrides).filter(([field]) => offered.has(field)));
    if (Object.keys(keptOverrides).length !== Object.keys(overrides).length) {
      setAdvancedOverrides(keptOverrides);
    }
    const values = draft.fields.getState().advancedValues;
    const keptValues = Object.fromEntries(Object.entries(values).filter(([field]) => offered.has(field)));
    if (Object.keys(keptValues).length !== Object.keys(values).length) {
      setAdvancedValues(keptValues);
    }
    // An error is state about a FIELD, not about the host: the error of a kept
    // field survives (it explains a validation the user still has to fix), while
    // a dropped field's error goes with the field. Clearing the whole map here
    // discarded the kept fields' errors - the fields stayed invalid with nothing
    // left saying why (component 07b review, round four).
    const errors = draft.fields.getState().advancedErrors;
    const keptErrors = Object.fromEntries(Object.entries(errors).filter(([field]) => offered.has(field)));
    if (Object.keys(keptErrors).length !== Object.keys(errors).length) {
      setAdvancedErrors(keptErrors);
    }
  }, [
    harnessesHostSettled,
    schemaHostSettled,
    submittedSource,
    harnesses,
    schemaOptions,
    draft,
    setHarness,
    setAdvancedOverrides,
    setAdvancedValues,
    setAdvancedErrors,
  ]);
  // Persisted defaults span every project, so only an explicitly global Evener
  // catalog has authority to sweep them. Picker catalogs may belong to another
  // harness or directory. Refresh/unmount retires the request for all consumers.
  // biome-ignore lint/correctness/useExhaustiveDependencies: auth generation and provider instances trigger a fresh global catalog
  useEffect(() => {
    // A host change owes a fresh answer: the previous host's list says nothing
    // about the one selected now, so the gate reopens until this host's lands.
    if (modelHostRef.current !== submittedSource) {
      modelHostRef.current = submittedSource;
      setModelHostSettled(null);
    }
    const request = {
      active: true,
      promise: hostRequest(client, submittedSource, "model/list", { harness: "evener" }),
    };
    setGlobalModelRequest(request);
    request.promise.then(
      (r) => {
        if (!request.active) return;
        // An answer - the host's own list, for the host it names.
        setModelHostSettled(submittedSource);
        // model/list can serialize an empty Go slice as `data: null`
        // (appwire.ModelListResponse.Data carries no omitempty). Normalize here
        // so the sweep never iterates a non-iterable and skips its work.
        // Persisted defaults are the CONTROLLER's own localStorage, and a
        // remote host's catalog describes only that host. Sweeping local
        // defaults against it would permanently delete models the controller
        // still offers. Only the local host's catalog has authority here; a
        // remote draft is validated against the remote list by the effect
        // below, which never writes storage.
        if (isLocalHost(submittedSource)) sweepStaleModels(r.data ?? []);
      },
      () => {
        // A refusal is a SETTLEMENT, not an answer: it releases the submit gate
        // (an offline host must not hold Start hostage - the same rule the
        // harness/schema gate follows) while the draft validator below still
        // clears a model this host turns out not to serve.
        if (request.active) setModelHostSettled(submittedSource);
      },
    );
    return () => {
      request.active = false;
    };
  }, [client, submittedSource, providerSetup.instances, credentialsGeneration]);

  // Validate each entered draft independently of storage: an earlier sweep may
  // already have deleted its saved model while the live draft still retains it.
  // Navigation does not cancel origin-owned validation; provider refresh does.
  //
  // The catalog it judges against is the SELECTED host's (component 07b): the
  // routed model/list answers for that host, so a draft's model is discarded
  // when the host does not offer it. Storage stays the controller's business -
  // the sweep above is local-only - so this path never writes a saved default.
  useEffect(() => {
    if (!globalModelRequest || !usesEvenerModels) return;
    const initial = draft.fields.getState();
    if (!initial.model) return;
    globalModelRequest.promise.then(
      (r) => {
        const current = draft.fields.getState();
        if (!globalModelRequest.active || current.model !== initial.model || current.harness !== initial.harness)
          return;
        const verdict = modelValidityAgainstList(initial.model, r.data ?? []);
        if (verdict === "stale" || verdict === "malformed") {
          draft.fields.setState({ model: "", staleModelNotice: initial.model });
        }
      },
      () => {},
    );
  }, [draft, globalModelRequest, usesEvenerModels]);

  // Pane-level merged catalog for the Effort select's per-model ladder: the
  // same model/list catalog the pickers load on demand. Reloads with the
  // harness/cwd scope, exactly like loadCatalog itself. Fail-open: a rejected
  // load leaves modelCatalog null and the select on the fallback ladder.
  // Debounced because cwd updates straight from the path field's onChange. The
  // catalog is scoped by harness+cwd, so it settles with the path instead of
  // chasing every keystroke; model pickers call the same keyed loader on
  // demand.
  // biome-ignore lint/correctness/useExhaustiveDependencies: harness/cwd are trigger-only deps - the effect body only snapshots them into requestScope, but must re-run (and re-stamp) whenever the scope they define changes, same idiom as the cursor-restore layout effect in the composer
  useEffect(() => {
    let active = true;
    // The scope this request fetches for, stamped on commit below. A scope
    // change re-runs the effect and retires the previous run via active, so
    // only the latest scope's response commits its stamp. loadCatalog's own
    // identity is the full refresh-trigger key (client, harness, cwd,
    // instances, generation), so stamping it invalidates the snapshot across
    // credential/client refreshes too — not just scope changes.
    const requestScope = `${harness}\0${cwd}`;
    const requestLoader = loadCatalog;
    const settle = setTimeout(() => {
      loadCatalog().then(
        (catalog) => {
          if (active) {
            setModelCatalog((previous) => mergeCatalogSnapshot(previous, catalog));
            setModelCatalogStamp({ scope: requestScope, loader: requestLoader });
          }
        },
        () => {
          // Fail the stamp closed on refresh failure: the merged catalog
          // keeps serving display, but validation must not accept values
          // against a snapshot a failed refresh may have left behind.
          if (active) setModelCatalogStamp(null);
        },
      );
    }, CATALOG_SETTLE_MS);
    return () => {
      active = false;
      clearTimeout(settle);
    };
  }, [loadCatalog]);
  // The catalog pre-start /model validation may read: the pane catalog only
  // when its stamp matches the current scope AND the current loader, null
  // otherwise. Display surfaces (pickers, effort ladder) keep the merged
  // catalog for continuity; validation fail-closes through the mismatch
  // window instead of accepting a value the new scope never offered.
  const scopedModelCatalog =
    modelCatalogStamp !== null &&
    modelCatalogStamp.scope === `${harness}\0${cwd}` &&
    modelCatalogStamp.loader === loadCatalog
      ? modelCatalog
      : null;
  // Proven staleness: a snapshot committed for a DIFFERENT scope or loader.
  // Unlike a never-committed catalog (stamp null), this is positive knowledge
  // that the snapshot on hand belongs elsewhere, so validation fail-closes on
  // it rather than treating the value as "don't know".
  const modelCatalogScopeMismatch =
    modelCatalogStamp !== null &&
    (modelCatalogStamp.scope !== `${harness}\0${cwd}` || modelCatalogStamp.loader !== loadCatalog);

  // Branch HEAD resolution (floor §1.7): the readout is read-only, so HEAD is
  // its ONLY source - re-resolved on every working-dir change with no
  // user-edited escape hatch to respect. `active` still guards a late response
  // from a directory the user has already navigated away from.
  useEffect(() => {
    if (cwd.trim() === "") return undefined;
    let active = true;
    resolveHeadBranch(client, cwd, submittedSource).then((head) => {
      if (active) setBranchHead({ cwd, host: submittedSource, head });
    });
    return () => {
      active = false;
    };
  }, [client, cwd, submittedSource]);

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
      setDefaultPreview(null);
      return undefined;
    }
    let active = true;
    const settle = setTimeout(() => {
      Promise.all([resolveConfig(advancedOverrides), loadModels().catch(() => null)]).then(
        ([result, models]) => {
          if (!active) return;
          setDefaultPreview({ draft, host: submittedSource, effective: result.effective });
          const defaultModel = (result.effective.model ?? "").trim();
          // A remote target resolves its own default model from its own host's
          // credentials and catalog, so this controller's launchable set is not
          // evidence about it: injecting models[0] here would ride thread/start
          // and stop the selected host from resolving its own default (round
          // seven). The controller-local preview itself still lands above - the
          // disclosure below the form already says those readings are ours.
          if (defaultModel === "" || modelRef.current !== "" || !models || models.length === 0 || remoteLaunch) return;
          const slash = defaultModel.indexOf("/");
          const defaultProvider = slash === -1 ? defaultModel : defaultModel.slice(0, slash);
          const defaultCredentialed = models.some((m) => m.provider === defaultProvider);
          const fallback = models[0];
          // Only a harness whose model comes from Evener's providers may have
          // its untouched Model substituted: an unmanaged harness carries its
          // own model, so writing a qualified Evener "provider/model" here
          // would overwrite what it actually launches with.
          //
          // And only while the top-level chip is what launches: an Advanced-
          // options model override wins at submit (floor §1.11, spawnSchema's
          // resolveScalars), so with one set the chip does not launch -
          // substituting it would display a model that does not launch. The
          // override the user configured stays visible in Advanced options,
          // and a bad one still surfaces through thread/start's own error,
          // exactly as a chip the user picked does.
          if (
            usesEvenerModels &&
            advancedModel === "" &&
            !defaultCredentialed &&
            fallback &&
            !providerChoiceScopes.current.has(`${harness}\0${cwd}`)
          ) {
            const installed = `${fallback.provider}/${fallback.model}`;
            setDefaultModelFallback(installed);
            setModel(installed);
          }
        },
        () => {
          if (active) {
            setDefaultPreview(null);
          }
        },
      );
    }, CATALOG_SETTLE_MS);
    return () => {
      active = false;
      clearTimeout(settle);
    };
  }, [
    cwd,
    draft,
    submittedSource,
    advancedOverrides,
    advancedModel,
    resolveConfig,
    loadModels,
    remoteLaunch,
    setModel,
    setDefaultModelFallback,
    harness,
    usesEvenerModels,
  ]);

  // Retire a model this fallback installed once the target becomes remote: the
  // value describes the CONTROLLER's catalog, and forwarding it would prevent
  // the selected host from resolving its own default (round seven). Only a
  // value still equal to the one the fallback installed is cleared, so a
  // person's own choice - or a sticky draft default - is never touched. The
  // mark is this draft's own (round eight), so another draft's identical model
  // string is untouched, and it survives a pane remount.
  useEffect(() => {
    if (!remoteLaunch) return;
    if (defaultModelFallback !== null && modelRef.current === defaultModelFallback) {
      setDefaultModelFallback(null);
      setModel("");
    }
  }, [remoteLaunch, defaultModelFallback, setDefaultModelFallback, setModel]);

  // The Effort ladder belongs to the model that will actually launch, in the
  // same precedence thread/start applies (floor §1.11, spawnSchema's
  // resolveScalars): an Advanced-options model override first, then the
  // top-level chip, then the hub's resolved default for this cwd.
  // advancedModel itself is derived above, next to the override state.
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
    if (knownEffortLevels === null || scopedModelCatalog === null) return;
    if (reasoningEffort !== "" && reasoningEffort !== "none" && !knownEffortLevels.includes(reasoningEffort)) {
      setReasoningEffort("");
    }
  }, [knownEffortLevels, reasoningEffort, setReasoningEffort, scopedModelCatalog]);

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

  // Touching the visible top-level Model or Effort control is the user's newest
  // intent, so a standing Advanced-options override for that SAME field must not
  // silently win at submit (roborev: the chip would display the new value while
  // thread/start launched the stale override). Clear only that field's raw
  // advanced value and its collected override - the Advanced panel must not keep
  // displaying a value that is no longer launched. The OTHER field's override is
  // deliberately untouched, and an Advanced override set afterwards still wins.
  function clearAdvancedOverride(wireField: "model" | "reasoningEffort"): void {
    setAdvancedValues((prev) => {
      if (!(wireField in prev)) return prev;
      const next = { ...prev };
      delete next[wireField];
      return next;
    });
    setAdvancedOverrides((prev) => {
      if (prev[wireField] === undefined) return prev;
      const next = { ...prev };
      delete next[wireField];
      return next;
    });
  }

  function handleModelChange(next: string): void {
    // Any value the person sets is their own choice, not the
    // uncredentialed-default fallback's controller-derived pick, so it is no
    // longer retired if the launch target becomes remote.
    setDefaultModelFallback(null);
    setModel(next);
    clearAdvancedOverride("model");
    if (next !== "") setStaleNotice(null); // any new model clears the discard notice (floor §1.10)
  }

  function handleEffortChange(next: string): void {
    setReasoningEffort(next);
    clearAdvancedOverride("reasoningEffort");
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
    // The picker loads through the same keyed loadCatalog the pane effect
    // uses, so a successful pick is a current-scope validation snapshot even
    // when the background load failed (or hasn't landed): stamp it, or a
    // typed /model for the just-picked model stays refused against the stale
    // (usually null) stamp.
    setModelCatalogStamp({ scope: `${harness}\0${cwd}`, loader: loadCatalog });
  }

  function handlePromptKeyDown(event: React.KeyboardEvent<HTMLTextAreaElement>): void {
    // Inline slash-completion's own keyboard mechanics, ADAPTED for Spawn's
    // submit model (deliberately NOT a verbatim Composer port - Composer's
    // Enter sends, Spawn's plain Enter is a newline and only Mod/Ctrl+Enter
    // submits): ArrowUp/Down move the highlighted option (wrapping at both
    // ends) OVER the caret rather than moving the caret itself, unmodified Tab
    // OR unmodified non-composing Enter commits the highlighted option,
    // Escape dismisses without touching the prompt. Modified Tab (notably
    // Shift+Tab) falls through for focus navigation. The committing Enter never
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
        (event.key === "Tab" && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey) ||
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
  // handleSpawn may refuse a submit that CAN succeed.
  //
  // A catalog that never committed for this scope (stamp null and no scope
  // mismatch) cannot vouch for the value, so - parallel to doSpawn - a
  // shape-valid `provider/model` still bootstraps and thread/start judges it;
  // withholding Start here would dead-end the ~250ms settle window (or a
  // failed refresh) for a value that would succeed. A scope-matched catalog
  // still has to list the value, and proven staleness (another scope/loader's
  // stamp) still withholds Start.
  const slashModelBootstrap =
    modelRequired && pluginSelectionSupported && attachments.items.length === 0
      ? (() => {
          const match = matchBuiltinInvocation(prompt, spawnBuiltinCommands());
          if (match?.command.id !== "model" || match.argsText.trim() === "") return null;
          const value = match.argsText.trim();
          if (scopedModelCatalog === null && !modelCatalogScopeMismatch) {
            const { provider, model: modelId } = splitModelId(value);
            return provider !== "" && modelId !== "" ? value : null;
          }
          return findBuiltinArgument(resolveSpawnModelItems(scopedModelCatalog), value) !== undefined ? value : null;
        })()
      : null;

  async function doSpawn(submittedPromptRevision: number, ownsLaunchView: () => boolean): Promise<void> {
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
    // A match is CONSUMED like in-session Composer: the invocation text is
    // stripped from the start input (these builtins are frontend-only, so the
    // daemon would only receive the literal slash line as noise), and the
    // builtin applies through its own path instead. Non-builtin prompts
    // (including plugin commands/skills) still spawn verbatim - the daemon
    // expands those in the first input itself.
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
    // Bare /goal fail-closes pre-start like bare /reasoning-effort: there is
    // no goal to clear before the session exists, so starting one just to
    // no-op its clearing is waste, and sending the literal "/goal" as the
    // first turn is noise.
    if (builtinMatch && builtinMatch.command.id === "goal" && builtinMatch.argsText.trim() === "") {
      toasts.push("error", "/goal needs a value");
      busyRef.current = false;
      setBusy(false);
      setBusyStartedAt(null);
      return;
    }
    if (builtinMatch && (builtinMatch.command.id === "model" || builtinMatch.command.id === "reasoning-effort")) {
      // Pre-start validation for enum-arg builtins: there is no cheaper
      // moment to refuse than before the session exists. Unknown value ->
      // toast the blocked message and abort WITHOUT thread/start - AND reset
      // the busy guard handleSpawn set above, or Start strands disabled.
      // Empty /model means "(default)": fail-open, no override at all.
      const value = builtinMatch.argsText.trim();
      if (builtinMatch.command.id === "model" && value === "") {
        // Fall through to the ordinary start below with no model override.
      } else if (remoteLaunch) {
        // Remote target: the selected host's own hub validates the forwarded
        // value. This controller's catalog describes ITS providers and models
        // - not the host's - so matching against it here would refuse a model
        // or effort level that only the selected host supports, contradicting
        // the host-authority model the launch already follows for cwd,
        // providers and plugins. (Source-aware discovery needs the
        // evener/host/request proxy, absent from this branch.) The value is
        // forwarded verbatim: a model as "provider/model" (the split every
        // other /model path uses), an effort level as typed. Bare /model is the
        // "(default)" branch above and forwards no override; bare
        // /reasoning-effort still fails closed, since an empty enum value is
        // not a controller-catalog judgment.
        if (value === "") {
          toasts.push("error", "/reasoning-effort needs a value");
          busyRef.current = false;
          setBusy(false);
          setBusyStartedAt(null);
          return;
        }
        if (builtinMatch.command.id === "model") {
          const { provider, model: modelId } = splitModelId(value);
          // Unlike every other splitModelId caller, this input is RAW user text,
          // not a provider/model catalog id that always contains a slash: "foo"
          // splits to provider "foo" with an empty model, and the selected host
          // ignores a provider with no model (its own model field stays empty),
          // so the requested model would be silently dropped rather than
          // refused. The host's own parser requires provider/model too, so
          // refuse here with the same unknown-value message the local path
          // uses - a clear pre-launch rejection instead of a silent no-op
          // (round seven).
          if (provider === "" || modelId === "") {
            toasts.push("error", `/${builtinMatch.command.id}: unknown value "${value}"`);
            busyRef.current = false;
            setBusy(false);
            setBusyStartedAt(null);
            return;
          }
          slashScalars = { modelProvider: provider, model: modelId };
        } else {
          slashScalars = { reasoningEffort: value };
        }
      } else {
        // Effort validation reads the scope-stamped catalog, not the merged
        // display ladder: same staleness hole as /model (a value valid only
        // in the previous scope must not validate). Only PROVEN staleness
        // fail-closes (a stamp for another scope/loader): an unknown ladder
        // with no stamp — never loaded, failed refresh, or an entry without
        // ladder metadata — falls back to the fallback ladder, the pre-load
        // status quo. Conflating "don't know" with "known empty" would
        // refuse valid values whenever the catalog lists the model without
        // ladder details.
        const scopedEffortEntry =
          effortModel === ""
            ? undefined
            : scopedModelCatalog?.models.find((entry) => `${entry.provider}/${entry.model}` === effortModel);
        const scopedKnownEffortLevels = catalogEffortLevels(scopedEffortEntry);
        // Proven staleness fail-closes to no levels — and the chip value must
        // not re-authorize itself through `current`: effortOptionLevels
        // appends a missing current, so resolving against the chip would
        // validate a value the new scope never offered. Passing "" keeps the
        // stale chip out of the candidate set (bare effort fails closed on
        // the empty query regardless).
        const scopedEffortLevels = modelCatalogScopeMismatch ? [] : (scopedKnownEffortLevels ?? FALLBACK_EFFORT_LEVELS);
        const scopedEffortCurrent = modelCatalogScopeMismatch ? "" : reasoningEffort;
        // A model value cannot be validated against a catalog that never
        // committed for the current scope: during the ~250ms CATALOG_SETTLE_MS
        // window after mount - or after a failed refresh - resolveSpawnModelItems
        // resolves zero items for EVERY value, known or not, so fail-closing
        // here toasts a spurious "unknown value" for a model the scope does
        // offer. Treat it as "don't know" and forward the typed value as the
        // launch scalar, letting the start call's own check decide. Only PROVEN
        // staleness (a stamp for another scope/loader) and a scope-matched
        // catalog that omits the value still fail closed.
        const modelCatalogUnknown =
          builtinMatch.command.id === "model" && scopedModelCatalog === null && !modelCatalogScopeMismatch;
        const items =
          builtinMatch.command.id === "model"
            ? resolveSpawnModelItems(scopedModelCatalog)
            : resolveSpawnEffortItems(scopedEffortLevels, scopedEffortCurrent);
        // Bare /reasoning-effort fails CLOSED pre-start: the "" head of
        // resolveSpawnEffortItems (the "(default)" entry) must not count as
        // known here, so an empty effort value toasts
        // "/reasoning-effort needs a value" and aborts without thread/start
        // (palette parity - in-session bare /reasoning-effort errors with no
        // side effects). Bare /model stays fail-open via the branch above.
        const matched =
          builtinMatch.command.id === "reasoning-effort" && value === ""
            ? undefined
            : findBuiltinArgument(items, builtinMatch.argsText);
        if (!matched && !modelCatalogUnknown) {
          const message = value
            ? `/${builtinMatch.command.id}: unknown value "${value}"`
            : `/${builtinMatch.command.id} needs a value`;
          toasts.push("error", message);
          busyRef.current = false;
          setBusy(false);
          setBusyStartedAt(null);
          return;
        }
        if (builtinMatch.command.id === "model") {
          // A forwarded-but-unvalidated value (modelCatalogUnknown) is RAW user
          // text, not a provider/model catalog id: "foo" splits to provider
          // "foo" with an empty model, and the launch would carry no model at
          // all. Refuse the same way the remote path above does rather than
          // silently drop the request - this is a shape check, not a catalog
          // judgment, so it holds even while the catalog is unknown.
          const { provider, model: modelId } = splitModelId(matched ? matched.id : value);
          if (provider === "" || modelId === "") {
            toasts.push("error", `/${builtinMatch.command.id}: unknown value "${value}"`);
            busyRef.current = false;
            setBusy(false);
            setBusyStartedAt(null);
            return;
          }
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
      // A matched builtin is consumed: its invocation text is stripped so the
      // session starts dormant/configured rather than with a literal slash
      // line as its first turn. Anything else spawns verbatim.
      prompt: builtinMatch ? "" : prompt,
      attachments: attachments.toInputAttachments(),
      harness: harness || undefined,
      modelProvider: scalars.modelProvider,
      model: scalars.model,
      reasoningEffort: scalars.reasoningEffort,
      accessMode,
      launchOverrides: Object.keys(overrides).length > 0 ? overrides : undefined,
      // The draft's own host until a settled manifest can confirm a fallback,
      // then the resolved choice. Omitted for local (startThread drops "local"
      // from the wire), so the single-host request stays identical to before
      // the host picker existed.
      source: submittedSource,
    });
    if (builtinMatch && builtinMatch.command.id === "goal") {
      // Post-start application runs AFTER navigation below: awaiting goal/set
      // here would hold the UI in "Starting…" for the RPC timeout on a
      // delayed follow-up even though the session already exists (and a retry
      // could then create a duplicate session). Failure still toasts without
      // blocking anything - the session started fine, only the follow-up
      // setting failed. Not awaited: the pane stays mounted behind the
      // session pane (floor §1.14), and toasts are global, so the outcome
      // still surfaces.
      void runSpawnBuiltinAfterStart(builtinMatch.command.id, builtinMatch.argsText, ref, toasts);
    }
    saveDefaults({
      cwd,
      harness,
      model,
      accessMode,
      reasoningEffort,
      harnessUsesEvenerModels: usesEvenerModels,
      // A remote launch's cwd/model describe the selected host, not this
      // controller, so they must not overwrite the global scalar defaults a
      // later local spawn reads (round seven).
      remoteLaunch,
    });
    // Reset transient form state on success, before navigating away (floor
    // §1.14 L186: the pending-attachment bag is cleared and the paste
    // marker-counter reset). The spawn pane is a dockview singleton that can
    // still be mounted behind the session pane this navigates to, so without
    // this an already-sent prompt/image stays staged and re-sendable if the
    // user returns to it. Sticky defaults (harness/model/cwd/access
    // mode, floor §1.9-§1.10) are deliberately left untouched - only the
    // one-shot prompt/attachments reset.
    if (draft.fields.getState().promptRevision === submittedPromptRevision) {
      updatePrompt("");
    }
    attachments.clearSubmitted(submittedMarkers);
    if (draft.fields.getState().pluginSelection === pluginSelection) {
      handlePluginSelectionChange({ mode: "default" });
    }
    // Same defect class: both callers set busy=true before awaiting this
    // function but only their OWN catch blocks ever reset it back to false,
    // so a success fell through with the button stuck disabled/"Starting…"
    // forever on a pane that can outlive the navigation below.
    busyRef.current = false;
    setBusy(false);
    setBusyStartedAt(null);
    const url = paneToURL("session", { ref });
    if (url && ownsLaunchView()) navigate(url);
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
    // The selected host's harness/schema answers are still in flight, so the
    // draft's launch config has not been reconciled against them yet - a submit
    // now could carry a value the host does not offer. Same reasoning as the
    // disabled Start button; this catches the ⌘/Ctrl+Enter chord.
    // The model list is the same question asked of the same host, and it is the
    // field the harness/schema answer cannot vouch for, so it gates here too.
    if (hostCatalogPending || modelHostPending) return;
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
    const submittedPromptRevision = draft.fields.getState().promptRevision;
    const ownsLaunchView = captureLaunchView();
    try {
      // The working-directory preflight is host-scoped (component 07b):
      // evener/path/validate and evener/dirs/create answer for the hub they
      // reach, so a remote target's cwd is checked - and offered for creation -
      // on the SELECTED HOST through evener/host/request, the same host the
      // launch will use.
      const outcome = await preflightDir(client, cwd, submittedSource);
      if (outcome.kind === "abort") {
        toasts.push("error", outcome.message);
        busyRef.current = false;
        setBusy(false);
        setBusyStartedAt(null);
        return;
      }
      if (outcome.kind === "offer-create") {
        // Stamped with the host this preflight ran against (submittedSource is
        // the value the request carried), so the confirmation is bound to that
        // host rather than to whatever is selected when it is answered. The
        // launch target, not the picker's settled value: with the manifest still
        // in flight the two differ, and the request followed submittedSource.
        setCreateDialogPath(outcome.path);
        setCreateDialogHost(submittedSource);
        busyRef.current = false;
        setBusy(false);
        setBusyStartedAt(null);
        return;
      }
      await doSpawn(submittedPromptRevision, ownsLaunchView);
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
    // The confirmation belongs to the host that preflighted this path, not to
    // the host selected when the button is clicked: confirming across a host
    // change would create the path and start the session on a machine the user
    // never checked, with a draft that machine never offered. The host-switch
    // effect above dismisses the dialog, so this catches a confirm that raced
    // it or a dialog restored from the draft onto a selection that has since
    // moved. Abort rather than re-home: re-running the preflight against the
    // new host would take a second, materially different action off one click.
    if (createDialogHost !== submittedSource) {
      toasts.push(
        "error",
        "The selected host changed after this directory was checked, so nothing was created. Press Start to launch on the host you want.",
      );
      closeCreateDialog();
      return;
    }
    // The gate handleSpawn enforces (and the disabled Start button mirrors): the
    // selected host's harness/schema and model answers are still in flight, so
    // its launch config has not been reconciled against them and a submit could
    // carry a value that host does not offer. No toast: both requests always
    // SETTLE - a refusal releases the gate too (round five) - so a click here is
    // held briefly, never dead-ended, and the same reasoning as handleSpawn's
    // applies (the state is already explained, and a repeating chord must not
    // stack toasts).
    if (hostCatalogPending || modelHostPending) return;
    busyRef.current = true;
    setBusy(true);
    setBusyStartedAt(Date.now());
    const submittedPromptRevision = draft.fields.getState().promptRevision;
    const ownsLaunchView = captureLaunchView();
    try {
      await createDir(client, path, submittedSource);
      await doSpawn(submittedPromptRevision, ownsLaunchView);
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
      closeCreateDialog();
    }
  }

  // The dir-picker's "last accepted directory" seed (GLOBAL_LAST_WORKING_DIR_KEY)
  // is the CONTROLLER's own browse history: it seeds this hub's pickers, local
  // ones included. A remote target's cwd belongs to the SELECTED HOST, so
  // recording it would open the next local picker at a path that usually does
  // not exist here (Component 06b review, round eight - the picker-seed half of
  // round seven's controller-scoped-cwd rule).
  const commitLastWorkingDir = useCallback(
    (path: string) => {
      if (!remoteLaunch) setGlobalLastWorkingDir(path);
    },
    [remoteLaunch],
  );

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

        {/* Host picker (Component 06b): rendered only when the manifest lists a
            non-local source, so the common single-host form is byte-for-byte
            unchanged. Local is preselected (the draft default). An offline host
            still renders - the reader can see it exists - but its option is
            disabled and carries the reason in its own label. The row sits
            ABOVE the working directory: the folder list, recents, and
            validation all come from the selected machine (hostRequest), so
            the form reads pick-the-machine first, then the folder on it. */}
        {displayRemoteHosts.length > 0 && (
          <FormRow
            label="Host"
            htmlFor="spawn-host"
            help={
              displayRemoteHosts.some((candidate) => !candidate.online)
                ? "Where the session runs. Offline hosts can't be selected."
                : "Where the session runs."
            }
          >
            <select
              id="spawn-host"
              className={CLASS.hostSelect}
              value={displayHostChoice}
              // A submit snapshots this choice (handleSpawn's closure carries
              // the submittedSource/remoteLaunch that thread/start and
              // saveDefaults receive) and then awaits the local directory
              // preflight, so a change mid-submit would silently diverge from
              // what actually launches and from which defaults are saved.
              // Disabled while busy; the guard also covers the same-tick window
              // before that attribute commits (kata 61v2's busyRef discipline).
              disabled={busy}
              onChange={(event) => {
                if (busyRef.current) return;
                const next = event.target.value;
                setSource(next);
                // Selecting a host is never itself an attach request: an online
                // row is already attached (a dial would only be redundant), and
                // an offline row's option is disabled, so a select event cannot
                // name it — the Connect affordance below is the single path that
                // reaches an offline host.
              }}
            >
              {displaySources.map((candidate) => (
                <option key={candidate.id} value={candidate.id} disabled={!candidate.online}>
                  {candidate.online ? candidate.label : `${candidate.label} (offline)`}
                </option>
              ))}
            </select>
            {displayRemoteHosts.some((candidate) => !candidate.online) && (
              <div className={CLASS.hostConnectRow}>
                {displayRemoteHosts
                  .filter((candidate) => !candidate.online)
                  .map((candidate) => (
                    <Button
                      key={candidate.id}
                      variant="quiet"
                      size="xs"
                      type="button"
                      disabled={connectingHosts.has(candidate.id)}
                      onClick={() => connectHost(candidate.id)}
                    >
                      {connectingHosts.has(candidate.id)
                        ? `Connecting ${candidate.label}…`
                        : `Connect ${candidate.label}`}
                    </Button>
                  ))}
              </div>
            )}
          </FormRow>
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
            fallbackDir={pickerFallbackDir}
            complete={complete}
            listRecents={listRecents}
            validatePath={validatePath}
            createDirectory={createDirectory}
            onClose={() => setDirectoryOpen(false)}
            onPick={(path) => {
              setCwd(path);
              commitLastWorkingDir(path);
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
                      connectionRequest={modelHandoff}
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
                      onChange={(e) => handleEffortChange(e.target.value)}
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
                      pluginSelectionBlocked ||
                      hostCatalogPending ||
                      modelHostPending
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
            {providerSetupIsLocal ? (
              <>
                <span>Connect a provider to use a model. Sign in or add an API key here.</span>
                <Button onClick={openProviderSetup}>Connect provider</Button>
              </>
            ) : (
              // A remote host's provider registry is not editable from this
              // browser: the credentials editor is controller-scoped, so it
              // would configure the wrong machine. Say where the work has to
              // happen instead of offering an action that cannot unblock the
              // spawn.
              <span>No provider is configured on {remoteSourceLabel}. Configure one on that host to use a model.</span>
            )}
            <Button variant="quiet" onClick={() => void providerSetup.retry()}>
              Retry provider check
            </Button>
          </div>
        )}
        {usesEvenerModels && providerSetup.status === "error" && (
          <div className={CLASS.notice} role="status">
            <span>Could not check provider configuration.</span>
            <Button onClick={() => void providerSetup.retry()}>Retry provider check</Button>
            {providerSetupIsLocal && (
              <Button variant="quiet" onClick={openProviderSetup}>
                Review providers
              </Button>
            )}
          </div>
        )}
        {connectingProvider && providerSetupIsLocal && (
          <ConnectProviderDialogBoundary
            key={dialogChunkVersion}
            onRetry={retryProviderDialog}
            reloadAvailable={dialogReloadAvailable}
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
            {noDefaultModel
              ? "This hub has no default model configured — choose one to start."
              : "Choose one of the connected provider's models to start."}
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
            fallbackDir={pickerFallbackDir}
            onCwdPanelClose={commitLastWorkingDir}
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
          values={advancedValues}
          onValuesChange={setAdvancedValues}
          errors={advancedErrors}
          onErrorsChange={setAdvancedErrors}
          readValues={readAdvancedValues}
          draftId={draft}
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
        open={focused && createDialogPath !== null}
        title="Create directory?"
        confirmLabel="Create & start"
        destructive={false}
        busy={busy}
        onConfirm={() => void handleCreateConfirm()}
        onCancel={closeCreateDialog}
      >
        The directory {createDialogPath} doesn't exist yet. Create it and start the session?
      </ConfirmDialog>
    </PaneScaffold>
  );
}
