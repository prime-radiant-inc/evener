import type { JSX, MouseEvent } from "react";
import { useCallback, useEffect, useReducer, useRef } from "react";
import {
  renderSafeMarkdown,
  type TrustedHTMLString,
} from "../../conversation/markdown";
import type { AttachmentRef } from "../../conversation/model";
import type {
  ViewerFetchResult,
  ViewerService,
} from "../../services/viewerService";
import { isViewerError } from "../../services/viewerService";

export interface SafeViewerProps {
  readonly attachment: AttachmentRef;
  readonly viewerService: ViewerService;
  readonly onExternalLink?: (url: string) => void;
  readonly onClose: () => void;
}

// --- state machine ----------------------------------------------------------

type ViewerState =
  | { readonly phase: "loading" }
  | {
      readonly phase: "image";
      readonly objectUrl: string;
      readonly contentType: string;
    }
  | {
      readonly phase: "text";
      readonly text: string;
      readonly markdown: boolean;
    }
  | { readonly phase: "unsupported"; readonly format: string }
  | {
      readonly phase: "error";
      readonly message: string;
      readonly kind: "fetch_failed" | "oversized" | "invalid_utf8";
    }
  | { readonly phase: "cancelled" };

type ViewerAction =
  | { readonly type: "reset" }
  | { readonly type: "fetched"; readonly result: ViewerFetchResult }
  | {
      readonly type: "error";
      readonly code: string;
      readonly message: string;
      readonly format?: string;
    }
  | { readonly type: "cancelled" };

function viewerReducer(state: ViewerState, action: ViewerAction): ViewerState {
  switch (action.type) {
    case "reset":
      return { phase: "loading" };
    case "fetched": {
      if (state.phase !== "loading") return state;
      if (action.result.kind === "image") {
        const objectUrl = URL.createObjectURL(
          new Blob([action.result.bytes.slice().buffer], {
            type: action.result.contentType,
          }),
        );
        return {
          phase: "image",
          objectUrl,
          contentType: action.result.contentType,
        };
      }
      const markdown = action.result.contentType === "text/markdown";
      const text = new TextDecoder("utf-8").decode(action.result.bytes);
      return { phase: "text", text, markdown };
    }
    case "error": {
      if (state.phase !== "loading") return state;
      if (action.code === "unsupported_format") {
        return { phase: "unsupported", format: action.format ?? "unknown" };
      }
      if (action.code === "cancelled") {
        return { phase: "cancelled" };
      }
      return {
        phase: "error",
        message: action.message,
        kind: action.code as "fetch_failed" | "oversized" | "invalid_utf8",
      };
    }
    case "cancelled": {
      if (state.phase !== "loading") return state;
      return { phase: "cancelled" };
    }
    default:
      return state;
  }
}

// --- component --------------------------------------------------------------

/**
 * Safe authenticated document/image viewer. Fetches attachment bytes through a
 * ViewerService (which validates HTTP status, content type, magic-byte
 * signatures, byte count, and UTF-8 validity), then renders only supported
 * media:
 *
 * - Images (JPEG/PNG/WebP/GIF ≤20 MiB) via an object URL that is revoked on
 *   close/unmount.
 * - Text/Markdown (≤2 MiB, valid UTF-8) as plain text or sanitized Markdown
 *   via `renderSafeMarkdown`. Never loads remote embedded images.
 *
 * Unsupported formats (HTML, PDF, executables, archives, unknown binary) show
 * an explicit error row naming the format. Fetch failures, oversized bodies,
 * and invalid UTF-8 show a distinct error state. Cancellation shows a neutral
 * cancelled state.
 */
