// The keybindings overrides store both apps' shortcut settings run on: one
// hub's keybinding overrides (evener/settings/keybindings/{get,patch} and the
// `changed` broadcast), reconciled into a live keybinding registry as a DELTA
// where the host has one, with a checkpointed draft editor for a host that
// edits offline. createKeybindingsStore is a factory - each app builds the one
// instance it wires to its connection and its view layer, tests build their
// own - returning the framework-free triple plus the connection-lifecycle
// methods the host drives. Pure logic - no DOM, no React, no storage of its
// own: the client, the registry, the character-key pref and the draft storage
// are all ports.
//
// The hub layer is the ONLY layer - there is no localStorage layer: an
// unsupported or unreachable hub means defaults only, never a local fallback
// copy. Applied overrides live in the registry, not in this store's state:
// startup `get` and every `changed` notification are validated semantically
// (keybindingValidation.ts) and reconciled into the registry as a DELTA - only
// actions whose effective chord changed are rebound, and actions whose
// overrides vanished get their defaults restored - so in-flight dispatcher
// state for untouched actions is never torn down. Validation failures degrade
// to warnings + skipped rules; nothing here can crash startup on malformed
// persisted data.
//
// Two write paths share the PATCH: patchOverrides is the live editor's direct
// write (serialized, preflight-validated against the registry, hubError on
// failure); saveDraft is the offline editor's checkpointed write (the intent
// is persisted through the draft port BEFORE the request leaves, and a lost
// reply leaves it `writeUncertain` until an authoritative read). Which one a
// host uses is the host's product decision; the hub state they confirm is one.

import { assertDraftDiscardable, discardCheckpointedDraft, persistCheckpointedDraft } from "./checkpointedDraftEditor";
import type { AppwireClient } from "./client";
import {
  createDraftRepository,
  type DiscardStoredDraftResult,
  type DraftPort,
  discardStoredDraft,
  UnreadableDraftError,
} from "./draftCheckpointPort";
import { errorText, wireRejectionPayload } from "./errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "./frameworkFreeStore";
import { serializeChord } from "./keybindingChord";
import { CHARACTER_KEY_TRIGGER_BINDING_ID } from "./keybindingDefaults";
import { rebindAction, removeActionBindings, restoreDefaultBinding } from "./keybindingOverrides";
import type { Binding, KeybindingsRegistry } from "./keybindingRegistry";
import { type ValidationWarning, validateOverrideRules } from "./keybindingValidation";
import { createReadyGenerationFence } from "./readyGenerationFence";
import {
  createSettingsHubGeneration,
  retireSettingsHubPayload,
  settleUnsettleableWrite,
} from "./settingsHubGeneration";
import type { AnyNotification, FeatureSet, KeybindingsOverrides, KeybindingsRule } from "./types.gen";

/** The two members of the client this store calls; AppwireClientLike satisfies it. */
export type KeybindingsClient = Pick<AppwireClient, "request" | "onNotification">;

export type KeybindingsSupport = "unknown" | "supported" | "unsupported";

/** Whether the connected hub advertises keybinding settings: unknown until
 * the handshake's feature set is in hand. */
export function keybindingsSupport(features: Pick<FeatureSet, "keybindingsSettings"> | undefined): KeybindingsSupport {
  if (features === undefined) return "unknown";
  return features.keybindingsSettings === true ? "supported" : "unsupported";
}

/** A draft persisted through the draft port: the rules the user proposed,
 * the hub revision they were composed against, and whether a PATCH carrying
 * them left without a confirmed outcome. */
export interface KeybindingDraftCheckpoint {
  id: string;
  baseRevision: number;
  rules: KeybindingsRule[];
  writeUncertain: boolean;
}

/** The storage port the checkpointed draft editor writes through. Every
 * method may throw; the store maps a throw to `storageUnavailable`. Field
 * for field the shared checkpointed-draft port, over this store's own
 * checkpoint shape. */
export type KeybindingDraftStorage = DraftPort<KeybindingDraftCheckpoint>;

/** The draft port a store without one runs on: the proposal lives in the
 * store's state only and does not survive the instance. There is no real
 * backing store here - only this one repository instance ever touches it -
 * so there is no concurrent writer a compare-and-swap could actually lose to;
 * removeIf/replaceIf report success unconditionally rather than the refusal
 * a byte-aware port reports when a record it named is gone: reporting false
 * there reads as "someone else replaced it" and
 * adopts a restoreDraft that reads null right back from this same fallback,
 * silently dropping the in-memory draft over a race that cannot happen
 * without real storage behind it. */
function memoryDraftStorage(): KeybindingDraftStorage {
  return {
    createId: () => "memory",
    load: () => null,
    save() {},
    insertIfAbsent: () => true,
    removeIf: () => true,
    replaceIf: () => true,
  };
}

/** The offline editor's proposal: the rules and confirmed revision they were
 * composed against, plus the ready generation that last confirmed them. A
 * restored checkpoint starts with no generation because storage does not
 * persist a hub-session identity. */
export interface KeybindingsDraft {
  version: 1;
  revision: number;
  rules: KeybindingsRule[];
  generation: number | null;
}

export interface KeybindingsStoreFields {
  hubSupport: KeybindingsSupport;
  hubLoading: boolean;
  hubError: string | null;
  /** Revision of the last confirmed hub payload (0 = shipped defaults). */
  revision: number;
  /** Bumps on every successful applyHubOverrides - a confirmed payload for
   * the current generation. Row-level error clearing keys on this (finding
   * 33): preflight and generation-fence rejections deliberately never set
   * hubError, so the hubError transition alone cannot clear their errors. */
  appliedSerial: number;
  /** The validated rules currently applied to the registry (the raw rules
   * verbatim when the store has no registry to validate against). */
  overrides: readonly KeybindingsRule[];
  /** The hub payload's rules VERBATIM, before validation filtering. The
   * editor composes whole-payload PATCHes from THIS set, not from
   * `overrides`: a rule validation skips (an unknown action from a newer
   * client, an unparseable chord) is still the hub's state, and a PATCH
   * composed from the validated set would silently delete it. */
  rawOverrides: readonly KeybindingsRule[];
  /** Semantic-validation warnings from the last applied payload. */
  warnings: readonly ValidationWarning[];
  /** True only while the applied state (revision/overrides/rawOverrides and
   * the registry's effective bindings) was confirmed by the hub for the
   * CURRENT ready generation. Set on every successful payload apply; cleared
   * when the ready generation ends (disconnect, client replacement, test
   * reset). Editing and patchOverrides both gate on it: a PATCH composed
   * from the previous hub's raw set with the previous hub's revision would
   * overwrite the new hub's config on a revision collision. */
  loaded: boolean;
  /** The confirmed GET's loadError: the hub's persisted state failed to load
   * and rawOverrides carries its shipped-default fallback (PATCH rejects).
   * Broadcasts and patch responses never carry one. */
  loadError: string | null;
  /** Set when a patch lost the revision race; the store has already refreshed
   * to the server's current state when this is non-null. */
  conflict: string | null;
  /** The offline editor's proposal: the rules and the confirmed revision they
   * were composed against. Restored from the draft port at creation. */
  draft: KeybindingsDraft | null;
  /** A checkpointed write is in flight. */
  saving: boolean;
  /** A checkpointed write left without a confirmed outcome; edits stay
   * blocked until an authoritative read (a GET without loadError) lands. */
  writeUncertain: boolean;
  /** The draft port threw; edits stay blocked until refreshOverrides can
   * restore the checkpoint again. */
  storageUnavailable: boolean;
  /** The port answered but what it held could not be read. The RECORD is the
   * problem, not the port: the section still loads, and discarding is
   * allowed and is what clears it. A host must offer that discard, or the
   * section is locked with no way out. */
  draftUnreadable: boolean;
  /** The draft's base revision is not the confirmed revision (the hub moved
   * under it, or a write's outcome is unknown): saveDraft refuses until
   * rebaseDraft reviews the current rules. */
  draftConflict: boolean;
  /** The draft port's own failures (save, discard, restore, cleanup);
   * hub-sourced failures ride hubError and an unconfirmed write is the
   * `writeUncertain` fact itself. */
  draftError: string | null;
}

export interface KeybindingsStoreActions {
  /** Refreshes from the hub under the current ready generation; a no-op
   * without one, while unsupported, or while the draft port is unavailable. */
  refreshOverrides(): Promise<void>;
  /** Writes SERIALIZE at the store: at most one PATCH is in flight, and each
   * write runs only after the previous one has fully landed. Pass a THUNK
   * to compose the whole-payload rule set at execution time (against the
   * then-current rawOverrides) - a rules array composed at call time would
   * race the in-flight write it queues behind: same expectedRevision, and a
   * payload missing the first edit's confirmed change. */
  patchOverrides(rules: readonly KeybindingsRule[] | (() => readonly KeybindingsRule[])): Promise<KeybindingsOverrides>;
  /** Replaces the draft with `rules`, checkpointed through the draft port.
   * Throws while the store is not editable (see assertEditable). */
  editDraft(rules: readonly KeybindingsRule[]): void;
  /** PATCHes `rules` (default: the draft, else the confirmed rules) with the
   * draft's base revision as expectedRevision, checkpointing the intent
   * before the request leaves. Rejects on a stale draft (rebaseDraft first),
   * while a save is in flight, and on any failed or unconfirmed write. */
  saveDraft(rules?: readonly KeybindingsRule[]): Promise<KeybindingsOverrides>;
  /** Drops the draft and its checkpoint. */
  discardDraft(): void;
  /** Moves the draft's base onto the confirmed revision the user reviewed;
   * throws when the hub has moved again since. */
  rebaseDraft(reviewedRevision: number): void;
}

