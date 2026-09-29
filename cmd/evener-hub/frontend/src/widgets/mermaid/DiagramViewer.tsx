import { type PointerEvent as ReactPointerEvent, type WheelEvent as ReactWheelEvent, useRef, useState } from "react";
import { CopyButton } from "../copybutton";
import dialogStyles from "../dialog/dialog.module.css";
import { OverlayPanel } from "../dialog/OverlayPanel";
import { requireClass } from "../internal/requireClass";
import styles from "./mermaid.module.css";

const CLASS = {
  viewerPanel: requireClass(styles.viewerPanel, "mermaid.module.css", "viewerPanel"),
  viewerBody: requireClass(styles.viewerBody, "mermaid.module.css", "viewerBody"),
  zoomSurface: requireClass(styles.zoomSurface, "mermaid.module.css", "zoomSurface"),
  zoomContent: requireClass(styles.zoomContent, "mermaid.module.css", "zoomContent"),
  source: requireClass(styles.source, "mermaid.module.css", "source"),
};

// The panel chassis is dialog.module.css's shared .panel (Dialog and Sheet both
// start from it, see widgets/dialog/index.tsx's BASE_PANEL_CLASS). This class
// adds only the viewer's near-viewport geometry on top, the same way the
// dialog's own variant classes do.
const BASE_PANEL_CLASS = requireClass(dialogStyles.panel, "dialog.module.css", "panel");
const PANEL_CLASS = `${BASE_PANEL_CLASS} ${CLASS.viewerPanel}`;

// The zoom bounds: a quarter-size floor keeps a diagram visible while zoomed
// far out, and an eight-times ceiling is the useful upper end before the SVG's
// own rasterization turns to mush. Both are deliberate product bounds.
const MIN_SCALE = 0.25;
const MAX_SCALE = 8;
const ZOOM_STEP = 1.2;

export interface DiagramViewerProps {
  open: boolean;
  /** Sanitized SVG markup (security.ts's two layers already ran upstream). */
  svg: string;
  /** The original mermaid source, shown by the source toggle and copied. */
  source: string;
  onClose: () => void;
}

/**
 * Fullscreen viewer for one inline mermaid diagram (Task 7): an OverlayPanel
 * whose body is a dependency-free pan/zoom surface, with a source toggle and a
 * copy control in the footer. `svg` arrives already sanitized by security.ts;
 * re-inserting it here is the same trusted path MermaidDiagram itself uses.
 */
export function DiagramViewer({ open, svg, source, onClose }: DiagramViewerProps) {
  const [showSource, setShowSource] = useState(false);
  const [view, setView] = useState({ scale: 1, x: 0, y: 0 });
  const drag = useRef<{ pointerId: number; startX: number; startY: number; baseX: number; baseY: number } | null>(null);

  function handleWheel(event: ReactWheelEvent<HTMLDivElement>) {
    // Zoom around the cursor: keep the point under the cursor stationary.
    const rect = event.currentTarget.getBoundingClientRect();
    const cursorX = event.clientX - rect.left;
    const cursorY = event.clientY - rect.top;
    setView((current) => {
      const factor = event.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP;
      const next = Math.min(MAX_SCALE, Math.max(MIN_SCALE, current.scale * factor));
      const ratio = next / current.scale;
      return {
        scale: next,
        x: cursorX - ratio * (cursorX - current.x),
        y: cursorY - ratio * (cursorY - current.y),
      };
    });
  }

  function handlePointerDown(event: ReactPointerEvent<HTMLDivElement>) {
    event.currentTarget.setPointerCapture(event.pointerId);
    drag.current = {
      pointerId: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      baseX: view.x,
      baseY: view.y,
    };
  }

  function handlePointerMove(event: ReactPointerEvent<HTMLDivElement>) {
    const active = drag.current;
    if (active === null || active.pointerId !== event.pointerId) return;
    setView((current) => ({
      ...current,
      x: active.baseX + event.clientX - active.startX,
      y: active.baseY + event.clientY - active.startY,
    }));
  }

  function handlePointerUp(event: ReactPointerEvent<HTMLDivElement>) {
    if (drag.current?.pointerId === event.pointerId) drag.current = null;
  }

  const zoomStyle = { transform: `translate(${view.x}px, ${view.y}px) scale(${view.scale})` };

  return (
    <OverlayPanel
      open={open}
      onClose={onClose}
      title="Diagram"
      panelClassName={PANEL_CLASS}
      bodyClassName={CLASS.viewerBody}
      footer={
        <>
          <button type="button" onClick={() => setShowSource((value) => !value)}>
            {showSource ? "Show diagram" : "Show source"}
          </button>
          <button type="button" onClick={() => setView({ scale: 1, x: 0, y: 0 })}>
            Reset zoom
          </button>
          <CopyButton text={source} label="Copy source" />
        </>
      }
    >
      {showSource ? (
        <pre className={CLASS.source}>
          <code>{source}</code>
        </pre>
      ) : (
        <div
          className={CLASS.zoomSurface}
          onWheel={handleWheel}
          onPointerDown={handlePointerDown}
          onPointerMove={handlePointerMove}
          onPointerUp={handlePointerUp}
          onPointerCancel={handlePointerUp}
        >
          {/* biome-ignore lint/security/noDangerouslySetInnerHtml: sanitized upstream in security.ts */}
          <div className={CLASS.zoomContent} style={zoomStyle} dangerouslySetInnerHTML={{ __html: svg }} />
        </div>
      )}
    </OverlayPanel>
  );
}
