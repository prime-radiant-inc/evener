// Local to the settings sections: the gate's states are section chrome, not a widget.
import type { ReactNode } from "react";
import { Button, InlineNotice, Skeleton, VisuallyHidden } from "../../../widgets";

export interface SectionGateProps {
  loading: boolean;
  error: string | null;
  title?: string;
  onRetry?: () => void;
  skeletonLines?: number;
  /** The loaded body. Omit when the caller renders its own body after the gate. */
  children?: ReactNode;
}

export function SectionGate({ loading, error, title, onRetry, skeletonLines, children }: SectionGateProps) {
  // The polite region is mounted from the first render and only its text
  // changes, so a first-load failure is announced: a region created with its
  // content already inside may never be announced. The visible notice and its
  // Retry stay outside the region (LaunchConfigForm.tsx's status line is the
  // same shape).
  const failed = !loading && error !== null;
  const announcement = failed ? (title === undefined ? error : `${title}: ${error}`) : "";
  return (
    <>
      <VisuallyHidden>
        <span role="status" data-testid="section-gate-status" data-reason={failed ? "unavailable" : undefined}>
          {announcement}
        </span>
      </VisuallyHidden>
      {loading ? (
        <Skeleton lines={skeletonLines} />
      ) : error === null ? (
        children
      ) : (
        <InlineNotice
          tone="danger"
          live="off"
          title={title}
          action={
            onRetry ? (
              <Button size="sm" onClick={onRetry}>
                Retry
              </Button>
            ) : undefined
          }
        >
          {error}
        </InlineNotice>
      )}
    </>
  );
}

export function RefreshErrorNotice({ onRetry, children }: { onRetry: () => void; children: ReactNode }) {
  return (
    <InlineNotice
      tone="danger"
      live="status"
      data-reason="refresh-failed"
      action={
        <Button type="button" onClick={onRetry}>
          Retry
        </Button>
      }
    >
      {children}
    </InlineNotice>
  );
}
