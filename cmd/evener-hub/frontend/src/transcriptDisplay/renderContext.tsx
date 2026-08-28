import { createContext, type ReactNode, useContext, useLayoutEffect, useMemo, useRef } from "react";
import type { ThreadModel } from "../protocol/model";
import { beginDisclosureBaseline } from "../widgets/disclosure/disclosureStore";
import {
  type ContentVector,
  configFingerprint,
  makeTranscriptDisplayConfig,
  normalizeConfig,
  presetContent,
  type TranscriptDisplayConfigV1,
} from "./config";
import type { TranscriptMetadataVisibility, TranscriptProjection } from "./projector";

export type TranscriptSurface = "live" | "readOnly" | "preview";

export interface TranscriptRenderContextValue {
  readonly config: TranscriptDisplayConfigV1;
  /** Advanced metadata surviving the projector's visibility pass. */
  readonly metadata: TranscriptMetadataVisibility;
  /** Descriptive alias for consumers that name the value by its producer. */
  readonly projectedMetadata: TranscriptMetadataVisibility;
  readonly surface: TranscriptSurface;
  readonly disclosureScope: string;
  readonly eligibleDisclosureIds: readonly string[];
  /** Incremented by a caller that wants the same Full view to start anew. */
  readonly fullBaselineGeneration: number;
  /** Snapshot supplied by the owning transcript; never read from threadsStore. */
  readonly thread?: ThreadModel;
}

export interface TranscriptRenderContextInput {
  config: TranscriptDisplayConfigV1;
  metadata?: TranscriptMetadataVisibility;
  projectedMetadata?: TranscriptMetadataVisibility;
  projection?: Pick<TranscriptProjection, "metadata" | "eligibleDisclosureIds">;
  surface?: TranscriptSurface;
  disclosureScope?: string;
  sessionRef?: string;
  eligibleDisclosureIds?: readonly string[];
  fullBaselineGeneration?: number;
  thread?: ThreadModel;
}

export function contentVectorForConfig(config: TranscriptDisplayConfigV1): ContentVector {
  return config.content.kind === "preset" ? presetContent(config.content.level) : config.content;
}

export function expandDetailsByDefault(config: TranscriptDisplayConfigV1): boolean {
  return contentVectorForConfig(config).expandByDefault;
}

/** Keep direct leaf renders session-isolated while a caller is still using the
 * transition default. Shared TranscriptBody callers provide their own scope. */
export function disclosureScopeForSession(
  context: TranscriptRenderContextValue,
  sessionRef: string | undefined,
): string {
  if (sessionRef === undefined || context.disclosureScope !== defaultDisclosureScope(context.surface))
    return context.disclosureScope;
  return defaultDisclosureScope(context.surface, sessionRef);
}

export function defaultDisclosureScope(surface: TranscriptSurface, sessionRef?: string): string {
  return `transcript:${surface}:${sessionRef ?? "default"}`;
}

const DEFAULT_CONFIG = makeTranscriptDisplayConfig(
  { kind: "preset", level: "activity" },
  { roundTimings: true, systemEvents: true, promptEvents: true },
);

const DEFAULT_METADATA: TranscriptMetadataVisibility = { ...DEFAULT_CONFIG.advanced };

const DEFAULT_CONTEXT: TranscriptRenderContextValue = {
  config: DEFAULT_CONFIG,
  metadata: DEFAULT_METADATA,
  projectedMetadata: DEFAULT_METADATA,
  surface: "readOnly",
  disclosureScope: defaultDisclosureScope("readOnly"),
  eligibleDisclosureIds: [],
  fullBaselineGeneration: 0,
  thread: undefined,
};

const TranscriptRenderContext = createContext<TranscriptRenderContextValue | null>(null);

function metadataFor(
  config: TranscriptDisplayConfigV1,
  metadata: TranscriptMetadataVisibility | undefined,
): TranscriptMetadataVisibility {
  return metadata === undefined ? { ...config.advanced } : { ...metadata };
}

function metadataFingerprint(metadata: TranscriptMetadataVisibility): string {
  return [
    metadata.roundTimings,
    metadata.tokenCounts,
    metadata.estimatedCost,
    metadata.systemEvents,
    metadata.promptEvents,
    metadata.hookExits,
  ]
    .map(String)
    .join("\0");
}

function eligibleDisclosureFingerprint(ids: readonly string[]): string {
  return ids.map((id) => `${id.length}:${id}`).join("\0");
}

export function createTranscriptRenderContext(input: TranscriptRenderContextInput): TranscriptRenderContextValue {
  const config = normalizeConfig(input.config);
  const metadata = metadataFor(config, input.metadata ?? input.projectedMetadata ?? input.projection?.metadata);
  const eligibleDisclosureIds = [...(input.eligibleDisclosureIds ?? input.projection?.eligibleDisclosureIds ?? [])];
  return {
    config,
    metadata,
    projectedMetadata: metadata,
    surface: input.surface ?? "readOnly",
    disclosureScope: input.disclosureScope ?? defaultDisclosureScope(input.surface ?? "readOnly", input.sessionRef),
    eligibleDisclosureIds,
    fullBaselineGeneration: input.fullBaselineGeneration ?? 0,
    thread: input.thread,
  };
}

