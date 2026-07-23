import { useEffect, useState } from "react";
import {
  DOC_FILE_MAX_BYTES,
  type DocFileContent,
  DocFileError,
  type DocFileErrorKind,
  docImageURL,
  readDocFile,
} from "../../protocol/docContent";
import type { PaneProps } from "../../shell/paneRegistry";
import { Chip, Dialog, EmptyState, Markdown, PaneScaffold, Skeleton } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { filenameOf, formatDocBytes, isMarkdownPath } from "./docFile";
import styles from "./docpane.module.css";
import type { DocParams } from "./openDoc";

const CLASS = {
  pre: requireClass(styles.pre, "docpane.module.css", "pre"),
  imageButton: requireClass(styles.imageButton, "docpane.module.css", "imageButton"),
  image: requireClass(styles.image, "docpane.module.css", "image"),
  lightboxImg: requireClass(styles.lightboxImg, "docpane.module.css", "lightboxImg"),
  notice: requireClass(styles.notice, "docpane.module.css", "notice"),
  noticeText: requireClass(styles.noticeText, "docpane.module.css", "noticeText"),
};

// Error kind -> empty-state copy. The raw endpoint shares the HTML variant's
// guard/status contract exactly (cmd/serf-hub/doc_serve.go:57-73): 403 for a
// path that escapes the session cwd, 404 for a missing file or an unknown /
// non-local session, and a generic error for anything else.
const ERROR_COPY: Record<DocFileErrorKind, { title: string; hint: string }> = {
  forbidden: { title: "Access denied", hint: "This path is outside the session's working directory." },
  "not-found": { title: "File not available", hint: "This file was not found in the session's working directory." },
  error: { title: "Couldn't load file", hint: "The hub returned an unexpected error." },
};

type FileState =
  | { status: "loading" }
  | { status: "error"; kind: DocFileErrorKind }
  | { status: "ok"; content: DocFileContent };

function DocFileView({ session, path }: { session: string; path: string }) {
  const [state, setState] = useState<FileState>({ status: "loading" });

  useEffect(() => {
    let cancelled = false;
    setState({ status: "loading" });
    readDocFile(session, path).then(
      (content) => {
        if (!cancelled) setState({ status: "ok", content });
      },
      (err: unknown) => {
        if (!cancelled) setState({ status: "error", kind: err instanceof DocFileError ? err.kind : "error" });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [session, path]);

  if (state.status === "loading") return <Skeleton />;
  if (state.status === "error") {
    const copy = ERROR_COPY[state.kind];
    return <EmptyState title={copy.title} hint={copy.hint} />;
  }

  const { content } = state;
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
      {isMarkdownPath(path) ? <Markdown source={content.text} /> : <pre className={CLASS.pre}>{content.text}</pre>}
    </>
  );
}

function DocImageView({ session, path }: { session: string; path: string }) {
  const [failed, setFailed] = useState(false);
  const [zoomed, setZoomed] = useState(false);
  const name = filenameOf(path);
  const src = docImageURL(session, path);

  if (failed) {
    return (
      <EmptyState
        title="Image not available"
        hint="This image could not be loaded from the session's working directory."
      />
    );
  }

  return (
    <>
      <button type="button" aria-label="Zoom image" className={CLASS.imageButton} onClick={() => setZoomed(true)}>
        <img data-testid="doc-image" className={CLASS.image} src={src} alt={name} onError={() => setFailed(true)} />
      </button>
      {zoomed && (
        <Dialog open onClose={() => setZoomed(false)} title={name}>
          <img data-testid="doc-lightbox-img" className={CLASS.lightboxImg} src={src} alt={name} />
        </Dialog>
      )}
    </>
  );
}

// The native doc-viewer pane: an image (raw bytes via /doc/image, in a
// click-to-zoom lightbox) or a file (raw bytes via /doc/file?format=raw,
// rendered as sanitized markdown, escaped text, or a binary notice). It
// replaces the legacy iframe-to-HTML-page boundary the rewrite removes.
export default function DocPane({ params }: PaneProps<DocParams>) {
  return (
    <PaneScaffold title={filenameOf(params.path)}>
      {params.kind === "image" ? (
        <DocImageView session={params.session} path={params.path} />
      ) : (
        <DocFileView session={params.session} path={params.path} />
      )}
    </PaneScaffold>
  );
}
