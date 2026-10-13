import {
  type AcceptedReadResult,
  type AnnotationView,
  type CapturedPassage,
  contextForRange,
  type DestinationProvenance,
  type DocumentKey,
  displayedFingerprint,
  type ExpiryReceipt,
  editCommentText,
  invalidateCommentCapture,
  selectCommentPassage,
  sourcePositions,
} from "@evener/appwire-client";
import {
  bindFilePath,
  DOC_FILE_MAX_BYTES,
  type DocFileContent,
  DocFileError,
  type DocFileErrorKind,
  docImageReadURL,
  type FileReference,
  readDocFile,
  rebindFileReference,
} from "@evener/appwire-client/docContent";
import { type ReactNode, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { PaneProps } from "../../shell/paneRegistry";
import { usePaneVisible } from "../../shell/paneVisibility";
import { documentPaneState, recordDocumentPaneState, useWorkspaceStore, workspaceStore } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import type { DocumentAnnotationPorts } from "../../stores/documentAnnotationRuntime";
import { createSecureUUID } from "../../stores/secureUUID";
import { threadsStore, useThreadsStore } from "../../stores/threads";
import { Button, Chip, CodeBlock, Dialog, EmptyState, PaneScaffold, Skeleton, ZoomableMedia } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { DocumentMarkdown } from "../../widgets/markdown/DocumentMarkdown";
import type { BoundDocumentMap } from "../../widgets/markdown/documentSourceMap";
import { browserDocPort } from "./browserDocPort";
import { DocumentCommentBox, type DocumentCommentStatus } from "./DocumentCommentBox";
import { DocumentHighlights } from "./DocumentHighlights";
import { filenameOf, formatDocBytes, isMarkdownPath } from "./docFile";
import styles from "./docpane.module.css";
import {
  type DocumentReaderWork,
  documentReaderWork,
  getDocumentAnnotationPorts,
  publishReader,
  retryDocumentReaders,
  saveReaderAnnotation,
  subscribeDocumentReaderWork,
} from "./documentReadState";
import { observeFinishedDocumentSelections } from "./documentSelection";
import type { DocOpeningOrigin, DocParams } from "./openDoc";
import { useDocumentRead } from "./useDocumentRead";

const CLASS = {
  markdown: requireClass(styles.markdown, "docpane.module.css", "markdown"),
  image: requireClass(styles.image, "docpane.module.css", "image"),
  lightboxImg: requireClass(styles.lightboxImg, "docpane.module.css", "lightboxImg"),
  notice: requireClass(styles.notice, "docpane.module.css", "notice"),
  noticeText: requireClass(styles.noticeText, "docpane.module.css", "noticeText"),
  status: requireClass(styles.status, "docpane.module.css", "status"),
};

const ERROR_COPY: Record<DocFileErrorKind, { title: string; hint: string }> = {
  forbidden: { title: "Access denied", hint: "This path is outside the session's working directory." },
  "not-found": { title: "File not available", hint: "This file was not found in the session's working directory." },
  "host-unsupported": {
    title: "Open it on the host",
    hint: "This session's host runs an older Evener that can't send its files here yet.",
  },
  error: { title: "Couldn't load file", hint: "The hub returned an unexpected error." },
};

type FileState =
  | { status: "loading" }
  | { status: "error"; kind: DocFileErrorKind }
  | { status: "ok"; content: DocFileContent };

function errorKind(err: unknown): DocFileErrorKind {
  return err instanceof DocFileError ? err.kind : "error";
}

// The read-only renderer, kept unchanged for the annotation-less path (an older
// injected-port absence or a document whose annotation port is unavailable).
// The annotation-less file view: presentational over the content the
// workspace's reference-bound read hook supplies, rendering markdown through
// the same provenance-aware DocumentMarkdown the annotated reader uses,
// with a static identity and no map consumer.
function DocFileView({ content, path }: { content: DocFileContent; path: string }) {
  const displayId = useIdFallback();
  if (content.binary) {
    return (
      <EmptyState title="Binary file not shown" hint={`${filenameOf(path)} (${formatDocBytes(content.sizeBytes)})`} />
    );
  }
  return (
    <>
      {content.truncated && <TruncationNotice content={content} />}
      {isMarkdownPath(path) ? (
        <div className={CLASS.markdown}>
          <DocumentMarkdown
            source={content.text}
            identity={{ displayedSnapshotId: displayId, mappingId: displayId }}
            onMap={ignoreDocumentMap}
          />
        </div>
      ) : (
        <CodeBlock text={content.text} fold={false} />
      )}
    </>
  );
}

function TruncationNotice({ content }: { content: DocFileContent }) {
  return (
    <div className={CLASS.notice}>
      <Chip tone="attention">Truncated</Chip>
      <span className={CLASS.noticeText}>
        {content.totalBytes !== undefined
          ? `Showing the first ${formatDocBytes(DOC_FILE_MAX_BYTES)} of ${formatDocBytes(content.totalBytes)}.`
          : `Showing the first ${formatDocBytes(DOC_FILE_MAX_BYTES)}.`}
      </span>
    </div>
  );
}

const ignoreDocumentMap = () => {};

// A platform that reports no Range rectangles (jsdom without browser layout)
// cannot paint exact highlight bands, so the overlay is mounted only where the
// capability exists. Real browsers always provide it.
function canMeasureRanges(): boolean {
  return typeof Range !== "undefined" && typeof Range.prototype.getClientRects === "function";
}

// useId without importing React's hook name into the module-level CLASS block.
function useIdFallback(): string {
  return useMemo(() => createSecureUUID(), []);
}

// Build the complete accepted capture from the verified DOM selection and the
// current accepted displayed source. Line/column, context and fingerprint come
// from the shared helpers; the mapping/display identities come from the bound
// map, never a guess.
function buildCapture(
  selection: {
    displayedSnapshotId: string;
    mappingId: string;
    sourceRange: { start: number; end: number };
    selectedRenderedText: string;
  },
  content: DocFileContent,
): CapturedPassage {
  const { before, after } = contextForRange(content.text, selection.sourceRange);
  const positions = sourcePositions(content.text, selection.sourceRange);
  return {
    evidence: { wholeRevision: content.revision, fingerprint: displayedFingerprint(content.text, content.truncated) },
    sourceRange: selection.sourceRange,
    start: positions.start,
    endInclusive: positions.endInclusive,
    selectedRenderedText: selection.selectedRenderedText,
    contextBefore: before,
    contextAfter: after,
    mappingId: selection.mappingId,
    displayedSnapshotId: selection.displayedSnapshotId,
  };
}

// A capture's evidence describes the displayed source the user annotated. Only a
// DEFINITE change retires retained authority; missing or incomparable evidence is
// uncertainty, never proof of expiry. A serialized fingerprint difference alone can
// merely describe different coverage or scope, which the shared accepted-read
// comparator (`compareDisplayedSources`) treats as insufficient coverage rather than a
// change. The retained capture stores a fingerprint only, not the old raw source, so
// positive proof here needs either a known differing whole revision or a comparison
// over the same displayed range.
function evidenceProvesChange(content: DocFileContent, evidence: CapturedPassage["evidence"]): boolean {
  if (
    evidence.wholeRevision !== undefined &&
    content.revision !== undefined &&
    evidence.wholeRevision !== content.revision
  ) {
    return true;
  }
  const retained = evidence.fingerprint;
  const current = displayedFingerprint(content.text, content.truncated);
  if (retained.scope === "displayed-full" && current.scope === "displayed-full") {
    // Both observations are whole-file reads, so any difference is a definite change.
    return retained.value !== current.value || retained.coverage.end !== current.coverage.end;
  }
  // Only an identical displayed range is comparable from fingerprint evidence alone; a
  // different coverage interval is insufficiency, not proof of change.
  const sameRange =
    retained.coverage.start === current.coverage.start && retained.coverage.end === current.coverage.end;
  return sameRange && retained.value !== current.value;
}

// The status the pane-level indicator and the comment box share. `saved` and
// `saved, pending draft` come from ACTUAL annotation views; a retained attempt
// or a newer unsaved comment keeps the honest `unsaved` state.
function commentStatus(work: DocumentReaderWork, views: readonly AnnotationView[]): DocumentCommentStatus {
  if (work.phase === "expiry-pending") return "expiry-pending";
  if (work.attempted || work.comment.note.trim() !== "") return "unsaved";
  const hasApplied = views.some((v) => v.handoff.state === "applied" || v.handoff.state === "already-applied");
  const hasPending = views.some((v) => v.handoff.state === "pending");
  if (hasApplied) return hasPending ? "saved, pending draft" : "saved";
  if (hasPending) return "saved, pending draft";
  return "saved";
}

interface DocReaderProps {
  session: string;
  path: string;
  paneId: string;
  focused: boolean;
  origin?: DocOpeningOrigin;
  ports: DocumentAnnotationPorts;
}

// The annotated reader: real accepted read tickets, retained comment work, the
// real comment box and highlight overlay, and the pane-level status.
function DocReader({ session, path, paneId, focused, origin, ports }: DocReaderProps) {
  const [state, setState] = useState<FileState>({ status: "loading" });
  const [views, setViews] = useState<AnnotationView[]>([]);
  const [boundMap, setBoundMap] = useState<BoundDocumentMap | null>(null);
  const [identity, setIdentity] = useState(() => ({
    displayedSnapshotId: createSecureUUID(),
    mappingId: createSecureUUID(),
  }));
  // True while the newest raw response was NOT accepted (a thrown storage fault):
  // usable highlights/selection mapping stay suspended, but the retained lease,
  // attempt and exact note/capture are kept for recovery.
  const [authoritySuspended, setAuthoritySuspended] = useState(false);
  const [scrollElement, setScrollElement] = useState<HTMLElement | null>(null);
  const [readNonce, setReadNonce] = useState(0);
  const [, bump] = useState(0);
  const hostRef = useRef<HTMLDivElement>(null);
  // The most recent fence's receipt, awaiting this reader's reread: the reread's
  // acceptance proves the fence settled and completes the expired epoch's
  // deletion then. A fault leaves the persisted receipt for the retry sweep.
  const expiryReceiptRef = useRef<ExpiryReceipt | null>(null);
  // Consecutive stale-ticket acceptances while this reader stayed leaseless.
  // One retry recovers a reader whose ticket a COMPLETED read superseded; a
  // second consecutive stale is the two-reader interleave whose retry the raw
  // render below replaces.
  const leaselessStaleRef = useRef(0);

  const key: DocumentKey = useMemo(
    () => ({ namespace: { kind: "browser", origin: location.origin }, sessionRef: session, path }),
    [session, path],
  );
  // The pane's retained binding decides what this reader fetches: an absolute
  // provenance keeps its exact read target across cwd changes, and a relative
  // one rebinds with a changed cwd, the same resolution the workspace's
  // reference-bound reader performs below. The pane's params.path remains the
  // document's identity for annotations and the pane record; it is only the
  // fetch target when no binding was ever recorded. The stores are read per
  // render, never subscribed: the binding re-resolves on every render - a
  // foreground reread and any later read fetch the rebound target - while the
  // reader keeps rendering only for the state it already observes, so a
  // closed pane's committed retirement can never be resurrected by an extra
  // render of a reader waiting to unmount.
  const pane = workspaceStore.getState().panes.find((candidate) => candidate.id === paneId);
  const retained = pane ? documentPaneState(pane) : undefined;
  let readTarget = path;
  if (retained) {
    const cwd = threadsStore.getState().threads.get(session)?.cwd;
    const reference =
      cwd && cwd !== retained.reference.cwd ? rebindFileReference(retained.reference, cwd) : retained.reference;
    readTarget = reference.readTarget;
  }
  const work = documentReaderWork(paneId, key, ports);
  const destination: DestinationProvenance = origin ?? { kind: "browser-restored-session" };

  useEffect(
    () =>
      subscribeDocumentReaderWork(paneId, () => {
        void work;
        bump((value) => value + 1);
      }),
    [paneId, work],
  );

  // Retire the retained lease/attempt and the stale capture on a PROVEN source
  // or namespace-reset change, keeping the exact editable note. The runtime
  // drops the local failed operation so it cannot be re-saved against dead
  // authority. `phase` is the honest status to publish for that retirement.
  const retireAuthority = useCallback(
    (phase: DocumentReaderWork["phase"]) => {
      work.lease = null;
      work.attempted = null;
      work.attemptedComment = null;
      work.phase = phase;
      work.comment = invalidateCommentCapture(work.comment);
      ports.runtime.invalidate(key);
      publishReader(work);
    },
    [work, ports, key],
  );

  // A committed wake that shows no visible receipt while retained authority
  // exists may be a namespace reset. Prove it with a current ticket's durable
  // incarnation; an untyped wake, an empty list or a moved head revision alone
  // is not proof. Ordinary notifications compare equal and change nothing, so
  // this never loops. A proven reset retires the old authority, publishes the
  // honest status and starts a current read on its own, so a fresh accepted lease
  // and bound map appear without a later foreground action or remount.
  const revalidateOnWake = useCallback(() => {
    if (!work.lease && !work.attempted) return;
    void ports.repository.beginRead(key).then(
      (ticket) => {
        const authority = work.lease ?? work.attempted?.lease ?? null;
        if (!authority || ticket.namespaceIncarnation === authority.namespaceIncarnation) return;
        retireAuthority("reading");
        setReadNonce((value) => value + 1);
      },
      () => {},
    );
  }, [work, ports, key, retireAuthority]);

  // A leaseless expiry-pending reader lost its fence, and ANY committed wake may
  // be the replacement epoch a repository retry just committed — the expired
  // epoch's rows are suppressed, so an empty list is the EXPECTED wake shape
  // here and the reset-proof gate must not gate this too. Schedule a current
  // read: an unchanged world merely re-validates through real acceptance, and
  // a committed replacement adopts fresh authority without waiting for a
  // foreground event or a remount. The read itself never wakes, so this
  // cannot loop.
  const recoverLeaselessExpiryOnWake = useCallback(() => {
    if (work.lease === null && work.attempted === null && work.phase === "expiry-pending") {
      setReadNonce((value) => value + 1);
    }
  }, [work]);

  // A committed wake whose views all name a foreign epoch is a peer's fence
  // committed while this reader still displays the retained snapshot: the
  // expired epoch's rows are already suppressed, so the replacement's
  // operations are the only evidence left. Schedule the current read that
  // adopts the replacement epoch (or restores the retained one); the read
  // itself never wakes, so this cannot loop.
  const recoverReplacedEpochOnWake = useCallback(
    (next: readonly AnnotationView[]): void => {
      const authority = work.lease ?? work.attempted?.lease ?? null;
      if (!authority || next.length === 0) return;
      if (next.some((view) => view.receipt.epoch === authority.epoch)) return;
      setReadNonce((value) => value + 1);
    },
    [work],
  );

  // View refresh through the actual repository subscription. A committed wake
  // (a save/handoff, a namespace reset, a peer) refreshes the visible receipts
  // and revalidates the retained authority against the durable incarnation. The
  // INITIAL mount refresh must NOT revalidate: issuing a second ticket would
  // supersede the mount's own in-flight read ticket and strand the content of a
  // document that has no annotations. An empty list is not proof of reset, so
  // only a committed wake may revalidate, and revalidateOnWake keeps the durable
  // incarnation comparison.
  useEffect(() => {
    const load = (onWake: boolean): void => {
      void ports.repository.listAnnotations(key).then(
        (next) => {
          setViews(next);
          if (!onWake) return;
          // A committed wake that leaves NO visible receipt while retained
          // authority exists could be a namespace reset; prove it by comparing
          // the durable incarnation. An empty list alone is not proof, so an
          // ordinary empty result merely compares equal and changes nothing.
          if (next.length === 0) revalidateOnWake();
          // The leaseless expiry-pending recovery runs on every committed wake
          // regardless of the list's emptiness: the expired epoch's rows are
          // suppressed, so an empty list is the expected shape here.
          recoverLeaselessExpiryOnWake();
          // Views that all name a foreign epoch: a peer's fence replaced this
          // reader's epoch and the replacement's save just committed.
          recoverReplacedEpochOnWake(next);
        },
        () => {},
      );
    };
    load(false);
    return ports.repository.subscribe(key, () => {
      load(true);
    });
  }, [ports, key, revalidateOnWake, recoverLeaselessExpiryOnWake, recoverReplacedEpochOnWake]);

  // Retire the stale capture/attempt for an expiry-pending read while keeping
  // the exact editable note; the runtime drops the local failed operation.
  const enterExpiryPending = useCallback(() => {
    retireAuthority("expiry-pending");
  }, [retireAuthority]);

  // The read loop. The current ticket is issued BEFORE the raw fetch, so every
  // successful cycle — an unchanged reopen or foreground reread included —
  // advances the durable read generation and revalidates through real
  // acceptance. The repository preserves the durable head/snapshot identity for
  // an exactly identical current read, so a retained immutable attempt stays
  // valid without any pane-local content cache. A stale ticket is ignored, a
  // changed read fences (expiry-pending), and a committed fence is followed by
  // one more current read to adopt the replacement epoch. A failed raw read
  // never replaces the retained work and never accepts fabricated content.
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      void readNonce; // re-run the read for each foreground reread
      // A revalidation of already-rendered content keeps that content on
      // screen: a binding re-record (an origin churn, a restored pane gaining
      // its binding) re-reads the same document, and blanking it to the
      // loading skeleton would tear down the reading position, the selection
      // surface and any bound scope mid-read.
      if (readNonce === 0) setState((previous) => (previous.status === "ok" ? previous : { status: "loading" }));

      // A thrown beginRead storage fault is uncertainty, not proof the document
      // changed: fall through to the raw read and suspend usable authority,
      // keeping the exact note/capture/attempt for recovery.
      let ticket: Awaited<ReturnType<typeof ports.repository.beginRead>> | null = null;
      try {
        ticket = await ports.repository.beginRead(key);
      } catch {
        ticket = null;
      }
      if (cancelled) return;

      let content: DocFileContent;
      try {
        content = await readDocFile(session, readTarget, browserDocPort);
      } catch (err) {
        // A failed raw read preserves the exact authored note and unsent work.
        if (!cancelled) setState({ status: "error", kind: errorKind(err) });
        return;
      }
      if (cancelled) return;

      if (ticket === null) {
        // Render the real raw content without adopting or fabricating accepted
        // authority; usable highlights/selection stay suspended while the exact
        // attempt, note and prior lease evidence are retained for recovery.
        setState({ status: "ok", content });
        setAuthoritySuspended(true);
        return;
      }

      let accepted: AcceptedReadResult;
      try {
        accepted = await ports.repository.acceptRead(ticket, content);
      } catch {
        if (cancelled) return;
        const evidence = work.attempted?.capture.evidence ?? work.comment.capture?.evidence;
        if (evidence && evidenceProvesChange(content, evidence)) {
          // A DEFINITE change to the annotated source is confirmed expiry even
          // though acceptance threw: retire the stale capture/attempt.
          setState({ status: "ok", content });
          enterExpiryPending();
          return;
        }
        // Otherwise the throw is uncertainty, not confirmed expiry: render the
        // raw content, suspend usable authority and retain the exact
        // note/capture/attempt and the shared retry ownership.
        setState({ status: "ok", content });
        setAuthoritySuspended(true);
        return;
      }
      if (cancelled) return;
      if (accepted.state === "stale-ticket") {
        // A concurrently mounted pane's ticket can be superseded by another
        // pane's issuance alone; this read was honest work against a dead
        // ticket. A leaseless pane with no retained operation has nothing to
        // lose: one fresh reread whose ticket is the newest issued recovers it
        // instead of leaving it on its loading skeleton forever. A pane holding
        // a lease keeps its rendered authority - its stale foreground rereads
        // stay rendered and the next current acceptance adopts or retires them
        // - so it never retries from here.
        if (work.lease !== null || work.attempted !== null) return;
        if (leaselessStaleRef.current === 0) {
          leaselessStaleRef.current = 1;
          setReadNonce((value) => value + 1);
          return;
        }
        // A SECOND consecutive stale acceptance means another reader's
        // in-flight issuance keeps superseding ours; re-issuing would only
        // supersede theirs back, and two leaseless readers would fetch and
        // write read generations forever. The fetch itself succeeded: render
        // its real raw content with suspended authority - the same honest
        // display a failed beginRead produces - and stop issuing. The other
        // reader's ticket then settles, and a foreground reread, a reopen or
        // a later committed wake revalidates this pane.
        leaselessStaleRef.current = 0;
        setState({ status: "ok", content });
        setAuthoritySuspended(true);
        return;
      }
      setState({ status: "ok", content });
      if (accepted.state === "accepted") {
        leaselessStaleRef.current = 0;
        // A durable difference from the retained authority proves this reader's
        // captured work is stale: a namespace reset changes the incarnation, and
        // another reader's fence of a changed source replaces the epoch under the
        // same incarnation. Retire the old attempt/capture before adopting the
        // new authority, keeping the exact editable note; a source change THIS
        // reader fences itself is handled by the expiry-pending path above.
        const priorAuthority = work.lease ?? work.attempted?.lease ?? null;
        if (
          priorAuthority !== null &&
          (priorAuthority.namespaceIncarnation !== accepted.lease.namespaceIncarnation ||
            priorAuthority.epoch !== accepted.lease.epoch)
        ) {
          work.attempted = null;
          work.attemptedComment = null;
          work.comment = invalidateCommentCapture(work.comment);
          ports.runtime.invalidate(key);
        }
        work.lease = accepted.lease;
        work.phase = work.attempted ? "unsaved" : "reading";
        setAuthoritySuspended(false);
        setIdentity({ displayedSnapshotId: accepted.lease.displayedSnapshotId, mappingId: createSecureUUID() });
        publishReader(work);
        const fencedExpiry = expiryReceiptRef.current;
        if (fencedExpiry !== null) {
          expiryReceiptRef.current = null;
          // This acceptance proves the fence committed, so the expired epoch's
          // deletion can complete now. A faulting deletion leaves the
          // persisted receipt unfinished for the next retry pass's sweep; the
          // reader tries once per acceptance and never blocks adoption on it.
          void ports.repository.finishExpiry(fencedExpiry).catch(() => undefined);
        }
        return;
      }
      // A RETURNED expiry-pending is confirmed source expiry, even while its
      // fence commit is still pending: retire the stale capture/attempt but keep
      // the exact note, then adopt the replacement epoch when the fence commits.
      enterExpiryPending();
      leaselessStaleRef.current = 0;
      if (accepted.receipt) {
        // Retain the receipt: the reread's acceptance settles the fence and
        // completes the expired epoch's deletion there.
        expiryReceiptRef.current = accepted.receipt;
        setReadNonce((value) => value + 1);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [session, readTarget, key, ports, work, readNonce, enterExpiryPending]);

  // A genuine foreground reread of the current ticket, in addition to the shared
  // retained-attempt retry.
  useEffect(() => {
    const onForeground = (): void => {
      if (document.visibilityState === "visible") setReadNonce((value) => value + 1);
    };
    document.addEventListener("visibilitychange", onForeground);
    return () => document.removeEventListener("visibilitychange", onForeground);
  }, []);

  // The PaneScaffold body is the real scroll owner: retain and restore its
  // position across host swaps.
  // A callback ref publishes the real PaneScaffold body (the host's parent) as
  // the scroll owner, and restores the retained position on each swap.
  const bindHost = useCallback((node: HTMLDivElement | null) => {
    hostRef.current = node;
    setScrollElement(node?.parentElement ?? null);
  }, []);
  useEffect(() => {
    const body = scrollElement;
    if (!body) return;
    body.scrollTop = work.scrollTop;
    const onScroll = (): void => {
      work.scrollTop = body.scrollTop;
    };
    body.addEventListener("scroll", onScroll);
    return () => body.removeEventListener("scroll", onScroll);
  }, [scrollElement, work]);

  // A state setter is not map installation: the retained reader-open retry fires
  // only after the CURRENT accepted map for the accepted lease is really bound.
  // An identical durable snapshot still gets a fresh render mapping identity, so
  // this bind always happens on a successful read cycle.
  const handleMap = useCallback(
    (map: BoundDocumentMap | null) => {
      setBoundMap(map);
      if (
        map &&
        !authoritySuspended &&
        work.attempted &&
        work.lease &&
        map.identity.displayedSnapshotId === work.lease.displayedSnapshotId
      ) {
        void retryDocumentReaders("reader-open");
      }
    },
    [work, authoritySuspended],
  );

  // The bound map is usable only when it matches the CURRENT accepted lease:
  // never capture a selection or paint highlights from an obsolete map or with
  // an absent/pending lease.
  const currentLease = work.lease;
  const boundForLease =
    !authoritySuspended &&
    boundMap &&
    currentLease &&
    boundMap.identity.displayedSnapshotId === currentLease.displayedSnapshotId
      ? boundMap
      : null;

  // Only the displayed lease's own epoch may drive the status or paint: a
  // peer's fence-and-save leaves this reader displaying the snapshot its
  // lease names, and the replacement epoch's offsets belong to text it does
  // not display. The committed wake adopts the replacement epoch, after which
  // its annotations paint over the text they were captured against.
  const epochViews = useMemo(
    () => (currentLease ? views.filter((view) => view.receipt.epoch === currentLease.epoch) : []),
    [views, currentLease],
  );

  // Finished selections open the box and freeze the capture at the first edit.
  useEffect(() => {
    const root = hostRef.current;
    if (!root || !boundForLease || state.status !== "ok") return;
    return observeFinishedDocumentSelections({
      root,
      map: boundForLease,
      onFinish: (selection) => {
        work.comment = selectCommentPassage(work.comment, buildCapture(selection, state.content));
        publishReader(work);
      },
    });
  }, [boundForLease, state, work]);

  const status = commentStatus(work, epochViews);
  const boxVisible = work.attempted !== null || work.comment.capture !== undefined || work.comment.note.trim() !== "";
  const saving = work.phase === "saving";

  const onNote = (note: string): void => {
    work.comment = editCommentText(work.comment, note);
    publishReader(work);
  };
  const onSend = (): void => {
    // A suspended authority keeps the retained lease/capture for recovery, but the
    // newest raw response was never accepted: sending would persist an operation
    // from unconfirmed evidence. Keep the note editable and Cancel usable.
    if (authoritySuspended) return;
    void saveReaderAnnotation(work, destination);
  };
  const onCancel = (): void => {
    // Dismiss only this reader's unsaved comment: retire the exact immutable
    // attempt from the shared runtime's failed-work set (its namespace-qualified
    // identity) BEFORE clearing the reader slot, so neither the UI retry nor a
    // later ordinary trigger can re-save it. A different reader's failed
    // operation for the same document remains retained.
    const attempted = work.attempted;
    if (attempted) ports.runtime.retireAttempted(attempted);
    work.comment = { note: "", captureFrozen: false };
    work.attempted = null;
    work.attemptedComment = null;
    if (work.phase !== "reading" && work.phase !== "expiry-pending") work.phase = "reading";
    publishReader(work);
  };

  // The reader owns its scaffold so the comment controls can sit in the
  // scaffold's footer: a sibling of the scrolling body, inside the pane's own
  // visible area. Placing them after the Markdown inside the scroll body left
  // them at the document's end, past the reading position on long documents.
  const scaffold = (children: ReactNode, footer?: ReactNode) => (
    <PaneScaffold
      title={filenameOf(path)}
      paneId={paneId}
      focused={focused}
      scaffoldMarker="doc-reader-scroll"
      footer={footer}
    >
      {children}
    </PaneScaffold>
  );

  if (state.status === "loading") return scaffold(<Skeleton />);
  if (state.status === "error") {
    const copy = ERROR_COPY[state.kind];
    return scaffold(
      <EmptyState title={copy.title} hint={copy.hint} />,
      <div className={CLASS.status} role="status">
        {status}
      </div>,
    );
  }

  const { content } = state;
  if (content.binary) {
    return scaffold(
      <EmptyState title="Binary file not shown" hint={`${filenameOf(path)} (${formatDocBytes(content.sizeBytes)})`} />,
    );
  }

  return scaffold(
    <>
      {content.truncated && <TruncationNotice content={content} />}
      {isMarkdownPath(path) ? (
        <div className={CLASS.markdown} ref={bindHost}>
          <DocumentMarkdown source={content.text} identity={identity} onMap={handleMap} />
          {boundForLease && scrollElement && canMeasureRanges() && (
            <DocumentHighlights map={boundForLease} annotations={epochViews} scrollElement={scrollElement} />
          )}
        </div>
      ) : (
        <CodeBlock text={content.text} fold={false} />
      )}
    </>,
    boxVisible ? (
      <DocumentCommentBox
        work={work.comment}
        onNote={onNote}
        onSend={onSend}
        onCancel={onCancel}
        saving={saving}
        status={status}
        suspended={authoritySuspended}
      />
    ) : (
      <div className={CLASS.status} role="status">
        {status}
      </div>
    ),
  );
}

function DocImageView({ src, path }: { src: string; path: string }) {
  const [zoomed, setZoomed] = useState(false);
  const name = filenameOf(path);
  return (
    <>
      <ZoomableMedia label="Zoom image" onOpen={() => setZoomed(true)}>
        <img data-testid="doc-image" className={CLASS.image} src={src} alt={name} />
      </ZoomableMedia>
      {zoomed && (
        <Dialog open onClose={() => setZoomed(false)} title={name} size="large">
          <img data-testid="doc-lightbox-img" className={CLASS.lightboxImg} src={src} alt={name} />
        </Dialog>
      )}
    </>
  );
}

function BoundDocument({
  params,
  paneId,
  focused,
  reference,
  reopen,
  visible,
}: PaneProps<DocParams> & { reference: FileReference; reopen: number; visible: boolean }) {
  const read = useDocumentRead(params.session, reference, reopen, visible);
  const hasContent = read.content !== undefined || read.imageGeneration !== undefined;
  let body: ReactNode;
  if (params.kind === "image" && read.imageGeneration) {
    body = (
      <DocImageView
        key={JSON.stringify([params.session, reference])}
        path={reference.path}
        src={docImageReadURL(browserDocPort.origin, params.session, reference.readTarget, read.imageGeneration)}
      />
    );
  } else if (read.content) body = <DocFileView content={read.content} path={reference.path} />;
  else if (read.errorKind) {
    const copy = ERROR_COPY[read.errorKind];
    body =
      params.kind === "image" ? (
        <EmptyState title="Image not available" hint={read.notice} />
      ) : (
        <EmptyState title={copy.title} hint={copy.hint} />
      );
  } else body = <Skeleton />;
  return (
    <PaneScaffold
      title={filenameOf(reference.path)}
      paneId={paneId}
      focused={focused}
      actions={
        <Button size="sm" variant="quiet" onClick={read.reload}>
          Reload
        </Button>
      }
    >
      {hasContent && read.notice && (
        <div className={CLASS.notice} role="status">
          <span className={CLASS.noticeText}>{read.notice}</span>
        </div>
      )}
      {body}
    </PaneScaffold>
  );
}

export default function DocPane({
  params,
  paneId,
  focused,
  annotations,
}: PaneProps<DocParams> & { annotations?: DocumentAnnotationPorts }) {
  // This wrapper owns no hooks, so the annotated-reader return below cannot
  // reorder the workspace component's hooks.
  let ports: DocumentAnnotationPorts | undefined = annotations;
  if (!ports) {
    try {
      ports = getDocumentAnnotationPorts();
    } catch {
      ports = undefined;
    }
  }

  // The annotated reader owns its own PaneScaffold and read loop: its
  // accepted-read tickets precede its raw fetches, so it does not ride the
  // workspace's reference/reopen machinery.
  if (params.kind !== "image" && ports) {
    return (
      <DocReader
        session={params.session}
        path={params.path}
        paneId={paneId}
        focused={focused}
        origin={params.origin}
        ports={ports}
      />
    );
  }
  return <WorkspaceDocument params={params} paneId={paneId} focused={focused} />;
}

// The reference-bound doc pane: the workspace's retained file reference, reopen
// generation and visibility drive one shared read hook for images and
// annotation-less files.
function WorkspaceDocument({ params, paneId, focused }: PaneProps<DocParams>) {
  const visible = usePaneVisible();
  const panes = useWorkspaceStore((state) => state.panes);
  const pane = panes.find((candidate) => candidate.id === paneId);
  const retained = pane && documentPaneState(pane);
  const cwd = useThreadsStore((state) => state.threads.get(params.session)?.cwd);

  useEffect(() => {
    let started = false;
    const tryStart = () => {
      if (started || connectionStore.getState().state !== "ready") return;
      started = true;
      threadsStore
        .getState()
        .ensureThread(params.session)
        .catch(() => {});
    };
    tryStart();
    const unsubscribe = connectionStore.subscribe(tryStart);
    return () => {
      unsubscribe();
      if (started) threadsStore.getState().releaseThread(params.session);
    };
  }, [params.session]);

  let reference: FileReference | undefined;
  if (retained) {
    if (cwd && cwd !== retained.reference.cwd) reference = rebindFileReference(retained.reference, cwd);
    else reference = retained.reference;
  } else if (cwd) reference = bindFilePath(params.path, cwd);
  useLayoutEffect(() => {
    if (!pane || !reference || retained?.reference === reference) return;
    recordDocumentPaneState(pane, { reference, origin: retained?.origin, reopen: retained?.reopen ?? 0 });
  }, [pane, reference, retained]);

  if (!reference)
    return (
      <PaneScaffold title={filenameOf(params.path)} paneId={paneId} focused={focused}>
        <Skeleton />
      </PaneScaffold>
    );
  return (
    <BoundDocument
      params={params}
      paneId={paneId}
      focused={focused}
      reference={reference}
      reopen={retained?.reopen ?? 0}
      visible={visible}
    />
  );
}
