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

  function dismissOnOutsideClick(picker, close, insideEls) {
    setTimeout(() => {
      if (!picker.parentNode) return;
      const offClick = (e) => {
        if (Array.isArray(insideEls) && insideEls.some((el) => el && el.contains && el.contains(e.target))) {
          return;
        }
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

  function trimTrailingSlash(value) {
    const path = String(value || "").trim();
    if (path === "/") return "/";
    return path.replace(/\/+$/, "");
  }

  function withTrailingSlash(value) {
    const path = trimTrailingSlash(value);
    if (!path || path === "/") return path;
    return path + "/";
  }

  function parentDir(value) {
    const path = trimTrailingSlash(value);
    if (!path || path === "/") return "";
    const i = path.lastIndexOf("/");
    if (i <= 0) return "/";
    return path.slice(0, i);
  }

  function baseName(value) {
    const path = trimTrailingSlash(value);
    if (!path || path === "/") return path || "/";
    const i = path.lastIndexOf("/");
    return i >= 0 ? path.slice(i + 1) : path;
  }

  function checkIcon() {
    if (global.SerfIcons && global.SerfIcons.ended) return global.SerfIcons.ended;
    return "&#10003;";
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
    let currentDir = trimTrailingSlash(options.currentValue || input.value || "");
    if (!inlineInput) {
      input.className = "chip-picker-search";
      input.placeholder = options.placeholder || "/path/to/repo";
      input.value = currentDir;
    }

    const pathBar = document.createElement("div");
    pathBar.className = "chip-picker-dir-bar";
    if (!inlineInput) {
      pathBar.appendChild(input);
    }
    const useButton = document.createElement("button");
    useButton.type = "button";
    useButton.className = "chip-picker-dir-use";
    useButton.setAttribute("aria-label", "Use current directory");
    useButton.title = "Use current directory";
    useButton.innerHTML = checkIcon();
    pathBar.appendChild(useButton);
    picker.appendChild(pathBar);

    const results = document.createElement("div");
    results.className = "chip-picker-results";
    picker.appendChild(results);

    let timer = null;
    let requestID = 0;

    function close() {
      if (timer) {
        clearTimeout(timer);
        timer = null;
      }
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

    function setInputValue(value) {
      input.value = value;
    }

    function browseTo(path) {
      currentDir = trimTrailingSlash(path);
      setInputValue(currentDir);
      fetchDirs(currentDir);
    }

    function appendParentRow() {
      const parent = parentDir(currentDir);
      if (!parent && currentDir !== "/") return;
      if (currentDir === "/") return;
      const row = document.createElement("button");
      row.type = "button";
      row.className = "chip-picker-dir-parent";
      row.title = parent || "/";
      row.innerHTML = '<span class="chip-picker-dir-name">..</span>';
      row.addEventListener("click", () => browseTo(parent || "/"));
      results.appendChild(row);
    }

    function appendDirRow(r) {
      const el = document.createElement("button");
      el.type = "button";
      el.className = "chip-picker-dir-row";
      el.dataset.dirPath = r.path;
      el.title = r.path;
      const name = document.createElement("span");
      name.className = "chip-picker-dir-name";
      name.textContent = baseName(r.path);
      el.appendChild(name);
      if (r.is_git) {
        const tag = document.createElement("span");
        tag.className = "chip-picker-dir-tag";
        tag.textContent = "git";
        el.appendChild(tag);
      }
      el.addEventListener("click", () => browseTo(r.path));
      results.appendChild(el);
    }

    function fetchDirs(dir) {
      const currentRequestID = ++requestID;
      const dirsPromise = completeDirs(withTrailingSlash(dir));
      dirsPromise.then((data) => {
        if (currentRequestID !== requestID || !picker.parentNode) return;
        results.innerHTML = "";
        appendParentRow();
        const list = normalizedResults(data);
        if (list.length === 0) {
          const empty = document.createElement("div");
          empty.className = "empty-state empty-state-picker";
          empty.innerHTML = '<p class="empty-state-body">No directories here</p>';
          results.appendChild(empty);
          return;
        }
        list.forEach(appendDirRow);
      }).catch(() => {});
    }

    useButton.addEventListener("click", () => accept(input.value || currentDir));

    input.addEventListener("input", () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => browseTo(input.value), 150);
    });

    // Enter commits the typed literal so manual paths stay available; clicks in
    // the list are reserved for browsing.
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        accept(input.value);
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
    setInputValue(currentDir);
    fetchDirs(currentDir);
    dismissOnOutsideClick(picker, close, [anchor, inlineInput]);
    return picker;
  }

  global.SerfDirPicker = {
    open: openDirPicker,
  };
})(window);
