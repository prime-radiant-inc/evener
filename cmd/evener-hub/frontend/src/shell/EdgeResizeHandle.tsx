// Shared edge drag-to-resize separator for docked shell sidebars (left rail on
// edge="right", right Overview on edge="left").
//
// TWO WRITE PATHS:
//   - during a pointer drag, each move paints `cssProperty` directly on
//     `targetRef` with no store write and no parent tree re-render.
//   - on pointerup/pointercancel, keyboard step, or double-click reset,
//     `onCommit` persists the final clamped width.
import { type KeyboardEvent, type PointerEvent, type RefObject, useRef, useState } from "react";
import { requireClass } from "../widgets/internal/requireClass";
import styles from "./EdgeResizeHandle.module.css";

const CLASS = {
  resizeHandle: requireClass(styles.resizeHandle, "EdgeResizeHandle.module.css", "resizeHandle"),
  edgeLeft: requireClass(styles.edgeLeft, "EdgeResizeHandle.module.css", "edgeLeft"),
  edgeRight: requireClass(styles.edgeRight, "EdgeResizeHandle.module.css", "edgeRight"),
};

// One arrow key nudges by the layout grid's own step (var(--space-4) = 16px);
// Shift covers ground faster for a keyboard-only resize of the full range.
const KEY_STEP = 16;
const KEY_STEP_COARSE = 64;

export interface EdgeResizeHandleProps {
  edge: "left" | "right";
  width: number;
  minWidth: number;
  maxWidth: number;
  defaultWidth: number;
  cssProperty: string;
  label: string;
  testId: string;
  onCommit: (width: number) => void;
  targetRef: RefObject<HTMLElement | null>;
}

export function EdgeResizeHandle({
  edge,
  width,
  minWidth,
  maxWidth,
  defaultWidth,
  cssProperty,
  label,
  testId,
  onCommit,
  targetRef,
}: EdgeResizeHandleProps) {
  const [dragWidth, setDragWidth] = useState<number | null>(null);
  const dragWidthRef = useRef<number | null>(null);
  const shownWidth = dragWidth ?? width;

  function clamp(value: number): number {
    if (!Number.isFinite(value)) return Math.round(Math.min(maxWidth, Math.max(minWidth, defaultWidth)));
    return Math.round(Math.min(maxWidth, Math.max(minWidth, value)));
  }

  function paint(value: number): void {
    targetRef.current?.style.setProperty(cssProperty, `${value}px`);
  }

  function handlePointerDown(event: PointerEvent<HTMLDivElement>): void {
    if (event.button !== 0) return;
    const target = targetRef.current;
    if (!target) return;
    event.currentTarget.setPointerCapture(event.pointerId);
    event.preventDefault();
    const initial = clamp(target.getBoundingClientRect().width);
    dragWidthRef.current = initial;
    setDragWidth(initial);
  }

  function handlePointerMove(event: PointerEvent<HTMLDivElement>): void {
    if (dragWidthRef.current === null) return;
    const target = targetRef.current;
    if (!target) return;
    const rect = target.getBoundingClientRect();
    const raw = edge === "right" ? event.clientX - rect.left : rect.right - event.clientX;
    const next = clamp(raw);
    dragWidthRef.current = next;
    setDragWidth(next);
    paint(next);
  }

  function endDrag(event: PointerEvent<HTMLDivElement>): void {
    const finalWidth = dragWidthRef.current;
    if (finalWidth === null) return;
    dragWidthRef.current = null;
    event.currentTarget.releasePointerCapture(event.pointerId);
    setDragWidth(null);
    onCommit(finalWidth);
  }

  function step(delta: number): void {
    const next = clamp(shownWidth + delta);
    paint(next);
    onCommit(next);
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>): void {
    // Alt/Ctrl/Meta belong to global chords; Shift selects the coarse step.
    if (event.altKey || event.ctrlKey || event.metaKey) return;
    const coarse = event.shiftKey ? KEY_STEP_COARSE : KEY_STEP;
    const sign = edge === "right" ? 1 : -1;
    switch (event.key) {
      case "ArrowLeft":
        step(-sign * coarse);
        break;
      case "ArrowRight":
        step(sign * coarse);
        break;
      case "Home":
        step(minWidth - shownWidth);
        break;
      case "End":
        step(maxWidth - shownWidth);
        break;
      default:
        return;
    }
    event.preventDefault();
  }

  return (
    // Interactive vertical resize separator requires keyboard and pointer
    // handlers that a void <hr> element cannot host.
    // biome-ignore lint/a11y/useSemanticElements: see comment above
    <div
      data-testid={testId}
      role="separator"
      aria-orientation="vertical"
      aria-label={label}
      aria-valuenow={shownWidth}
      aria-valuemin={minWidth}
      aria-valuemax={maxWidth}
      tabIndex={0}
      className={`${CLASS.resizeHandle} ${edge === "left" ? CLASS.edgeLeft : CLASS.edgeRight}`}
      onPointerDown={handlePointerDown}
      onPointerMove={handlePointerMove}
      onPointerUp={endDrag}
      onPointerCancel={endDrag}
      onKeyDown={handleKeyDown}
      onDoubleClick={() => {
        const next = clamp(defaultWidth);
        paint(next);
        onCommit(next);
      }}
    />
  );
}
