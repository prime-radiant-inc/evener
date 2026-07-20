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
    // Clamp inside the viewport: at tablet widths an anchor near the right
    // edge would push the fixed-width panel off-screen.
    var pw = picker.offsetWidth || 520;
    var maxLeft = Math.max(8, (global.innerWidth || 1024) - pw - 8);
    picker.style.left = Math.min(anchor.offsetLeft, maxLeft) + "px";
    picker.style.top = (anchor.offsetTop + anchor.offsetHeight + 4) + "px";
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
    return global.SerfAppwire.completeDirs(prefix);
  }

  // recentProjects fetches the hub's most recently used project directories
  // (server-capped at 15) to prepopulate the picker on an empty query. Older
  // hub clients without the RPC degrade to no recent section.
  function recentProjects() {
    if (global.SerfAppwire && typeof global.SerfAppwire.recentProjects === "function") {
      return global.SerfAppwire.recentProjects().catch(() => []);
    }
    return Promise.resolve([]);
  }

  function removeExisting() {
    const existing = document.querySelector(".chip-picker");
    if (existing) {
      if (typeof existing.__serfDirPickerClose === "function") {
        existing.__serfDirPickerClose();
        return;
      }
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
    const initialValue = String(options.currentValue || input.value || "");
    let currentDir = trimTrailingSlash(initialValue);
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
    // The dropdown is prepopulated with the most recent projects (issue #35)
    // on its initial listing only; browsing or typing swaps to plain
    // completion results.
    let pendingRecent = true;

    function close() {
      if (timer) {
        clearTimeout(timer);
        timer = null;
      }
      if (typeof picker.__serfDirPickerCleanup === "function") {
        picker.__serfDirPickerCleanup();
      }
      input.removeEventListener("input", onInput);
      input.removeEventListener("keydown", onKeydown);
      picker.__serfDirPickerClose = null;
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
      pendingRecent = false;
      currentDir = trimTrailingSlash(path);
      setInputValue(withTrailingSlash(currentDir));
      fetchDirs(withTrailingSlash(currentDir));
    }

    function searchFromInput(value) {
      pendingRecent = false;
      const query = String(value || "");
      currentDir = query.endsWith("/") ? trimTrailingSlash(query) : parentDir(query);
      fetchDirs(query);
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

    // Recent-project options accept on click (they pick the project, not
    // browse into it) and show the full path — basenames collide across
    // projects, so the basename alone would be ambiguous.
    function appendRecentRows(recents) {
      const header = document.createElement("div");
      header.className = "chip-picker-dir-recent-header";
      header.textContent = "Recent projects";
      results.appendChild(header);
      recents.forEach((path) => {
        const el = document.createElement("button");
        el.type = "button";
        el.className = "chip-picker-dir-row chip-picker-dir-recent";
        el.dataset.recentPath = path;
        el.title = path;
        const name = document.createElement("span");
        name.className = "chip-picker-dir-name";
        name.textContent = baseName(path);
        el.appendChild(name);
        const full = document.createElement("span");
        full.className = "chip-picker-dir-recent-path";
        full.textContent = path;
        el.appendChild(full);
        el.addEventListener("click", () => accept(path));
        results.appendChild(el);
      });
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

    function fetchDirs(prefix) {
      const currentRequestID = ++requestID;
      const showRecent = pendingRecent;
      pendingRecent = false;
      const recentsPromise = showRecent ? recentProjects() : Promise.resolve([]);
      const dirsPromise = completeDirs(prefix);
      Promise.all([dirsPromise, recentsPromise]).then(([data, recents]) => {
        if (currentRequestID !== requestID || !picker.parentNode) return;
        results.innerHTML = "";
        appendParentRow();
        if (showRecent && recents.length > 0) {
          appendRecentRows(recents);
        }
        const list = normalizedResults(data);
        if (list.length === 0) {
          if (!showRecent || recents.length === 0) {
            const empty = document.createElement("div");
            empty.className = "empty-state empty-state-picker";
            empty.innerHTML = '<p class="empty-state-body">No directories here</p>';
            results.appendChild(empty);
          }
          return;
        }
        list.forEach(appendDirRow);
      }).catch(() => {});
    }

    useButton.addEventListener("click", () => accept(trimTrailingSlash(input.value || currentDir)));

    function onInput() {
      if (timer) clearTimeout(timer);
      const query = input.value;
      timer = setTimeout(() => searchFromInput(query), 150);
    }

    // Enter commits the typed literal so manual paths stay available; clicks in
    // the list are reserved for browsing.
    function onKeydown(e) {
      if (e.key === "Enter") {
        e.preventDefault();
        accept(input.value);
      } else if (e.key === "Escape") {
        e.preventDefault();
        close();
      }
    }

    input.addEventListener("input", onInput);
    input.addEventListener("keydown", onKeydown);
    picker.__serfDirPickerClose = close;

    anchor.parentNode.style.position = "relative";
    anchor.parentNode.appendChild(picker);
    picker.style.position = "absolute";
    placeChipPicker(picker, anchor);
    if (options.minWidth) picker.style.minWidth = options.minWidth;

    if (!inlineInput) input.focus();
    setInputValue(initialValue || currentDir);
    if (options.searchOnOpen) {
      searchFromInput(initialValue);
    } else {
      fetchDirs(withTrailingSlash(currentDir));
    }
    dismissOnOutsideClick(picker, close, [anchor, inlineInput]);
    return picker;
  }

  global.SerfDirPicker = {
    open: openDirPicker,
    placeChipPicker: placeChipPicker, // test seam (test-picker-clamp.js)
  };
})(window);
