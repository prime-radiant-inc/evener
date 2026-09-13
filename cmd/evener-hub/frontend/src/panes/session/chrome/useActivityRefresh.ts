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
  const backgroundEstablished = owner.kind === "background" ? summary.established : undefined;
  const backgroundLastFetchedBump = owner.kind === "background" ? summary.lastFetchedBump : undefined;

  useEffect(() => {
    currentRef.current = ref;
    if (owner.kind !== "body") return;
    bodyGenerationRef.current += 1;
    bodyMountedRef.current = true;
    return () => {
      bodyMountedRef.current = false;
    };
  }, [owner.kind, ref]);

  // The body samples retained load and summary freshness only when its old
  // mount/model/hydration inputs run the effect. Request or continuation
  // completion must not turn a retained failure into an automatic root retry.
  // biome-ignore lint/correctness/useExhaustiveDependencies: entry.load and summary.lastFetchedBump are completion state; body refreshes are driven only by mount/model/hydration inputs
  useEffect(() => {
    if (owner.kind !== "body") return;
    const bumpMismatch = summary.lastFetchedBump !== model.jobsUpdatedAt;
    const retainedNonReady =
      entry.load.kind === "idle" ||
      entry.load.kind === "failed" ||
      entry.load.kind === "unsupported" ||
      entry.load.kind === "ended";
    const unprovenFreshness = model.jobsUpdatedAt === null;
    if (!(bumpMismatch || retainedNonReady || unprovenFreshness)) return;

    // refreshRoot queues forced calls refused by its in-flight gate. Sampling
    // the live store immediately before dispatch prevents co-mounted owners
    // from manufacturing a duplicate forced follow-up without making request
    // completion an effect dependency.
    const force = retainedNonReady || unprovenFreshness;
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
  }, [hydrationGeneration, model.jobsUpdatedAt, owner.kind, ref, reportFailure]);

  // Background owners observe establishment and bump changes, but retained
  // load completion remains sampled so it cannot create a retry loop.
  // biome-ignore lint/correctness/useExhaustiveDependencies: entry.load is sampled only to choose force during initial discovery
  useEffect(() => {
    if (owner.kind !== "background") return;
    if (bodyOwnsFreshness) {
      // A mounted body owns freshness, so a generation it sees must not be
      // queued for the background owner after that body unmounts.
      handledGenerationRef.current = hydrationGeneration;
      return;
    }
    if (suppressed) return;
    if (!backgroundEstablished && !discoverUnestablished) return;

    const generationChanged = hydrationGeneration !== handledGenerationRef.current;
    if (backgroundEstablished && !generationChanged && backgroundLastFetchedBump === model.jobsUpdatedAt) {
      return;
    }

    const bumpMismatch = backgroundLastFetchedBump !== model.jobsUpdatedAt;
    const retainedNonReady =
      entry.load.kind === "idle" ||
      entry.load.kind === "failed" ||
      entry.load.kind === "unsupported" ||
      entry.load.kind === "ended";
    const unprovenFreshness = model.jobsUpdatedAt === null;
    const initialDiscovery = !backgroundEstablished;
    if (!(initialDiscovery || generationChanged || bumpMismatch)) return;

    const force = generationChanged || (initialDiscovery && (retainedNonReady || unprovenFreshness));
    handledGenerationRef.current = hydrationGeneration;
    if (force && activitySummaryStore.getState().entries.get(ref)?.loading) return;
    refreshActivityRoot(ref, model.jobsUpdatedAt, undefined, force);
  }, [
    backgroundEstablished,
    backgroundLastFetchedBump,
    bodyOwnsFreshness,
    discoverUnestablished,
    hydrationGeneration,
    model.jobsUpdatedAt,
    owner.kind,
    ref,
    suppressed,
  ]);
}
