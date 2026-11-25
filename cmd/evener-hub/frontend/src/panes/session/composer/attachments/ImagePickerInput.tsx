import type { ChangeEvent, RefObject } from "react";

export interface ImagePickerInputProps {
  /** The surface's hidden image input; AttachImageControl clicks it and the
   * picker fills it. */
  inputRef: RefObject<HTMLInputElement | null>;
  onFiles: (files: File[]) => void;
}

/** The hidden image input shared by the two surfaces that stage images (the
 * session composer and the spawn prompt card). AttachImageControl opens it,
 * and a pick lands here. The value is cleared after every change so
 * re-picking the same file still fires change. */
export function ImagePickerInput({ inputRef, onFiles }: ImagePickerInputProps) {
  function handleChange(event: ChangeEvent<HTMLInputElement>): void {
    const files = Array.from(event.target.files ?? []);
    if (files.length > 0) onFiles(files);
    event.target.value = "";
  }

  return <input ref={inputRef} type="file" accept="image/*" multiple hidden onChange={handleChange} />;
}