export type KeybindingsStoreState = KeybindingsStoreFields & KeybindingsStoreActions;

export interface KeybindingsStore extends FrameworkFreeStore<KeybindingsStoreState> {
  /** Publishes the connection's support for keybinding settings. Resolving to
   * unsupported un-applies the hub's overrides and drops its payload state;
   * flapping back to supported while a ready generation is active begins a
   * new one so no pre-flap in-flight work lands. */
  setSupport(support: KeybindingsSupport): void;
  /** The client is ready: subscribe to its notifications and fence every
   * later refresh and write to this generation. The host refreshes next. */
  beginReadyGeneration(): void;
  /** The ready generation ended (disconnect, client replacement): drop the
   * subscription, fence in-flight work out, and mark the confirmed state as
   * no longer current. The registry keeps the overrides - a transient
   * disconnect of a supported hub keeps its shortcuts firing. */
  endReadyGeneration(): void;
  /** The client was replaced by one for a possibly different hub: un-apply
   * the old hub's overrides and reset its payload state. Returns false when
   * the un-apply rolled back against a wedged registry (the old overrides
   * are still firing and the payload is retained so a later apply can
   * reconcile from the intact applied map). */
  detachHub(): boolean;
  /** Re-runs the pref-aware apply of the confirmed raw rules after the
   * character-key pref flipped: a rule skipped for overlapping the "?"
   * trigger validates clean once the pref is off, and vice versa. */
  reapplyOverrides(): void;
  /** Ends the generation, un-applies every override and returns the state to
   * its initial values; the host re-publishes support afterwards. */
  reset(): void;
  /** Ends the generation for good: every later refresh and write is refused
   * and nothing in flight lands. */
  dispose(): void;
}

export interface KeybindingsStoreDeps {
  client: KeybindingsClient;
  /** The live registry the hub's overrides reconcile into. Without one the
   * hub's rules publish verbatim and nothing is validated - the shape for a
   * host with no dispatcher. */
  registry?: KeybindingsRegistry;
  /** The character-key pref reader (default: on). Gates the conditional "?"
   * trigger in both the restore simulation and the actual restore. */
  characterKeyTriggers?: () => boolean;
  /** The draft port. Without one the draft editor keeps its state in memory. */
  drafts?: KeybindingDraftStorage;
}

function initialState(): KeybindingsStoreFields {
  return {
    hubSupport: "unknown",
    hubLoading: false,
    hubError: null,
    revision: 0,
    appliedSerial: 0,
    overrides: [],
    rawOverrides: [],
    warnings: [],
    loaded: false,
    loadError: null,
    conflict: null,
    draft: null,
    saving: false,
    writeUncertain: false,
    storageUnavailable: false,
    draftUnreadable: false,
    draftConflict: false,
    draftError: null,
  };
}

/** Surfaced when support loss or a client rewire could not un-apply the
 * overrides (the restore rolled back against a wedged registry): the
 * overrides are still in effect and the hub payload state is RETAINED, so
 * the next refresh or connection change - which re-runs the un-apply or
 * reconciles from the intact applied map - gets a fresh attempt. */
const UNAPPLY_ROLLED_BACK_MESSAGE =
  "Could not restore the built-in default shortcuts: a conflicting binding is holding a default chord. This hub's keybinding overrides are still in effect; restoring retries on the next refresh or connection change.";
const UNAVAILABLE_MESSAGE = "Hub keybindings settings are unavailable.";
const MALFORMED_MESSAGE = "Hub returned malformed keybindings overrides";
/** The draft port's fixed failure copy: a raw storage error may carry a local
 * path, so it never reaches state verbatim. */
const DRAFT_SAVE_FAILED_MESSAGE = "Could not save the shortcut draft locally.";
const DRAFT_DISCARD_FAILED_MESSAGE = "Could not discard the shortcut draft locally.";
/** The one draft-port message the native offline provider also renders: its
 * store-free probe reads the same record a live store's restoreDraft does, so
 * the retained/offline projection imports this constant rather than
 * re-declaring the copy - one literal, so the two surfaces cannot drift. */
export const DRAFT_RESTORE_FAILED_MESSAGE =
  "Could not restore the saved shortcut draft. Check current shortcuts to retry.";
const DRAFT_CLEANUP_FAILED_MESSAGE =
  "The hub confirmed this save, but the local draft could not be updated. Check current shortcuts to retry.";
const DRAFT_REVIEW_AGAIN_MESSAGE = "Shortcuts changed again. Review the current values.";

/** The hub's whitespace, enumerated: `strings.TrimSpace` tests each rune with
 * Go's `unicode.IsSpace`, which is U+0009-U+000D, U+0020, U+0085 and U+00A0
 * below Latin-1, and U+1680, U+2000-U+200A, U+2028, U+2029, U+202F, U+205F and
 * U+3000 above it (go/src/unicode/graphic.go `IsSpace` and the `White_Space`
 * table in go/src/unicode/tables.go). Enumerated rather than trimmed because
 * JS `trim()` is a DIFFERENT set in both directions: it does not treat U+0085
 * as whitespace, so a rule of only U+0085 would pass here and then be trimmed
 * to nothing by the hub, which rejects every later whole-payload PATCH for it;
 * and it does strip U+FEFF, which the hub keeps as an ordinary character. */
// biome-ignore lint/suspicious/noControlCharactersInRegex: the hub's whitespace set contains control characters (U+0009-U+000D, U+0085); enumerating them is the point of this class
const HUB_WHITESPACE_ONLY = /^[\u0009-\u000D\u0020\u0085\u00A0\u1680\u2000-\u200A\u2028\u2029\u202F\u205F\u3000]*$/;

/** Names nothing, by the hub's own reading: empty, or whitespace only. */
function blank(value: string): boolean {
  return HUB_WHITESPACE_ONLY.test(value);
}

/** Structural check for a wire payload (get result, changed params, patch
 * response, conflict `current`). The server's own validation already ran; this
 * is the trust-boundary re-check so a malformed payload degrades to an error
 * state instead of a crash. */
export function fromWireOverrides(value: unknown): KeybindingsOverrides | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
  const candidate = value as Record<string, unknown>;
  if (candidate.version !== 1) return undefined;
  if (typeof candidate.revision !== "number" || !Number.isSafeInteger(candidate.revision) || candidate.revision < 0)
    return undefined;
  if (!Array.isArray(candidate.rules)) return undefined;
  // loadError rides GET only: the hub's persisted state failed to load and
  // rules carries the shipped-default fallback. String-or-absent.
  if (candidate.loadError !== undefined && typeof candidate.loadError !== "string") return undefined;
  // An action id is a string naming an action; a chord is null (an unbind) or
  // a string naming a chord. Blank strings are rejected at the boundary for
  // both hosts, trimmed as the hub trims them (appwire/keybindings.go
  // ValidateKeybindingsConfig): neither names anything, and letting one
  // through would put a rule in rawOverrides that the registry can only skip
  // and that every later whole-payload PATCH is rejected for.
  for (const rule of candidate.rules) {
    if (typeof rule !== "object" || rule === null || Array.isArray(rule)) return undefined;
    const entry = rule as Record<string, unknown>;
    if (typeof entry.action !== "string" || blank(entry.action)) return undefined;
    if (!(entry.chord === null || (typeof entry.chord === "string" && !blank(entry.chord)))) return undefined;
  }
  return value as KeybindingsOverrides;
}

function cloneRules(rules: readonly KeybindingsRule[]): KeybindingsRule[] {
  return rules.map((rule) => ({ action: rule.action, chord: rule.chord }));
}

/** A draft composed against one confirmed revision is stale once the hub has
 * confirmed a different one. A ready-generation change is a new hub session,
 * whose revision numbering may restart, so a stamped draft is stale even when
 * the new session happens to report the same revision. */
function staleDraft(draft: KeybindingsDraft | null, confirmedRevision: number, currentGeneration: number): boolean {
  if (draft === null) return false;
  if (draft.generation !== null && draft.generation !== currentGeneration) return true;
  return draft.revision !== confirmedRevision;
}

function invalidDraft(): never {
  throw new UnreadableDraftError("Invalid keybinding draft.");
}

/** A user can type or paste malformed rules; that is a plain validation
 * failure, never UnreadableDraftError - which is reserved for a stored
 * record this build cannot read. Same message as invalidDraft(): the two
 * differ only in which exception type the caller must be able to tell apart
 * from a decode failure, not in what the editor tells the user. */
function invalidUserRules(): never {
  throw new Error("Invalid keybinding draft.");
}

/** The strict rule check the draft editor runs on user input and on a
 * restored checkpoint: an action id and a chord must be non-empty strings
 * (or a null chord for an unbind). Always throws the plain validation
 * error - draftCheckpoint's own try/catch is what turns a decode failure
 * into UnreadableDraftError, so this never has to tell the two callers
 * apart itself. */
