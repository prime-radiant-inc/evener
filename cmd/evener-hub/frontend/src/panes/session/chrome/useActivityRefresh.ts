import { useEffect, useRef } from "react";
import type { ThreadModel } from "../../../protocol/model";
import { EMPTY_ACTIVITY_PANEL_ENTRY, useActivityPanelStore } from "../../../stores/activityPanel";
import {
  activitySummaryStore,
  EMPTY_ACTIVITY_SUMMARY_ENTRY,
  useActivitySummaryStore,
} from "../../../stores/activitySummary";
import { threadsStore, useThreadsStore } from "../../../stores/threads";

interface BodyRefreshOwner {
  kind: "body";
  onFailure?: (sentence: string) => void;
}

interface BackgroundRefreshOwner {
  kind: "background";
  bodyOwnsFreshness: boolean;
  suppressed: boolean;
  discoverUnestablished: boolean;
}

export type ActivityRefreshOwner = BodyRefreshOwner | BackgroundRefreshOwner;

export function refreshActivityRoot(
  ref: string,
  bump: number | null,
  onFailure?: (sentence: string) => void,
  force = false,
): number | null {
  return activitySummaryStore
    .getState()
    .refreshRoot(ref, bump, (sessionRef) => threadsStore.getState().listJobs(sessionRef), onFailure, force);
}

/** Owns root-activity freshness for a mounted body or its background chrome owner. */
export function useActivityRefresh(
  ref: string,
  model: ThreadModel,
  owner: ActivityRefreshOwner = { kind: "body" },
): void {
  const entry = useActivityPanelStore((state) => state.entries.get(ref)) ?? EMPTY_ACTIVITY_PANEL_ENTRY;
  const summary = useActivitySummaryStore((state) => state.entries.get(ref)) ?? EMPTY_ACTIVITY_SUMMARY_ENTRY;
  // Full-snapshot publication is the only freshness signal when the jobs bump
  // is null both before and after a reconnect or targeted resync.
  const hydrationGeneration = useThreadsStore((state) => state.hydrations.get(ref) ?? 0);
  const handledGenerationRef = useRef(hydrationGeneration);
  const bodyMountedRef = useRef(false);
  const bodyGenerationRef = useRef(0);
  const currentRef = useRef(ref);
  currentRef.current = ref;
  const bodyOwnsFreshness = owner.kind === "background" && owner.bodyOwnsFreshness;
  const discoverUnestablished = owner.kind === "background" && owner.discoverUnestablished;
  const suppressed = owner.kind === "background" && owner.suppressed;
  const reportFailure = owner.kind === "body" ? owner.onFailure : undefined;

  useEffect(() => {
    currentRef.current = ref;
    if (owner.kind !== "body") return;
    bodyGenerationRef.current += 1;
    bodyMountedRef.current = true;
    return () => {
      bodyMountedRef.current = false;
    };
  }, [owner.kind, ref]);

  // The load and loading states are deliberately sampled, not dependencies.
  // Request completion must not turn a retained failure into a retry loop.
  // biome-ignore lint/correctness/useExhaustiveDependencies: entry.load and summary.loading are completion state; owner fields below are the actual effect inputs
  useEffect(() => {
    if (owner.kind === "background") {
      if (bodyOwnsFreshness) {
        // A mounted body owns freshness, so a generation it sees must not be
        // queued for the background owner after that body unmounts.
        handledGenerationRef.current = hydrationGeneration;
        return;
      }
      if (suppressed) return;
      if (!summary.established && !discoverUnestablished) return;
    }

    const generationChanged = hydrationGeneration !== handledGenerationRef.current;
    if (
      owner.kind === "background" &&
      summary.established &&
      !generationChanged &&
      summary.lastFetchedBump === model.jobsUpdatedAt
    ) {
      return;
    }

    const bumpMismatch = summary.lastFetchedBump !== model.jobsUpdatedAt;
    const retainedNonReady =
      entry.load.kind === "idle" ||
      entry.load.kind === "failed" ||
      entry.load.kind === "unsupported" ||
      entry.load.kind === "ended";
    // A null bump cannot prove retained data is current. A body also retries
    // retained non-ready states; an unestablished background owner uses those
    // same complete initial-discovery conditions.
    const unprovenFreshness = model.jobsUpdatedAt === null;
    const initialDiscovery = owner.kind === "background" && !summary.established;
    const shouldRefresh =
      owner.kind === "body"
        ? bumpMismatch || retainedNonReady || unprovenFreshness
        : initialDiscovery || generationChanged || bumpMismatch;
    if (!shouldRefresh) return;

    const force =
      owner.kind === "body"
        ? retainedNonReady || unprovenFreshness
        : generationChanged || (initialDiscovery && (retainedNonReady || unprovenFreshness));
    handledGenerationRef.current = hydrationGeneration;

    // refreshRoot queues forced calls refused by its in-flight gate. Sampling
    // the live store immediately before dispatch prevents co-mounted owners
    // from manufacturing a duplicate forced follow-up without making request
    // completion an effect dependency.
    if (force && activitySummaryStore.getState().entries.get(ref)?.loading) return;
    const requestGeneration = bodyGenerationRef.current;
    const onFailure = reportFailure
      ? (sentence: string) => {
          if (bodyMountedRef.current && currentRef.current === ref && bodyGenerationRef.current === requestGeneration) {
            reportFailure(sentence);
          }
        }
      : undefined;
    refreshActivityRoot(ref, model.jobsUpdatedAt, onFailure, force);
  }, [
    bodyOwnsFreshness,
    discoverUnestablished,
    hydrationGeneration,
    model.jobsUpdatedAt,
    owner.kind,
    ref,
    reportFailure,
    summary.established,
    summary.lastFetchedBump,
    suppressed,
  ]);
}
