// composer-attachments.js — shared paste-to-image helper used by both the
// session workspace composer (renderer.js) and the /new spawn form
// (spawn.js). Owns paste-event handling and chip rendering for a
// reference-passable pendingState object of the shape {items: []}. Each
// item: {type:"image", mediaType:"image/png", data:ArrayBuffer, name, width, height}.
//
// Encoding choice: at this layer we keep image bytes as ArrayBuffer (NOT
// base64). Browsers can read clipboard images straight into ArrayBuffer via
// blob.arrayBuffer(); deferring base64 to the submit/fetch layer (kata v80q)
// avoids a 33% memory blow-up during composition and a needless re-encode
// when the same image is dropped and removed without sending.
//
// Scope intentionally narrow (kata r6a1): paste only. Drag-drop + file
// picker live in 65mm. Submit/fetch lives in v80q.
(function () {
  "use strict";

  // Convert any image blob to a PNG blob via canvas. For PNG inputs we still
  // round-trip through canvas to strip color profiles + EXIF — matches the
  // TUI's "always re-encode pasted clipboard image data to PNG" rule (codex
  // clipboard_paste.rs). Returns a Promise<{blob, width, height}>.
  function reencodeToPng(window, blob) {
    return new Promise((resolve, reject) => {
      const url = window.URL.createObjectURL(blob);
      const img = new window.Image();
      img.onload = () => {
        const canvas = window.document.createElement("canvas");
        canvas.width = img.width || 1;
        canvas.height = img.height || 1;
        const ctx = canvas.getContext("2d");
        if (ctx) ctx.drawImage(img, 0, 0);
        canvas.toBlob((out) => {
          window.URL.revokeObjectURL(url);
          if (!out) { reject(new Error("toBlob returned null")); return; }
          resolve({ blob: out, width: canvas.width, height: canvas.height });
        }, "image/png");
      };
      img.onerror = () => {
        window.URL.revokeObjectURL(url);
        reject(new Error("image decode failed"));
      };
      img.src = url;
    });
  }

  // Pull every image File off a ClipboardEvent. Text portions are left for
  // the browser's default paste handler so "see this:" + screenshot still
  // inserts the prose alongside the chip.
  function imageFilesFromClipboard(clipboardData) {
    const files = [];
    if (!clipboardData || !clipboardData.items) return files;
    for (const item of clipboardData.items) {
      if (item.kind === "file" && item.type && item.type.indexOf("image/") === 0) {
        const f = item.getAsFile && item.getAsFile();
        if (f) files.push(f);
      }
    }
    return files;
  }

  // attachComposerImageHandlers wires a paste listener onto textareaEl that
  // appends every pasted image to pendingState.items as a {type:"image", ...}
  // entry. Re-renders chips through any element with data-attachments-target
  // referencing it (best-effort — callers can additionally call
  // renderAttachmentChips themselves). pendingState is owned by the caller
  // and must be the same reference passed to renderAttachmentChips so the
  // remove buttons mutate the same list.
  function attachComposerImageHandlers(textareaEl, pendingState) {
    if (!textareaEl || !pendingState) return;
    if (!Array.isArray(pendingState.items)) pendingState.items = [];
    const window = textareaEl.ownerDocument.defaultView;

    textareaEl.addEventListener("paste", async (e) => {
      const files = imageFilesFromClipboard(e.clipboardData);
      if (files.length === 0) return; // text-only paste — let the browser insert
      // We deliberately DO NOT preventDefault when text is also present,
      // so any accompanying text portion still gets inserted into the
      // textarea by the default handler. (preventDefault would block both.)
      for (const file of files) {
        try {
          const { blob, width, height } = await reencodeToPng(window, file);
          const buf = await blob.arrayBuffer();
          const ts = Date.now();
          pendingState.items.push({
            type: "image",
            mediaType: "image/png",
            data: buf,
            name: "paste-" + ts + ".png",
            width,
            height,
          });
        } catch (err) {
          // Best-effort: drop the failing image silently. The user can
          // re-paste; we don't have a banner channel from this helper.
        }
      }
      // Re-render any chip containers that were bound to this state.
      for (const container of pendingState.__containers || []) {
        renderAttachmentChips(container, pendingState);
      }
    });
  }

  // renderAttachmentChips repaints containerEl from pendingState.items.
  // Each chip:
  //   <div class="composer-attachment" data-attachment>
  //     📎 paste-<ts>.png (412×512)
  //     <button data-attachment-remove>×</button>
  //   </div>
  function renderAttachmentChips(containerEl, pendingState) {
    if (!containerEl || !pendingState) return;
    if (!Array.isArray(pendingState.items)) pendingState.items = [];
    // Remember the container so the paste listener can re-render after
    // queueing without callers having to wire it up themselves.
    if (!pendingState.__containers) pendingState.__containers = [];
    if (pendingState.__containers.indexOf(containerEl) < 0) {
      pendingState.__containers.push(containerEl);
    }
    containerEl.innerHTML = "";
    if (!containerEl.classList.contains("composer-attachments")) {
      containerEl.classList.add("composer-attachments");
    }
    pendingState.items.forEach((item, idx) => {
      const chip = containerEl.ownerDocument.createElement("div");
      chip.className = "composer-attachment";
      chip.setAttribute("data-attachment", "");
      const label = containerEl.ownerDocument.createElement("span");
      label.className = "composer-attachment-label";
      const dims = (typeof item.width === "number" && typeof item.height === "number")
        ? " (" + item.width + "×" + item.height + ")"
        : "";
      label.textContent = "📎 " + (item.name || "image") + dims;
      chip.appendChild(label);
      const remove = containerEl.ownerDocument.createElement("button");
      remove.type = "button";
      remove.className = "composer-attachment-remove";
      remove.setAttribute("data-attachment-remove", "");
      remove.textContent = "×";
      remove.addEventListener("click", () => {
        pendingState.items.splice(idx, 1);
        renderAttachmentChips(containerEl, pendingState);
      });
      chip.appendChild(remove);
      containerEl.appendChild(chip);
    });
  }

  window.SerfComposerAttachments = {
    attachComposerImageHandlers,
    renderAttachmentChips,
  };
})();