function keybindingRules(value: unknown): KeybindingsRule[] {
  if (!Array.isArray(value)) invalidUserRules();
  return value.map((item): KeybindingsRule => {
    if (item === null || typeof item !== "object" || Array.isArray(item)) invalidUserRules();
    const rule = item as Record<string, unknown>;
    if (
      typeof rule.action !== "string" ||
      blank(rule.action) ||
      (rule.chord !== null && (typeof rule.chord !== "string" || blank(rule.chord)))
    )
      invalidUserRules();
    return { action: rule.action, chord: rule.chord };
  });
}

function draftCheckpoint(value: unknown): KeybindingDraftCheckpoint {
  try {
    if (value === null || typeof value !== "object" || Array.isArray(value)) throw new Error("invalid");
    const item = value as Record<string, unknown>;
    if (
      typeof item.id !== "string" ||
      !item.id.length ||
      typeof item.baseRevision !== "number" ||
      !Number.isSafeInteger(item.baseRevision) ||
      item.baseRevision < 0 ||
      typeof item.writeUncertain !== "boolean"
    )
      throw new Error("invalid");
    return {
      id: item.id,
      baseRevision: item.baseRevision,
      rules: keybindingRules(item.rules),
      writeUncertain: item.writeUncertain,
    };
  } catch {
    invalidDraft();
  }
}

/** Whether `value` decodes as a valid keybindings draft checkpoint - the
 * check a store-free discard (no live repository to hold identity) runs
 * before removing a record shown as unreadable, so it refuses instead of
 * deleting one a concurrent writer has since replaced with something this
 * build can actually read (see draftCheckpointPort's discardStoredDraft). */
export function isReadableKeybindingDraft(value: unknown): boolean {
  try {
    draftCheckpoint(value);
    return true;
  } catch {
    return false;
  }
}

/** discardStoredDraft, defaulting `isReadable` to isReadableKeybindingDraft:
 * the generic function's own default (a caller with no decoder at all gets
 * the original always-remove behavior) is the wrong default for a
 * keybinding-named export, since a caller that forgets to pass its own
 * decoder would silently delete a record this build can actually read
 * instead of refusing. */
export function discardStoredKeybindingDraft(
  storage: DraftPort<KeybindingDraftCheckpoint>,
  isReadable: (value: unknown) => boolean = isReadableKeybindingDraft,
): DiscardStoredDraftResult | "storageUnavailable" {
  return discardStoredDraft(storage, isReadable);
}

/** The draft fields a checkpoint (or its absence) describes. Keeping this
 * mapping beside restoreDraft gives the live store its typed projection
 * without requiring a confirmed hub revision; the public decoder below keeps
 * its existing storage-only shape for store-free callers. */
function draftFieldsFrom(
  checkpoint: KeybindingDraftCheckpoint | null,
  generation: number | null = null,
): {
  draft: KeybindingsDraft | null;
  writeUncertain: boolean;
} {
  return {
    draft: checkpoint ? { version: 1, revision: checkpoint.baseRevision, rules: checkpoint.rules, generation } : null,
    writeUncertain: checkpoint?.writeUncertain ?? false,
  };
}

/** Decodes a stored value into the draft fields restoreDraft publishes. A
 * store-free caller uses this after classifying a readable replacement; an
 * invalid value is treated as no draft because classification already owns
 * the unreadable-record signal. */
export function decodeKeybindingDraftFields(value: unknown): {
  draft: KeybindingsOverrides | null;
  writeUncertain: boolean;
} {
  try {
    const { draft, writeUncertain } = draftFieldsFrom(draftCheckpoint(value));
    return {
      draft: draft === null ? null : { version: draft.version, revision: draft.revision, rules: draft.rules },
      writeUncertain,
    };
  } catch {
    return { draft: null, writeUncertain: false };
  }
}

/** The conflict rejection's `current` (the server's state after a lost
 * revision race) and the post-rename durable failure's `applied` (the hub's
 * rename already published the patch and only a follow-up sync failed, so the
 * write must be treated as applied - not as a hubError that leaves editing
 * disabled over live new bindings; roborev PR #884 round 2), both decoded via
 * wireRejectionPayload - see its own doc. */
function rejectionPayload(error: unknown, info: string, key: string): KeybindingsOverrides | undefined {
  return wireRejectionPayload(error, info, key, fromWireOverrides);
}

/** What applying a confirmed payload does to the host's bindings. The
 * registry-backed reconciler owns the applied map, the delta rebind and the
 * rollback; the verbatim one is for a host with no dispatcher. */
interface OverrideReconciler {
  /** Reconciles the host to `rules`; throws with the host rolled back. */
  apply(rules: readonly KeybindingsRule[]): Pick<KeybindingsStoreFields, "overrides" | "warnings">;
  /** See registryReconciler's doc. Returns false when the restore rolled back. */
  unapplyAll(): boolean;
  /** The warnings `rules` would introduce over what `baseline` already produces. */
  introducedWarnings(baseline: readonly KeybindingsRule[], rules: readonly KeybindingsRule[]): ValidationWarning[];
  /** Mirrors the character-key pref into the host before a re-apply. */
  prepareReapply(): void;
}

function verbatimReconciler(): OverrideReconciler {
  return {
    apply: (rules) => ({ overrides: rules, warnings: [] }),
    unapplyAll: () => true,
    introducedWarnings: () => [],
    prepareReapply: () => {},
  };
}

function registryReconciler(registry: KeybindingsRegistry, characterKeyTriggers: () => boolean): OverrideReconciler {
  /** The action -> effective override (serialized chord, or null for an
   * unbind) currently applied to the registry. */
  const applied = new Map<string, string | null>();

  /** Restores a bindings snapshot taken before a failed reconcile: unwinds
   * every current binding and re-registers the snapshot in order, returning
   * the registry to its last good state. Re-registering a previously-valid
   * set cannot conflict. */
  function rollback(snapshot: readonly Binding[]): void {
    const state = registry.getState();
    for (const binding of state.bindings) state.unregisterBinding(binding.id);
    for (const binding of snapshot) {
      state.registerBinding({
        id: binding.id,
        actionId: binding.actionId,
        chord: binding.chord,
        scope: binding.scope,
        ...(binding.when === undefined ? {} : { when: binding.when }),
        allowInEditable: binding.allowInEditable,
        allowInModal: binding.allowInModal,
        ignoreIfDefaultPrevented: binding.ignoreIfDefaultPrevented,
      });
    }
  }

  function validate(rules: readonly KeybindingsRule[], pref: boolean) {
    return validateOverrideRules(rules, registry, undefined, new Set(applied.keys()), pref);
  }

  return {
    /** Reconciles the registry to the payload's effective overrides:
     * validates, strips the bindings of every action whose effective chord
     * changed, then re-establishes each (override or restored default).
     * Two-phase so a payload that moves a chord between actions never trips a
     * transient conflict. The mutation is atomic: a throw rolls the registry
     * back to its pre-reconcile state before propagating, so callers surface
     * the failure with the last good bindings intact. */
    apply(rules) {
      // The pref gates BOTH the restore simulation and the actual restore
      // below (finding 27): with character-key triggers off the live registry
      // has no "?" cheatsheet binding, so neither a simulated nor a real
      // restore may claim one - a real restore registering it would collide
      // with a user rule holding exactly [Shift]+? and throw mid-reconcile.
      const pref = characterKeyTriggers();
      const validated = validate(rules, pref);
      const next = new Map<string, string | null>();
      for (const rule of validated.rules) {
        next.set(rule.action, rule.chord === null ? null : serializeChord(rule.chord));
      }
      const changedActions = new Set<string>();
      for (const [action, chord] of next) {
        if (!applied.has(action) || applied.get(action) !== chord) changedActions.add(action);
      }
      for (const action of applied.keys()) {
        if (!next.has(action)) changedActions.add(action);
      }
      if (changedActions.size > 0) {
        const snapshot = registry.getState().bindings;
        try {
          for (const action of changedActions) removeActionBindings(registry, action);
          for (const action of changedActions) {
            if (next.has(action)) {
              rebindAction(registry, action, next.get(action) ?? null);
            } else {
              restoreDefaultBinding(registry, action, { characterKeyTriggers: pref });
            }
          }
        } catch (error) {
          rollback(snapshot);
          throw error;
        }
      }
      applied.clear();
      for (const [action, chord] of next) applied.set(action, chord);
      return {
        overrides: validated.rules.map((rule) => ({
          action: rule.action,
          chord: rule.chord === null ? null : serializeChord(rule.chord),
        })),
        warnings: validated.warnings,
      };
    },

    /** Restores defaults for every applied override and clears the applied
     * map: the registry must stop presenting a hub's overrides the moment that
     * hub's state stops being current (client replacement, test reset) - that
     * staleness one layer down is the same defect as a stale store payload.
     * Atomic, mirroring apply: two-phase (strip every applied action's
     * bindings first, THEN restore each default) so a chord that moved between
     * two overridden actions never trips a transient conflict mid-unwind, and
     * any throw rolls the registry back to its pre-unwind snapshot. On rollback
     * the applied map stays INTACT - clearing it while the registry still holds
     * overrides would detach the unwind bookkeeping from the registry, and the
     * next reconcile's delta math would misread the wedged bindings. The reset
     * itself never propagates (a wedged registry - a foreign binding squatting
     * a default chord - must not make reset throw); the next reconcile or the
     * next test's registry rebuild retries from the intact map.
     *
     * ORDERING (the web cheatsheetController's contract): this mutates the
     * registry ONLY. Callers must fire any store setState - which is what the
     * character-key reconcile subscribes to - AFTER this returns, so the
     * reconcile sees the final shape (restore re-registers the conditional "?"
     * entry with no knowledge of the pref; the reconcile then removes it if the
     * pref is off).
     *
     * Returns false when the restore rolled back (the registry still holds the
     * overrides and the applied map is intact), true otherwise - callers that
     * discard the hub payload state after un-applying must NOT do so on false:
     * the retained payload is the only thing that can re-drive reconciliation
     * once the wedge clears (finding 31). */
    unapplyAll() {
      if (applied.size === 0) return true;
      const snapshot = registry.getState().bindings;
      try {
        for (const action of applied.keys()) removeActionBindings(registry, action);
        // Pref-aware like apply's restore: while the character-key pref is off
        // the conditional "?" entry is not re-registered (the
        // cheatsheetController owns it), so the restore cannot collide with a
        // user rule holding exactly [Shift]+? (finding 27).
        for (const action of applied.keys())
          restoreDefaultBinding(registry, action, { characterKeyTriggers: characterKeyTriggers() });
      } catch {
        // See above: roll back, keep the applied map, never propagate.
        rollback(snapshot);
        return false;
      }
      applied.clear();
      return true;
    },

    /** Pre-flight semantic validation (the parked 2b minor the settings
     * editor makes live): the SAME simulation apply runs on a confirmed
     * payload, run BEFORE the hub write. A rule the reconcile would skip -
     * unknown action, unparseable or platform-reserved chord, or a conflict on
     * the simulated final map - would otherwise be accepted by the hub (the
     * server validates structure only) and then silently not apply. The
     * payload is composed from the hub's RAW rules, so it can carry a
     * PRESERVED rule validation skips - an unknown action from a newer client,
     * say. That is a pre-existing condition, not a defect this call
     * introduced: only warnings the CURRENT raw set does not already produce
     * count, keyed by action+message (the baseline simulation runs against the
     * same live registry and applied set, so a preserved rule reproduces its
     * apply-time warning verbatim). */
    introducedWarnings(baseline, rules) {
      const pref = characterKeyTriggers();
      const key = (warning: ValidationWarning) => `${warning.rule.action} ${warning.message}`;
      const baselineKeys = new Set(validate(baseline, pref).warnings.map(key));
      return validate(rules, pref).warnings.filter((warning) => !baselineKeys.has(key(warning)));
    },

    /** The registry must mirror the pref BEFORE the re-apply mutates: tinykeys
     * canonicalizes the shifted character, so the conditional entry and a
     * Shift+? claim serialize identically - a pref-off re-apply registering
     * that claim while the entry is still live would hit registerBinding's
     * exact-match conflict and roll back. Unregistering is idempotent and
     * mirrors what the web's cheatsheetController reconcile is about to do (it
     * re-derives the same end state and no-ops). The pref-ON direction needs
     * no mirror: the re-apply's pref-authoritative simulation un-applies a
     * conflicting claim first, and the reconcile - fired by the store's
     * setState - registers "?" against the clean map. */
    prepareReapply() {
      if (!characterKeyTriggers()) registry.getState().unregisterBinding(CHARACTER_KEY_TRIGGER_BINDING_ID);
    },
  };
}

