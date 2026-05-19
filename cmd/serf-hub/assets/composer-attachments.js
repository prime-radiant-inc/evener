// composer-attachments.js — shared image-attachment helper used by both the
// session workspace composer (renderer.js) and the /new spawn form
// (spawn.js). Owns paste / drag-drop / file-picker gesture handling and
// chip rendering for a reference-passable pendingState object of the shape
// {items: []}. Each item: {type:"image", mediaType:"image/png",
// data:ArrayBuffer, name, width, height}.
//
// Encoding choice: at this layer we keep image bytes as ArrayBuffer (NOT
// base64). Browsers can read clipboard images straight into ArrayBuffer via
// blob.arrayBuffer(); deferring base64 to the submit/fetch layer (kata v80q)
// avoids a 33% memory blow-up during composition and a needless re-encode
// when the same image is dropped and removed without sending.
//
// Scope: paste (kata r6a1), drag-drop + file picker (kata 65mm). All three
// surfaces funnel through the same canvas re-encode + pendingState push so
// the (forthcoming v80q) submit handler can ship one unified bag of bytes.
// Submit/fetch wiring lives in v80q.
(function () {
  "use strict";

  const maxAttachmentCount = 8;
  const maxAttachmentBytes = 8 * 1024 * 1024;

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

  // nextMarker advances a per-composer high-water counter. Monotonic — never
  // reused. Removing chip 3 from [1,2,3] leaves [1,2]; the next attach gets
  // 4. The "leave gaps" decision keeps existing marker references stable in
  // the prose the user has already typed.
  function nextMarker(pendingState) {
    let max = (typeof pendingState.__nextMarker === "number") ? pendingState.__nextMarker : 0;
    for (const it of pendingState.items || []) {
      if (it && typeof it.marker === "number" && it.marker > max) max = it.marker;
    }
    pendingState.__nextMarker = max + 1;
    return pendingState.__nextMarker;
  }

  function resetMarkerCounter(pendingState) {
    if (!pendingState) return;
    pendingState.__nextMarker = 0;
  }

  // insertAtCursor splices `str` into textareaEl.value at the current
  // selection (replacing any selected range) and moves the cursor to just
  // after the inserted text. No-op if textareaEl is falsy.
  function insertAtCursor(textareaEl, str) {
    if (!textareaEl) return;
    const v = textareaEl.value || "";
    let s = textareaEl.selectionStart;
    let e = textareaEl.selectionEnd;
    if (typeof s !== "number") s = v.length;
    if (typeof e !== "number") e = s;
    textareaEl.value = v.slice(0, s) + str + v.slice(e);
    const pos = s + str.length;
    try { textareaEl.selectionStart = pos; textareaEl.selectionEnd = pos; } catch (_) {}
  }

  // stripMarker removes the FIRST literal occurrence of `[image N]` from
  // textareaEl.value. Literal string search (not regex) avoids escaping
  // surprises. If the cursor sat past the deletion point, shift it back by
  // the marker's length so it stays anchored to the same character.
  function stripMarker(textareaEl, n) {
    if (!textareaEl) return;
    const needle = "[image " + n + "]";
    const v = textareaEl.value || "";
    const idx = v.indexOf(needle);
    if (idx < 0) return;
    textareaEl.value = v.slice(0, idx) + v.slice(idx + needle.length);
    const cur = textareaEl.selectionStart;
    if (typeof cur === "number" && cur > idx) {
      const shifted = Math.max(idx, cur - needle.length);
      try { textareaEl.selectionStart = shifted; textareaEl.selectionEnd = shifted; } catch (_) {}
    }
  }

  // reserveMarkerAndInsert is the shared pre-decode step for paste / drop /
  // file-picker: reserve marker N, then insert "[image N]" at the cursor
  // of pendingState.__textarea (if wired). Skips insertion when __textarea
  // is unset (e.g. a chip-only consumer), but still returns the marker so
  // the finalized item can keep stable numbering.
  function reserveMarkerAndInsert(pendingState) {
    const n = nextMarker(pendingState);
    insertAtCursor(pendingState.__textarea, "[image " + n + "]");
    return n;
  }

  function rejectFileName(file) {
    return file && file.name ? file.name : "unknown";
  }

  function attachmentRejection(file, reservedCount) {
    if (!file || typeof file.type !== "string" || file.type.indexOf("image/") !== 0) {
      return rejectFileName(file);
    }
    if (reservedCount >= maxAttachmentCount) {
      return rejectFileName(file) + " (maximum " + maxAttachmentCount + " images)";
    }
    if (typeof file.size === "number" && file.size > maxAttachmentBytes) {
      return rejectFileName(file) + " (maximum 8 MB)";
    }
    return "";
  }

  function reserveAttachmentItems(pendingState, files, rejected, nameForFile) {
    const items = [];
    let reserved = pendingState.items.length;
    for (const file of files) {
      const rejection = attachmentRejection(file, reserved);
      if (rejection) {
        rejected.push(rejection);
        continue;
      }
      reserved++;
      const marker = reserveMarkerAndInsert(pendingState);
      const item = {
        type: "image",
        mediaType: "image/png",
        name: nameForFile(file),
        marker,
        pending: true,
      };
      pendingState.items.push(item);
      items.push({ file, item, marker });
    }
    return items;
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
    // Stash the textarea so drop / file-picker ingest paths (which only
    // hand us an anchor element) and the chip remove handler can find it
    // for marker insertion / stripping (kata 2stz).
    pendingState.__textarea = textareaEl;
    const window = textareaEl.ownerDocument.defaultView;

    textareaEl.addEventListener("paste", async (e) => {
      const files = imageFilesFromClipboard(e.clipboardData);
      if (files.length === 0) return; // text-only paste — let the browser insert
      // We deliberately DO NOT preventDefault when text is also present,
      // so any accompanying text portion still gets inserted into the
      // textarea by the default handler. (preventDefault would block both.)
      let attached = 0;
      const rejected = [];
      const reservedItems = reserveAttachmentItems(pendingState, files, rejected, () => "paste-" + Date.now() + ".png");
      for (const reserved of reservedItems) {
        const { file, item, marker } = reserved;
        try {
          const { blob, width, height } = await reencodeToPng(window, file);
          const buf = await blob.arrayBuffer();
          item.data = buf;
          item.width = width;
          item.height = height;
          item.pending = false;
          attached++;
        } catch (err) {
          stripMarker(pendingState.__textarea, marker);
          const idx = pendingState.items.indexOf(item);
          if (idx >= 0) pendingState.items.splice(idx, 1);
          rejected.push(rejectFileName(file));
        }
      }
      // Auto-clear any stale rejection banner left over from a previous
      // drop / file-picker gesture (kata xpnk). A successful paste counts
      // as the user moving on — leaving an obsolete "Not an image: …"
      // message above a freshly-attached chip is misleading.
      surfaceRejections(textareaEl, attached > 0 && rejected.length === 0 ? [] : rejected);
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
        const gone = pendingState.items.splice(idx, 1)[0];
        if (gone && typeof gone.marker === "number") {
          stripMarker(pendingState.__textarea, gone.marker);
        }
        renderAttachmentChips(containerEl, pendingState);
      });
      chip.appendChild(remove);
      containerEl.appendChild(chip);
    });
  }

  // Shared image-file acceptor used by drop + file-picker. Splits a FileList
  // into images and rejections, re-encodes each image to PNG, and pushes the
  // resulting {type:"image", ...} entries onto pendingState.items. If any
  // non-image was present, surfaces ONE banner (text content updated, not
  // appended) on the first [data-attachment-error] element reachable from
  // anchorEl — searched as a sibling/descendant of the enclosing form-ish
  // ancestor. After all images have been re-encoded, runs the same chip
  // re-render that the paste handler does.
  async function ingestFiles(anchorEl, pendingState, fileList) {
    if (!pendingState) return;
    if (!Array.isArray(pendingState.items)) pendingState.items = [];
    const window = anchorEl.ownerDocument.defaultView;
    const files = Array.from(fileList || []);
    const rejected = [];
    const reservedItems = reserveAttachmentItems(
      pendingState,
      files,
      rejected,
      (file) => file.name || ("attachment-" + Date.now() + ".png"),
    );
    for (const reserved of reservedItems) {
      const { file, item, marker } = reserved;
      try {
        const { blob, width, height } = await reencodeToPng(window, file);
        const buf = await blob.arrayBuffer();
        item.data = buf;
        item.width = width;
        item.height = height;
        item.pending = false;
      } catch (err) {
        stripMarker(pendingState.__textarea, marker);
        const idx = pendingState.items.indexOf(item);
        if (idx >= 0) pendingState.items.splice(idx, 1);
        rejected.push((file && file.name) || "decode-failed");
      }
    }
    surfaceRejections(anchorEl, rejected);
    // Re-render any chip containers bound to this state.
    for (const container of pendingState.__containers || []) {
      renderAttachmentChips(container, pendingState);
    }
  }

  // Locate the [data-attachment-error] banner element nearest to anchorEl.
  // Walks up ancestors looking for a descendant match — the workspace +
  // spawn templates place the banner inside the same form/container as the
  // drop zone / file picker, so a closest+querySelector pair finds it.
  function findErrorBanner(anchorEl) {
    if (!anchorEl) return null;
    const direct = anchorEl.querySelector && anchorEl.querySelector("[data-attachment-error]");
    if (direct) return direct;
    let node = anchorEl;
    while (node && node.parentElement) {
      node = node.parentElement;
      const hit = node.querySelector && node.querySelector("[data-attachment-error]");
      if (hit) return hit;
    }
    return null;
  }

  function surfaceRejections(anchorEl, rejected) {
    const banner = findErrorBanner(anchorEl);
    if (!banner) return;
    if (!rejected || rejected.length === 0) {
      banner.textContent = "";
      banner.hidden = true;
      return;
    }
    const names = rejected.filter(Boolean).join(", ");
    const msg = rejected.length === 1
      ? "Not an image: " + names
      : "Skipped " + rejected.length + " non-image files: " + names;
    banner.textContent = msg;
    banner.hidden = false;
  }

  // attachComposerDropHandlers wires dragenter / dragover / dragleave / drop
  // listeners onto dropZoneEl so dropping image files into the composer area
  // appends them to pendingState.items. dragenter adds a .drop-active class
  // for visual feedback; dragleave / drop remove it. Non-image files are
  // rejected with an inline banner via surfaceRejections.
  function attachComposerDropHandlers(dropZoneEl, pendingState) {
    if (!dropZoneEl || !pendingState) return;
    if (!Array.isArray(pendingState.items)) pendingState.items = [];

    // dragover MUST preventDefault for the drop event to fire. dragenter
    // adds the visual class; dragleave removes it.
    dropZoneEl.addEventListener("dragenter", (e) => {
      e.preventDefault();
      dropZoneEl.classList.add("drop-active");
    });
    dropZoneEl.addEventListener("dragover", (e) => {
      e.preventDefault();
    });
    dropZoneEl.addEventListener("dragleave", (e) => {
      // Only strip the class when truly leaving (e.target === dropZone or
      // its relatedTarget is outside the zone). For simple JSDOM dispatch
      // there's no relatedTarget so we just unconditionally remove — the
      // dragenter handler restores it if the cursor re-enters.
      dropZoneEl.classList.remove("drop-active");
    });
    dropZoneEl.addEventListener("drop", async (e) => {
      e.preventDefault();
      dropZoneEl.classList.remove("drop-active");
      const dt = e.dataTransfer;
      const fileList = (dt && dt.files) || [];
      await ingestFiles(dropZoneEl, pendingState, fileList);
    });
  }

  // attachComposerFilePickerHandlers wires the visible buttonEl to trigger
  // the hidden <input type=file> fileInputEl, then handles its change event
  // to ingest the picked files via the same path as drop. The input is
  // reset to "" after each change so re-picking the same file re-fires.
  function attachComposerFilePickerHandlers(buttonEl, fileInputEl, pendingState) {
    if (!buttonEl || !fileInputEl || !pendingState) return;
    if (!Array.isArray(pendingState.items)) pendingState.items = [];
    // Defensive: ensure accept hint is set. The template sets it but if a
    // caller hands us a bare <input type=file> we still want image/* gating.
    if (!fileInputEl.getAttribute("accept")) {
      fileInputEl.setAttribute("accept", "image/*");
    }
    buttonEl.addEventListener("click", () => fileInputEl.click());
    fileInputEl.addEventListener("change", async (e) => {
      const files = (e.target && e.target.files) || fileInputEl.files || [];
      await ingestFiles(fileInputEl, pendingState, files);
      try { fileInputEl.value = ""; } catch (_) { /* JSDOM may resist */ }
    });
  }

  window.SerfComposerAttachments = {
    attachComposerImageHandlers,
    attachComposerDropHandlers,
    attachComposerFilePickerHandlers,
    renderAttachmentChips,
    resetMarkerCounter,
  };
})();
