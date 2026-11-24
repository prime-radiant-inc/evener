// Local to the settings sections: the gate's states are section chrome, not a widget.
import type { ReactNode } from "react";
import { Button, InlineNotice, Skeleton } from "../../../widgets";

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
  if (loading) return <Skeleton lines={skeletonLines} />;
  if (error === null) return <>{children}</>;
  return (
    <InlineNotice
      tone="danger"
      live="status"
      data-reason="unavailable"
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
