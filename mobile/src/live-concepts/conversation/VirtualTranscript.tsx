import type { HTMLAttributes, ReactElement, ReactNode } from "react";

export interface VirtualTranscriptProps
  extends Omit<HTMLAttributes<HTMLElement>, "children"> {
  readonly children: ReactNode;
}

/**
 * Bounded structural adapter for the transcript's one scroll owner. Task 3
 * replaces the children strategy with shared windowing without changing the
 * frame boundary or scroll ownership.
 */
export function VirtualTranscript({
  children,
  ...attributes
}: VirtualTranscriptProps): ReactElement {
  return (
    <section
      {...attributes}
      aria-label="Transcript"
      data-page-scroll-owner="true"
    >
      {children}
    </section>
  );
}
