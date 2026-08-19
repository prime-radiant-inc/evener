// imageFilesFromClipboard pulls every image File off a paste event's
// clipboardData (parity-m5-composer.md §G): only image-kind items are
// intercepted - a text item alongside them is deliberately left untouched
// for the browser's own default paste insertion (the composer's paste
// handler must never call preventDefault, so accompanying prose still
// lands in the textarea next to an attached image).
export function imageFilesFromClipboard(clipboardData: DataTransfer | null): File[] {
  const files: File[] = [];
  if (!clipboardData) return files;
  for (const item of clipboardData.items) {
    if (item.kind === "file" && item.type.startsWith("image/")) {
      const file = item.getAsFile();
      if (file) files.push(file);
    }
  }
  return files;
}