export interface TranscriptRenderProviderProps extends Partial<Omit<TranscriptRenderContextInput, "config">> {
  children: ReactNode;
  config?: TranscriptDisplayConfigV1;
  value?: TranscriptRenderContextValue;
}

function isFullConfig(config: TranscriptDisplayConfigV1): boolean {
  return config.content.kind === "preset" && config.content.level === "full";
}

/**
 * Supplies the common display state to every transcript renderer. The layout
 * provider owns the only baseline side effect: ordinary config rerenders keep
 * explicit choices intact, while entering Full (or receiving a new generation)
 * starts one open baseline for the eligible source ids.
 */
export function TranscriptRenderProvider({
  children,
  value,
  config,
  metadata,
  projectedMetadata,
  projection,
  surface,
  disclosureScope,
  sessionRef,
  eligibleDisclosureIds,
  fullBaselineGeneration,
  thread,
}: TranscriptRenderProviderProps) {
  const semanticConfig = normalizeConfig(config ?? DEFAULT_CONFIG);
  const semanticMetadata = metadataFor(semanticConfig, metadata ?? projectedMetadata ?? projection?.metadata);
  const semanticEligibleDisclosureIds = [...(eligibleDisclosureIds ?? projection?.eligibleDisclosureIds ?? [])];
  const semanticSurface = surface ?? "readOnly";
  const semanticScope = disclosureScope ?? defaultDisclosureScope(semanticSurface, sessionRef);
  const semanticGeneration = fullBaselineGeneration ?? 0;
  const semanticKey = [
    configFingerprint(semanticConfig),
    metadataFingerprint(semanticMetadata),
    eligibleDisclosureFingerprint(semanticEligibleDisclosureIds),
    semanticSurface,
    semanticScope,
    semanticGeneration,
  ].join("\0");
  const semanticContextRef = useRef<
    { key: string; thread: ThreadModel | undefined; context: TranscriptRenderContextValue } | undefined
  >(undefined);
  // biome-ignore lint/correctness/useExhaustiveDependencies: semanticKey covers semantic display values; thread identity refreshes snapshot-derived summaries
  const context = useMemo(() => {
    if (value !== undefined) return value;
    const previous = semanticContextRef.current;
    if (previous?.key === semanticKey && previous.thread === thread) return previous.context;
    const next = createTranscriptRenderContext({
      config: semanticConfig,
      metadata: semanticMetadata,
      eligibleDisclosureIds: semanticEligibleDisclosureIds,
      surface: semanticSurface,
      disclosureScope: semanticScope,
      fullBaselineGeneration: semanticGeneration,
      thread,
    });
    semanticContextRef.current = { key: semanticKey, thread, context: next };
    return next;
  }, [semanticKey, thread, value]);
  const full = isFullConfig(context.config);
  const previous = useRef<{ scope: string; full: boolean; generation: number; eligible: string } | undefined>(
    undefined,
  );
  const eligibleFingerprint = eligibleDisclosureFingerprint(context.eligibleDisclosureIds);

  useLayoutEffect(() => {
    const prior = previous.current;
    const sameScope = prior?.scope === context.disclosureScope;
    const enteringFull =
      full && (!sameScope || prior?.full !== true || prior.generation !== context.fullBaselineGeneration);
    if (enteringFull) {
      // A generation change while already Full is an explicit new baseline.
      // Force the store through a closed boundary without touching choices in
      // unrelated scopes, then establish the open baseline for this scope.
      if (sameScope && prior?.full === true && prior.generation !== context.fullBaselineGeneration)
        beginDisclosureBaseline(context.disclosureScope, [], false);
      beginDisclosureBaseline(context.disclosureScope, context.eligibleDisclosureIds, true);
    } else if (
      full &&
      sameScope &&
      prior?.full === true &&
      prior.generation === context.fullBaselineGeneration &&
      prior.eligible !== eligibleFingerprint
    ) {
      // Keep the active Full baseline's inventory current. The disclosure
      // store removes only stale pre-baseline closes; choices made after the
      // baseline remain authoritative.
      beginDisclosureBaseline(context.disclosureScope, context.eligibleDisclosureIds, true);
    } else if (!full && (!sameScope || prior?.full === true || prior?.generation !== context.fullBaselineGeneration)) {
      beginDisclosureBaseline(context.disclosureScope, context.eligibleDisclosureIds, false);
    }
    previous.current = {
      scope: context.disclosureScope,
      full,
      generation: context.fullBaselineGeneration,
      eligible: eligibleFingerprint,
    };
  }, [context, context.disclosureScope, context.fullBaselineGeneration, eligibleFingerprint, full]);

  return <TranscriptRenderContext.Provider value={context}>{children}</TranscriptRenderContext.Provider>;
}

/** Test/transition hook: returns null when a caller has not supplied a provider. */
export function useOptionalTranscriptRenderContext(): TranscriptRenderContextValue | null {
  return useContext(TranscriptRenderContext);
}

/** Renderer hook with a stable compatibility default for direct leaf tests. */
export function useTranscriptRenderContext(): TranscriptRenderContextValue {
  return useOptionalTranscriptRenderContext() ?? DEFAULT_CONTEXT;
}
