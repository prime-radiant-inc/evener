import type { ReactNode } from "react";
import { ThemeFlip } from "./ThemeFlip";

/**
 * Shared scaffold for a widget gallery section: the section heading followed
 * by the two-theme flip around the section's demo rows. Lives outside
 * gallery-sections/ so WidgetGallery's eager `./gallery-sections/*.tsx` glob
 * never treats this helper as a section of its own.
 */
export function GallerySection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section>
      <h2>{title}</h2>
      <ThemeFlip>{children}</ThemeFlip>
    </section>
  );
}
