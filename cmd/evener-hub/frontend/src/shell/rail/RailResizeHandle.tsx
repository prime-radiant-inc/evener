// The docked rail's right-edge resize handle. Delegates mechanics to the
// shared EdgeResizeHandle used by both the left rail and desktop Overview.
import type { RefObject } from "react";
import { SIDEBAR_WIDTH_DEFAULT, SIDEBAR_WIDTH_MAX, sidebarWidthMin } from "../../stores/prefs";
import { EdgeResizeHandle } from "../EdgeResizeHandle";

// The custom property Rail.module.css's own `.rail` width reads. Set inline by
// Rail on every render, and imperatively by the drag below.
export const RAIL_WIDTH_PROPERTY = "--rail-width";

export interface RailResizeHandleProps {
  // The currently displayed width, and where a released drag / keyboard step lands.
  width: number;
  minWidth?: number;
  maxWidth?: number;
  onCommit: (width: number) => void;
  // The element being resized: the drag paints its --rail-width directly.
  railRef: RefObject<HTMLElement | null>;
}

export function RailResizeHandle({
  width,
  minWidth,
  maxWidth = SIDEBAR_WIDTH_MAX,
  onCommit,
  railRef,
}: RailResizeHandleProps) {
  return (
    <EdgeResizeHandle
      edge="right"
      width={width}
      minWidth={minWidth ?? sidebarWidthMin()}
      maxWidth={maxWidth}
      defaultWidth={SIDEBAR_WIDTH_DEFAULT}
      cssProperty={RAIL_WIDTH_PROPERTY}
      label="Resize sidebar"
      testId="rail-resize-handle"
      onCommit={onCommit}
      targetRef={railRef}
    />
  );
}
