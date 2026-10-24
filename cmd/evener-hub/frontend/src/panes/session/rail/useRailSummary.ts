// The rail summary hook owns the rail's job projection by thread ref: one
// cache entry per ref, refreshed only by that ref's live job signals, with
// ended sessions held at their terminal revision. It never polls, never lets
// two requests overlap, and keeps a cached turn summary when only the job lane
// fails.
import { useCallback, useEffect, useRef, useState } from "react";
import {
  type RailJobHistory,
  type RailSummary,
  type RailSummaryCache,
  type RailSummaryCacheEntry,
  type RailSummaryLoader,
  railSummaryCache,
} from "./railSummary";

export type RailSummaryFailure = "none" | "job-only" | "fatal";

export interface UseRailSummaryOptions {
  ref: string;
  load: RailSummaryLoader;
  /** ThreadModel.jobsUpdatedAt: bumped by job started/finished (and the other
   * job/delegate signals that share the field). */
  jobsUpdatedAt: number | null;
  /** ThreadModel.jobsTreeRevision: the root activity tree's revision. */
  jobsTreeRevision: number | null;
  /** The thread store's per-ref hydration count; a bump means reconnect or
   * targeted resync replaced the model. The store also deletes it when a ref is
   * released, so it is a refresh signal, never an ordering fence. */
  hydration: number;
  /** Ended session: the cached projection is held at its terminal revision. */
  terminal?: boolean;
  cache?: RailSummaryCache;
}

export interface RailSummaryState {
  summary: RailSummary | null;
  /** null until a projection exists; never a guessed completeness. */
  jobHistory: RailJobHistory | null;
  loading: boolean;
  failure: RailSummaryFailure;
  refresh: () => void;
}

interface Snapshot {
  ref: string;
  summary: RailSummary | null;
  jobHistory: RailJobHistory | null;
  failure: RailSummaryFailure;
  loading: boolean;
}

function signalKey(jobsUpdatedAt: number | null, jobsTreeRevision: number | null): string {
  return `${jobsUpdatedAt ?? "-"}:${jobsTreeRevision ?? "-"}`;
}

function snapshotFromCache(ref: string, cache: RailSummaryCache): Snapshot {
  const entry = cache.get(ref);
  if (!entry) {
    return { ref, summary: null, jobHistory: null, failure: "none", loading: true };
  }
  return {
    ref,
    summary: entry.summary,
    jobHistory: entry.jobHistory ?? entry.summary.jobHistory,
    failure: "none",
    loading: false,
  };
}

