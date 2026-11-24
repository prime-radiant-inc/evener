// Local to the settings sections: the gate's states are section chrome, not a widget.
import type { ReactNode } from "react";
import { Button, InlineNotice, Skeleton } from "../../../widgets";

export interface SectionGateProps {
  loading: boolean;
  error: string | null;
  title: string;
  onRetry: () => void;
  skeletonLines?: number;
}

export function SectionGate({ loading, error, title, onRetry, skeletonLines }: SectionGateProps) {
  if (loading) return <Skeleton lines={skeletonLines} />;
  if (error === null) return null;
  return (
    <InlineNotice
      tone="danger"
      live="status"
      data-reason="unavailable"
      title={title}
      action={
        <Button size="sm" onClick={onRetry}>
          Retry
        </Button>
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