/** Builds a store over `client`, reconciling into `registry` when given. */
export function createKeybindingsStore(deps: KeybindingsStoreDeps): KeybindingsStore {
  const { client } = deps;
  const reconciler =
    deps.registry === undefined
      ? verbatimReconciler()
      : registryReconciler(deps.registry, deps.characterKeyTriggers ?? (() => true));
  const drafts = createDraftRepository(deps.drafts ?? memoryDraftStorage(), draftCheckpoint);

  // Ready-generation wiring: every refresh, write and notification captures
  // the generation it started under and lands only through the shared fence.
  const fence = createReadyGenerationFence(isSupported);
  /** Set when an un-apply rolled back against a wedged registry (findings 31
   * and 32): the overrides are STILL firing, so the rollback hubError is not
   * stale and refreshFor's entry clear must not wipe it. Cleared by the next
   * successful apply (which reconciles the registry past the wedge) and by
   * reset. */
  let unapplyRolledBack = false;
  /** Set when a changed-notification is dropped because the current
   * generation has no confirmed state yet (finding 25): the refresh that
   * lands the state may carry a response PREDATING the dropped change, so a
   * successful refresh with the flag set fires ONE follow-up fetch. Per
   * generation: beginReadyGeneration starts every generation clean. */
  let missedChangeNotification = false;
  /** The write-serialization chain: every patchOverrides queues behind the
   * previous write's full settlement (success OR failure). Reset with the
   * rest of the wiring state so a never-resolving write cannot leak into the
   * next test. */
  let writeQueue: Promise<void> = Promise.resolve();
  /** The direct write's own claim, RESERVED the moment patchOverrides() is
   * called (not once its queued turn starts running) until this write's own
   * settlement clears it. A patchOverrides() call only queues behind
   * writeQueue - its `run` may not execute for a microtask or more - and a
   * same-turn saveDraft() call reads this field before that queued turn has
   * a chance to claim the shared write token, so the reservation itself has
   * to be synchronous. `token` stays null until the queued turn actually
   * claims one; a null token still blocks saveDraft (below) as long as its
   * generation is live, the same posture a claimed token takes via
   * fence.writeStillMine. Both write paths share one write token
   * (fence.claimWrite): a checkpointed save starting here would claim a
   * NEWER one, fencing the direct write's own reply out as superseded even
   * though the hub may have already applied it. Comparing by OBJECT
   * IDENTITY (not a token value) in `run`'s finally means a stale call's
   * cleanup can never clear a newer call's still-pending reservation. */
  let directWrite: { generation: number; token: number | null } | null = null;

  const store = createFrameworkFreeStore<KeybindingsStoreState>(() => ({
    ...initialState(),
    // The draft restore is part of the initial state so a host that builds
    // the store synchronously (native, per connection) sees the persisted
    // proposal on its first read, before any refresh.
    ...restoreDraft({ loaded: false, revision: 0 }),
    refreshOverrides,
    patchOverrides,
    editDraft,
    saveDraft,
    discardDraft,
    rebaseDraft,
  }));
  const { getState, setState } = store;

  /** The fields a restored checkpoint (or its absence, or a failed restore)
   * sets; `confirmed` is passed in because the creation-time restore runs
   * before there is any state to read. `generation` is only supplied by the
   * identity-aware recovery path; an ordinary restore always uses null. */
  function restoreDraft(
    confirmed: { loaded: boolean; revision: number },
    generation: number | null = null,
    checkpointOverride?: KeybindingDraftCheckpoint | null,
  ): Partial<KeybindingsStoreFields> {
    try {
      const checkpoint = checkpointOverride === undefined ? drafts.load() : checkpointOverride;
      const { draft, writeUncertain } = draftFieldsFrom(checkpoint, generation);
      return {
        draft,
        writeUncertain,
        storageUnavailable: false,
        draftUnreadable: false,
        draftError: null,
        draftConflict: confirmed.loaded && staleDraft(draft, confirmed.revision, fence.generation),
      };
    } catch (error) {
      // An UnreadableDraftError names the RECORD as the problem, not the
      // port: whatever draft/writeUncertain/draftConflict described before
      // this call described a record that no longer exists to describe, so
      // they clear too - otherwise a write left uncertain by an earlier
      // attempt would stay that way forever, and assertDiscardable's
      // unconditional writeUncertain check would refuse the one recovery
      // (discard) an unreadable record is supposed to allow. A genuine port
      // failure (the read itself failed, not what it read) says nothing
      // about whether the in-memory state is still accurate, so it is left
      // alone.
      const unreadable = error instanceof UnreadableDraftError;
      return {
        storageUnavailable: true,
        draftError: DRAFT_RESTORE_FAILED_MESSAGE,
        // Only an unreadable RECORD says anything about draftUnreadable - a
        // genuine port failure (this branch otherwise) says nothing about
        // whether an earlier unreadable classification still holds, so it
        // is omitted rather than reset to false, which would silently hide
        // the one recovery (discard) an unreadable record allows.
        ...(unreadable ? { draftUnreadable: true, draft: null, writeUncertain: false, draftConflict: false } : {}),
      };
    }
  }

  /** Re-reads after a port failure without laundering a stale generation:
   * only the same classified checkpoint identity may carry forward the
   * in-memory stamp. A replacement with equal fields is still a new record
   * and takes the ordinary null-generation restore path. */
  function reloadDraft(confirmed: { loaded: boolean; revision: number }): Partial<KeybindingsStoreFields> {
    try {
      const { checkpoint, sameIdentity } = drafts.reload();
      const generation = sameIdentity ? (getState().draft?.generation ?? null) : null;
      return restoreDraft(confirmed, generation, checkpoint);
    } catch (error) {
      const unreadable = error instanceof UnreadableDraftError;
      return {
        storageUnavailable: true,
        draftError: DRAFT_RESTORE_FAILED_MESSAGE,
        ...(unreadable ? { draftUnreadable: true, draft: null, writeUncertain: false, draftConflict: false } : {}),
      };
    }
  }

  function isSupported(): boolean {
    return getState().hubSupport === "supported";
  }

  /** The generation to stamp a freshly composed or reconciled draft with -
   * null before any ready generation has begun. */
  function currentGeneration(): number | null {
    return fence.generation >= 0 ? fence.generation : null;
  }

  /** Publishes the end of a write whose reply can never be settled by
   * anything else. This store's own draftConflict (a field the shared
   * helper's generic Fields type does not carry) rides through
   * settleUnsettleableWrite's `extra` - the same posture every other
   * unknown-outcome settle takes (no reply at all, a malformed reply): the
   * proposal needs review, not just a retry. */
  function settleLostHubWrite(generation: number, token: number): void {
    settleUnsettleableWrite(fence, generation, token === fence.writeToken, getState, setState, {
      draftConflict: true,
    });
  }

  /** The confirmed payload can no longer be acted on (the generation ended,
   * support dropped, the hub was replaced) - see retireSettingsHubPayload for
   * the in-flight flags this resets. `extra` is the site's own addition
   * (payload arrays, hubError) in the same publish; revision resets to 0
   * with them (revision numbering is hub-scoped, so a retained revision
   * would let applyHubOverrides' stale guard silently discard a lower
   * revision the returning hub legitimately reports - roborev PR #884
   * round 11). */
  function retirePayload(extra: Partial<KeybindingsStoreFields> = {}): void {
    retireSettingsHubPayload(fence, getState, setState, { revision: 0, ...extra });
  }

  /** Applies a confirmed hub payload (get result, changed params, patch
   * response): stale revisions are ignored, the rest reconcile the registry.
   * A GET carrying loadError is authoritative at ANY revision: the hub is
   * reporting that its persisted state is gone and the fallback is in effect,
   * and a retained higher revision would keep editing enabled against a hub
   * whose PATCH rejects. The revision advances ONLY after a successful
   * reconcile: a failed apply leaves the previous revision in place so the
   * payload stays retryable and a later `changed` with the same revision is
   * not eaten by the stale guard. `extra` lands in the same publish
   * regardless of whether the payload is applied or ignored. `settle` is
   * resolved, and lands in that same publish, ONLY once the reconcile has
   * succeeded: a settling side effect (settledWrite's checkpoint
   * reclassification) must never run for a payload the stale guard is about
   * to ignore, nor for a reconcile that then throws before this can publish
   * it. Returns false for an ignored payload. */
  function applyHubOverrides(
    payload: KeybindingsOverrides,
    extra: Partial<KeybindingsStoreFields> = {},
    settle?: () => Partial<KeybindingsStoreFields>,
  ): boolean {
    const state = getState();
    if (payload.revision < state.revision && payload.loadError === undefined) {
      if (Object.keys(extra).length > 0) setState(extra);
      return false;
    }
    const rules = cloneRules(payload.rules);
    const reconciled = reconciler.apply(rules);
    // The reconcile succeeded, so any rolled-back un-apply's wedge is cleared
    // with it: the rollback hubError is now stale and may clear normally.
    unapplyRolledBack = false;
    const resolved = settle?.() ?? {};
    // A successful apply supersedes any earlier apply failure's hubError AND
    // any earlier patch's revision-race conflict - the store is now confirmed
    // at this payload either way. Clearing one without the other was the
    // parked 2b asymmetry: a stale conflict notice outlived the state it
    // described (only a successful PATCH cleared it).
    // rawOverrides is retained VERBATIM (validation filtering already happened
    // inside the reconciler): an edit's whole-payload PATCH is composed from
    // this set so a rule validation skips survives an unrelated edit. Only a
    // successful reconcile advances it - a failed apply leaves the last good
    // raw set beside the last good revision.
    const draft =
      state.draft !== null && state.draft.generation === null && fence.generation >= 0
        ? { ...state.draft, generation: fence.generation }
        : state.draft;
    setState({
      ...reconciled,
      rawOverrides: rules,
      revision: payload.revision,
      // Row-level error clearing keys on this bump (finding 33): preflight and
      // generation-fence rejections deliberately never set hubError, so the
      // hubError transition alone cannot clear a row error they left behind.
      appliedSerial: state.appliedSerial + 1,
      // Every call site is generation-guarded (refreshFor, the notification
      // wrapper, the writes), so a successful apply confirms the state for
      // the CURRENT ready generation - editing and patching gate on this.
      loaded: true,
      // A GET payload carrying loadError (the hub's persisted state failed to
      // load; the fallback defaults are in effect and PATCH rejects) maps onto
      // hubError so the editing gate stays CLOSED with the diagnostic showing,
      // instead of flapping writable-then-failing on every save (roborev PR
      // #884 round 6). Broadcasts and patch responses never carry it, so an
      // ordinary apply clears hubError exactly as before.
      hubError: payload.loadError ?? null,
      loadError: payload.loadError ?? null,
      conflict: null,
      draft,
      draftConflict: staleDraft(draft, payload.revision, fence.generation),
      ...extra,
      ...resolved,
    });
    return true;
  }

  /** Applies a confirmed payload while guaranteeing `settled` publishes even
   * if the reconciler throws (the registry has already rolled back): every
   * settle path clears `saving`/`writeUncertain` the same way whether or not
   * the local apply succeeded, so a wedged registry can never leave the
   * editor disabled. Returns the thrown error, if any, for the caller to
   * re-throw once its own draft-port cleanup has run. */
  function applyHubOverridesSettling(payload: KeybindingsOverrides, settled: Partial<KeybindingsStoreFields>): unknown {
    try {
      applyHubOverrides(payload, settled);
      return null;
    } catch (error) {
      setState({ ...settled, hubError: errorText(error) });
      return error;
    }
  }

  const { beginReadyGeneration, endReadyGeneration } = createSettingsHubGeneration({
    fence,
    wireNotifications: (generation) => {
      missedChangeNotification = false;
      return client.onNotification((notification) => {
        if (!fence.isCurrent(generation)) return;
        onNotification(notification);
      });
    },
    retirePayload,
  });

  function setSupport(support: KeybindingsSupport): void {
    const state = getState();
    if (support === "supported") {
      // A flap BACK from unsupported (finding 24): the pre-flap generation's
      // in-flight work must not survive into the refreshed state - a patch
      // response composed against the pre-flap revision, a queued write
      // fenced at call time under the pre-flap generation, a notification
      // dispatched before the transition. Begin a NEW ready generation on
      // the transition (the epoch bump fences all three paths; the
      // notification rewire drops the old wrapper): isCurrent fails for
      // every token captured pre-flap. The refresh below runs AFTER the bump
      // and fires under the NEW epoch, so the new generation's own refresh is
      // not fenced out by its own bump.
      // Only the TRANSITION bumps: a same-value re-notification (hubSupport
      // already "supported") and the unknown window (a transient disconnect
      // keeps its state and its in-flight work) leave the generation intact.
      // The DOWNWARD flap needs no bump: isSupported() gates every path for
      // the unsupported window itself, and refreshFor's entry guard refuses
      // to run while unsupported.
      if (state.hubSupport === support) return;
      if (state.hubSupport === "unsupported" && fence.generation >= 0) beginReadyGeneration();
      setState({ hubSupport: support });
      // The transition INTO supported with a ready generation active is the
      // load trigger - from unknown (the handshake's features resolving after
      // the client was ready) as much as from unsupported. Before the
      // generation exists the host's first refresh loads.
      if (fence.generation >= 0) void refreshFor(fence.generation);
      return;
    }
    let unrestored = false;
    if (support === "unsupported") {
      // Support resolving to UNSUPPORTED is not the transient-disconnect case
      // (a supported hub that is temporarily unreachable keeps its overrides
      // firing - the ruled behavior): the feature set is KNOWN and does not
      // advertise keybindings, so the settings section claims "the built-in
      // defaults are in effect". The registry must match that claim. Un-apply
      // BEFORE the setState - the character-key reconcile subscribes to the
      // store and must see the final registry shape (see unapplyAll).
      unrestored = !reconciler.unapplyAll();
      unapplyRolledBack = unrestored;
    }
    // The unsupported drop also discards the hub PAYLOAD state: retaining
    // loaded/revision/rawOverrides across a flap would let a later supported
    // reconnect's refresh be eaten by the stale guard (the retained revision
    // can be HIGHER than the returning hub's - a restored backup, a reset
    // state file) and would leave edits composing from the old hub's raw set
    // with the old expectedRevision. EXCEPT when the un-apply rolled back:
    // the registry still fires the overrides, so forgetting the payload would
    // strand the section claiming "defaults in effect" against live user
    // behavior with no way to re-drive reconciliation - retain the hub state
    // and surface a retryable hubError instead (finding 31). The setState
    // stays AFTER the registry mutation, per the reconcile ordering contract.
    // Even on the rollback the REVISION resets (finding 34, aligned with the
    // rewire path's finding 32): revision numbering is hub-scoped, so a
    // retained old revision would let applyHubOverrides' stale guard silently
    // discard a flap-back refresh carrying a LOWER revision (a restored
    // backup, a reset state file), stranding the rollback state indefinitely.
    // The raw set stays retained either way - it is what re-drives the retry.
    // loaded drops too (finding 36, now fully aligned with the rewire path):
    // the retained payload is retained-but-UNCONFIRMED - that is exactly what
    // loaded means. Keeping it true would let a changed-notification landing
    // between the flap-back and its refresh pass onNotification's loaded gate
    // and apply against the pre-flap state, after which the authoritative
    // refresh (revision possibly lower, or equal-but-different) is discarded
    // by the stale guard - the notification's version stays unverified. With
    // loaded false the notification takes the finding-25 dirty-flag path and
    // the flap-back refresh is the confirmation point that flips loaded back.
    const dropHubState =
      support === "unsupported" &&
      !unrestored &&
      (state.loaded || state.revision !== 0 || state.overrides.length > 0 || state.rawOverrides.length > 0);
    // The conflict notice clears with hubError here too (the 2b clear
    // asymmetry): a support drop disconnects the store from the hub state the
    // conflict described, so keeping it would be as stale as keeping hubError.
    // A rolled-back un-apply's message survives the unknown window too
    // (finding 32: a client swap re-runs this with support "unknown" before
    // the new hub's features resolve, and the wedge it describes is still
    // live in the registry).
    const hubError = unapplyRolledBack ? UNAPPLY_ROLLED_BACK_MESSAGE : null;
    if (support === "unsupported") {
      retirePayload({
        hubSupport: support,
        hubError,
        conflict: null,
        ...(dropHubState ? { overrides: [], rawOverrides: [], warnings: [], loadError: null } : {}),
      });
      return;
    }
    // The unknown window is the transient-disconnect case: the payload keeps
    // presenting until endReadyGeneration retires it, so only the connection
    // facts publish, and only when one changed.
    if (state.hubSupport !== support || state.hubLoading || state.hubError !== null || state.conflict !== null)
      setState({ hubSupport: support, hubLoading: false, hubError, conflict: null });
  }

  function onNotification(notification: AnyNotification): void {
    if (notification.method !== "evener/settings/keybindings/changed") return;
    // A late notification landing during an unsupported window must not
    // re-install overrides into a registry the support drop just un-applied.
    if (!isSupported()) return;
    // A notification arriving before the CURRENT generation's refresh has
    // confirmed state carries pre-refresh - potentially pre-flap - cargo
    // (finding 24): its revision predates the flap, and applying it over the
    // reset state would let the stale guard eat the in-flight refresh's
    // older-but-current payload. Drop it; the refresh fetches the truth.
    // Not lost, though (finding 25): mark the generation dirty so the
    // refresh that confirms its state fires ONE follow-up fetch - the
    // in-flight get's response may PREDATE the dropped change, and without
    // the follow-up the store would settle on the older snapshot until the
    // next unrelated refresh.
    if (!getState().loaded) {
      missedChangeNotification = true;
      return;
    }
    const payload = fromWireOverrides(notification.params);
    if (payload === undefined) {
      // The hub says something changed and this build cannot read what: the
      // change is not dropped, the store reads the truth itself. Auto-refresh
      // is what both hosts do here - never a "refresh to inspect" prompt.
      void refreshFor(fence.generation);
      return;
    }
    try {
      applyHubOverrides(payload);
    } catch (error) {
      // Same posture as refreshFor: a reconcile failure surfaces as hubError
      // (the registry has already rolled back to its last good state), never
      // as an exception escaping the client's notification dispatch.
      setState({ hubError: errorText(error) });
    }
  }

  /** What an authoritative read (a GET without loadError) settles for the
   * draft editor: an uncertain write's outcome is now whatever the hub
   * confirmed, so the checkpoint is re-marked and edits unblock. A read that
   * started before the write left, or landed while one is in flight, says
   * nothing about that write and settles nothing. `savingAtReadStart` covers
   * the case the CURRENT `saving`/write-token check misses: a write already
   * in flight when this read began can fail (clearing `saving`, leaving
   * writeUncertain) before this read's reply lands, with no NEWER write ever
   * claiming a token - `fence.writeToken` never moves, so by reply time
   * `saving` already reads false and the token still matches. A read whose
   * own snapshot predates that write's outcome must not settle it. */
  function settledWrite(
    payload: KeybindingsOverrides,
    writeSerialAtStart: number,
    savingAtReadStart: boolean,
  ): Partial<KeybindingsStoreFields> {
    const { draft, writeUncertain, saving } = getState();
    if (payload.loadError !== undefined || writeSerialAtStart !== fence.writeToken || saving || savingAtReadStart)
      return {};
    if (draft !== null && writeUncertain) {
      let replaced: boolean;
      try {
        // A fresh id: settledWrite has no checkpoint reference to reuse one
        // from (only the in-memory draft, which carries no id), the same
        // reason every settle here mints rather than reuses.
        replaced = drafts.replaceClassified({
          id: drafts.createId(),
          baseRevision: draft.revision,
          rules: draft.rules,
          writeUncertain: false,
        });
      } catch {
        return { storageUnavailable: true, draftError: DRAFT_SAVE_FAILED_MESSAGE };
      }
      // The checkpoint this write was settling is gone, replaced by another
      // window's edit while the outcome was unknown: adopt whatever is
      // actually on disk now rather than overwrite it.
      if (!replaced) return restoreDraft({ loaded: true, revision: payload.revision });
    }
    return { writeUncertain: false };
  }

  async function refreshFor(generation: number): Promise<void> {
    if (!fence.liveHub(generation)) return;
    const serial = fence.claimRead();
    const writeSerialAtStart = fence.writeToken;
    const savingAtReadStart = getState().saving;
    // Finding 23: a refresh rejecting after support was lost would otherwise
    // overwrite the support-drop cleanup with a stale "could not load"
    // hubError while the section claims the built-in defaults are in effect.
    const stillMine = () => fence.readStillMine(generation, serial);
    setState({ hubLoading: true, ...(unapplyRolledBack ? {} : { hubError: null }) });
    try {
      const result = await client.request("evener/settings/keybindings/get", {});
      if (!stillMine()) return;
      const payload = fromWireOverrides(result);
      if (payload === undefined) throw new Error(MALFORMED_MESSAGE);
      // hubLoading clears in the same publish whether the payload applies or
      // is ignored (`extra`); settledWrite's checkpoint reclassification
      // (`settle`) runs only once the reconcile has actually succeeded.
      applyHubOverrides(payload, { hubLoading: false }, () =>
        settledWrite(payload, writeSerialAtStart, savingAtReadStart),
      );
      if (missedChangeNotification) {
        // A changed-notification was dropped while this generation had no
        // confirmed state (finding 25) and THIS get's response may predate
        // it. One follow-up fetch converges: the serial guard makes any
        // older in-flight refresh lose, and the flag is cleared FIRST so it
        // cannot loop - a notification arriving after this load applies
        // directly (loaded is now true) instead of re-arming the flag.
        missedChangeNotification = false;
        void refreshFor(generation);
      }
    } catch (error) {
      if (stillMine()) setState({ hubError: errorText(error), hubLoading: false });
    }
  }

  async function refreshOverrides(): Promise<void> {
    // A draft port that failed gets one more restore attempt per refresh. The
    // hub read waits only on a port that could not be READ FROM, so an edit
    // cannot compose against a confirmed payload with the draft unknown. An
    // unreadable RECORD is not that: the draft is simply absent, and holding
    // the section's shortcuts hostage to it would lock a user out of settings
    // they never edited.
    if (getState().storageUnavailable) {
      setState(reloadDraft(getState()));
      if (getState().storageUnavailable && !getState().draftUnreadable) return;
    }
    if (fence.generation < 0) return;
    await refreshFor(fence.generation);
  }

  function detachHub(): boolean {
    // The loaded state belongs to the PREVIOUS hub. Until this client's
    // refresh lands it must not present as current: a patch composed from the
    // old raw set with the old expectedRevision can overwrite the new hub's
    // config on a revision collision, and the registry firing the old hub's
    // overrides is the same staleness one layer down. Un-apply first, THEN
    // setState - the character-key reconcile subscribes to the store and must
    // see the final registry shape (see unapplyAll). hubSupport is
    // connection-sourced, not hub state, so it is left to setSupport.
    const unrestored = !reconciler.unapplyAll();
    unapplyRolledBack = unrestored;
    if (unrestored) {
      // Finding 32 (mirror of finding 31's support-loss rollback): the restore
      // rolled back against a wedged registry, so the OLD hub's overrides are
      // still firing. Retain its confirmed payload (rawOverrides/overrides/
      // warnings) so the display stays truthful and the new hub's incoming
      // applies can reconcile from the intact applied map - but reset revision
      // to 0: revision sequences are per-hub, and carrying the old hub's
      // revision would eat the new hub's lower revisions via applyHubOverrides'
      // stale guard. refreshFor's entry clear skips hubError while the
      // rollback flag is set, so the message survives the refresh the host
      // kicks next; when that refresh lands it either confirms
      // (applyHubOverrides clears hubError) or re-fails the reconcile against
      // the same wedge and surfaces its own hubError.
      retirePayload({ hubError: UNAPPLY_ROLLED_BACK_MESSAGE, conflict: null });
      return false;
    }
    retirePayload({ overrides: [], rawOverrides: [], warnings: [], loadError: null, hubError: null, conflict: null });
    return true;
  }

  function reapplyOverrides(): void {
    reconciler.prepareReapply();
    try {
      setState(reconciler.apply(getState().rawOverrides));
    } catch (error) {
      // Same posture as the changed-notification path: a reconcile failure
      // surfaces as hubError (the registry has already rolled back to its last
      // good state), never as an exception escaping the pref dispatch.
      setState({ hubError: errorText(error) });
    }
  }

  function patchOverrides(
    rulesOrCompose: readonly KeybindingsRule[] | (() => readonly KeybindingsRule[]),
  ): Promise<KeybindingsOverrides> {
    // Fence the write to the ready generation it was CREATED under. A queued
    // write executes only after the previous write settles, which can be
    // after a rewire to a NEW hub: the thunk's compose-at-execution is right
    // WITHIN one generation (finding 16), but a write whose generation has
    // ended carries edit intent made against the hub whose state the user
    // was looking at - landing it on the new hub's config is the wrong
    // default even though the payload would compose cleanly there. It
    // rejects with the same unavailable-class error instead, before any
    // wire request.
    const callGeneration = fence.generation;
    // Reserved HERE, synchronously - see the field's own comment for why a
    // same-turn saveDraft() needs this to be visible before `run` executes.
    const reservation: { generation: number; token: number | null } = {
      generation: callGeneration,
      token: null,
    };
    directWrite = reservation;
    const run = async (): Promise<KeybindingsOverrides> => {
      try {
        // Compose at EXECUTION time: a thunk reads the raw set as the
        // previous write left it, folding its confirmed payload into this
        // write's rules instead of racing it.
        const rules = typeof rulesOrCompose === "function" ? rulesOrCompose() : rulesOrCompose;
        const state = getState();
        // The call-time fence is checked SEPARATELY from the current-state
        // guard below (finding 22): this write was created under a ready
        // generation that has since ended, so the rejection belongs to a DEAD
        // generation. Setting hubError here would land on the LIVING hub's
        // freshly-loaded clean state - "Hub keybindings settings are
        // unavailable" plus the round-3 editing gate would read the new hub
        // read-only until something cleared it. Throw only.
        if (!fence.liveHub(callGeneration)) throw new Error(UNAVAILABLE_MESSAGE);
        // Support resolved to UNSUPPORTED is the same hygiene class as the
        // fence (finding 26): the unsupported state is deliberately clean -
        // the section says the built-in defaults are in effect - and the
        // write no longer owns it. A plain unsupported transition does not
        // bump the generation (finding 24: isSupported() gates the window),
        // so a write QUEUED while supported can reach this point, as can one
        // composed while unsupported. Both throw without hubError.
        if (state.hubSupport === "unsupported") throw new Error(UNAVAILABLE_MESSAGE);
        if (
          state.hubSupport !== "supported" ||
          state.loaded !== true ||
          state.hubLoading ||
          state.saving ||
          state.writeUncertain
        ) {
          // `loaded` is the defense-in-depth half of the editor's gate: the UI
          // is not the store's contract, and a patch composed from a STALE
          // generation's raw set (client replaced, refresh not yet landed) would
          // send the old hub's expectedRevision and rules to the new hub.
          // `hubLoading` is the same race WITHIN one generation: an in-flight
          // refresh is about to land a payload whose revision may differ from
          // the one a concurrent PATCH would send as expectedRevision.
          // `saving`/`writeUncertain` are the checkpointed editor's sibling
          // gate: both paths take the same write token, so starting here would
          // fence the checkpointed write's own reply out and strand it saving.
          setState({ hubError: UNAVAILABLE_MESSAGE });
          throw new Error(UNAVAILABLE_MESSAGE);
        }
        // Reject with the validation layer's own message and leave
        // hubError/conflict untouched: nothing hub-sourced happened.
        const introduced = reconciler.introducedWarnings(state.rawOverrides, rules);
        if (introduced.length > 0) throw new Error(introduced.map((warning) => warning.message).join("\n"));
        const token = fence.claimWrite();
        // A response landing after support loss must not re-apply - the
        // unsupported branch already un-applied and retired the hub state.
        const stillMine = () => fence.writeStillMine(callGeneration, token);
        // Promotes the reservation from pending to claimed: saveDraft's gate
        // switches from the generation-liveness check to the precise
        // fence.writeStillMine check the moment this happens.
        reservation.token = token;
        try {
          const result = await client.request("evener/settings/keybindings/patch", {
            expectedRevision: state.revision,
            config: { version: 1, rules: cloneRules(rules) },
          });
          if (!stillMine()) {
            const current = getState();
            return { version: 1, revision: current.revision, rules: [...current.rawOverrides] };
          }
          const payload = fromWireOverrides(result);
          if (payload === undefined) throw new Error("Hub returned malformed keybindings PATCH response");
          applyHubOverrides(payload);
          return payload;
        } catch (error) {
          if (stillMine()) {
            // Post-rename durable failure: the patch APPLIED on the hub (the
            // error carries the canonical applied state, and the broadcast
            // reconciles every client). Apply locally and report success -
            // surfacing hubError here would disable editing over bindings that
            // are already live.
            const applied = rejectionPayload(error, "keybindingsPostRename", "applied");
            if (applied !== undefined) {
              applyHubOverrides(applied);
              return applied;
            }
            // A lost revision race: the rejection carries the server's current
            // state, so refresh to it and surface the conflict.
            const current = rejectionPayload(error, "conflict", "current");
            if (current !== undefined) applyHubOverrides(current);
            const message = errorText(error);
            setState({ hubError: message, ...(current === undefined ? {} : { conflict: message }) });
          }
          throw error;
        }
      } finally {
        // Only this call's own reservation: a stale call's finally must not
        // clear a NEWER call's still-pending reservation (see the field's
        // own comment) - compared by object identity, since a queued call's
        // reservation has no token yet to compare by value.
        if (directWrite === reservation) directWrite = null;
      }
    };
    // Chain behind the previous write's SETTLEMENT: a failed write must not
    // block the queue, and the next write composes against whatever state
    // the failure left (the conflict path already refreshed it).
    const result = writeQueue.then(run, run);
    writeQueue = result.then(
      () => undefined,
      () => undefined,
    );
    return result;
  }

  /** The draft editor's gate: a confirmed, supported, idle hub state with a
   * usable draft port. Returns the confirmed payload the edit composes
   * against. */
  function assertEditable(): { revision: number; rules: readonly KeybindingsRule[] } {
    const state = getState();
    if (
      fence.disposed ||
      state.saving ||
      state.storageUnavailable ||
      state.writeUncertain ||
      state.hubSupport !== "supported" ||
      !state.loaded ||
      state.loadError !== null
    )
      throw new Error(UNAVAILABLE_MESSAGE);
    return { revision: state.revision, rules: state.rawOverrides };
  }

  /** discardDraft's own gate - see assertDraftDiscardable. */
  function assertDiscardable(): void {
    assertDraftDiscardable(fence, getState, UNAVAILABLE_MESSAGE);
  }

  /** Persists a freshly composed checkpoint - see persistCheckpointedDraft. */
  function persistDraft(input: Omit<KeybindingDraftCheckpoint, "id">): KeybindingDraftCheckpoint {
    return persistCheckpointedDraft(
      drafts,
      input,
      getState,
      setState,
      restoreDraft,
      DRAFT_SAVE_FAILED_MESSAGE,
      DRAFT_REVIEW_AGAIN_MESSAGE,
    );
  }

  function editDraft(rules: readonly KeybindingsRule[]): void {
    const current = assertEditable();
    const checked = keybindingRules(rules);
    const existing = getState().draft;
    const revision = existing?.revision ?? current.revision;
    persistDraft({ baseRevision: revision, rules: checked, writeUncertain: false });
    const generation = existing !== null ? existing.generation : currentGeneration();
    const draft: KeybindingsDraft = { version: 1, revision, rules: checked, generation };
    setState({ draft, draftConflict: staleDraft(draft, current.revision, fence.generation), draftError: null });
  }

  /** Settles a confirmed write against `checkpoint`. `payload` applies
   * (never letting a reconciler throw skip the settle - see
   * applyHubOverridesSettling) unless it is null: a newer external revision
   * landed while this write was out, and applying this write's own now-stale
   * confirmation over it would overwrite what is already current - only
   * `settled` publishes. `cleanup` decides the checkpoint's fate once the
   * outcome is settled: "remove" clears it; "remark" re-marks it settled in
   * place (replaceClassified, `checkpoint`'s own id - the stored id stops
   * rotating on a remark, which nothing reads) because the proposal stays
   * for review - the usual companion to a null payload, but not always the
   * same call, since a lost revision race
   * applies the server's current state AND keeps the proposal for review.
   * Either way, a refusal (another writer replaced the SAME checkpoint
   * while this write was out) adopts whatever restoreDraft finds on disk
   * instead of overwriting it. `settled` is the caller's own publish
   * alongside the apply. Returns the reconciler's own throw, if any, for
   * the caller to re-throw once this has run. */
  function settleWrite(
    payload: KeybindingsOverrides | null,
    checkpoint: KeybindingDraftCheckpoint,
    cleanup: "remove" | "remark",
    settled: Partial<KeybindingsStoreFields>,
  ): unknown {
    let applyFailure: unknown = null;
    if (payload === null) setState(settled);
    else applyFailure = applyHubOverridesSettling(payload, settled);
    let storageError: string | null = null;
    let refused: Partial<KeybindingsStoreFields> | null = null;
    try {
      if (cleanup === "remark") {
        const replaced = drafts.replaceClassified({ ...checkpoint, writeUncertain: false });
        if (!replaced) refused = restoreDraft(getState());
      } else if (!drafts.removeIf(checkpoint)) {
        refused = restoreDraft(getState());
      }
    } catch {
      storageError = DRAFT_CLEANUP_FAILED_MESSAGE;
    }
    setState(
      refused ?? {
        draft: cleanup === "remark" || storageError !== null ? getState().draft : null,
        storageUnavailable: storageError !== null,
        draftError: storageError,
      },
    );
    return applyFailure;
  }

  async function saveDraft(rules?: readonly KeybindingsRule[]): Promise<KeybindingsOverrides> {
    const current = assertEditable();
    // Both write paths share one write token (patchOverrides' sibling gate
    // on saving/writeUncertain is the reverse of this): claiming a NEWER
    // token here while a direct write is in flight (or merely QUEUED - see
    // the field's own comment) would fence that write's own reply out as
    // superseded, racing or conflicting with whichever PATCH the hub
    // actually processes first.
    // A direct write's reservation stops blocking here the moment the fence
    // would no longer count it as live - a generation end, a support flap or
    // a later write's supersede all retire it without waiting for the
    // original request's own await to settle. A still-queued reservation
    // (no token claimed yet) has nothing to supersede it with, so liveness
    // alone decides; a claimed one defers to the precise per-token check.
    if (
      directWrite !== null &&
      (directWrite.token === null
        ? fence.liveHub(directWrite.generation)
        : fence.writeStillMine(directWrite.generation, directWrite.token))
    )
      throw new Error(UNAVAILABLE_MESSAGE);
    const existing = getState().draft;
    if (getState().draftConflict) throw new Error("Review the current shortcuts before saving your changes.");
    const checked = keybindingRules(rules ?? existing?.rules ?? current.rules);
    const revision = existing?.revision ?? current.revision;
    // The durable intent must exist before the request can leave the device.
    const checkpoint = persistDraft({ baseRevision: revision, rules: checked, writeUncertain: true });
    const token = fence.claimWrite();
    const generation = fence.generation;
    const stillMine = () => fence.writeStillMine(generation, token);
    const draftGeneration = existing !== null ? existing.generation : currentGeneration();
    setState({
      saving: true,
      draft: { version: 1, revision, rules: checked, generation: draftGeneration },
      draftError: null,
    });
    let result: unknown;
    try {
      // The saving publish above may have disposed the store or retired the
      // payload (a host tearing down on the transition): the checkpoint stays
      // for the next instance to restore, and nothing leaves.
      if (!stillMine()) throw new Error("Shortcut save was cancelled.");
      result = await client.request("evener/settings/keybindings/patch", {
        expectedRevision: revision,
        config: { version: 1, rules: checked },
      });
    } catch (error) {
      if (!stillMine()) {
        settleLostHubWrite(generation, token);
        throw error;
      }
      // Post-rename durable failure: the patch APPLIED on the hub (the error
      // carries the canonical applied state, and the broadcast reconciles
      // every client) - the same known-outcome rule patchOverrides follows.
      // Apply locally and report success: surfacing writeUncertain here
      // would leave editing disabled over bindings that are already live.
      const applied = rejectionPayload(error, "keybindingsPostRename", "applied");
      if (applied !== undefined) {
        // Same posture as the confirmed-reply path below: draftConflict is
        // forced from newerExternal rather than left to applyHubOverrides'
        // own staleDraft check, which reads state.draft BEFORE settleWrite
        // clears it and would misread this write's own confirmed revision
        // (still the pre-write draft's revision at that point) as a conflict.
        // A newer external revision landing while this write was out keeps
        // the proposal for review instead of reporting it applied.
        const newerExternal = getState().revision > applied.revision;
        const applyFailure = settleWrite(
          newerExternal ? null : applied,
          checkpoint,
          newerExternal ? "remark" : "remove",
          {
            saving: false,
            writeUncertain: false,
            draftConflict: newerExternal,
          },
        );
        if (applyFailure !== null) throw applyFailure;
        return applied;
      }
      // A lost revision race: the rejection carries the server's current
      // state, so this is not a lost reply - the outcome is known - and the
      // checkpoint is re-marked settled rather than left claiming an unknown
      // one, the same rule the direct write follows.
      const conflictState = rejectionPayload(error, "conflict", "current");
      if (conflictState !== undefined) {
        const applyFailure = settleWrite(conflictState, checkpoint, "remark", {
          saving: false,
          writeUncertain: false,
          draftConflict: true,
        });
        throw applyFailure ?? error;
      }
      // No reply: the write's outcome is unknown, and that fact is the state
      // (writeUncertain) rather than a message. The checkpoint already says so.
      setState({ saving: false, draftConflict: true, writeUncertain: true });
      throw error;
    }
    // The reply is back. What follows is ONE ordered sequence with no side
    // effect ahead of the fence, because every earlier shape of it left a hole:
    // (1) FENCE. If this reply is no longer ours (the generation ended, support
    //     dropped, a later write left, the store was disposed), retirePayload
    //     has already published writeUncertain and the checkpoint on the port
    //     is the only durable record of that uncertainty - so nothing here may
    //     touch state, registry or storage. Return the current payload, as
    //     patchOverrides does for a fenced reply. Same order as patchOverrides:
    //     a reply that is not ours is discarded without interpretation.
    // (2) DECODE. A malformed reply is hub-sourced: hubError, like
    //     patchOverrides; the outcome stays unknown (writeUncertain), the
    //     checkpoint stays.
    // (3) APPLY, settling the in-flight flags on EVERY path. The hub confirmed
    //     the write, so saving ends and writeUncertain clears whether or not the
    //     registry can take the rules: a reconciler throw (a wedged registry;
    //     it has already rolled back) rides hubError and rejects this call the
    //     way patchOverrides' does, but never leaves saving true behind. A
    //     newer external revision that landed meanwhile keeps the proposal for
    //     review instead of reporting it applied - the one place the
    //     comparison is `>`, not "differs": an equal revision is this write's
    //     own broadcast arriving ahead of its reply.
    // (4) STORAGE LAST. Only once the outcome is in state does the checkpoint
    //     change: released on a confirmed write, re-marked settled when the
    //     proposal stays for review. A cleanup failure keeps the draft in view
    //     with the port marked unavailable, and never turns a confirmed write
    //     back into an unknown outcome.
    if (!stillMine()) {
      settleLostHubWrite(generation, token);
      const state = getState();
      return { version: 1, revision: state.revision, rules: [...state.rawOverrides] };
    }
    const value = fromWireOverrides(result);
    if (value === undefined || value.loadError !== undefined || value.revision < revision) {
      setState({ saving: false, draftConflict: true, writeUncertain: true, hubError: MALFORMED_MESSAGE });
      throw new Error(MALFORMED_MESSAGE);
    }
    const newerExternal = getState().revision > value.revision;
    const applyFailure = settleWrite(newerExternal ? null : value, checkpoint, newerExternal ? "remark" : "remove", {
      saving: false,
      writeUncertain: false,
      draftConflict: newerExternal,
    });
    if (applyFailure !== null) throw applyFailure;
    return value;
  }

  function discardDraft(): void {
    assertDiscardable();
    discardCheckpointedDraft(drafts, getState, setState, restoreDraft, DRAFT_DISCARD_FAILED_MESSAGE);
  }

  function rebaseDraft(reviewedRevision: number): void {
    const current = assertEditable();
    const { draft, hubLoading } = getState();
    if (draft === null || hubLoading || current.revision !== reviewedRevision)
      throw new Error(DRAFT_REVIEW_AGAIN_MESSAGE);
    persistDraft({ baseRevision: current.revision, rules: draft.rules, writeUncertain: false });
    setState({
      draft: { ...draft, revision: current.revision, generation: currentGeneration() },
      draftConflict: false,
      draftError: null,
    });
  }

  return {
    ...store,
    setSupport,
    beginReadyGeneration,
    endReadyGeneration,
    detachHub,
    reapplyOverrides,
    reset() {
      endReadyGeneration();
      missedChangeNotification = false;
      unapplyRolledBack = false;
      writeQueue = Promise.resolve();
      directWrite = null;
      // Restore defaults for every applied override so the registry cannot
      // leak overrides into the next test (the next test rebuilds the
      // registry from scratch, which removes any binding a wedged restore left).
      reconciler.unapplyAll();
      setState({ ...initialState() });
    },
    dispose() {
      if (fence.disposed) return;
      endReadyGeneration();
      fence.dispose();
    },
  };
}
