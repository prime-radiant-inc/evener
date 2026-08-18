import { type DragEvent, type ReactNode, useState } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./dropzone.module.css";

export interface DropzoneProps {
  children: ReactNode;
  /** Called with every File the user dropped, in order. Generic and
   * wire-free: this widget has no opinion about type/count/size - a caller
   * that only wants images (or has its own count/size caps) filters the
   * array itself, exactly as it would a file-picker's FileList. */
  onFiles: (files: File[]) => void;
  disabled?: boolean;
}

const BASE_CLASS = {
  zone: requireClass(styles.zone, "dropzone.module.css", "zone"),
  active: requireClass(styles.active, "dropzone.module.css", "active"),
};

/**
 * A generic drag-and-drop file target: wraps `children`, calls `onFiles`
 * with whatever was dropped, and toggles a visual "active" state while a
 * drag is over it. Carries no wire/protocol knowledge and no file-type
 * opinion - the composer's own image-only + count/size rules (or any other
 * caller's rules) live entirely outside this widget.
 */
export function Dropzone({ children, onFiles, disabled = false }: DropzoneProps) {
  const [active, setActive] = useState(false);

  function handleDragEnter(event: DragEvent<HTMLDivElement>): void {
    event.preventDefault();
    if (!disabled) setActive(true);
  }

  function handleDragOver(event: DragEvent<HTMLDivElement>): void {
    // preventDefault is required here for `drop` to fire at all - the
    // browser's default for an unhandled dragover is to reject the drop.
    event.preventDefault();
  }

  function handleDragLeave(event: DragEvent<HTMLDivElement>): void {
    event.preventDefault();
    setActive(false);
  }

  function handleDrop(event: DragEvent<HTMLDivElement>): void {
    event.preventDefault();
    setActive(false);
    if (disabled) return;
    const files = Array.from(event.dataTransfer?.files ?? []);
    if (files.length > 0) onFiles(files);
  }

  return (
    // HTML5 drag-and-drop has no keyboard equivalent to give this element a
    // role for - it's inherently pointer-only, and every caller of this
    // widget is expected to offer a keyboard-reachable alternative (a
    // file-picker button) for the same outcome, same rationale as Toast's
    // own pointer-only pause convenience (see that widget's index.tsx).
    // biome-ignore lint/a11y/noStaticElementInteractions: drag-and-drop is inherently pointer-only; callers pair this with a keyboard-reachable file-picker button for the same outcome, see above
    <div
      className={active ? `${BASE_CLASS.zone} ${BASE_CLASS.active}` : BASE_CLASS.zone}
      onDragEnter={handleDragEnter}
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      {children}
    </div>
  );
}