export function useRailSummary(options: UseRailSummaryOptions): RailSummaryState {
  const { ref, load, jobsUpdatedAt, jobsTreeRevision, hydration, terminal = false, cache = railSummaryCache } = options;
  const signal = signalKey(jobsUpdatedAt, jobsTreeRevision);

  const [snapshot, setSnapshot] = useState<Snapshot>(() => snapshotFromCache(ref, cache));
  if (snapshot.ref !== ref) setSnapshot(snapshotFromCache(ref, cache));

  // Everything a settled request needs is read through refs, so a new loader
  // or callback identity (a rerender, not a job signal) never refetches.
  const loader = useRef(load);
  loader.current = load;
  const cacheRef = useRef(cache);
  cacheRef.current = cache;
  const refRef = useRef(ref);
  refRef.current = ref;
  const signalRef = useRef(signal);
  signalRef.current = signal;
  const hydrationRef = useRef(hydration);
  hydrationRef.current = hydration;
  const terminalRef = useRef(terminal);
  terminalRef.current = terminal;

  // A generation bump fences responses that started before it: a reconnect or a
  // ref change can never have an older response replace the newer data.
  const generation = useRef(0);
  const inFlight = useRef<AbortController | null>(null);
  const inFlightTerminal = useRef<boolean | null>(null);
  const trailing = useRef(false);
  // One revision-retry budget per invalidation cycle: an older request's stale
  // answer must not consume the budget of a newer signal.
  const cycle = useRef(0);
  const retriedCycle = useRef<number | null>(null);
  const previousTerminal = useRef(terminal);
  const previousRef = useRef(ref);
  const previousHydration = useRef(hydration);

  const request = useCallback(() => {
    if (inFlight.current) {
      trailing.current = true;
      return;
    }
    const controller = new AbortController();
    inFlight.current = controller;
    const gen = generation.current;
    const requestCycle = cycle.current;
    const target = refRef.current;
    const requestedSignal = signalRef.current;
    const requestedHydration = hydrationRef.current;
    const requestedTerminal = terminalRef.current;
    inFlightTerminal.current = requestedTerminal;

    const settle = () => {
      // A superseded request (ref change) must not clear or settle the request
      // that replaced it.
      if (inFlight.current !== controller) return;
      inFlight.current = null;
      inFlightTerminal.current = null;
      if (trailing.current) {
        trailing.current = false;
        request();
      }
    };

    // A fenced response (reconnect or ref change) must not leave a spinner
    // behind when nothing replaces it: the terminal freeze can drop the
    // trailing request an abort would otherwise start.
    const abandon = () => {
      settle();
      if (inFlight.current === null) {
        setSnapshot((current) => (current.ref === target ? { ...current, loading: false } : current));
      }
    };

    const recordDegradedCached = (entry: RailSummaryCacheEntry) => {
      const jobHistory: RailJobHistory = {
        // A failure may only downgrade: a cached fatal lane stays fatal.
        status: (entry.jobHistory?.status ?? entry.summary.jobHistory.status) === "fatal" ? "fatal" : "degraded",
        revision: entry.summary.jobHistory.revision,
      };
      cacheRef.current.set(target, { ...entry, jobHistory });
      setSnapshot((current) =>
        current.ref === target
          ? { ...current, summary: entry.summary, jobHistory, failure: "job-only", loading: false }
          : current,
      );
    };

    setSnapshot((current) => (current.ref === target ? { ...current, loading: true } : current));

    // Promise.resolve().then keeps a loader that throws synchronously on the
    // same rejection path as a failed request.
    Promise.resolve()
      .then(() => loader.current(target, { signal: controller.signal }))
      .then(
        (summary) => {
          if (gen !== generation.current) {
            abandon();
            return;
          }
          const previousRevision = cacheRef.current.get(target)?.summary.jobHistory.revision ?? null;
          const incomingRevision = summary.jobHistory.revision;
          if (previousRevision !== null && (incomingRevision === null || incomingRevision < previousRevision)) {
            // An older (or unversioned) projection never replaces a revisioned
            // one. Ask once more so a quiet live ref cannot stay pinned to the
            // stale copy; if the retry repeats the regression, stop asking and
            // leave the cached data with an honest not-loading state.
            if (requestCycle !== cycle.current) {
              // An older cycle's stale answer. The newer cycle's own queued
              // request owns its recovery budget.
            } else if (retriedCycle.current !== requestCycle) {
              retriedCycle.current = requestCycle;
              trailing.current = true;
            } else {
              // Recovery is exhausted: keep the rows but say the lane is no
              // longer trustworthy, so the rail shows degraded and offers Retry.
              const cached = cacheRef.current.get(target);
              if (cached) recordDegradedCached(cached);
              else setSnapshot((current) => (current.ref === target ? { ...current, loading: false } : current));
            }
            settle();
            return;
          }
          cacheRef.current.set(target, {
            summary,
            signal: requestedSignal,
            hydration: requestedHydration,
            terminal: requestedTerminal,
          });
          setSnapshot((current) =>
            current.ref === target
              ? { ref: target, summary, jobHistory: summary.jobHistory, failure: "none", loading: false }
              : current,
          );
          settle();
        },
        () => {
          if (gen !== generation.current) {
            abandon();
            return;
          }
          const cached = cacheRef.current.get(target);
          if (cached) {
            // The attempt covered this signal, so a remount shows the degraded
            // lane instead of re-asking until the next signal or Retry.
            recordDegradedCached({
              ...cached,
              signal: requestedSignal,
              hydration: requestedHydration,
              terminal: requestedTerminal,
            });
          } else {
            setSnapshot((current) =>
              current.ref === target
                ? {
                    ref: target,
                    summary: null,
                    jobHistory: { status: "fatal", revision: null },
                    failure: "fatal",
                    loading: false,
                  }
                : current,
            );
          }
          settle();
        },
      );
  }, []);

  // The one refresh trigger: this ref's job signal, its hydration generation,
  // a live<->ended transition (an ended mount must capture the terminal job
  // projection, and a live mount must not inherit a frozen one), or a first
  // mount with nothing cached.
  useEffect(() => {
    const entry = cache.get(ref);
    const becameTerminal = !previousTerminal.current && terminal;
    previousTerminal.current = terminal;
    const hydrationChanged = previousHydration.current !== hydration;
    previousHydration.current = hydration;
    if (previousRef.current !== ref) {
      previousRef.current = ref;
      generation.current += 1;
      inFlight.current?.abort();
      inFlight.current = null;
      inFlightTerminal.current = null;
      trailing.current = false;
    }
    if (hydrationChanged) {
      // Wait for the open request to settle (an abort rejects a real fetch)
      // before asking again, so two requests never overlap.
      generation.current += 1;
      if (inFlight.current) {
        trailing.current = true;
        inFlight.current.abort();
      }
    }
    let shouldRequest = false;
    if (!entry) shouldRequest = true;
    else if (becameTerminal || entry.terminal !== terminal) shouldRequest = true;
    else if (terminal) {
      // A terminal entry captured at its terminal revision stays cached.
      // Exception: an open request captured while the session was live must
      // still be followed by the terminal refresh, or its response would write
      // a live-captured entry and skip the final job state.
      if (inFlightTerminal.current !== false) trailing.current = false;
      return;
    } else if (entry.signal !== signal || entry.hydration !== hydration) shouldRequest = true;
    if (!shouldRequest) return;
    // An independent invalidation starts a new revision-retry budget; only the
    // cycle's own trailing retry keeps the old one.
    cycle.current += 1;
    retriedCycle.current = null;
    request();
  }, [cache, hydration, ref, request, signal, terminal]);

  // One mount-scoped abort: an unmounted rail leaves no request running. It is
  // deliberately not the signal effect's cleanup, which would abort on every
  // job signal and defeat coalescing.
  useEffect(() => {
    return () => {
      generation.current += 1;
      inFlight.current?.abort();
      inFlight.current = null;
    };
  }, []);

  const refresh = useCallback(() => {
    // A person's explicit retry outranks the terminal freeze. The open request
    // is abandoned rather than awaited: a loader that ignores its abort signal
    // must not leave the retry dead.
    generation.current += 1;
    cycle.current += 1;
    retriedCycle.current = null;
    inFlightTerminal.current = null;
    trailing.current = false;
    inFlight.current?.abort();
    inFlight.current = null;
    request();
  }, [request]);

  return {
    summary: snapshot.summary,
    jobHistory: snapshot.jobHistory,
    loading: snapshot.loading,
    failure: snapshot.failure,
    refresh,
  };
}
