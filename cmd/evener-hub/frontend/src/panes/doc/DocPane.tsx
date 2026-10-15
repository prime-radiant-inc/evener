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
  createDocumentReadDemand,
  DOC_FILE_MAX_BYTES,
  type DocFileContent,
  DocFileError,
  type DocFileErrorKind,
  type DocumentReadAttempt,
  type DocumentReadDemand,
  type DocumentReadOutcome,
  docImageReadURL,
  type FileReference,
  readDocFile,
  rebindFileReference,
} from "@evener/appwire-client/docContent";
import { type ReactNode, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { PaneProps } from "../../shell/paneRegistry";
import { usePaneVisible } from "../../shell/paneVisibility";
import { documentPaneState, recordDocumentPaneState, useWorkspaceStore, workspaceStore } from "../../shell/workspace";
import { connectionStore, onConnectionNotification, readyConnectionTransition } from "../../stores/connection";
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
  type CommentSelection,
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
import { useReopenGeneration } from "./useReopenGeneration";

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
  | { status: "ok"; content: DocFileContent; rereadError?: DocFileErrorKind };

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
  // A mixed observation hides a definite change the same-range comparison can
  // never see - the same rule the shared accepted-read comparator applies: a
  // complete observation pins the document's true end while a truncated one
  // proves content existed beyond its coverage, so a truncated end at or
  // beyond the complete end means the annotated continuation vanished (or
  // content appeared past the EOF the capture watched) even when every
  // overlapping byte is identical.
  if (retained.scope !== current.scope) {
    const completeEnd = retained.scope === "displayed-full" ? retained.coverage.end : current.coverage.end;
    const truncatedEnd = retained.scope === "displayed-prefix" ? retained.coverage.end : current.coverage.end;
    if (truncatedEnd >= completeEnd) return true;
  }
  // Only an identical displayed range is comparable from fingerprint evidence alone; a
  // different coverage interval is insufficiency, not proof of change.
  const sameRange =
    retained.coverage.start === current.coverage.start && retained.coverage.end === current.coverage.end;
  return sameRange && retained.value !== current.value;
}

