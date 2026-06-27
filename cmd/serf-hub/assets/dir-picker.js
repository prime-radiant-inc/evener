// dir-picker.js — shared directory picker used by all serf-hub web UI path controls.
(function (global) {
  "use strict";

  // Position a chip picker: a full-width bottom sheet on phone (.chip-picker-sheet),
  // else anchored just below its chip. Mirrors spawn.js's placeChipPicker.
  function placeChipPicker(picker, anchor) {
    if (global.matchMedia && global.matchMedia("(max-width: 767px)").matches) {
      picker.classList.add("chip-picker-sheet");
      // Re-parent to <body> so the sheet outranks its body-level scrim (inside a
      // positioned wrapper its z-index can't, so the scrim ate every tap), then
      // sit above the scrim.
      document.body.appendChild(picker);
      picker.style.position = "";
      picker.style.top = "";
      picker.style.left = "";
      picker.style.zIndex = "901";
      addPickerScrim(picker);
      return;
    }
    picker.style.top = (anchor.offsetTop + anchor.offsetHeight + 4) + "px";
    picker.style.left = anchor.offsetLeft + "px";
    picker.style.zIndex = "50";
  }

  // Dimming backdrop behind the mobile bottom sheet; removes itself once the
  // picker leaves the DOM. A click on it is an outside click → picker dismisses.
  function addPickerScrim(picker) {
    if (document.querySelector(".chip-picker-scrim")) return;
    const scrim = document.createElement("div");
    scrim.className = "chip-picker-scrim";
    document.body.appendChild(scrim);
    const obs = new MutationObserver(() => {
      if (!document.body.contains(picker)) { scrim.remove(); obs.disconnect(); }
    });
    obs.observe(document.body, { childList: true, subtree: true });
  }

  function completeDirs(prefix) {
    if (global.SerfAppwire && typeof global.SerfAppwire.completeDirs === "function") {
      return global.SerfAppwire.completeDirs(prefix);
    }
    return fetch("/api/dirs?prefix=" + encodeURIComponent(prefix || ""), {
      credentials: "same-origin",
    }).then((r) => r.json());
  }

  function removeExisting() {
    const existing = document.querySelector(".chip-picker");
    if (existing) {
      if (typeof existing.__serfDirPickerCleanup === "function") {
        existing.__serfDirPickerCleanup();
      }
      existing.remove();
    }
  }

  function dismissOnOutsideClick(picker, close) {
    setTimeout(() => {
      if (!picker.parentNode) return;
      const offClick = (e) => {
        if (!picker.contains(e.target)) {
          close();
        }
      };
      picker.__serfDirPickerCleanup = () => {
        document.removeEventListener("click", offClick);
        picker.__serfDirPickerCleanup = null;
      };
      document.addEventListener("click", offClick);
    }, 0);
  }

  function normalizedResults(data) {
    const list = data && Array.isArray(data.results) ? data.results : [];
    return list.map((item) => {
      if (typeof item === "string") return { path: item, is_git: false };
      return { path: item.path || "", is_git: !!item.is_git };
    }).filter((item) => item.path);
  }

  function openDirPicker(options) {
    options = options || {};
    const anchor = options.anchor;
    if (!anchor || !anchor.parentNode) return null;

    removeExisting();

    const picker = document.createElement("div");
    picker.className = "chip-picker chip-picker-dir";

    const inlineInput = options.inlineInput || null;
    const input = inlineInput || document.createElement("input");
    if (!inlineInput) {
      input.className = "chip-picker-search";
      input.placeholder = options.placeholder || "/path/to/repo";
      input.value = options.currentValue || "";
      picker.appendChild(input);
    }

    const results = document.createElement("div");
    results.className = "chip-picker-results";
    picker.appendChild(results);

    let timer = null;
    let requestID = 0;

    function close() {
      if (typeof picker.__serfDirPickerCleanup === "function") {
        picker.__serfDirPickerCleanup();
      }
      picker.remove();
    }

    function accept(value) {
      const path = String(value || "");
      if (!path.trim()) return;
      if (typeof options.onAccept === "function") options.onAccept(path);
      close();
    }

    function fetchDirs(prefix) {
      const currentRequestID = ++requestID;
      const dirsPromise = completeDirs(prefix);
      dirsPromise.then((data) => {
        if (currentRequestID !== requestID || !picker.parentNode) return;
        results.innerHTML = "";
        const list = normalizedResults(data);
        if (list.length === 0) {
          const empty = document.createElement("div");
          empty.className = "empty-state empty-state-picker";
          empty.innerHTML = '<p class="empty-state-body">No matching directories</p>';
          results.appendChild(empty);
          return;
        }
        list.forEach((r) => {
          const el = document.createElement("div");
          el.className = "chip-picker-dir-row";
          const path = document.createElement("span");
          path.className = "chip-picker-dir-path";
          path.textContent = r.path;
          el.appendChild(path);
          if (r.is_git) {
            const tag = document.createElement("span");
            tag.className = "chip-picker-dir-tag";
            tag.textContent = "git";
            el.appendChild(tag);
          }
          el.addEventListener("click", () => accept(r.path));
          results.appendChild(el);
        });
      }).catch(() => {});
    }

    input.addEventListener("input", () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => fetchDirs(input.value), 150);
    });

    // Tab autocompletes to first result + "/". Enter prefers an exact
    // match and otherwise commits the typed literal so the UI does not
    // silently choose the wrong directory.
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        const typed = input.value;
        if (!typed.trim()) return;
        const rows = results.querySelectorAll(".chip-picker-dir-row");
        let exact = null;
        for (const row of rows) {
          const p = row.querySelector(".chip-picker-dir-path");
          if (p && p.textContent === typed) { exact = row; break; }
        }
        if (exact) exact.click();
        else accept(typed);
      } else if (e.key === "Tab") {
        e.preventDefault();
        const first = results.querySelector(".chip-picker-dir-path");
        if (first) input.value = first.textContent + "/";
      } else if (e.key === "Escape") {
        e.preventDefault();
        close();
      }
    });

    anchor.parentNode.style.position = "relative";
    anchor.parentNode.appendChild(picker);
    picker.style.position = "absolute";
    placeChipPicker(picker, anchor);
    if (options.minWidth) picker.style.minWidth = options.minWidth;

    if (!inlineInput) input.focus();
    fetchDirs(input.value);
    dismissOnOutsideClick(picker, close);
    return picker;
  }

  global.SerfDirPicker = {
    open: openDirPicker,
  };
})(window);
