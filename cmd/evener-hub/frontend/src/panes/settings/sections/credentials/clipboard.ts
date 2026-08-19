// clipboard.ts: copy-to-clipboard for the device-code editor's "Copy code"
// button (parity-m7-settings.md §7h). Tries the async Clipboard API first;
// falls back to a hidden-textarea + execCommand("copy") when unavailable or
// throwing (e.g. a non-secure-context remote hub, where navigator.clipboard
// is undefined entirely) - mirrors templates/partials/credentials.html's own
// fallback exactly.
function copyViaExecCommand(text: string): boolean {
  const textarea = document.createElement("textarea");
  textarea.value = text;
  // Off-screen but still focusable/selectable - execCommand("copy") only
  // acts on the current selection.
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.focus();
  textarea.select();
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  } finally {
    document.body.removeChild(textarea);
  }
  return ok;
}

export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // fall through to the execCommand fallback below
    }
  }
  return copyViaExecCommand(text);
}
