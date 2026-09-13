// Opening a URL in a new tab must not leave the new document with an opener:
// an opener-created window copies this tab's sessionStorage - where the
// per-client mutation identity lives (stores/mutationClientIdentity.ts) - and a
// shared identity would let both tabs claim each other's durable sends. That
// copy happens as the window is created, so nothing done to the handle
// afterwards can take it back: a browser that ignores `window.open`'s
// "noopener" feature string (Safari does) has already copied the storage by the
// time the handle comes back, and nulling its opener only hides the channel.
// An anchor carrying `rel="noopener noreferrer"` is honored by every browser
// the app runs in - it is the policy the app's other new-tab links already
// carry - so the open goes through one.
export function openInNewTab(url: string): void {
  const anchor = document.createElement("a");
  anchor.setAttribute("href", url);
  anchor.target = "_blank";
  anchor.rel = "noopener noreferrer";
  // Hidden, and attached for the click: a detached element's click is not a
  // navigation in every browser. Both are undone as soon as the click returns,
  // so the anchor never reaches a layout the user can see.
  anchor.style.display = "none";
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
}
