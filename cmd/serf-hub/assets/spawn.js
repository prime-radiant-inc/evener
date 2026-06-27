(function () {
  "use strict";

  // spawnEncodeAttachmentData base64-encodes ArrayBuffer-shaped image bytes
  // for the /api/spawn REST fallback when SerfAppwire isn't installed (kata
  // v80q). The appwire path uses the same shape via inputItemsFromAttachments
  // — this is the fetch fallback only. Cross-realm-safe (duck-typed) so
  // JSDOM-spawned buffers from tests still survive the round-trip.
  function spawnEncodeAttachmentData(data) {
    if (data == null) return "";
    if (typeof data === "string") return data;
    let bytes;
    if (ArrayBuffer.isView(data)) {
      bytes = data.buffer
        ? new Uint8Array(data.buffer, data.byteOffset || 0, data.byteLength)
        : data;
    } else if (typeof data === "object" && typeof data.byteLength === "number") {
      bytes = new Uint8Array(data);
    } else {
      return "";
    }
    const CHUNK = 0x8000;
    let binary = "";
    for (let i = 0; i < bytes.length; i += CHUNK) {
      const slice = bytes.subarray(i, i + CHUNK);
      binary += String.fromCharCode.apply(null, slice);
    }
    return (typeof btoa === "function")
      ? btoa(binary)
      : Buffer.from(binary, "binary").toString("base64");
  }

  function projectKey(workingDir) {
    return "serf-hub.spawn-defaults." + (workingDir || "global");
  }

  function currentWorkingDir() {
    const el = document.querySelector('input[type=hidden][name="working_dir"]');
    return el ? el.value : "";
  }

  function loadDefaults() {
    try {
      const wd = currentWorkingDir();
      const perProject = JSON.parse(localStorage.getItem(projectKey(wd)) || "{}");
      // Layer the global model default underneath the per-project value
      const globalModel = localStorage.getItem("serf-hub.spawn-defaults.global.model") || "";
      if (!perProject.model && globalModel) perProject.model = globalModel;
      // Layer the global working_dir default when visiting /new without a pre-filled dir
      if (!wd && !perProject.working_dir) {
        const globalWorkingDir = localStorage.getItem("serf-hub.spawn-defaults.global.working_dir") || "";
        if (globalWorkingDir) perProject.working_dir = globalWorkingDir;
      }
      return perProject;
    } catch (e) { return {}; }
  }
  function saveDefaults(d) {
    const wd = currentWorkingDir();
    const saved = Object.assign({}, d);
    if (!harnessUsesSerfModels(saved.harness)) delete saved.model;
    localStorage.setItem(projectKey(wd), JSON.stringify(saved));
    // Persist model globally across projects
    if (harnessUsesSerfModels(d.harness) && d.model) localStorage.setItem("serf-hub.spawn-defaults.global.model", d.model);
    // Persist working_dir globally so it is restored on /new visits
    if (d.working_dir) localStorage.setItem("serf-hub.spawn-defaults.global.working_dir", d.working_dir);
  }

  // clearStoredModelDefault removes stored defaults whose model matches
  // `value`. Per-project keys are JSON blobs, so mutate them in place rather
  // than dropping the whole record (working_dir / branch / access_mode
  // defaults stay intact).
  function clearStoredModelDefault(value) {
    try {
      if (localStorage.getItem("serf-hub.spawn-defaults.global.model") === value) {
        localStorage.removeItem("serf-hub.spawn-defaults.global.model");
      }
    } catch (e) { /* ignore quota / privacy mode */ }
    try {
      const wd = currentWorkingDir();
      const key = projectKey(wd);
      const raw = localStorage.getItem(key);
      if (!raw) return;
      const parsed = JSON.parse(raw);
      if (parsed && parsed.model && parsed.model === value) {
        delete parsed.model;
        if (Object.keys(parsed).length === 0) {
          localStorage.removeItem(key);
        } else {
          localStorage.setItem(key, JSON.stringify(parsed));
        }
      }
    } catch (e) { /* malformed JSON — leave it for the user to notice */ }
  }

  function clearModelPrefillNotice() {
    const existing = document.querySelector("[data-model-prefill-notice]");
    if (existing) existing.remove();
  }

  function showModelPrefillNotice(form, discardedValue) {
    clearModelPrefillNotice();
    const message = "Discarded last-used model `" + discardedValue + "` — no longer offered by this hub.";
    const el = window.SerfDiagnostics && window.SerfDiagnostics.render
      ? window.SerfDiagnostics.render({ severity: "note", source: "hub", title: "Model default cleared", message })
      : (function () {
          const div = document.createElement("div");
          div.className = "diagnostic diagnostic-note diagnostic-source-hub";
          div.setAttribute("role", "status");
          div.textContent = message;
          return div;
        })();
    el.dataset.modelPrefillNotice = "true";
    // Anchor just above the prompt textarea so the user sees the notice
    // beside the chips that changed.
    const anchor = form.querySelector("textarea[name=prompt]") || form.querySelector(".spawn-actions");
    form.insertBefore(el, anchor || null);
  }

  // modelValidityAgainstList classifies a stored `provider/model` string
  // against the harness model list. Returns one of:
  //   "malformed"  — no `/` separator (legacy bare model name)
  //   "stale"      — provider IS enumerated but the model is gone
  //   "unknown"    — provider not enumerated (OAuth-only, openrouter-anthropic, …)
  //   "valid"      — exact match in the list
  function modelValidityAgainstList(value, models) {
    if (typeof value !== "string" || value === "") return "valid";
    if (!value.includes("/")) return "malformed";
    if (!Array.isArray(models) || models.length === 0) return "unknown";
    const slash = value.indexOf("/");
    const provider = value.slice(0, slash);
    const modelName = value.slice(slash + 1);
    let providerEnumerated = false;
    for (let i = 0; i < models.length; i++) {
      const m = models[i];
      if (!m || m.provider !== provider) continue;
      providerEnumerated = true;
      if (m.model === modelName) return "valid";
    }
    return providerEnumerated ? "stale" : "unknown";
  }

  // sweepStaleModelDefaults walks every per-project spawn-defaults blob in
  // localStorage and the `serf-hub.spawn-defaults.global.model` key, and
  // removes stored models that the harness no longer offers. Other fields
  // in each per-project blob (working_dir, branch, access_mode) are
  // preserved; only stale `.model` entries are stripped (and the wrapping
  // blob is removed when emptied).
  //
  // Per-project keys live at `serf-hub.spawn-defaults.<workingDir>` (see
  // projectKey above). We match the broader prefix and explicitly skip the
  // known global scalar keys so we don't try to JSON.parse them.
  //
  // Returns the number of cleanup actions performed so the caller can log
  // it. Failures from a read-only / quota-bound localStorage are swallowed
  // so init never breaks because the sweep can't write.
  function sweepStaleModelDefaults(models) {
    let cleaned = 0;
    let keys;
    try {
      keys = Object.keys(localStorage);
    } catch (e) {
      return 0;
    }
    const prefix = "serf-hub.spawn-defaults.";
    const globalScalars = new Set([
      "serf-hub.spawn-defaults.global.model",
      "serf-hub.spawn-defaults.global.working_dir",
      "serf-hub.spawn-defaults.global.last-working-dir",
    ]);
    for (let i = 0; i < keys.length; i++) {
      const key = keys[i];
      if (key.indexOf(prefix) !== 0) continue;
      if (globalScalars.has(key)) continue;
      let raw;
      try { raw = localStorage.getItem(key); } catch (e) { continue; }
      if (!raw) continue;
      let parsed;
      try {
        parsed = JSON.parse(raw);
      } catch (e) {
        // Malformed JSON — leave alone, the user can sort it out.
        continue;
      }
      if (!parsed || typeof parsed !== "object" || !("model" in parsed)) continue;
      const verdict = modelValidityAgainstList(parsed.model, models);
      if (verdict !== "malformed" && verdict !== "stale") continue;
      delete parsed.model;
      try {
        if (Object.keys(parsed).length === 0) {
          localStorage.removeItem(key);
        } else {
          localStorage.setItem(key, JSON.stringify(parsed));
        }
        cleaned++;
      } catch (e) { /* ignore quota / privacy mode */ }
    }
    // Idempotent global-model check (validatePrefilledModel handles the
    // chip-bound case for the current cwd; this catches the standalone key
    // when no per-project blob covers it).
    try {
      const globalModel = localStorage.getItem("serf-hub.spawn-defaults.global.model");
      if (globalModel) {
        const verdict = modelValidityAgainstList(globalModel, models);
        if (verdict === "malformed" || verdict === "stale") {
          localStorage.removeItem("serf-hub.spawn-defaults.global.model");
          cleaned++;
        }
      }
    } catch (e) { /* ignore */ }
    return cleaned;
  }

  // validatePrefilledModel checks that the chip's current value still
  // appears in the harness model list and discards it inline when the
  // provider IS enumerated but the specific model is gone. Providers that
  // aren't enumerated at all (OAuth-only anthropic, openrouter-anthropic,
  // etc.) are left untouched — we don't know whether the value is valid.
  //
  // Before per-cwd validation runs, sweep ALL stored project blobs so a
  // user with many per-project entries pointing at a retired model gets
  // every entry cleaned in a single /new visit (kata hnvv).
  function validatePrefilledModel(form) {
    if (!harnessUsesSerfModels(currentHarness())) return;
    const hidden = form.querySelector('input[type=hidden][name="model"]');
    const current = hidden ? hidden.value.trim() : "";

    // Legacy malformed entry (no provider prefix). The picker always
    // stores `provider/model`, so a bare model name is stale data. We
    // still handle this synchronously to drop the chip before the user
    // can submit, even if listModels hasn't resolved yet.
    if (current && !current.includes("/")) {
      clearStoredModelDefault(current);
      setChipValue("model", "");
      showModelPrefillNotice(form, current);
    }

    listModelsForHarness(currentHarness()).then(function (models) {
      if (!Array.isArray(models) || models.length === 0) return;

      // Sweep every stored project blob + the standalone global-model
      // key. Idempotent and covers projects the user isn't viewing right
      // now (the original chip-bound flow only touched the current cwd).
      const cleaned = sweepStaleModelDefaults(models);
      if (cleaned > 0) {
        try {
          console.info("Cleared " + cleaned + " stale spawn-form model default(s).");
        } catch (e) { /* ignore */ }
      }

      // Confirm the chip still has the same value we're validating —
      // the user could have picked something else while the fetch was
      // in flight.
      const hiddenNow = form.querySelector('input[type=hidden][name="model"]');
      const chipValue = hiddenNow ? hiddenNow.value.trim() : "";
      if (!chipValue || chipValue !== current) return;

      const verdict = modelValidityAgainstList(chipValue, models);
      if (verdict === "valid" || verdict === "unknown") return;
      // "malformed" and "stale" both need to clear the chip + notice.
      clearStoredModelDefault(chipValue);
      setChipValue("model", "");
      showModelPrefillNotice(form, chipValue);
    }).catch(function () {
      // Network/parse error — without the list we can't tell whether the
      // value is valid. Leave the pre-fill alone; the server-side 503
      // path will still surface the truth on submit.
    });
  }

  function currentHarness() {
    const el = document.querySelector('input[type=hidden][name="harness"]');
    return (el && el.value) || "serf";
  }

  function harnessUsesSerfModels(harness) {
    return !harness || harness === "serf";
  }

  function modelPlaceholder(harness) {
    harness = harness || "serf";
    return harnessUsesSerfModels(harness) ? "(pick a model)" : harness + " default";
  }

  // abbreviateModel strips the first slash-segment (the instance name) and
  // any trailing date suffix from a model identifier so it fits in a narrow
  // chip. The model picker groups by instance, so the instance name is already
  // shown as a group header; stripping it from the chip keeps the display clean.
  //   "openai/gpt-5.5"                      → "gpt-5.5"
  //   "work/gpt-5"                           → "gpt-5"
  //   "openrouter/anthropic/claude-opus-4"  → "anthropic/claude-opus-4"
  //   "openai/gpt-5-20260101"               → "gpt-5"
  //   "bare-model"                           → "bare-model"
  function abbreviateModel(id) {
    if (!id || typeof id !== "string") return id;
    var s = id;
    // Strip first slash-segment (the instance name), whatever it is.
    var slash = s.indexOf("/");
    if (slash >= 0) {
      s = s.slice(slash + 1);
    }
    // Strip trailing date suffix of the form -YYYYMMDD (8 digits).
    s = s.replace(/-\d{8}$/, "");
    return s;
  }

  function setModelValue(value, displayValue) {
    const display = document.querySelector("[data-chip-value-model]");
    const hidden = document.querySelector('input[type=hidden][name="model"]');
    var shown = displayValue || (value ? abbreviateModel(value) : null) || modelPlaceholder(currentHarness());
    if (display) display.textContent = shown;
    if (hidden) hidden.value = value || "";
  }

  function setChipValue(name, value) {
    if (name === "model") {
      setModelValue(value);
      // When the user picks a non-empty model, dismiss any stale
      // "discarded last-used model" notice — they've answered the prompt.
      if (value) clearModelPrefillNotice();
      return;
    }
    const display = document.querySelector('[data-chip-value-' + name + ']');
    const hidden = document.querySelector('input[type=hidden][name="' + name + '"]');
    if (display) display.textContent = value || "(default)";
    if (hidden) hidden.value = value || "";
    // When working_dir changes, resolve and display the git HEAD branch
    // in the branch chip if no explicit branch has been chosen.
    if (name === "working_dir" && value) {
      resolveAndSetHeadBranch(value);
    }
  }

  // resolveAndSetHeadBranch fetches the HEAD branch for cwd and updates the
  // branch chip display text when the chip value is still empty (default).
  function resolveAndSetHeadBranch(cwd) {
    const branchHidden = document.querySelector('input[type=hidden][name="branch"]');
    // Only resolve when no explicit branch has been set.
    if (branchHidden && branchHidden.value) return;
    const url = window.SerfAppwire
      ? null // appwire doesn't expose git/head yet; fall through to fetch
      : "/api/git/head?cwd=" + encodeURIComponent(cwd);
    if (!url) return;
    fetch(url).then(r => r.json()).then(data => {
      const branch = (data && data.branch) ? data.branch : "";
      const display = document.querySelector("[data-chip-value-branch]");
      // Only update if the user still hasn't chosen an explicit branch.
      const hiddenNow = document.querySelector('input[type=hidden][name="branch"]');
      if (display && hiddenNow && !hiddenNow.value) {
        display.textContent = branch || "(default)";
        // Store the resolved head in a data attribute so the chip can tell
        // the user what "(default)" actually means.
        display.dataset.resolvedHead = branch;
      }
    }).catch(() => {});
  }

  function applyHarnessModelPolicy(harness) {
    if (!harnessUsesSerfModels(harness)) {
      setModelValue("");
      return;
    }
    const hidden = document.querySelector('input[type=hidden][name="model"]');
    if (!hidden || !hidden.value || !hidden.value.includes("/")) setModelValue("");
  }

  function routeID(spawnResult) {
    spawnResult = spawnResult || {};
    const ref = String(spawnResult.ref || "");
    if (ref && !ref.startsWith("local:")) return ref;
    const sessionID = String(spawnResult.session_id || spawnResult.sessionId || "");
    if (sessionID.startsWith("local:")) return sessionID.slice("local:".length);
    if (sessionID) return sessionID;
    if (ref.startsWith("local:")) return ref.slice("local:".length);
    return ref;
  }

  function sessionPath(spawnResult) {
    return "/s/" + encodeURIComponent(routeID(spawnResult));
  }

  function spawnErrorMessage(text) {
    try {
      const parsed = JSON.parse(text || "{}");
      if (parsed && parsed.error) return parsed.error;
    } catch (e) {
      // Plain-text errors come from older fallback paths.
    }
    return text;
  }

  function clearSpawnError(form) {
    const existing = form.querySelector("[data-spawn-error]");
    if (existing) existing.remove();
  }

  function spawnFailureMessage(err) {
    const detail = (err && err.message) ? err.message : String(err || "unknown error");
    return detail.toLowerCase().startsWith("spawn failed") ? detail : "spawn failed: " + detail;
  }

  function fallbackSpawnDiagnostic(message) {
    const el = document.createElement("div");
    el.className = "diagnostic diagnostic-error diagnostic-source-hub";
    el.setAttribute("role", "alert");

    const header = document.createElement("div");
    header.className = "diagnostic-header";

    const badge = document.createElement("span");
    badge.className = "diagnostic-badge";
    badge.textContent = "Hub error";
    header.appendChild(badge);

    const title = document.createElement("span");
    title.className = "diagnostic-title";
    title.textContent = "Hub spawn error";
    header.appendChild(title);
    el.appendChild(header);

    const body = document.createElement("div");
    body.className = "diagnostic-message";
    body.textContent = message;
    el.appendChild(body);

    return el;
  }

  // Position a chip picker. On phone it becomes a full-width bottom sheet
  // (styled via .chip-picker-sheet); on larger screens it's anchored just below
  // its chip. Fixed desktop widths (520/480px) overflow a phone otherwise.
  function placeChipPicker(picker, anchor) {
    if (window.matchMedia && window.matchMedia("(max-width: 767px)").matches) {
      picker.classList.add("chip-picker-sheet");
      // Re-parent to <body> so the sheet shares the root stacking context with
      // its scrim. Left inside the positioned chip wrapper, the sheet's z-index
      // couldn't beat the body-level scrim, so the scrim sat on top and every
      // tap dismissed instead of selecting. Then clear the inline absolute
      // positioning (the CSS fixed sheet takes over) and sit ABOVE the scrim.
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

  // A dimming backdrop behind the mobile bottom sheet. Clicking it counts as an
  // outside click (so the picker's own dismiss closes it); it removes itself
  // once the picker leaves the DOM.
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

  // Insert a banner just above the attach/spawn row. The row is a direct child
  // of the form; .spawn-actions inside it is NOT, and insertBefore requires the
  // reference node to be a direct child (else it throws NotFoundError). Falls
  // back to appending when the row is absent.
  function insertAboveSpawnRow(form, el) {
    const row = form.querySelector(".spawn-attach-row");
    form.insertBefore(el, row && row.parentNode === form ? row : null);
  }

  function renderSpawnError(form, err) {
    clearSpawnError(form);
    const message = spawnFailureMessage(err);
    const el = window.SerfDiagnostics && window.SerfDiagnostics.render
      ? window.SerfDiagnostics.render({ severity: "error", source: "hub", title: "Hub spawn error", message })
      : fallbackSpawnDiagnostic(message);
    el.dataset.spawnError = "true";
    insertAboveSpawnRow(form, el);
  }

  // Inline confirmation asking whether to create a not-yet-existing working
  // directory. Resolves true (create) / false (cancel). Rendered in-form rather
  // than via a native dialog to match the rest of the spawn UI.
  function confirmCreateDir(form, displayPath) {
    clearSpawnError(form);
    return new Promise((resolve) => {
      const el = document.createElement("div");
      el.className = "spawn-confirm";
      el.dataset.spawnConfirm = "true";
      el.setAttribute("role", "alertdialog");

      const msg = document.createElement("div");
      msg.className = "spawn-confirm-message";
      const strong = document.createElement("span");
      strong.className = "spawn-confirm-path";
      strong.textContent = displayPath;
      msg.append("The directory ", strong, " doesn’t exist yet. Create it and start the session?");
      el.appendChild(msg);

      const actions = document.createElement("div");
      actions.className = "spawn-confirm-actions";
      const cancel = document.createElement("button");
      cancel.type = "button";
      cancel.className = "btn btn-ghost";
      cancel.textContent = "Cancel";
      const create = document.createElement("button");
      create.type = "button";
      create.className = "btn btn-primary";
      create.textContent = "Create & start";
      actions.append(cancel, create);
      el.appendChild(actions);

      const done = (result) => { el.remove(); resolve(result); };
      cancel.addEventListener("click", () => done(false));
      create.addEventListener("click", () => done(true));

      insertAboveSpawnRow(form, el);
      create.focus();
    });
  }

  // Pre-flight for the proposed working directory. Returns true when the spawn
  // may proceed (the directory exists, or the user agreed to create it and it
  // was created); false when the user cancelled or it can't be used (a
  // diagnostic is shown in that case). Fails OPEN on any check error so a
  // flaky validate never blocks a spawn — the daemon still reports real issues.
  async function ensureWorkingDir(form, dir) {
    let state;
    try {
      const resp = await fetch("/api/path/validate?path=" + encodeURIComponent(dir) + "&kind=dir");
      state = await resp.json();
    } catch (e) {
      return true;
    }
    if (!state || state.valid !== false) return true;
    const err = (state.error || "").toString();
    // Validator's deterministic messages for problems that creating a directory
    // would NOT fix — surface them instead of offering to create.
    if (err === "path is not a directory" || err === "absolute path required" || err === "path is required") {
      renderSpawnError(form, new Error("Working directory: " + err));
      return false;
    }
    // Otherwise the path simply isn't there yet — offer to create it.
    const displayPath = state.path || dir;
    if (!(await confirmCreateDir(form, displayPath))) return false;
    try {
      const resp = await fetch("/api/dirs/create", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: dir }),
      });
      if (resp.ok) return true;
      let detail = "HTTP " + resp.status;
      try { const j = await resp.json(); if (j && j.error) detail = j.error; } catch (_) { /* keep status */ }
      renderSpawnError(form, new Error("Couldn’t create " + displayPath + ": " + detail));
      return false;
    } catch (e) {
      renderSpawnError(form, new Error("Couldn’t create " + displayPath + ": " + ((e && e.message) || e)));
      return false;
    }
  }

  function safeEnvFallbacks() {
    const out = {};
    document.querySelectorAll("[data-launch-env-fallback]").forEach((el) => {
      const name = el.dataset.envName || "";
      if (name) out[name] = el.value || "";
    });
    return out;
  }

  function schemaPathKind(kind) {
    return kind === "outputFile" ? "output-file" : (kind || "");
  }

  function schemaSupportedForSpawn(opt) {
    if (!opt || !opt.perLaunch) return false;
    if (opt.driverSupport && opt.driverSupport.serf === false) return false;
    return !opt.driverSupport || opt.driverSupport.serf === true;
  }

  function textInputForOption(opt, multiline) {
    const input = document.createElement(multiline ? "textarea" : "input");
    if (multiline) {
      input.className = "val-input";
      input.rows = 6;
    } else {
      input.type = opt.kind === "integer" ? "number" : "text";
    }
    input.dataset.launchField = opt.field || "";
    input.dataset.launchWireField = opt.wireField || "";
    input.dataset.launchKind = opt.kind || "";
    if (opt.pathKind) {
      input.dataset.launchPathKind = opt.pathKind;
      input.dataset.settingsDirInput = "true";
      input.addEventListener("change", () => validatePathInput(input));
    }
    if (opt.kind === "integer") {
      input.min = "0";
      input.step = "1";
    }
    return input;
  }

  function appendEnvFallback(label, opt) {
    const fallback = opt && opt.envFallback;
    if (!fallback || fallback.secret) return;
    const safe = safeEnvFallbacks();
    const value = safe[fallback.name];
    if (!value) return;
    const hint = document.createElement("span");
    hint.className = "spawn-advanced-env-fallback";
    hint.textContent = "env " + fallback.name + ": " + value;
    label.appendChild(hint);
  }

  function modelPickerControl(opt) {
    const wrap = document.createElement("span");
    wrap.className = "sp-model-wrap spawn-advanced-model";
    const hidden = document.createElement("input");
    hidden.type = "hidden";
    hidden.dataset.launchField = opt.field || "";
    hidden.dataset.launchWireField = opt.wireField || "";
    hidden.dataset.launchKind = opt.kind || "";
    const display = document.createElement("span");
    display.className = "sp-model-display";
    display.textContent = "(default)";
    const button = document.createElement("button");
    button.type = "button";
    button.dataset.settingsModelPicker = "true";
    button.textContent = "pick";
    wrap.appendChild(hidden);
    wrap.appendChild(display);
    wrap.appendChild(button);
    return { wrap, hidden, display };
  }

  function addControlsWrap() {
    const wrap = document.createElement("div");
    wrap.className = "spawn-advanced-add-controls";
    return wrap;
  }

  function renderScalarControl(row, opt) {
    const label = document.createElement("label");
    label.textContent = opt.label || opt.field;

    if (opt.kind === "select") {
      const select = document.createElement("select");
      select.dataset.launchField = opt.field || "";
      select.dataset.launchWireField = opt.wireField || "";
      select.dataset.launchKind = opt.kind || "";
      (opt.choices || []).forEach((choice) => {
        const o = document.createElement("option");
        o.value = choice.value || "";
        o.textContent = choice.label || choice.value || "(default)";
        if (choice.disabled) o.disabled = true;
        select.appendChild(o);
      });
      label.appendChild(select);
      appendEnvFallback(label, opt);
      row.appendChild(label);
      return;
    }

    if (opt.kind === "radio") {
      const wrap = document.createElement("div");
      wrap.className = "spawn-advanced-radio";
      (opt.choices || []).forEach((choice, i) => {
        const choiceLabel = document.createElement("label");
        const radio = document.createElement("input");
        radio.type = "radio";
        radio.name = "launch-" + (opt.field || "");
        radio.value = choice.value || "";
        radio.dataset.launchField = opt.field || "";
        radio.dataset.launchWireField = opt.wireField || "";
        radio.dataset.launchKind = opt.kind || "";
        if (i === 0) radio.checked = true;
        choiceLabel.appendChild(radio);
        choiceLabel.appendChild(document.createTextNode(choice.label || choice.value || "(default)"));
        wrap.appendChild(choiceLabel);
      });
      label.appendChild(wrap);
      row.appendChild(label);
      return;
    }

    if (opt.kind === "boolean") {
      const select = document.createElement("select");
      select.dataset.launchField = opt.field || "";
      select.dataset.launchWireField = opt.wireField || "";
      select.dataset.launchKind = opt.kind || "";
      [
        ["", "(default)"],
        ["true", "true"],
        ["false", "false"],
      ].forEach(([value, text]) => {
        const o = document.createElement("option");
        o.value = value;
        o.textContent = text;
        select.appendChild(o);
      });
      label.appendChild(select);
      row.appendChild(label);
      return;
    }

    if (opt.kind === "modelPicker") {
      label.appendChild(modelPickerControl(opt).wrap);
      appendEnvFallback(label, opt);
      row.appendChild(label);
      return;
    }

    const input = textInputForOption(opt, opt.kind === "multilineText");
    label.appendChild(input);
    appendEnvFallback(label, opt);
    row.appendChild(label);
  }

  function appendListRow(list, value) {
    const li = document.createElement("li");
    li.dataset.value = value;
    const span = document.createElement("span");
    span.textContent = value;
    const rm = document.createElement("button");
    rm.type = "button";
    rm.textContent = "remove";
    rm.addEventListener("click", () => li.remove());
    li.appendChild(span);
    li.appendChild(rm);
    list.appendChild(li);
  }

  function renderListControl(row, opt) {
    const wrap = document.createElement("div");
    wrap.className = "spawn-advanced-list-control";
    wrap.dataset.launchField = opt.field || "";
    wrap.dataset.launchWireField = opt.wireField || "";
    wrap.dataset.launchKind = opt.kind || "";
    if (opt.pathKind) wrap.dataset.launchPathKind = opt.pathKind;

    const title = document.createElement("div");
    title.className = "spawn-advanced-field-label";
    title.textContent = opt.label || opt.field;
    const list = document.createElement("ul");
    list.className = "spawn-advanced-list";
    list.dataset.launchList = "true";

    const controls = addControlsWrap();
    let input;
    if (opt.kind === "modelList") {
      const modelControl = modelPickerControl(opt);
      input = modelControl.hidden;
      controls.appendChild(modelControl.wrap);
    } else {
      input = document.createElement("input");
      input.type = "text";
      if (opt.pathKind) input.dataset.settingsDirInput = "true";
      controls.appendChild(input);
    }
    const add = document.createElement("button");
    add.type = "button";
    add.textContent = "add";
    add.addEventListener("click", async () => {
      const value = input.value.trim();
      if (!value) return;
      if (opt.pathKind && window.launchconfig && window.launchconfig.validatePath) {
        const result = await window.launchconfig.validatePath(value, schemaPathKind(opt.pathKind));
        if (!result || !result.valid) {
          input.setCustomValidity((result && result.error) || "invalid path");
          input.reportValidity();
          return;
        }
        input.setCustomValidity("");
        appendListRow(list, result.path || value);
      } else {
        appendListRow(list, value);
      }
      input.value = "";
      const display = controls.querySelector(".sp-model-display");
      if (display) display.textContent = "(default)";
    });
    controls.appendChild(add);

    wrap.appendChild(title);
    wrap.appendChild(list);
    wrap.appendChild(controls);
    row.appendChild(wrap);
  }

  function renderEnvControl(row, opt) {
    const wrap = document.createElement("div");
    wrap.className = "spawn-advanced-list-control";
    wrap.dataset.launchField = opt.field || "";
    wrap.dataset.launchWireField = opt.wireField || "";
    wrap.dataset.launchKind = opt.kind || "";
    const title = document.createElement("div");
    title.className = "spawn-advanced-field-label";
    title.textContent = opt.label || opt.field;
    const list = document.createElement("ul");
    list.className = "spawn-advanced-list";
    list.dataset.launchEnvList = "true";
    const controls = addControlsWrap();
    const name = document.createElement("input");
    name.type = "text";
    name.placeholder = "NAME";
    const value = document.createElement("input");
    value.type = "text";
    value.placeholder = "value";
    const add = document.createElement("button");
    add.type = "button";
    add.textContent = "add";
    add.addEventListener("click", () => {
      const k = name.value.trim();
      if (!k) return;
      const li = document.createElement("li");
      li.dataset.name = k;
      li.dataset.value = value.value;
      const span = document.createElement("span");
      span.textContent = k + "=" + value.value;
      const rm = document.createElement("button");
      rm.type = "button";
      rm.textContent = "remove";
      rm.addEventListener("click", () => li.remove());
      li.appendChild(span);
      li.appendChild(rm);
      list.appendChild(li);
      name.value = "";
      value.value = "";
    });
    controls.appendChild(name);
    controls.appendChild(value);
    controls.appendChild(add);
    wrap.appendChild(title);
    wrap.appendChild(list);
    wrap.appendChild(controls);
    row.appendChild(wrap);
  }

  function renderMCPControl(row, opt) {
    const wrap = document.createElement("div");
    wrap.className = "spawn-advanced-list-control";
    wrap.dataset.launchField = opt.field || "";
    wrap.dataset.launchWireField = opt.wireField || "";
    wrap.dataset.launchKind = opt.kind || "";
    const title = document.createElement("div");
    title.className = "spawn-advanced-field-label";
    title.textContent = opt.label || opt.field;
    const list = document.createElement("ul");
    list.className = "spawn-advanced-list";
    list.dataset.launchMcpList = "true";
    const controls = addControlsWrap();
    const name = document.createElement("input");
    name.type = "text";
    name.placeholder = "name";
    const command = document.createElement("input");
    command.type = "text";
    command.placeholder = "command";
    command.dataset.launchMcpCommand = "true";
    command.addEventListener("change", () => validateMCPCommandInput(command));
    const args = document.createElement("input");
    args.type = "text";
    args.placeholder = "args";
    const add = document.createElement("button");
    add.type = "button";
    add.textContent = "add";
    add.addEventListener("click", async () => {
      const spec = { name: name.value.trim(), command: command.value.trim(), args: args.value.trim() ? args.value.trim().split(/\s+/) : [] };
      if (!spec.name || !spec.command) return;
      if (!(await validateMCPCommandInput(command))) return;
      spec.command = command.value.trim();
      const li = document.createElement("li");
      li.dataset.spec = JSON.stringify(spec);
      const span = document.createElement("span");
      span.textContent = spec.name + ": " + spec.command + (spec.args.length ? " " + spec.args.join(" ") : "");
      const rm = document.createElement("button");
      rm.type = "button";
      rm.textContent = "remove";
      rm.addEventListener("click", () => li.remove());
      li.appendChild(span);
      li.appendChild(rm);
      list.appendChild(li);
      name.value = "";
      command.value = "";
      args.value = "";
    });
    controls.appendChild(name);
    controls.appendChild(command);
    controls.appendChild(args);
    controls.appendChild(add);
    wrap.appendChild(title);
    wrap.appendChild(list);
    wrap.appendChild(controls);
    row.appendChild(wrap);
  }

  function renderSchemaOption(group, opt) {
    const row = document.createElement("div");
    row.className = "spawn-advanced-row";
    row.dataset.launchOption = opt.field || "";
    if (opt.kind === "pathList" || opt.kind === "modelList") {
      renderListControl(row, opt);
    } else if (opt.kind === "envMap") {
      renderEnvControl(row, opt);
    } else if (opt.kind === "mcpServerList") {
      renderMCPControl(row, opt);
    } else {
      renderScalarControl(row, opt);
    }
    group.appendChild(row);
  }

  async function renderSchemaAdvanced() {
    const root = document.querySelector("[data-launch-advanced-root]");
    if (!root || root.__schemaRendered) return;
    root.__schemaRendered = true;
    const loading = root.querySelector("[data-launch-schema-loading]");
    const groupsRoot = root.querySelector("[data-launch-advanced-groups]") || root;
    if (!window.launchconfig || !window.launchconfig.schema) {
      if (loading) loading.textContent = "Advanced launch schema unavailable.";
      return;
    }
    if (loading) loading.hidden = false;
    try {
      const schema = await window.launchconfig.schema();
      const options = ((schema && schema.options) || []).filter(schemaSupportedForSpawn);
      if (window.LaunchConfigControls && window.LaunchConfigControls.render) {
        window.LaunchConfigControls.render(root, {
          mode: "spawn",
          options,
          groupsRoot,
          includeEnvFallbacks: true,
          envFallbacks: safeEnvFallbacks(),
        });
      } else {
        groupsRoot.innerHTML = "";
        let currentGroup = "";
        let fieldset = null;
        options.forEach((opt) => {
          if (opt.group !== currentGroup) {
            currentGroup = opt.group || "";
            fieldset = document.createElement("fieldset");
            fieldset.className = "spawn-advanced-group";
            fieldset.dataset.launchGroup = currentGroup;
            const legend = document.createElement("legend");
            legend.textContent = currentGroup;
            fieldset.appendChild(legend);
            groupsRoot.appendChild(fieldset);
          }
          renderSchemaOption(fieldset, opt);
        });
      }
      if (loading) loading.hidden = true;
      if (window.SettingsPickers && window.SettingsPickers.init) {
        window.SettingsPickers.init(root);
      }
    } catch (err) {
      if (loading) loading.textContent = "Advanced launch schema unavailable.";
    }
  }

  async function validatePathInput(input) {
    if (!input || !input.dataset.launchPathKind || typeof input.setCustomValidity !== "function") return true;
    const value = (input.value || "").trim();
    input.dataset.launchInvalid = "";
    input.setCustomValidity("");
    if (!value) return true;
    if (!window.launchconfig || !window.launchconfig.validatePath) return true;
    const result = await window.launchconfig.validatePath(value, schemaPathKind(input.dataset.launchPathKind));
    if (!result || !result.valid) {
      input.dataset.launchInvalid = "true";
      input.setCustomValidity((result && result.error) || "invalid path");
      input.reportValidity();
      return false;
    }
    input.value = result.path || value;
    input.setCustomValidity("");
    return true;
  }

  function validateAdvancedPathScalars() {
    const inputs = Array.from(document.querySelectorAll("input[data-launch-path-kind][data-launch-wire-field], textarea[data-launch-path-kind][data-launch-wire-field], select[data-launch-path-kind][data-launch-wire-field]"));
    if (inputs.length === 0) return true;
    return (async () => {
      for (const input of inputs) {
        if (!(await validatePathInput(input))) return false;
      }
      return true;
    })();
  }

  async function validateMCPCommandInput(input) {
    if (!input) return true;
    const value = (input.value || "").trim();
    input.dataset.launchInvalid = "";
    input.setCustomValidity("");
    if (!value) return true;
    if (!window.launchconfig || !window.launchconfig.validatePath) return true;
    const result = await window.launchconfig.validatePath(value, "command");
    if (!result || !result.valid) {
      input.dataset.launchInvalid = "true";
      input.setCustomValidity((result && result.error) || "invalid command");
      input.reportValidity();
      return false;
    }
    input.value = result.path || value;
    input.setCustomValidity("");
    return true;
  }

  function validateAdvancedMCPCommands() {
    const commands = Array.from(document.querySelectorAll("[data-launch-mcp-command]"));
    if (commands.length === 0) return true;
    return (async () => {
      for (const input of commands) {
        if (!(await validateMCPCommandInput(input))) return false;
      }
      return true;
    })();
  }

  async function validateAdvancedControls() {
    const pathResult = validateAdvancedPathScalars();
    if (pathResult !== true && !(await pathResult)) return false;
    const mcpResult = validateAdvancedMCPCommands();
    if (mcpResult !== true && !(await mcpResult)) return false;
    return true;
  }

  function collectAdvancedOverrides() {
    const overrides = {};
    document.querySelectorAll("[data-launch-wire-field]").forEach((el) => {
      const wire = el.dataset.launchWireField;
      const kind = el.dataset.launchKind;
      if (!wire || kind === "pathList" || kind === "modelList" || kind === "mcpServerList" || kind === "envMap") return;
      if (el.type === "radio" && !el.checked) return;
      if (kind === "boolean") {
        if (el.value === "true") overrides[wire] = true;
        if (el.value === "false") overrides[wire] = false;
        return;
      }
      if (el.dataset.launchInvalid === "true") return;
      const value = (el.value || "").trim();
      if (!value) return;
      overrides[wire] = kind === "integer" ? Number(value) : value;
    });
    document.querySelectorAll(".settings-collection[data-launch-wire-field]").forEach((wrap) => {
      const wire = wrap.dataset.launchWireField;
      const kind = wrap.dataset.launchKind;
      if (!wire) return;
      if (kind === "envMap") {
        const env = {};
        wrap.querySelectorAll("[data-launch-env-list] li").forEach((li) => {
          if (li.dataset.name) env[li.dataset.name] = li.dataset.value || "";
        });
        if (Object.keys(env).length) overrides[wire] = env;
        return;
      }
      if (kind === "mcpServerList") {
        const specs = [];
        wrap.querySelectorAll("[data-launch-mcp-list] li").forEach((li) => {
          try { specs.push(JSON.parse(li.dataset.spec || "{}")); } catch (e) { /* skip malformed local row */ }
        });
        if (specs.length) overrides[wire] = specs;
        return;
      }
      const values = Array.from(wrap.querySelectorAll("[data-launch-list] li"))
        .map((li) => li.dataset.value || "")
        .filter(Boolean);
      if (values.length) overrides[wire] = values;
    });
    return Object.keys(overrides).length ? overrides : undefined;
  }

  function advancedOverrideValue(overrides, wireField) {
    overrides = overrides || {};
    return Object.prototype.hasOwnProperty.call(overrides, wireField) ? overrides[wireField] : "";
  }

  function init() {
    const form = document.querySelector("[data-spawn-form]");
    if (!form) return;

    // Apply sticky defaults on top of server-provided defaults
    const defaults = loadDefaults();
    // Apply branch before working_dir so the HEAD-resolution triggered by
    // setChipValue("working_dir") can check whether a branch is already set.
    ["harness", "branch", "access_mode"].forEach(k => {
      if (defaults[k]) setChipValue(k, defaults[k]);
    });
    if (defaults.working_dir) setChipValue("working_dir", defaults.working_dir);
    // If the server pre-filled working_dir (via ?dir= param) and no defaults
    // override it, still resolve the HEAD branch for the pre-filled value.
    if (!defaults.working_dir) {
      const prefilledDir = currentWorkingDir();
      if (prefilledDir) resolveAndSetHeadBranch(prefilledDir);
    }
    if (harnessUsesSerfModels(currentHarness()) && defaults.model) {
      setChipValue("model", defaults.model);
    } else {
      applyHarnessModelPolicy(currentHarness());
    }

    // Validate the pre-filled model against the harness model list.
    // Drops stale localStorage entries (e.g. `openai/gpt-5-mini` after the
    // harness retires it) before the user submits and hits a 503.
    validatePrefilledModel(form);

    renderSchemaAdvanced();

    // Scroll the advanced section into view when it opens so the content
    // isn't clipped below the viewport.
    const advancedDetails = form.querySelector(".spawn-advanced");
    if (advancedDetails) {
      advancedDetails.addEventListener("toggle", () => {
        if (advancedDetails.open) {
          advancedDetails.scrollIntoView({ block: "nearest", behavior: "smooth" });
        }
      });
    }

    // Show resolved config
    const showResolvedBtn = document.getElementById("ovr-show-resolved");
    if (showResolvedBtn) {
      showResolvedBtn.addEventListener("click", async () => {
        if (!(await validateAdvancedControls())) return;
        const cwd = document.querySelector("[name=working_dir]").value;
        const overrides = collectAdvancedOverrides();
        const r = await launchconfig.resolve(cwd, overrides);
        document.getElementById("ovr-resolved-out").textContent = JSON.stringify(r, null, 2);
      });
    }

    // Chip pickers
    document.querySelectorAll(".btn-chip").forEach(chip => {
      chip.addEventListener("click", () => openPicker(chip));
    });

    // Recent prompt click pre-fills
    document.querySelectorAll("[data-recent-prompt]").forEach(row => {
      row.addEventListener("click", () => {
        form.querySelector("textarea[name=prompt]").value = row.dataset.recentPrompt;
        form.querySelector("textarea[name=prompt]").focus();
      });
    });

    // ⌘↵ submits
    form.querySelector("textarea[name=prompt]").addEventListener("keydown", (e) => {
      if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        form.requestSubmit();
      }
    });

    // Wire the shared composer-attachments helpers (paste from r6a1; drag-
    // drop + file picker from 65mm). pendingState lives on the form element
    // so the (forthcoming v80q) submit wiring can read it back when
    // serializing the spawn body. Chips render into the
    // [data-composer-attachments] container just above the textarea; the
    // [data-attachment-error] sibling surfaces rejection banners.
    if (window.SerfComposerAttachments) {
      const promptTa = form.querySelector("textarea[name=prompt]");
      const pasteContainer = form.querySelector("[data-composer-attachments]");
      const dropZone = form.querySelector("[data-drop-zone]");
      const attachTrigger = form.querySelector("[data-attach-trigger]");
      const filePicker = form.querySelector("[data-file-picker]");
      const pendingState = { items: [] };
      form.__composerPasteState = pendingState;
      if (promptTa) window.SerfComposerAttachments.attachComposerImageHandlers(promptTa, pendingState);
      if (dropZone) window.SerfComposerAttachments.attachComposerDropHandlers(dropZone, pendingState);
      if (attachTrigger && filePicker) {
        window.SerfComposerAttachments.attachComposerFilePickerHandlers(attachTrigger, filePicker, pendingState);
      }
      if (pasteContainer) window.SerfComposerAttachments.renderAttachmentChips(pasteContainer, pendingState);
    }

    // Submit handler
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const fd = new FormData(form);
      // Guard against empty-prompt submissions. The picker search input
      // and other inner inputs can trigger implicit form submission via
      // Enter; without this check the user would land on a 0-turn session
      // and not know why. Trim only for the *check* — preserve newlines
      // and leading whitespace in the payload itself.
      const rawPrompt = (fd.get("prompt") || "").toString();
      // Pull pending image attachments off the form-attached state set up
      // earlier in init(). Empty array when none — appwire (and the /api/spawn
      // REST shim) both treat an empty items list as "no attachments".
	      const pendingState = form.__composerPasteState || { items: [] };
	      const attachments = (pendingState.items || []).slice();
	      if (attachments.some((item) => item && item.pending)) {
	        renderSpawnError(form, new Error("Image attachment is still processing."));
	        return;
	      }
	      if (!rawPrompt.trim() && attachments.length === 0) {
        renderSpawnError(form, new Error("Prompt is empty. Type something before spawning."));
        const ta = form.querySelector('textarea[name=prompt]');
        if (ta) ta.focus();
        return;
      }
      const validationResult = validateAdvancedPathScalars();
      if (validationResult !== true && !(await validationResult)) return;
      const mcpValidation = validateAdvancedMCPCommands();
      if (mcpValidation !== true && !(await mcpValidation)) return;
      const launchOverrides = collectAdvancedOverrides();
      const chipModel = fd.get("model") || "";
      const body = {
        launch_overrides: launchOverrides,
        prompt: rawPrompt,
        harness: fd.get("harness") || "serf",
        model: advancedOverrideValue(launchOverrides, "model") || chipModel,
        working_dir: fd.get("working_dir") || "",
        branch: fd.get("branch") || "",
        access_mode: fd.get("access_mode") || "full",
        agent: advancedOverrideValue(launchOverrides, "agent") || fd.get("agent") || "default",
        reasoning_effort: advancedOverrideValue(launchOverrides, "reasoningEffort") || fd.get("reasoning_effort") || "",
        // appwire.startThread reads body.attachments; the REST fallback
        // below converts the same set to base64-encoded items in the JSON
        // payload (the wire shape /api/spawn already accepts via
        // appwire.InputItem.Data).
        attachments,
      };
      // If the proposed working directory doesn't exist yet, offer to create it
      // before spawning (rather than letting the daemon fail to launch in it).
      const proposedDir = (body.working_dir || "").trim();
      if (proposedDir && !(await ensureWorkingDir(form, proposedDir))) return;
      // Persist sticky defaults (excluding the prompt override and the
      // ephemeral attachments list).
      saveDefaults({
        model: chipModel,
        harness: body.harness,
        working_dir: body.working_dir,
        branch: body.branch,
        access_mode: body.access_mode,
      });
      clearSpawnError(form);
      const btn = form.querySelector(".spawn-btn");
      if (btn) { btn.disabled = true; btn.textContent = "spawning…"; }
      try {
        let json;
        if (window.SerfAppwire) {
          json = await window.SerfAppwire.startThread(body);
        } else {
          // No-appwire fallback path. Encode attachments to the items shape
          // the /api/spawn handler expects (base64-encoded Data field). We
          // strip the local `attachments` blob from the body so it doesn't
          // accidentally serialize ArrayBuffer placeholders.
          const restBody = Object.assign({}, body);
          delete restBody.attachments;
          restBody.items = attachments.map((a) => ({
            type: "image",
            mediaType: a.mediaType || "",
            data: spawnEncodeAttachmentData(a.data),
            name: a.name || "",
          }));
          json = await fetch("/api/spawn", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(restBody),
          }).then(async (resp) => {
            if (!resp.ok) throw new Error(spawnErrorMessage(await resp.text()));
            return resp.json();
          });
        }
        // Successful response: clear the pending bag so a back-button
        // return doesn't double-send the same images on retry.
        pendingState.items = [];
        if (window.SerfComposerAttachments) {
          window.SerfComposerAttachments.resetMarkerCounter(pendingState);
        }
        window.location.href = sessionPath(json);
      } catch (err) {
        if (btn) { btn.disabled = false; btn.innerHTML = 'spawn <kbd>⌘↵</kbd>'; }
        renderSpawnError(form, err);
      }
    });
  }

  function openPicker(chip) {
    const kind = chip.dataset.chip;
    if (kind === "harness") { openHarnessPicker(chip); return; }
    if (kind === "model") { openModelPicker(chip); return; }
    if (kind === "reasoning_effort") { openEffortPicker(chip); return; }
    if (kind === "working_dir") { openDirPicker(chip); return; }
    if (kind === "branch") { openTextPicker(chip, "branch", "branch / worktree"); return; }
    const display = chip.querySelector(".chip-value");
    const current = display.textContent.trim();
    let value;
    if (kind === "access_mode") {
      value = current === "full" ? "read-only" : "full";
    }
    if (value !== null && value !== undefined && value !== "") setChipValue(kind, value);
  }

  function openTextPicker(chip, name, placeholder) {
    const existing = document.querySelector(".chip-picker");
    if (existing) { existing.remove(); return; }

    const display = chip.querySelector(".chip-value");
    const current = display ? display.textContent.trim() : "";

    const picker = document.createElement("div");
    picker.className = "chip-picker chip-picker-text";

    const input = document.createElement("input");
    input.className = "chip-picker-search";
    input.placeholder = placeholder;
    input.value = current === "(default)" ? "" : current;
    picker.appendChild(input);

    function accept() {
      const value = input.value.trim();
      if (value) setChipValue(name, value);
      picker.remove();
    }

    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        accept();
      } else if (e.key === "Escape") {
        e.preventDefault();
        picker.remove();
      }
    });

    chip.parentNode.style.position = "relative";
    chip.parentNode.appendChild(picker);
    picker.style.position = "absolute";
    placeChipPicker(picker, chip);

    input.focus();

    setTimeout(() => {
      const offClick = (e) => {
        if (!picker.contains(e.target)) {
          picker.remove();
          document.removeEventListener("click", offClick);
        }
      };
      document.addEventListener("click", offClick);
    }, 0);
  }

  function openHarnessPicker(chip) {
    const existing = document.querySelector(".chip-picker");
    if (existing) { existing.remove(); return; }
    const picker = document.createElement("div");
    picker.className = "chip-picker";
    const options = Array.from(document.querySelectorAll("[data-harness-option]")).map(el => ({
      id: el.value || "",
      label: el.dataset.label || el.value || "",
    })).filter(opt => opt.id);
    if (options.length === 0) options.push({ id: "serf", label: "serf" });
    for (const opt of options) {
      const row = document.createElement("div");
      row.className = "chip-picker-option";
      row.textContent = opt.label;
      row.addEventListener("click", () => {
        setChipValue("harness", opt.id);
        applyHarnessModelPolicy(opt.id);
        picker.remove();
      });
      picker.appendChild(row);
    }
    chip.parentNode.style.position = "relative";
    chip.parentNode.appendChild(picker);
    picker.style.position = "absolute";
    placeChipPicker(picker, chip);
    attachPickerDismiss(picker);
  }

  function openModelPicker(chip) {
    const existing = document.querySelector(".chip-picker");
    if (existing) { existing.remove(); return; }
    const harness = currentHarness();

    const modelsPromise = listModelsWithDiagnosticsForHarness(harness);
    modelsPromise.then(result => {
      const models = Array.isArray(result && result.models) ? result.models : [];
      const diagnostics = Array.isArray(result && result.diagnostics) ? result.diagnostics : [];
      if (models.length === 0 && diagnostics.length === 0 && !harnessUsesSerfModels(harness)) {
        openHarnessDefaultModelPicker(chip);
        return;
      }

      // Group by provider
      const byProvider = {};
      models.forEach(m => {
        if (!byProvider[m.provider]) byProvider[m.provider] = [];
        byProvider[m.provider].push(m);
      });
      const providers = Object.keys(byProvider).sort();

      const picker = document.createElement("div");
      picker.className = "chip-picker chip-picker-wide";

      // Search box
      const search = document.createElement("input");
      search.className = "chip-picker-search";
      search.placeholder = "search models…";
      picker.appendChild(search);

      // One scrollable list grouped by provider — no master-detail drill, so a
      // tap selects a model directly (and never re-renders the tapped element
      // out from under the dismiss handler, which used to false-close it).
      const list = document.createElement("div");
      list.className = "chip-picker-list";
      picker.appendChild(list);

      // Configured providers whose model listing failed (bad key, network,
      // provider down) are reported here so they don't silently vanish.
      if (diagnostics.length > 0) {
        const diagBox = document.createElement("div");
        diagBox.className = "chip-picker-diagnostics";
        diagnostics.forEach(d => {
          const row = document.createElement("div");
          row.className = "chip-picker-diagnostic";
          const prov = (d && d.provider) ? String(d.provider) : "provider";
          const msg = (d && d.message) ? String(d.message) : "unknown error";
          row.textContent = "⚠ " + prov + " unavailable: " + msg;
          if (d && d.hint) row.title = String(d.hint);
          diagBox.appendChild(row);
        });
        picker.appendChild(diagBox);
      }

      function formatCtx(n) {
        if (n >= 1000000) return (n / 1000000).toFixed(1).replace(".0", "") + "M";
        if (n >= 1000) return (n / 1000).toFixed(0) + "K";
        return String(n);
      }

      function selectModel(m) {
        if (harnessUsesSerfModels(harness)) {
          setChipValue("model", m.provider + "/" + m.model);
        } else {
          setModelValue(m.model, modelOptionLabel(m));
        }
        picker.remove();
      }

      function renderList(filter) {
        list.innerHTML = "";
        let shown = 0;
        providers.forEach(p => {
          const matches = byProvider[p].filter(m =>
            !filter || (m.model + " " + (m.display_name || "")).toLowerCase().includes(filter)
          );
          if (matches.length === 0) return;
          const header = document.createElement("div");
          header.className = "chip-picker-group";
          header.textContent = p;
          list.appendChild(header);
          matches.forEach(m => {
            shown++;
            const el = document.createElement("button");
            el.type = "button";
            el.className = "chip-picker-model";
            const name = document.createElement("div");
            name.className = "chip-picker-model-name";
            name.textContent = m.model;
            const meta = document.createElement("div");
            meta.className = "chip-picker-model-meta";
            const parts = [];
            if (m.context_window) parts.push(formatCtx(m.context_window) + " ctx");
            if (m.input_cost_per_million != null) parts.push("$" + m.input_cost_per_million.toFixed(2) + "/M in");
            if (m.output_cost_per_million != null) parts.push("$" + m.output_cost_per_million.toFixed(2) + "/M out");
            meta.textContent = parts.join(" · ");
            el.appendChild(name);
            if (parts.length) el.appendChild(meta);
            el.addEventListener("click", () => selectModel(m));
            list.appendChild(el);
          });
        });
        if (shown === 0) {
          const empty = document.createElement("div");
          empty.className = "chip-picker-empty";
          empty.textContent = filter ? "No models match." : "No models available.";
          list.appendChild(empty);
        }
      }

      search.addEventListener("input", () => renderList(search.value.toLowerCase().trim()));

      // Without an explicit keydown handler, pressing Enter inside the
      // search box triggers the enclosing form's implicit submit — and
      // since the picker lives inside <form data-spawn-form>, the user
      // ends up spawning whatever's in the textarea (often empty).
      // Enter selects the first visible model; Escape dismisses.
      search.addEventListener("keydown", (e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          const first = list.querySelector(".chip-picker-model");
          if (first) first.click();
        } else if (e.key === "Escape") {
          e.preventDefault();
          picker.remove();
        }
      });

      renderList("");

      chip.parentNode.style.position = "relative";
      chip.parentNode.appendChild(picker);
      picker.style.position = "absolute";
      placeChipPicker(picker, chip);

      search.focus();

      attachPickerDismiss(picker);
    }).catch(() => {
      if (!harnessUsesSerfModels(harness)) {
        openHarnessDefaultModelPicker(chip);
      }
    });
  }

  // attachPickerDismiss closes the picker on an outside press or Escape. It
  // listens on pointerdown (not click) and tests picker.contains(target) on the
  // LIVE target: pointerdown fires before any in-picker click handler can
  // re-render that target out of the DOM, so an inside tap is never mistaken
  // for an outside one (the old composedPath-on-click check false-closed the
  // picker whenever a tap re-rendered its own row/column).
  function attachPickerDismiss(picker) {
    function dismiss() {
      picker.remove();
      document.removeEventListener("pointerdown", offDown);
      document.removeEventListener("keydown", onKey);
    }
    function offDown(e) {
      if (!picker.contains(e.target)) dismiss();
    }
    function onKey(e) {
      if (e.key === "Escape") {
        e.preventDefault();
        dismiss();
      }
    }
    setTimeout(() => {
      document.addEventListener("pointerdown", offDown);
      document.addEventListener("keydown", onKey);
    }, 0);
  }

  // Fallback effort levels for models whose supported set the hub doesn't know.
  // The daemon clamps to what the model actually accepts, so an over-broad list
  // here is safe.
  const DEFAULT_EFFORT_LEVELS = ["minimal", "low", "medium", "high"];

  // effortLevelsForModel returns the reasoning-effort levels the given model
  // (a "provider/model" ref) supports, from the /api/models entry, falling back
  // to a default set when the model isn't found or declares none.
  function effortLevelsForModel(models, modelRef) {
    modelRef = (modelRef || "").trim();
    for (let i = 0; i < models.length; i++) {
      const m = models[i];
      const full = (m.provider ? m.provider + "/" : "") + m.model;
      if (modelRef && (full === modelRef || m.model === modelRef)) {
        const lvls = m.reasoning_effort_levels || m.reasoningEffortLevels;
        if (Array.isArray(lvls) && lvls.length > 0) {
          return lvls.slice();
        }
        return DEFAULT_EFFORT_LEVELS.slice();
      }
    }
    return DEFAULT_EFFORT_LEVELS.slice();
  }

  // fetchEnrichedModelsForHarness fetches the REST /api/models response, which
  // (unlike the appwire model list) carries per-model reasoning_effort_levels.
  function fetchEnrichedModelsForHarness(harness) {
    const params = {};
    if (!harnessUsesSerfModels(harness)) params.harness = harness;
    const cwd = currentWorkingDir();
    if (cwd) params.cwd = cwd;
    const query = new URLSearchParams();
    Object.keys(params).forEach(k => query.set(k, params[k]));
    const suffix = query.toString() ? "?" + query.toString() : "";
    return fetch("/api/models" + suffix)
      .then(r => r.json())
      .then(d => Array.isArray(d) ? d : (d && Array.isArray(d.models) ? d.models : []))
      .catch(() => []);
  }

  function openEffortPicker(chip) {
    const existing = document.querySelector(".chip-picker");
    if (existing) { existing.remove(); return; }
    const harness = currentHarness();
    // Reasoning effort is a serf launch-config setting; non-serf harnesses
    // (e.g. codex) ignore it, so say so instead of offering levels that no-op.
    if (!harnessUsesSerfModels(harness)) {
      const picker = document.createElement("div");
      picker.className = "chip-picker";
      const note = document.createElement("div");
      note.className = "chip-picker-option";
      note.textContent = "(reasoning effort applies to the serf harness only)";
      picker.appendChild(note);
      chip.parentNode.style.position = "relative";
      chip.parentNode.appendChild(picker);
      picker.style.position = "absolute";
      placeChipPicker(picker, chip);
      attachPickerDismiss(picker);
      return;
    }
    // Use the REST /api/models response: only it carries per-model
    // reasoning_effort_levels (the appwire model list returns provider/model
    // only), which the picker needs to offer the selected model's levels.
    fetchEnrichedModelsForHarness(harness).then(models => {
      const modelHidden = document.querySelector('input[name="model"]');
      const levels = effortLevelsForModel(models, modelHidden ? modelHidden.value : "");

      const picker = document.createElement("div");
      picker.className = "chip-picker";
      // Launch context: "(default)" means "inherit the global/project default",
      // while "none" overrides it to empty — the only way to clear an inherited
      // high/max. Both are offered (they differ here, unlike at runtime).
      const options = [{ value: "", label: "(default)" }];
      levels.forEach(l => options.push({ value: l, label: l }));
      options.push({ value: "none", label: "none" });
      options.forEach(opt => {
        const row = document.createElement("div");
        row.className = "chip-picker-option";
        row.textContent = opt.label;
        row.addEventListener("click", () => {
          setChipValue("reasoning_effort", opt.value);
          picker.remove();
        });
        picker.appendChild(row);
      });

      chip.parentNode.style.position = "relative";
      chip.parentNode.appendChild(picker);
      picker.style.position = "absolute";
      placeChipPicker(picker, chip);
      attachPickerDismiss(picker);
    });
  }

  function listModelsForHarness(harness) {
    const params = {};
    if (!harnessUsesSerfModels(harness)) params.harness = harness;
    const cwd = currentWorkingDir();
    if (cwd) params.cwd = cwd;
    if (window.SerfAppwire) {
      return window.SerfAppwire.listModels(params);
    }
    const query = new URLSearchParams();
    Object.keys(params).forEach(k => query.set(k, params[k]));
    const suffix = query.toString() ? "?" + query.toString() : "";
    return fetch("/api/models" + suffix).then(r => r.json());
  }

  // listModelsWithDiagnosticsForHarness resolves to {models, diagnostics} so the
  // picker can report configured providers whose listing failed instead of
  // dropping them silently. Falls back gracefully if the server or appwire path
  // only returns a bare model array.
  function listModelsWithDiagnosticsForHarness(harness) {
    const params = {};
    if (!harnessUsesSerfModels(harness)) params.harness = harness;
    const cwd = currentWorkingDir();
    if (cwd) params.cwd = cwd;
    if (window.SerfAppwire && typeof window.SerfAppwire.listModelsWithDiagnostics === "function") {
      return window.SerfAppwire.listModelsWithDiagnostics(params);
    }
    const query = new URLSearchParams();
    Object.keys(params).forEach(k => query.set(k, params[k]));
    query.set("diagnostics", "1");
    return fetch("/api/models?" + query.toString())
      .then(r => r.json())
      .then(d => ({
        models: (d && Array.isArray(d.models)) ? d.models : (Array.isArray(d) ? d : []),
        diagnostics: (d && Array.isArray(d.diagnostics)) ? d.diagnostics : [],
      }));
  }

  function modelOptionLabel(model) {
    const name = model && model.model ? String(model.model) : "";
    const provider = model && model.provider ? String(model.provider) : "";
    return provider ? provider + "/" + name : name;
  }

  function openHarnessDefaultModelPicker(chip) {
    const picker = document.createElement("div");
    picker.className = "chip-picker";
    const row = document.createElement("div");
    row.className = "chip-picker-option";
    row.textContent = modelPlaceholder(currentHarness());
    row.addEventListener("click", () => {
      setModelValue("");
      picker.remove();
    });
    picker.appendChild(row);
    chip.parentNode.style.position = "relative";
    chip.parentNode.appendChild(picker);
    picker.style.position = "absolute";
    placeChipPicker(picker, chip);
    attachPickerDismiss(picker);
  }

  function openDirPicker(chip) {
    if (!window.SerfDirPicker || typeof window.SerfDirPicker.open !== "function") return;

    const display = chip.querySelector(".chip-value");
    const chipText = display ? display.textContent.trim() : "";
    const current = (chipText === "(pick a directory)") ? "" : chipText;
    const fallback = window.localStorage.getItem("serf-hub.spawn-defaults.global.last-working-dir") || "";

    window.SerfDirPicker.open({
      anchor: chip,
      currentValue: current || fallback,
      placeholder: "/path/to/repo",
      onAccept(value) {
        setChipValue("working_dir", value);
        window.localStorage.setItem("serf-hub.spawn-defaults.global.last-working-dir", value);
      },
    });
  }

  // Re-init whenever a spawn form appears in the DOM (initial load or
  // workspace swap). Idempotent via a per-form marker.
  function tryInit() {
    const form = document.querySelector("[data-spawn-form]");
    if (!form || form.__spawnInitialized) return;
    form.__spawnInitialized = true;
    init();
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", tryInit);
  } else {
    tryInit();
  }
  document.addEventListener("DOMContentLoaded", () => {
    document.body.addEventListener("htmx:afterSwap", tryInit);
  });
  if (document.body) document.body.addEventListener("htmx:afterSwap", tryInit);
  window.SerfSpawn = {
    sessionPath,
    spawnErrorMessage,
    abbreviateModel,
  };
})();