// The status the pane-level indicator and the comment box share. `saved` and
// `saved, pending draft` come from ACTUAL annotation views; a retained attempt
// or a newer unsaved comment keeps the honest `unsaved` state. A preserved
// note whose capture a source expiry retired cannot Send until a fresh passage
// is selected: say so instead of the misleading `unsaved` (a retained attempt
// carries its own frozen capture and stays honestly `unsaved`).
function commentStatus(work: DocumentReaderWork, views: readonly AnnotationView[]): DocumentCommentStatus {
  if (work.phase === "expiry-pending") return "expiry-pending";
  if (work.attempted || work.comment.note.trim() !== "") {
    if (!work.attempted && !work.comment.capture) return "reselect the passage";
    return "unsaved";
  }
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
  // The read pipeline runs inside an effect whose closure cannot observe this
  // state changing (its inputs are captured per cycle from
  // latestReadInputs), so the pipeline reads the mirror instead: a cycle must
  // see the CURRENT suspension, including one an earlier cycle set, without
  // waiting for a re-render to land first.
  const authoritySuspendedRef = useRef(false);
  const suspendAuthority = useCallback((suspended: boolean): void => {
    authoritySuspendedRef.current = suspended;
    setAuthoritySuspended(suspended);
  }, []);
  const [scrollElement, setScrollElement] = useState<HTMLElement | null>(null);
  const [contentElement, setContentElement] = useState<HTMLDivElement | null>(null);
  const [, bump] = useState(0);
  // The reopen generation arrives through the stable-snapshot hook: a closing
  // pane's record disappearing must not change it, or the dying pane re-renders
  // into a read that supersedes live peers' tickets.
  const reopen = useReopenGeneration(paneId);
  const visible = usePaneVisible();
  // The read demand is the single scheduler of this reader's pipeline: it
  // coalesces concurrent triggers, paces transient-failure retries while the
  // pane is visible and connected, and retires stale attempts when a newer
  // demand supersedes them - the same engine the legacy reader schedules
  // through (useDocumentRead).
  const readDemandRef = useRef<DocumentReadDemand | undefined>(undefined);
  // Every read captures its inputs from here at start: rebinds and other
  // prop changes refresh the demand instead of re-running an effect.
  const latestReadInputs = useRef<
    | {
        session: string;
        readTarget: string;
        key: DocumentKey;
        ports: DocumentAnnotationPorts;
        work: DocumentReaderWork;
        enterExpiryPending: () => void;
        visible: boolean;
      }
    | undefined
  >(undefined);
  const previousReadIdentity = useRef<{ identity: string; reopen: number } | undefined>(undefined);
  const refreshRead = useCallback((): void => {
    readDemandRef.current?.refresh();
  }, []);
  const hostRef = useRef<HTMLDivElement>(null);
  // The most recent fence's receipt, awaiting this reader's reread: the reread's
  // acceptance proves the fence settled and completes the expired epoch's
  // deletion then. A fault leaves the persisted receipt for the retry sweep.
  const expiryReceiptRef = useRef<ExpiryReceipt | null>(null);
  // Consecutive stale-ticket acceptances while this reader had nothing
  // rendered to keep - a leaseless pane, or a remounted pane still on its
  // loading skeleton. One retry recovers a reader whose ticket a COMPLETED
  // read superseded; a second consecutive stale is the two-reader interleave
  // whose retry the raw render below replaces.
  const leaselessStaleRef = useRef(0);
  // True once this mount has displayed document content (a status:"ok"
  // state). A stale acceptance's early return preserves RENDERED authority -
  // but a remounted pane can hold its retained lease with nothing yet
  // displayed, and returning there would strand the fresh mount on its
  // loading skeleton forever, because a peer's successful acceptance emits
  // no wake. Within a mount, displayed content never goes back off screen
  // (failed rereads keep it), so the flag only ever turns true.
  const renderedContentRef = useRef(false);

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
  // render - the thread retention and cwd subscription effects below keep a
  // cwd change reaching this reader as a render, so the binding re-resolves
  // before any demand cycle can fetch the dead target - while the reader keeps
  // rendering only for the state it already observes, so a closed pane's
  // committed retirement can never be resurrected by an extra render of a
  // reader waiting to unmount.
  const pane = workspaceStore.getState().panes.find((candidate) => candidate.id === paneId);
  const retained = pane ? documentPaneState(pane) : undefined;
  let readTarget = path;
  let reboundReference: FileReference | undefined;
  if (retained) {
    const cwd = threadsStore.getState().threads.get(session)?.cwd;
    if (cwd && cwd !== retained.reference.cwd) {
      reboundReference = rebindFileReference(retained.reference, cwd);
      readTarget = reboundReference.readTarget;
    } else readTarget = retained.reference.readTarget;
  }
  const work = documentReaderWork(paneId, key, ports);
  const destination: DestinationProvenance = origin ?? { kind: "browser-restored-session" };

  // The document owns its resolved binding, not the ephemeral thread model:
  // record the rebound reference on the pane exactly as the workspace's
  // reference-bound reader records its own, so a later mount whose
  // conversation is gone reads the last-known binding instead of the open-time
  // one.
  useLayoutEffect(() => {
    if (!pane || !retained || !reboundReference) return;
    recordDocumentPaneState(pane, {
      reference: reboundReference,
      origin: retained.origin,
      reopen: retained.reopen ?? 0,
    });
  }, [pane, retained, reboundReference]);

  // Retain the owning thread for this document's lifetime, exactly as the
  // workspace's reference-bound reader does: closing the conversation pane
  // must not drop the thread model while a document still resolves its
  // relative binding through the session's cwd. Only a relative retained
  // binding resolves through the cwd - an absolute provenance keeps its exact
  // read target, and a record-less pane reads its raw path - so only that
  // binding keeps the thread model (and its own hydration machinery) alive.
  const retainsOwningThread = retained?.reference.provenance === "relative";
  useEffect(() => {
    if (!retainsOwningThread) return;
    let started = false;
    const tryStart = () => {
      if (started || connectionStore.getState().state !== "ready") return;
      started = true;
      threadsStore
        .getState()
        .ensureThread(session)
        .catch(() => {});
    };
    tryStart();
    const unsubscribe = connectionStore.subscribe(tryStart);
    return () => {
      unsubscribe();
      if (started) threadsStore.getState().releaseThread(session);
    };
  }, [session, retainsOwningThread]);

  // Observe the owning thread's cwd: a change re-renders this pane, the render
  // above re-resolves the rebind, the recording effect persists it and the
  // read-identity effect refreshes the demand with the rebound target, so the
  // reader never fetches the dead binding. The pane-record gate keeps a
  // closing pane's committed retirement from being resurrected by this
  // subscription's extra render.
  useEffect(() => {
    if (!retainsOwningThread) return;
    return threadsStore.subscribe((state, previous) => {
      if (state.threads.get(session)?.cwd === previous.threads.get(session)?.cwd) return;
      if (!workspaceStore.getState().panes.some((candidate) => candidate.id === paneId)) return;
      bump((value) => value + 1);
    });
  }, [session, paneId, retainsOwningThread]);

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
      const retired = work.lease ?? work.attempted?.lease ?? null;
      work.lease = null;
      work.attempted = null;
      work.attemptedComment = null;
      work.phase = phase;
      work.comment = invalidateCommentCapture(work.comment);
      ports.runtime.invalidate(key, retired);
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
        refreshRead();
      },
      () => {},
    );
  }, [work, ports, key, retireAuthority, refreshRead]);

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
      refreshRead();
    }
  }, [work, refreshRead]);

  // A committed wake whose views all name a foreign epoch is a peer's fence
  // committed while this reader still displays the retained snapshot: the
  // expired epoch's rows are already suppressed, so the replacement's
  // operations are the only evidence left. An EMPTY list under retained
  // authority is the same peer fence whose replacement epoch carries no
  // annotations of its own: the suppressed rows are gone and nothing names
  // the new epoch yet, so waiting for epoch evidence would strand the expired
  // authority until some later foreground or remount trigger. Either way,
  // schedule the current read that adopts the replacement epoch (or restores
  // the retained one); the read itself never wakes, so this cannot loop.
  const recoverReplacedEpochOnWake = useCallback(
    (next: readonly AnnotationView[]): void => {
      const authority = work.lease ?? work.attempted?.lease ?? null;
      if (!authority) return;
      if (next.length > 0 && next.some((view) => view.receipt.epoch === authority.epoch)) return;
      refreshRead();
    },
    [work, refreshRead],
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

  // The read demand schedules this reader's pipeline: the same engine the
  // legacy reader schedules through (useDocumentRead). Every read captures
  // its inputs at start, so rebinds and prop changes refresh the demand
  // instead of re-running an effect, concurrent triggers coalesce, and a
  // transient failure paces its retry while this pane is visible and
  // connected. The current ticket is issued BEFORE the raw fetch, so every
  // successful cycle — an unchanged reopen or foreground reread included —
  // advances the durable read generation and revalidates through real
  // acceptance. The repository preserves the durable head/snapshot identity
  // for an exactly identical current read, so a retained immutable attempt
  // stays valid without any pane-local content cache. A stale ticket is
  // ignored, a changed read fences (expiry-pending), and a committed fence
  // is followed by one more current read to adopt the replacement epoch. A
  // failed raw read never replaces the retained work and never accepts
  // fabricated content.
  useLayoutEffect(() => {
    latestReadInputs.current = { session, readTarget, key, ports, work, enterExpiryPending, visible };
  });
  useEffect(() => {
    const readDocument = async (attempt: DocumentReadAttempt): Promise<DocumentReadOutcome> => {
      const inputs = latestReadInputs.current;
      if (!inputs || !attempt.isCurrent()) return "success";
      const { session, readTarget, key, ports, work, enterExpiryPending } = inputs;
      // Display document content: from this point the mount has something
      // rendered that a stale acceptance's early return preserves.
      const displayContent = (displayed: DocFileContent): void => {
        renderedContentRef.current = true;
        setState({ status: "ok", content: displayed });
      };
      // A revalidation of already-rendered content keeps that content on
      // screen: a binding re-record (an origin churn, a restored pane gaining
      // its binding) re-reads the same document, and blanking it to the
      // loading skeleton would tear down the reading position, the selection
      // surface and any bound scope mid-read.
      if (!renderedContentRef.current)
        setState((previous) => (previous.status === "ok" ? previous : { status: "loading" }));

      // A thrown beginRead storage fault is uncertainty, not proof the document
      // changed: fall through to the raw read and suspend usable authority,
      // keeping the exact note/capture/attempt for recovery.
      let ticket: Awaited<ReturnType<typeof ports.repository.beginRead>> | null = null;
      try {
        ticket = await ports.repository.beginRead(key);
      } catch {
        ticket = null;
      }
      if (!attempt.isCurrent()) return "success";

      let content: DocFileContent;
      try {
        content = await readDocFile(session, readTarget, browserDocPort);
      } catch (err) {
        // A failed raw read preserves the exact authored note and unsent work.
        // A failed REREAD also keeps the readable snapshot on screen: a
        // temporary outage must not tear down the document, its highlights or
        // the comment editor. The failure shows alongside them until a later
        // reread succeeds; only a first read without content shows the
        // full-pane error state.
        const kind = errorKind(err);
        if (attempt.isCurrent())
          setState((previous) =>
            previous.status === "ok"
              ? { status: "ok", content: previous.content, rereadError: kind }
              : { status: "error", kind },
          );
        // A transient failure reports through the outcome so the demand paces
        // its retry while this pane stays visible and connected.
        return kind === "error" ? "transient" : "terminal";
      }
      if (!attempt.isCurrent()) return "success";

      if (ticket === null) {
        // The raw fetch succeeded, so its content is real evidence even though
        // the metadata read faulted: a DEFINITE change against any retained
        // capture (the unsaved attempt's, the composed comment's, or the last
        // save's) proves the stale work must retire - the same comparison the
        // acceptance-fault branch makes. The ticket this branch lacks is also
        // the ticket a fence needs, so the retirement must take the durable
        // pending-operation row with it: without the discard, the runtime's
        // next retry pass re-applies the obsolete operation with its old
        // identity. The discard runs FIRST and its own fault falls back to the
        // interim retention below - work, capture and durable row retire
        // together or not at all.
        const evidence =
          work.attempted?.capture.evidence ?? work.comment.capture?.evidence ?? work.saved?.capture.evidence;
        if (evidence && evidenceProvesChange(content, evidence)) {
          const attempted = work.attempted;
          if (attempted) {
            try {
              await ports.repository.discardPendingOperation(attempted.lease.key, attempted.operationId);
            } catch {
              // The discard is durable work under the same flaky storage that
              // faulted the metadata read; keep the exact retained work and
              // let the paced retry attempt the whole retirement again.
              if (!attempt.isCurrent()) return "success";
              displayContent(content);
              suspendAuthority(true);
              return "transient";
            }
            if (!attempt.isCurrent()) return "success";
          }
          // A SAVED annotation awaiting handoff kept no in-memory attempt to
          // retire: its durable row is the expiry machinery's to delete, but
          // its pending handoff must not append an obsolete reference behind
          // the changed source, so suppress the saved epoch until the expiry
          // completes. The recovery acceptance's own changed read then
          // installs the durable fence.
          if (work.saved) ports.repository.suppressEpochForExpiry(work.saved.lease);
          // No ticket was issued, so no fence recorded and no repository
          // expiry work exists to ever wake this reader: the outcome must
          // stay transient, keeping the demand's paced retry alive until a
          // real acceptance fences the proven change and the reread adopts
          // the replacement epoch - recovery then needs no reload, foreground
          // action or remount.
          displayContent(content);
          enterExpiryPending();
          return "transient";
        }
        // Otherwise the fault is uncertainty, not proof: render the real raw
        // content without adopting or fabricating accepted authority; usable
        // highlights/selection stay suspended while the exact attempt, note
        // and prior lease evidence are retained for recovery. The storage
        // fault is not a source change, so the demand paces a retry while
        // this pane is visible and connected; the next cycle revalidates
        // through a fresh ticket and a real acceptance.
        displayContent(content);
        suspendAuthority(true);
        return "transient";
      }

      let accepted: AcceptedReadResult;
      try {
        accepted = await ports.repository.acceptRead(ticket, content);
      } catch {
        if (!attempt.isCurrent()) return "success";
        const evidence =
          work.attempted?.capture.evidence ?? work.comment.capture?.evidence ?? work.saved?.capture.evidence;
        if (evidence && evidenceProvesChange(content, evidence)) {
          // The ticket this acceptance held is gone with the throw, so no
          // fence can ever record this expiry: retiring the in-memory attempt
          // alone would leave the durable pending-operation row reappliable by
          // the runtime's next retry pass - and the throw releases the
          // acceptance's provisional suppression, so that retry WOULD re-apply
          // the expired operation behind the changed source. The discard runs
          // FIRST and its own fault falls back to the interim retention below -
          // work, capture and durable row retire together or not at all.
          const attempted = work.attempted;
          if (attempted) {
            try {
              await ports.repository.discardPendingOperation(attempted.lease.key, attempted.operationId);
            } catch {
              // The discard is durable work under the same storage that
              // faulted the acceptance; keep the exact retained work and let
              // the paced retry attempt the whole retirement again.
              if (!attempt.isCurrent()) return "success";
              displayContent(content);
              suspendAuthority(true);
              return "transient";
            }
          }
          // The same saved-annotation completion as the beginRead-fault branch:
          // the durable row survives for the expiry machinery, the epoch's
          // pending handoff must not append behind the changed source, and
          // the recovery acceptance's own changed read installs the durable
          // fence.
          if (work.saved) ports.repository.suppressEpochForExpiry(work.saved.lease);
          // A DEFINITE change to the annotated source is confirmed expiry even
          // though acceptance threw: retire the stale capture/attempt into the
          // honest expiry-pending phase. The throw also means no fence
          // recorded, so no repository expiry work exists to ever wake this
          // reader: the outcome must stay transient, keeping the demand's
          // paced retry alive until a real acceptance fences the proven
          // change and the reread adopts the replacement epoch - recovery
          // then needs no reload, foreground action or remount.
          displayContent(content);
          enterExpiryPending();
          return "transient";
        }
        // Otherwise the throw is uncertainty, not confirmed expiry: render the
        // raw content, suspend usable authority and retain the exact
        // note/capture/attempt and the shared retry ownership.
        displayContent(content);
        suspendAuthority(true);
        // The acceptance never completed, so the demand paces a retry while
        // this pane is visible and connected; the next cycle revalidates
        // through a fresh ticket and a real acceptance.
        return "transient";
      }
      if (!attempt.isCurrent()) return "success";
      if (accepted.state === "stale-ticket") {
        // A concurrently mounted pane's ticket can be superseded by another
        // pane's issuance alone; this read was honest work against a dead
        // ticket. A pane with something already rendered keeps its rendered
        // authority - its stale foreground rereads stay rendered and the next
        // current acceptance adopts or retires them - so it never retries
        // from here. A REMOUNTED pane can hold its retained lease with
        // nothing yet displayed, and there the early return's justification
        // does not hold: nothing is being kept, and a peer's successful
        // acceptance emits no wake, so the return would strand the fresh
        // mount on its loading skeleton forever. With nothing rendered, the
        // same bounded recovery below applies. A SUSPENDED pane's retained
        // lease is not the usable authority this return protects: the pane is
        // waiting for the revalidation that only its own demand can run, and
        // the winning peer's acceptance emits no wake - returning success here
        // would strand the suspension forever. Suspended authority joins the
        // same bounded paced recovery.
        if (
          !authoritySuspendedRef.current &&
          (work.lease !== null || work.attempted !== null) &&
          renderedContentRef.current
        )
          return "success";
        if (leaselessStaleRef.current === 0) {
          leaselessStaleRef.current = 1;
          readDemandRef.current?.refresh();
          return "success";
        }
        // A SECOND consecutive stale acceptance means another reader's
        // in-flight issuance keeps superseding ours, so an immediate
        // re-issue would only supersede theirs back. The fetch itself
        // succeeded: render its real raw content with suspended authority -
        // the same honest display a failed beginRead produces - and hand the
        // recovery to the demand's paced retry instead of stopping forever:
        // the winning acceptance publishes no wake and this reader has no
        // lease, so nothing else would ever revalidate the pane. The paced
        // retry lands after the competing read has settled and accepts, so
        // both readers eventually regain annotation authority. The bound
        // stays honest: the stale counter is deliberately NOT reset, so one
        // immediate refresh per series (the first stale above) is all the
        // tight-loop interleave there is, and every later stale paces with the
        // demand's growing backoff instead of re-arming the immediate path.
        displayContent(content);
        suspendAuthority(true);
        return "transient";
      }
      displayContent(content);
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
          ports.runtime.invalidate(key, priorAuthority);
        }
        work.lease = accepted.lease;
        work.phase = work.attempted ? "unsaved" : "reading";
        suspendAuthority(false);
        setIdentity({ displayedSnapshotId: accepted.lease.displayedSnapshotId, mappingId: createSecureUUID() });
        publishReader(work);
        const fencedExpiry = expiryReceiptRef.current;
        if (fencedExpiry !== null) {
          expiryReceiptRef.current = null;
          // This acceptance proves the fence committed, so the expired epoch's
          // deletion can complete now. A faulting deletion leaves the
          // persisted receipt unfinished for the next retry pass's sweep, and
          // registering the runtime's retry need makes the NEXT real storage
          // success - this reader's next read cycle, a peer's - start that
          // pass instead of waiting for a foreground return or a composer-open
          // event. The reader tries once per acceptance and never blocks
          // adoption on it.
          void ports.repository.finishExpiry(fencedExpiry).catch(() => ports.runtime.registerRetryNeed());
        }
        return "success";
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
        refreshRead();
        return "success";
      }
      // A receipt-less pending means the fence itself faulted: the repository
      // retained the expiry work for its own retry, but that retry only runs
      // on a later storage success. Keep the outcome transient so THIS reader
      // paces its own recovery: the next cycle re-fences, and once the fence
      // commits the reread adopts the replacement epoch - no reopen,
      // foreground action or Reload required.
      return "transient";
    };
    const demand = createDocumentReadDemand(readDocument);
    readDemandRef.current = demand;
    const updateActive = () =>
      demand.setActive(
        (latestReadInputs.current?.visible ?? false) &&
          document.visibilityState !== "hidden" &&
          connectionStore.getState().state === "ready",
      );
    const unsubscribeConnection = connectionStore.subscribe((state, prior) => {
      updateActive();
      if (state.client !== prior.client && readyConnectionTransition(state, prior) && prior.state === "ready")
        demand.refresh();
    });
    // A resync names an exact session. There is no remote-attachment-ready
    // notification, so transient attachment failures also retain paced demand.
    const unsubscribeNotifications = onConnectionNotification((notification) => {
      if (
        notification.method === "evener/thread/resync" &&
        notification.params.ref === latestReadInputs.current?.session
      )
        demand.refresh();
    });
    // Every visibility transition recomputes the demand's activity: the demand
    // drops refreshes while inactive, so a pane mounted while the document was
    // hidden would otherwise depend on a committed wake or an annotation-retry
    // publication (which storage faults can delay) to start reading, and a
    // hidden document would keep armed paced retries under a stale active
    // state. The refresh runs FIRST - an inactive demand drops it, and the
    // activation's own series then reads exactly once, while an already-active
    // foreground keeps its genuine reread with the recompute a no-op.
    const onForeground = (): void => {
      demand.refresh();
      updateActive();
    };
    document.addEventListener("visibilitychange", onForeground);
    updateActive();
    return () => {
      demand.dispose();
      readDemandRef.current = undefined;
      unsubscribeConnection();
      unsubscribeNotifications();
      document.removeEventListener("visibilitychange", onForeground);
    };
  }, [refreshRead, suspendAuthority]);
  useLayoutEffect(() => {
    const inputs = latestReadInputs.current;
    if (!inputs) return;
    const identity = JSON.stringify([inputs.session, inputs.key, inputs.readTarget]);
    const previous = previousReadIdentity.current;
    previousReadIdentity.current = { identity, reopen };
    readDemandRef.current?.setActive(
      inputs.visible && document.visibilityState !== "hidden" && connectionStore.getState().state === "ready",
    );
    if (previous === undefined) return;
    if (previous.identity !== identity || previous.reopen !== reopen) readDemandRef.current?.refresh();
  });

  // The PaneScaffold body is the real scroll owner: retain and restore its
  // position across host swaps.
  // A callback ref publishes the real PaneScaffold body (the host's parent) as
  // the scroll owner, and restores the retained position on each swap.
  const bindHost = useCallback((node: HTMLDivElement | null) => {
    hostRef.current = node;
    setContentElement(node);
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
  // The pass also runs once on the FIRST accepted map of a mount that holds no
  // failed save of its own: an unfinished persisted expiry (a deletion that
  // faulted before this mount, in this runtime or a previous one) is retried
  // by the pass's sweep, so reopening or reloading a document completes it
  // without waiting for a foreground return or a composer-open event.
  const readerOpenRetryDoneRef = useRef(false);
  const handleMap = useCallback(
    (map: BoundDocumentMap | null) => {
      setBoundMap(map);
      if (
        map &&
        !authoritySuspended &&
        work.lease &&
        map.identity.displayedSnapshotId === work.lease.displayedSnapshotId
      ) {
        if (work.attempted) {
          readerOpenRetryDoneRef.current = true;
          void retryDocumentReaders("reader-open");
        } else if (!readerOpenRetryDoneRef.current) {
          // The first accepted map of a mount with no failed save needs only
          // the runtime's own pass - the sweep of unfinished persisted
          // expiries - not the reader-refresh publications the shared entry
          // point also performs, whose bumps land after any pass join and so
          // escape the test act windows around a plain mount.
          readerOpenRetryDoneRef.current = true;
          void ports.runtime.retryPending("reader-open");
        }
      }
    },
    [work, authoritySuspended, ports],
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
  // The subscription belongs to the rendered host NODE, not to a render: the
  // host only exists once content is displayed and can swap across state
  // transitions, so the effect follows the node - and within one node's
  // lifetime it reads the display state and the accepted map at event time,
  // so a revalidation read landing mid-gesture rebinds the map without
  // cancelling the armed gesture. The finished selection captures through the
  // current map; a display that is not the accepted snapshot captures nothing.
  const selectionTargetRef = useRef<{ state: typeof state; map: typeof boundForLease } | null>(null);
  useLayoutEffect(() => {
    selectionTargetRef.current = { state, map: boundForLease };
  });
  useEffect(() => {
    const root = contentElement;
    if (!root) return;
    return observeFinishedDocumentSelections({
      root,
      getMap: () => {
        const target = selectionTargetRef.current;
        return target && target.state.status === "ok" ? target.map : null;
      },
      onFinish: (selection) => {
        const target = selectionTargetRef.current;
        if (target?.state.status !== "ok") return;
        work.comment = selectCommentPassage(work.comment, buildCapture(selection, target.state.content));
        publishReader(work);
      },
    });
  }, [work, contentElement]);

  const status = commentStatus(work, epochViews);
  const boxVisible = work.attempted !== null || work.comment.capture !== undefined || work.comment.note.trim() !== "";
  const saving = work.phase === "saving";

  const onNote = (note: string): void => {
    work.comment = editCommentText(work.comment, note);
    publishReader(work);
  };
  const onSelection = (selection: CommentSelection): void => {
    // Retained for the next mount's restore; nothing renders from it, so no
    // publish - a keystroke must not churn render listeners.
    work.commentSelection = selection;
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
    work.commentSelection = null;
    work.attempted = null;
    work.attemptedComment = null;
    if (work.phase !== "reading" && work.phase !== "expiry-pending") work.phase = "reading";
    publishReader(work);
  };

  // The reader owns its scaffold so the comment controls can sit in the
  // scaffold's footer: a sibling of the scrolling body, inside the pane's own
  // visible area. Placing them after the Markdown inside the scroll body left
  // them at the document's end, past the reading position on long documents.
  // Retained comment work must stay reachable in EVERY reader state: while a
  // remount's first fetch is still loading or has failed, the note, its
  // copyable reference and Cancel remain usable, independently of document
  // content. Only a pane with no retained work keeps the bare footer shapes
  // the loading and error states always had.
  const commentBoxFooter = boxVisible ? (
    <DocumentCommentBox
      work={work.comment}
      onNote={onNote}
      onSend={onSend}
      onCancel={onCancel}
      saving={saving}
      status={status}
      suspended={authoritySuspended}
      retainedSelection={work.commentSelection}
      onSelection={onSelection}
    />
  ) : null;
  const statusFooter = (
    <div className={CLASS.status} role="status">
      {status}
    </div>
  );
  const scaffold = (children: ReactNode, footer?: ReactNode) => (
    <PaneScaffold
      title={filenameOf(path)}
      paneId={paneId}
      focused={focused}
      actions={
        // The same foreground reread control the legacy reader exposes: a
        // press refreshes the demand, which retires the in-flight attempt
        // and starts a fresh ticket-before-fetch cycle.
        <Button size="sm" variant="quiet" onClick={refreshRead}>
          Reload
        </Button>
      }
      scaffoldMarker="doc-reader-scroll"
      footer={footer}
    >
      {children}
    </PaneScaffold>
  );

  if (state.status === "loading") return scaffold(<Skeleton />, commentBoxFooter);
  if (state.status === "error") {
    const copy = ERROR_COPY[state.kind];
    return scaffold(<EmptyState title={copy.title} hint={copy.hint} />, commentBoxFooter ?? statusFooter);
  }

  const { content } = state;
  if (content.binary) {
    // A text-to-binary reread can strand an unsent note in retained work -
    // the same reachability the loading and error states owe it.
    return scaffold(
      <EmptyState title="Binary file not shown" hint={`${filenameOf(path)} (${formatDocBytes(content.sizeBytes)})`} />,
      commentBoxFooter ?? statusFooter,
    );
  }

  return scaffold(
    <>
      {state.rereadError !== undefined && (
        <div className={CLASS.notice}>
          <Chip tone="attention">Refresh failed</Chip>
          <span className={CLASS.noticeText}>
            {ERROR_COPY[state.rereadError].hint} Still showing the last loaded version.
          </span>
        </div>
      )}
      {content.truncated && <TruncationNotice content={content} />}
      {isMarkdownPath(path) ? (
        <div className={CLASS.markdown} ref={bindHost}>
          <DocumentMarkdown source={content.text} identity={identity} onMap={handleMap} />
          {boundForLease && scrollElement && canMeasureRanges() && (
            <DocumentHighlights
              map={boundForLease}
              annotations={epochViews}
              scrollElement={scrollElement}
              contentElement={contentElement}
            />
          )}
        </div>
      ) : (
        <CodeBlock text={content.text} fold={false} />
      )}
    </>,
    commentBoxFooter ?? statusFooter,
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
