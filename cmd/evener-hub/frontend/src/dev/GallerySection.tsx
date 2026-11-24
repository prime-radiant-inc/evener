import type { ReactNode } from "react";
import { ThemeFlip } from "./ThemeFlip";

/**
 * Shared scaffold for a widget gallery section: the section heading, an
 * optional note, and the section's demo rows in both theme panes. Lives
 * outside gallery-sections/ so WidgetGallery's eager `./gallery-sections/*.tsx`
 * glob never treats this helper as a section of its own.
 *
 * A section whose demos need more than one theme flip (one block per dialog,
 * sheet side, or variant) passes `flips` instead of `children`; each block
 * gets its own pair of theme panes.
 */
export function GallerySection({
  title,
  testId,
  note,
  flips,
  children,
}: {
  title: string;
  testId?: string;
  note?: ReactNode;
  flips?: readonly ReactNode[];
  children?: ReactNode;
}) {
  return (
    <section data-testid={testId}>
      <h2>{title}</h2>
      {note}
      {(flips ?? [children]).map((block, index) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: caller-authored fixed blocks, order never changes
        <ThemeFlip key={index}>{block}</ThemeFlip>
      ))}
    </section>
  );
}