export function SafeViewer({
  attachment,
  viewerService,
  onExternalLink,
  onClose,
}: SafeViewerProps): JSX.Element {
  const [state, dispatch] = useReducer(viewerReducer, { phase: "loading" });
  // Track the object URL so it can be revoked on unmount/close, even after a
  // state transition away from "image".
  const objectUrlRef = useRef<string | null>(null);
  const closedRef = useRef(false);

  // Revoke the current object URL (if any). Idempotent.
  const revokeObjectUrl = useCallback(() => {
    if (objectUrlRef.current !== null) {
      URL.revokeObjectURL(objectUrlRef.current);
      objectUrlRef.current = null;
    }
  }, []);

  // Fetch on mount (or when attachment / service changes).
  useEffect(() => {
    closedRef.current = false;
    // Reset to loading so a new fetch can dispatch results/errors regardless
    // of the previous phase. The sync-revocation effect below handles cleanup.
    dispatch({ type: "reset" });
    let active = true;
    viewerService
      .fetchAttachment(attachment.src, attachment.mediaType)
      .then((result) => {
        if (!active) {
          // Late result after unmount: revoke any object URL the reducer would
          // create. We re-run validation-free revocation by handling it below.
          if (result.kind === "image") {
            const late = URL.createObjectURL(
              new Blob([result.bytes.slice().buffer], {
                type: result.contentType,
              }),
            );
            URL.revokeObjectURL(late);
          }
          return;
        }
        dispatch({ type: "fetched", result });
      })
      .catch((cause: unknown) => {
        if (!active) return;
        if (isViewerError(cause)) {
          dispatch({
            type: "error",
            code: cause.code,
            message: cause.message,
            format: cause.format,
          });
        } else {
          dispatch({
            type: "error",
            code: "fetch_failed",
            message: cause instanceof Error ? cause.message : String(cause),
          });
        }
      });
    return () => {
      active = false;
      viewerService.cancel(attachment.src);
    };
  }, [attachment.src, attachment.mediaType, viewerService]);

  // Keep objectUrlRef in sync with image state, and revoke on transition out.
  useEffect(() => {
    if (state.phase === "image") {
      objectUrlRef.current = state.objectUrl;
    } else {
      revokeObjectUrl();
    }
  }, [state, revokeObjectUrl]);

  // Revoke on unmount.
  useEffect(() => {
    return () => {
      revokeObjectUrl();
    };
  }, [revokeObjectUrl]);

  // Close handler — revoke before closing.
  const handleClose = useCallback(() => {
    if (closedRef.current) return;
    closedRef.current = true;
    revokeObjectUrl();
    viewerService.cancel(attachment.src);
    onClose();
  }, [attachment.src, onClose, revokeObjectUrl, viewerService]);

  // External link handler for Markdown text content.
  const handleLinkClick = useCallback(
    (event: MouseEvent<HTMLDivElement>) => {
      if (!onExternalLink) return;
      const target = event.target as HTMLElement | null;
      const anchor = target?.closest("a") as HTMLAnchorElement | null;
      if (!anchor) return;
      event.preventDefault();
      onExternalLink(anchor.href);
    },
    [onExternalLink],
  );

  // --- render by phase ---

  if (state.phase === "loading") {
    return (
      <div
        className="evener-viewer evener-viewer--loading"
        role="status"
        aria-live="polite"
      >
        <div className="evener-viewer__spinner" aria-hidden="true" />
        <p className="evener-viewer__status">Loading…</p>
        <button
          type="button"
          className="evener-viewer__close"
          onClick={handleClose}
        >
          Close
        </button>
      </div>
    );
  }

  if (state.phase === "image") {
    return (
      <div className="evener-viewer evener-viewer--image">
        <img
          className="evener-viewer__image"
          src={state.objectUrl}
          alt={attachment.name ?? attachment.id}
        />
        <button
          type="button"
          className="evener-viewer__close"
          onClick={handleClose}
        >
          Close
        </button>
      </div>
    );
  }

  if (state.phase === "text") {
    if (state.markdown) {
      const trusted: TrustedHTMLString = renderSafeMarkdown(state.text);
      return (
        <div className="evener-viewer evener-viewer--text evener-viewer--markdown">
          {/* biome-ignore lint/a11y/noStaticElementInteractions: click is event-delegated to nested <a> elements; links remain keyboard-accessible natively */}
          {/* biome-ignore lint/a11y/useKeyWithClickEvents: the handler delegates to focusable anchors that already support keyboard activation */}
          <div
            className="evener-viewer__markdown"
            // biome-ignore lint/security/noDangerouslySetInnerHtml: HTML is produced by renderSafeMarkdown (DOMPurify-sanitized, raw-HTML-stripped). Same rules as AssistantMessage.
            dangerouslySetInnerHTML={{ __html: trusted.html }}
            onClick={handleLinkClick}
          />
          <button
            type="button"
            className="evener-viewer__close"
            onClick={handleClose}
          >
            Close
          </button>
        </div>
      );
    }
    return (
      <div className="evener-viewer evener-viewer--text">
        <pre className="evener-viewer__plaintext">{state.text}</pre>
        <button
          type="button"
          className="evener-viewer__close"
          onClick={handleClose}
        >
          Close
        </button>
      </div>
    );
  }

  if (state.phase === "unsupported") {
    return (
      <div className="evener-viewer evener-viewer--unsupported" role="alert">
        <p className="evener-viewer__error-title">
          Unsupported format: {state.format}
        </p>
        <p className="evener-viewer__error-detail">
          This content type cannot be viewed in the mobile app.
        </p>
        <button
          type="button"
          className="evener-viewer__close"
          onClick={handleClose}
        >
          Close
        </button>
      </div>
    );
  }

  if (state.phase === "error") {
    return (
      <div className="evener-viewer evener-viewer--error" role="alert">
        <p className="evener-viewer__error-title">Unable to load content</p>
        <p className="evener-viewer__error-detail">{state.message}</p>
        <button
          type="button"
          className="evener-viewer__close"
          onClick={handleClose}
        >
          Close
        </button>
      </div>
    );
  }

  // cancelled
  return (
    <div className="evener-viewer evener-viewer--cancelled" role="status">
      <p className="evener-viewer__status">Cancelled</p>
      <button
        type="button"
        className="evener-viewer__close"
        onClick={handleClose}
      >
        Close
      </button>
    </div>
  );
}
