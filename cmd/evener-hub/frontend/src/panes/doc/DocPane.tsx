import {
  bindFilePath,
  DOC_FILE_MAX_BYTES,
  type DocFileContent,
  type DocFileErrorKind,
  docImageReadURL,
  type FileReference,
  rebindFileReference,
} from "@evener/appwire-client/docContent";
import { type ReactNode, useEffect, useLayoutEffect, useState } from "react";
import type { PaneProps } from "../../shell/paneRegistry";
import { usePaneVisible } from "../../shell/paneVisibility";
import { documentPaneState, recordDocumentPaneState, useWorkspaceStore } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { threadsStore, useThreadsStore } from "../../stores/threads";
import { Button, Chip, Dialog, EmptyState, PaneScaffold, Skeleton } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { Markdown } from "../../widgets/markdown";
import { browserDocPort } from "./browserDocPort";
import { filenameOf, formatDocBytes, isMarkdownPath } from "./docFile";
import styles from "./docpane.module.css";
import type { DocParams } from "./openDoc";
import { useDocumentRead } from "./useDocumentRead";

const CLASS = {
  markdown: requireClass(styles.markdown, "docpane.module.css", "markdown"),
  pre: requireClass(styles.pre, "docpane.module.css", "pre"),
  imageButton: requireClass(styles.imageButton, "docpane.module.css", "imageButton"),
  image: requireClass(styles.image, "docpane.module.css", "image"),
  lightboxImg: requireClass(styles.lightboxImg, "docpane.module.css", "lightboxImg"),
  notice: requireClass(styles.notice, "docpane.module.css", "notice"),
  noticeText: requireClass(styles.noticeText, "docpane.module.css", "noticeText"),
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

function DocFileView({ content, path }: { content: DocFileContent; path: string }) {
  if (content.binary) {
    return (
      <EmptyState title="Binary file not shown" hint={`${filenameOf(path)} (${formatDocBytes(content.sizeBytes)})`} />
    );
  }
  return (
    <>
      {content.truncated && (
        <div className={CLASS.notice}>
          <Chip tone="attention">Truncated</Chip>
          <span className={CLASS.noticeText}>
            {content.totalBytes !== undefined
              ? `Showing the first ${formatDocBytes(DOC_FILE_MAX_BYTES)} of ${formatDocBytes(content.totalBytes)}.`
              : `Showing the first ${formatDocBytes(DOC_FILE_MAX_BYTES)}.`}
          </span>
        </div>
      )}
      {isMarkdownPath(path) ? (
        <div className={CLASS.markdown}>
          <Markdown source={content.text} />
        </div>
      ) : (
        <pre className={CLASS.pre}>{content.text}</pre>
      )}
    </>
  );
}

function DocImageView({ src, path }: { src: string; path: string }) {
  const [zoomed, setZoomed] = useState(false);
  const name = filenameOf(path);
  return (
    <>
      <button type="button" aria-label="Zoom image" className={CLASS.imageButton} onClick={() => setZoomed(true)}>
        <img data-testid="doc-image" className={CLASS.image} src={src} alt={name} />
      </button>
      {zoomed && (
        <Dialog open onClose={() => setZoomed(false)} title={name}>
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

export default function DocPane({ params, paneId, focused }: PaneProps<DocParams>) {
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

  const reference = retained
    ? cwd && cwd !== retained.reference.cwd
      ? rebindFileReference(retained.reference, cwd)
      : retained.reference
    : cwd
      ? bindFilePath(params.path, cwd)
      : undefined;
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
