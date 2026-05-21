// launchconfig.js — wrappers around serf/launch/* and serf/auth/* RPCs
// plus shared schema-backed launch option controls.
(function (global) {
  function request(method, params) {
    return global.SerfAppwire.request(method, params);
  }

  global.launchconfig = {
    schema: () => request("serf/launch/schema", {}),
    resolve: (cwd, overrides) =>
      request("serf/launch/resolve", { cwd, launchOverrides: overrides || undefined }),
    getLayer: (cwd, layer) => request("serf/launch/getLayer", { cwd, layer }),
    setLayer: (cwd, layer, config) => request("serf/launch/setLayer", { cwd, layer, config }),
    trustRepo: (cwd, hash) => request("serf/launch/trustRepo", { cwd, hash }),
    validatePath: (path, kind) => {
      if (global.SerfAppwire && global.SerfAppwire.validatePath) {
        return global.SerfAppwire.validatePath(path, kind);
      }
      return fetch("/api/path/validate?path=" + encodeURIComponent(path || "") + "&kind=" + encodeURIComponent(kind || ""), {
        credentials: "same-origin",
      }).then(r => r.json());
    },

    authList: () => request("serf/auth/list", {}),
    authStatus: (provider) => request("serf/auth/status", { provider }),
    authApiKeySet: (provider, value) => request("serf/auth/apiKey/set", { provider, value }),
    authLoginStart: (provider) => request("serf/auth/login/start", { provider }),
    authLoginComplete: (provider, flowId, redirectUrl) =>
      request("serf/auth/login/complete", { provider, flowId, redirectUrl }),
    authLogout: (provider) => request("serf/auth/logout", { provider }),
  };

  function schemaPathKind(kind) {
    return kind === "outputFile" ? "output-file" : (kind || "");
  }

  function optionSupportsLayer(opt, layer) {
    return !!opt && Array.isArray(opt.defaultableLayers) && opt.defaultableLayers.indexOf(layer) >= 0;
  }

  function optionSupportsSpawn(opt) {
    if (!opt || !opt.perLaunch) return false;
    if (opt.driverSupport && opt.driverSupport.serf === false) return false;
    return !opt.driverSupport || opt.driverSupport.serf === true;
  }

  function controlClass(mode, name) {
    return mode === "spawn" ? "spawn-advanced-" + name : "settings-launch-" + name;
  }

  function addControlsWrap(mode) {
    const wrap = document.createElement("div");
    wrap.className = controlClass(mode, "add-controls") + (mode === "settings" ? " settings-add-row" : "");
    return wrap;
  }

  function setControlData(el, opt) {
    el.dataset.launchField = opt.field || "";
    el.dataset.launchWireField = opt.wireField || "";
    el.dataset.launchKind = opt.kind || "";
    if (opt.pathKind) el.dataset.launchPathKind = opt.pathKind;
  }

  function fieldWrapFor(el) {
    if (!el || !el.closest) return null;
    if (el.classList && el.classList.contains("spawn-advanced-list-control")) return el;
    return el.closest(".spawn-advanced-list-control[data-launch-wire-field], [data-launch-option]");
  }

  function errorElFor(wrap) {
    if (!wrap) return null;
    let err = wrap.querySelector(":scope > [data-launch-validation-error]");
    if (!err) {
      err = document.createElement("div");
      err.dataset.launchValidationError = "true";
      err.className = "settings-error launch-validation-error";
      err.hidden = true;
      wrap.appendChild(err);
    }
    return err;
  }

  function setFieldError(wrap, message) {
    const err = errorElFor(wrap);
    if (!err) return;
    err.textContent = message || "";
    err.hidden = !message;
    if (message) wrap.dataset.launchInvalid = "true";
    else delete wrap.dataset.launchInvalid;
  }

  function clearValidation(root) {
    root.querySelectorAll("[data-launch-validation-error]").forEach((el) => {
      el.textContent = "";
      el.hidden = true;
    });
    root.querySelectorAll("[data-launch-invalid]").forEach((el) => {
      delete el.dataset.launchInvalid;
    });
  }

  function textInputForOption(opt, mode, multiline) {
    const input = document.createElement(multiline ? "textarea" : "input");
    if (multiline) {
      input.className = mode === "spawn" ? "spawn-advanced-textarea" : "settings-launch-textarea";
      input.rows = 6;
    } else {
      input.type = opt.kind === "integer" ? "number" : "text";
    }
    setControlData(input, opt);
    input.name = opt.wireField || opt.field || "";
    if (opt.pathKind) {
      input.dataset.settingsDirInput = "true";
      input.addEventListener("change", () => validatePathInput(input));
    }
    if (opt.kind === "integer") {
      input.min = "0";
      input.step = "1";
    }
    return input;
  }

  function modelPickerControl(opt, mode, placeholder) {
    const wrap = document.createElement("span");
    wrap.className = "sp-model-wrap " + (mode === "spawn" ? "spawn-advanced-model" : "settings-launch-model");
    const hidden = document.createElement("input");
    hidden.type = "hidden";
    hidden.name = opt.wireField || opt.field || "";
    setControlData(hidden, opt);
    const button = document.createElement("button");
    button.type = "button";
    button.className = "sp-model-btn";
    button.dataset.settingsModelPicker = "true";
    const display = document.createElement("span");
    display.className = "sp-model-display";
    display.textContent = placeholder || "(default)";
    const caret = document.createElement("span");
    caret.className = "sp-model-caret";
    caret.textContent = "▾";
    button.appendChild(display);
    button.appendChild(caret);
    const clear = document.createElement("button");
    clear.type = "button";
    clear.className = "sp-clear-btn";
    clear.title = "clear";
    clear.textContent = "✕";
    clear.addEventListener("click", () => {
      hidden.value = "";
      hidden.dispatchEvent(new Event("change", { bubbles: true }));
      display.textContent = placeholder || "(default)";
    });
    wrap.appendChild(hidden);
    wrap.appendChild(button);
    wrap.appendChild(clear);
    return { wrap, hidden, display };
  }

  function appendEnvFallback(label, opt, envFallbacks) {
    const fallback = opt && opt.envFallback;
    if (!fallback || fallback.secret || !envFallbacks) return;
    const value = envFallbacks[fallback.name];
    if (!value) return;
    const hint = document.createElement("span");
    hint.className = "spawn-advanced-env-fallback";
    hint.textContent = "env " + fallback.name + ": " + value;
    label.appendChild(hint);
  }

  function renderScalarControl(row, opt, ctx) {
    const label = document.createElement("label");
    label.textContent = opt.label || opt.field;
    const placeholder = ctx.layer === "project" ? "(use global default)" : "(default)";

    if (opt.kind === "select") {
      const select = document.createElement("select");
      select.name = opt.wireField || opt.field || "";
      setControlData(select, opt);
      const empty = document.createElement("option");
      empty.value = "";
      empty.textContent = placeholder;
      select.appendChild(empty);
      (opt.choices || []).forEach((choice) => {
        const o = document.createElement("option");
        o.value = choice.value || "";
        o.textContent = choice.label || choice.value || "(default)";
        if (choice.disabled) o.disabled = true;
        select.appendChild(o);
      });
      label.appendChild(select);
      appendEnvFallback(label, opt, ctx.envFallbacks);
      row.appendChild(label);
      return;
    }

    if (opt.kind === "radio") {
      const wrap = document.createElement("div");
      wrap.className = controlClass(ctx.mode, "radio");
      const emptyLabel = document.createElement("label");
      const emptyRadio = document.createElement("input");
      emptyRadio.type = "radio";
      emptyRadio.name = "launch-" + (ctx.rootId || "") + "-" + (opt.field || "");
      emptyRadio.value = "";
      emptyRadio.checked = true;
      setControlData(emptyRadio, opt);
      emptyLabel.appendChild(emptyRadio);
      emptyLabel.appendChild(document.createTextNode(placeholder));
      wrap.appendChild(emptyLabel);
      (opt.choices || []).forEach((choice) => {
        const choiceLabel = document.createElement("label");
        const radio = document.createElement("input");
        radio.type = "radio";
        radio.name = emptyRadio.name;
        radio.value = choice.value || "";
        setControlData(radio, opt);
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
      select.name = opt.wireField || opt.field || "";
      setControlData(select, opt);
      [[ "", placeholder ], [ "true", "true" ], [ "false", "false" ]].forEach(([value, text]) => {
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
      label.appendChild(modelPickerControl(opt, ctx.mode, placeholder).wrap);
      appendEnvFallback(label, opt, ctx.envFallbacks);
      row.appendChild(label);
      return;
    }

    const input = textInputForOption(opt, ctx.mode, opt.kind === "multilineText");
    input.placeholder = placeholder;
    label.appendChild(input);
    appendEnvFallback(label, opt, ctx.envFallbacks);
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

  function clearListAddValidation(input, wrap) {
    if (input && typeof input.setCustomValidity === "function") {
      input.setCustomValidity("");
    }
    if (input && input.dataset) {
      delete input.dataset.launchInvalid;
    }
    setFieldError(wrap, "");
  }

  function renderListControl(row, opt, ctx) {
    const wrap = document.createElement("div");
    wrap.className = "spawn-advanced-list-control";
    setControlData(wrap, opt);

    const title = document.createElement("div");
    title.className = ctx.mode === "spawn" ? "spawn-advanced-field-label" : "settings-launch-field-label";
    title.textContent = opt.label || opt.field;
    const list = document.createElement("ul");
    list.className = ctx.mode === "spawn" ? "spawn-advanced-list" : "settings-items-list";
    list.dataset.launchList = "true";

    const controls = addControlsWrap(ctx.mode);
    let input;
    if (opt.kind === "modelList") {
      const modelControl = modelPickerControl(opt, ctx.mode, "(add model)");
      input = modelControl.hidden;
      controls.appendChild(modelControl.wrap);
    } else {
      input = document.createElement("input");
      input.type = "text";
      input.style.flex = "1";
      input.style.minWidth = "160px";
      if (opt.pathKind) input.dataset.settingsDirInput = "true";
      controls.appendChild(input);
    }
    input.addEventListener("input", () => clearListAddValidation(input, wrap));
    input.addEventListener("change", () => clearListAddValidation(input, wrap));
    const add = document.createElement("button");
    add.type = "button";
    add.textContent = "add";
    add.addEventListener("click", async () => {
      const value = (input.value || "").trim();
      if (!value) return;
      setFieldError(wrap, "");
      if (opt.pathKind && global.launchconfig && global.launchconfig.validatePath) {
        const result = await global.launchconfig.validatePath(value, schemaPathKind(opt.pathKind));
        if (!result || !result.valid) {
          input.setCustomValidity((result && result.error) || "invalid path");
          input.reportValidity();
          setFieldError(wrap, input.validationMessage || (result && result.error) || "invalid path");
          return;
        }
        input.setCustomValidity("");
        appendListRow(list, result.path || value);
      } else {
        appendListRow(list, value);
      }
      input.value = "";
      const display = controls.querySelector(".sp-model-display");
      if (display) display.textContent = "(add model)";
    });
    controls.appendChild(add);

    wrap.appendChild(title);
    wrap.appendChild(list);
    wrap.appendChild(controls);
    errorElFor(wrap);
    row.appendChild(wrap);
  }

  function renderEnvControl(row, opt, ctx) {
    const wrap = document.createElement("div");
    wrap.className = "spawn-advanced-list-control";
    setControlData(wrap, opt);
    const title = document.createElement("div");
    title.className = ctx.mode === "spawn" ? "spawn-advanced-field-label" : "settings-launch-field-label";
    title.textContent = opt.label || opt.field;
    const list = document.createElement("ul");
    list.className = ctx.mode === "spawn" ? "spawn-advanced-list" : "settings-items-list";
    list.dataset.launchEnvList = "true";
    const controls = addControlsWrap(ctx.mode);
    const name = document.createElement("input");
    name.type = "text";
    name.placeholder = "NAME";
    const value = document.createElement("input");
    value.type = "text";
    value.placeholder = "value";
    value.style.flex = "1";
    value.style.minWidth = "120px";
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
    errorElFor(wrap);
    row.appendChild(wrap);
  }

  function renderMCPControl(row, opt, ctx) {
    const wrap = document.createElement("div");
    wrap.className = "spawn-advanced-list-control";
    setControlData(wrap, opt);
    const title = document.createElement("div");
    title.className = ctx.mode === "spawn" ? "spawn-advanced-field-label" : "settings-launch-field-label";
    title.textContent = opt.label || opt.field;
    const list = document.createElement("ul");
    list.className = ctx.mode === "spawn" ? "spawn-advanced-list" : "settings-items-list";
    list.dataset.launchMcpList = "true";
    const controls = addControlsWrap(ctx.mode);
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
    args.style.flex = "1";
    args.style.minWidth = "120px";
    const add = document.createElement("button");
    add.type = "button";
    add.textContent = "add";
    add.addEventListener("click", async () => {
      const spec = { name: name.value.trim(), command: command.value.trim(), args: args.value.trim() ? args.value.trim().split(/\s+/) : [] };
      if (!spec.name || !spec.command) return;
      if (!(await validateMCPCommandInput(command))) return;
      spec.command = command.value.trim();
      appendMCPRow(list, spec);
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
    errorElFor(wrap);
    row.appendChild(wrap);
  }

  function appendMCPRow(list, spec) {
    const li = document.createElement("li");
    li.dataset.spec = JSON.stringify(spec);
    const span = document.createElement("span");
    span.textContent = spec.name + ": " + spec.command + (spec.args && spec.args.length ? " " + spec.args.join(" ") : "");
    const rm = document.createElement("button");
    rm.type = "button";
    rm.textContent = "remove";
    rm.addEventListener("click", () => li.remove());
    li.appendChild(span);
    li.appendChild(rm);
    list.appendChild(li);
  }

  function renderSchemaOption(group, opt, ctx) {
    const row = document.createElement("div");
    row.className = ctx.mode === "spawn" ? "spawn-advanced-row" : "settings-launch-row";
    row.dataset.launchOption = opt.field || "";
    if (opt.kind === "pathList" || opt.kind === "modelList") renderListControl(row, opt, ctx);
    else if (opt.kind === "envMap") renderEnvControl(row, opt, ctx);
    else if (opt.kind === "mcpServerList") renderMCPControl(row, opt, ctx);
    else renderScalarControl(row, opt, ctx);
    group.appendChild(row);
  }

  function render(root, options) {
    options = options || {};
    const mode = options.mode || (root && root.dataset.launchAdvancedRoot !== undefined ? "spawn" : "settings");
    const layer = options.layer || (root && root.dataset.launchSettingsLayer) || "";
    const groupsRoot = options.groupsRoot || root.querySelector("[data-launch-advanced-groups], [data-launch-settings-groups]") || root;
    const opts = (options.options || []).filter((opt) => {
      if (mode === "spawn") return optionSupportsSpawn(opt);
      return optionSupportsLayer(opt, layer);
    });
    groupsRoot.innerHTML = "";
    const ctx = {
      mode,
      layer,
      rootId: root.id || Math.random().toString(36).slice(2),
      envFallbacks: options.includeEnvFallbacks ? (options.envFallbacks || {}) : null,
    };
    let currentGroup = "";
    let fieldset = null;
    opts.forEach((opt) => {
      if (opt.group !== currentGroup) {
        currentGroup = opt.group || "";
        fieldset = document.createElement("fieldset");
        fieldset.className = mode === "spawn" ? "spawn-advanced-group" : "settings-launch-group";
        fieldset.dataset.launchGroup = currentGroup;
        const legend = document.createElement("legend");
        legend.textContent = currentGroup;
        fieldset.appendChild(legend);
        groupsRoot.appendChild(fieldset);
      }
      renderSchemaOption(fieldset, opt, ctx);
    });
    populate(root, options.current || {});
    if (global.SettingsPickers && global.SettingsPickers.init) global.SettingsPickers.init(root);
  }

  function populate(root, current) {
    current = current || {};
    root.querySelectorAll("[data-launch-wire-field]").forEach((el) => {
      const wire = el.dataset.launchWireField;
      const kind = el.dataset.launchKind;
      if (!wire || kind === "pathList" || kind === "modelList" || kind === "mcpServerList" || kind === "envMap") return;
      const value = current[wire];
      if (kind === "boolean") {
        el.value = value === true ? "true" : value === false ? "false" : "";
      } else if (el.type === "radio") {
        el.checked = (value || "") === (el.value || "");
      } else {
        el.value = value == null ? "" : String(value);
      }
      if (kind === "modelPicker") {
        const display = el.closest(".sp-model-wrap") && el.closest(".sp-model-wrap").querySelector(".sp-model-display");
        if (display) display.textContent = value || display.textContent;
      }
    });
    root.querySelectorAll(".spawn-advanced-list-control[data-launch-wire-field]").forEach((wrap) => {
      const wire = wrap.dataset.launchWireField;
      const kind = wrap.dataset.launchKind;
      if (!wire) return;
      if (kind === "envMap") {
        const list = wrap.querySelector("[data-launch-env-list]");
        Object.entries(current[wire] || {}).forEach(([name, value]) => {
          const li = document.createElement("li");
          li.dataset.name = name;
          li.dataset.value = value;
          const span = document.createElement("span");
          span.textContent = name + "=" + value;
          const rm = document.createElement("button");
          rm.type = "button";
          rm.textContent = "remove";
          rm.addEventListener("click", () => li.remove());
          li.appendChild(span);
          li.appendChild(rm);
          list.appendChild(li);
        });
      } else if (kind === "mcpServerList") {
        const list = wrap.querySelector("[data-launch-mcp-list]");
        (current[wire] || []).forEach((spec) => appendMCPRow(list, spec));
      } else {
        const list = wrap.querySelector("[data-launch-list]");
        (current[wire] || []).forEach((value) => appendListRow(list, value));
      }
    });
  }

  async function validatePathInput(input) {
    if (!input || !input.dataset.launchPathKind || typeof input.setCustomValidity !== "function") return true;
    const value = (input.value || "").trim();
    input.dataset.launchInvalid = "";
    input.setCustomValidity("");
    setFieldError(fieldWrapFor(input), "");
    if (!value) return true;
    if (!global.launchconfig || !global.launchconfig.validatePath) return true;
    const result = await global.launchconfig.validatePath(value, schemaPathKind(input.dataset.launchPathKind));
    if (!result || !result.valid) {
      input.dataset.launchInvalid = "true";
      input.setCustomValidity((result && result.error) || "invalid path");
      input.reportValidity();
      setFieldError(fieldWrapFor(input), input.validationMessage || (result && result.error) || "invalid path");
      return false;
    }
    input.value = result.path || value;
    input.setCustomValidity("");
    return true;
  }

  async function validateMCPCommandInput(input) {
    if (!input) return true;
    const value = (input.value || "").trim();
    input.dataset.launchInvalid = "";
    input.setCustomValidity("");
    setFieldError(fieldWrapFor(input), "");
    if (!value) return true;
    if (!global.launchconfig || !global.launchconfig.validatePath) return true;
    const result = await global.launchconfig.validatePath(value, "command");
    if (!result || !result.valid) {
      input.dataset.launchInvalid = "true";
      input.setCustomValidity((result && result.error) || "invalid command");
      input.reportValidity();
      setFieldError(fieldWrapFor(input), input.validationMessage || (result && result.error) || "invalid command");
      return false;
    }
    input.value = result.path || value;
    input.setCustomValidity("");
    return true;
  }

  async function validate(root) {
    clearValidation(root);
    const inputs = Array.from(root.querySelectorAll("input[data-launch-path-kind][data-launch-wire-field], textarea[data-launch-path-kind][data-launch-wire-field], select[data-launch-path-kind][data-launch-wire-field]"));
    for (const input of inputs) {
      if (!(await validatePathInput(input))) return false;
    }
    const pathLists = Array.from(root.querySelectorAll(".spawn-advanced-list-control[data-launch-kind=\"pathList\"][data-launch-path-kind]"));
    for (const wrap of pathLists) {
      const kind = schemaPathKind(wrap.dataset.launchPathKind);
      const rows = Array.from(wrap.querySelectorAll("[data-launch-list] li"));
      for (const li of rows) {
        const value = (li.dataset.value || "").trim();
        if (!value) continue;
        if (!global.launchconfig || !global.launchconfig.validatePath) continue;
        const result = await global.launchconfig.validatePath(value, kind);
        if (!result || !result.valid) {
          setFieldError(wrap, (result && result.error) || "invalid path: " + value);
          if (wrap.scrollIntoView) wrap.scrollIntoView({ block: "nearest" });
          return false;
        }
        li.dataset.value = result.path || value;
        const span = li.querySelector("span");
        if (span) span.textContent = li.dataset.value;
      }
      setFieldError(wrap, "");
    }
    const commands = Array.from(root.querySelectorAll("[data-launch-mcp-command]"));
    for (const input of commands) {
      if (!(await validateMCPCommandInput(input))) return false;
    }
    return true;
  }

  function showBackendError(root, err) {
    const message = err && err.message ? err.message : String(err || "");
    const isEnvCredential = /\benv key\b/i.test(message) && /credential/i.test(message);
    if (!isEnvCredential) return false;
    const envWrap = root.querySelector("[data-launch-kind=\"envMap\"][data-launch-wire-field=\"env\"]");
    if (!envWrap) return false;
    setFieldError(envWrap, message);
    if (envWrap.scrollIntoView) envWrap.scrollIntoView({ block: "nearest" });
    return true;
  }

  function collect(root) {
    const out = {};
    root.querySelectorAll("[data-launch-wire-field]").forEach((el) => {
      const wire = el.dataset.launchWireField;
      const kind = el.dataset.launchKind;
      if (!wire || kind === "pathList" || kind === "modelList" || kind === "mcpServerList" || kind === "envMap") return;
      if (el.type === "radio" && !el.checked) return;
      if (kind === "boolean") {
        if (el.value === "true") out[wire] = true;
        else if (el.value === "false") out[wire] = false;
        return;
      }
      if (el.dataset.launchInvalid === "true") return;
      const value = (el.value || "").trim();
      if (!value) return;
      out[wire] = kind === "integer" ? Number(value) : value;
    });
    root.querySelectorAll(".spawn-advanced-list-control[data-launch-wire-field]").forEach((wrap) => {
      const wire = wrap.dataset.launchWireField;
      const kind = wrap.dataset.launchKind;
      if (!wire) return;
      if (kind === "envMap") {
        const env = {};
        wrap.querySelectorAll("[data-launch-env-list] li").forEach((li) => {
          if (li.dataset.name) env[li.dataset.name] = li.dataset.value || "";
        });
        if (Object.keys(env).length) out[wire] = env;
        return;
      }
      if (kind === "mcpServerList") {
        const specs = [];
        wrap.querySelectorAll("[data-launch-mcp-list] li").forEach((li) => {
          try { specs.push(JSON.parse(li.dataset.spec || "{}")); } catch (e) { /* skip malformed local row */ }
        });
        if (specs.length) out[wire] = specs;
        return;
      }
      const values = Array.from(wrap.querySelectorAll("[data-launch-list] li"))
        .map((li) => li.dataset.value || "")
        .filter(Boolean);
      if (values.length) out[wire] = values;
    });
    return out;
  }

  global.LaunchConfigControls = {
    render,
    populate,
    collect,
    validate,
    showBackendError,
    schemaPathKind,
    optionSupportsLayer,
    optionSupportsSpawn,
  };
})(window);
