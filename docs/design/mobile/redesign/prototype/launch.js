// New session: prompt first, recipes, and the host / project / model /
// effort / plugins / access / branch pickers.
(function () {
  const EV = window.EV;
  const { html, h, useRef, useEffect, useState } = EV;
  const I = EV.I;

  // Which projects exist on which host (the hub knows this from the host's roots).
  EV.projectsOn = function (hostId) {
    const S = EV.S;
    const ids = new Set(["evener", "home"]);
    S.sessions.forEach((s) => { if (s.host === hostId) ids.add(s.project); });
    return S.projects.filter((p) => ids.has(p.id));
  };

  EV.openNew = function (from, opts) {
    const S = EV.S;
    const base = S.lastUsed || S.recipes[0];
    const like = opts && opts.like && EV.sess(opts.like);
    S.launch = {
      prompt: "", images: 0, recipe: like ? null : "last",
      host: like ? like.host : base.host,
      project: (opts && opts.project) || (like ? like.project : base.project),
      model: like ? like.model : base.model,
      effort: like ? like.effort : base.effort,
      plugins: (like ? like.plugins : base.plugins).slice(),
      access: like ? like.access : base.access,
      branch: base.branch || "Current branch",
      error: null, note: null,
    };
    EV.log("new_session_open", { from });
    EV.openSheet("launch", {});
  };

  function applyRecipe(r) {
    const L = EV.S.launch;
    Object.assign(L, { host: r.host, project: r.project, model: r.model, effort: r.effort, plugins: r.plugins.slice(), access: r.access, branch: r.branch || "Current branch", recipe: r.id, error: null, note: null });
    EV.log("recipe_apply", { recipe: r.name || r.id });
    EV.update();
  }

  function titleFrom(prompt) {
    const w = prompt.replace(/[^\w\s'-]/g, " ").trim().split(/\s+/).filter(Boolean);
    const small = new Set(["a", "an", "the", "and", "or", "of", "to", "in", "on", "for", "why", "is"]);
    return w.slice(0, 6).map((x, i) => (i && small.has(x.toLowerCase()) ? x.toLowerCase() : x.charAt(0).toUpperCase() + x.slice(1))).join(" ") || "New Session";
  }

  EV.sheets.launch = function () {
    const S = EV.S;
    const L = S.launch;
    const ref = useRef(null);
    useEffect(() => { ref.current && ref.current.focus(); }, []);
    const hst = EV.host(L.host);
    const m = EV.model(L.model);
    const onHost = EV.projectsOn(L.host);
    const start = () => {
      const S2 = EV.S;
      if (!L.prompt.trim()) return;
      if (hst.state === "offline") { L.error = L.host + " is offline. Connect it or choose another host."; EV.update(); return; }
      if (!onHost.find((p) => p.id === L.project)) { L.error = L.host + " couldn't find ~/" + (S2.projects.find((p) => p.id === L.project) || { path: L.project }).path + ". Choose another project."; EV.log("start_rejected", { reason: "project_missing", host: L.host, project: L.project }); EV.update(); return; }
      const id = "s-new-" + Date.now();
      const now = Date.now();
      const s = { id, title: titleFrom(L.prompt), state: "working", activity: "Thinking", host: L.host, project: L.project, model: L.model, effort: L.effort, plugins: L.plugins.slice(), access: L.access,
        branch: L.branch === "New worktree branch" ? (L.branchName || "lane-" + id.slice(-4)) : "main", live: true, archived: false, unseen: false, attachments: [], subs: null, tasks: null, goal: null, notes: null,
        usage: { in: 0, out: 0, cache: 0 }, cost: "~$0.00", workSec: 0, ctx: { used: 4, window: m.ctx || 200 }, failedTools: 0, updatedAt: now, startedAt: now, actAt: now, pulse: [0, 0, 0, 0, 0, 0.4, 0.8] };
      S2.sessions.push(s);
      S2.transcripts[id] = [{ t: "time", label: "Just now" }, { t: "sys", text: "Started on " + L.host + " · " + L.model + " · " + L.effort + " · " + L.plugins.length + " plugins" }, { t: "user", text: L.prompt.trim() }, { t: "think", live: true }];
      S2.lastUsed = { host: L.host, project: L.project, model: L.model, effort: L.effort, plugins: L.plugins.slice(), access: L.access, branch: L.branch };
      EV.log("start_session", { sessionId: id, host: L.host, project: L.project, model: L.model, effort: L.effort, plugins: L.plugins.slice().sort(), access: L.access, branch: L.branch, prompt: L.prompt.trim(), images: L.images });
      EV.closeAllSheets();
      EV.openSession(id, { from: "new" });
      setTimeout(() => { const tr = S2.transcripts[id]; tr.pop(); tr.push({ t: "agent", md: "Starting on this now. I'll read the relevant code first and report back." }, { t: "act", live: true, steps: [{ i: "Read the relevant files", g: "cmd/evener-hub/…", s: "run" }] }); s.activity = "Reading the relevant files"; s.updatedAt = Date.now(); EV.update(); }, 3500);
    };
    const recipeChip = (id, name, run) => html`<button class=${"chip" + (L.recipe === id ? " on" : "")} onClick=${run}>${name}</button>`;
    return html`<${EV.Sheet} title="New session" left=${html`<button class="text-btn" onClick=${() => { EV.log("new_session_cancel", {}); EV.closeSheet(); }}>Cancel</button>`}
      right=${html`<button class="text-btn strong" disabled=${!L.prompt.trim()} onClick=${start}>Start</button>`} size="large">
      <div class="field" style="margin-top:2px">
        <textarea ref=${ref} id="launch-prompt" rows="4" placeholder="What should the agent do?" value=${L.prompt} onInput=${(e) => { L.prompt = e.currentTarget.value; EV.update(); }} aria-label="What should the agent do?"></textarea>
        <div style="display:flex;justify-content:space-between;align-items:center;margin-top:6px">
          <button class="cbtn" aria-label="Attach images" onClick=${() => { L.images++; EV.log("launch_attach", {}); EV.update(); }}>${I.plus({ s: 18 })}${L.images ? " " + L.images + " image" + (L.images > 1 ? "s" : "") : ""}</button>
          <span style="font-size:12px;color:var(--ink-low)">Dictation works from the keyboard mic</span>
        </div>
      </div>
      <div class="recipes" role="radiogroup" aria-label="Recipes">
        ${L.recipe ? null : html`<span class="chip on" aria-label="Custom settings">Custom</span>`}
        ${recipeChip("last", "Same as last time", () => applyRecipe(Object.assign({ id: "last", name: "Same as last time" }, S.lastUsed || S.recipes[0])))}
        ${S.recipes.map((r) => recipeChip(r.id, r.name, () => applyRecipe(r)))}
        <button class="chip" onClick=${() => EV.openSheet("saveRecipe", {})} aria-label="Save as recipe">${I.plus({ s: 14 })} Save</button>
      </div>
      <div class="recipe-sum">${L.recipe ? "Sets " : "Custom: "}${[L.host, L.project, m.name + " " + L.effort, L.plugins.length + " plugins", L.access].join(" · ")}</div>
      ${L.error ? html`<div class="notice" style="background:var(--danger-bg);border-color:var(--danger-edge);grid-template-columns:22px 1fr"><span class="ic" style="color:var(--danger)">${I.failed({ s: 16 })}</span><span class="txt">${L.error}</span></div>` : null}
      ${L.note ? html`<div class="gfoot" style="padding-top:10px">${L.note}</div>` : null}
      <div class="glabel">Where</div>
      <div class="group">
        ${h(EV.Gi, { icon: I.host({ s: 17 }), iconBg: "#5B6770", label: "Host", value: html`<span class=${"conn-dot" + (hst.state === "offline" ? " offline" : "")}></span>${L.host}`, chev: true, onClick: () => EV.openSheet("pickHost", {}) })}
        ${h(EV.Gi, { icon: I.folder({ s: 17 }), iconBg: "#8A6D3B", label: "Project", value: L.project, chev: true, onClick: () => EV.openSheet("pickProject", {}) })}
        ${h(EV.Gi, { icon: I.branch({ s: 17 }), iconBg: "#6B5B95", label: "Branch", value: L.branch === "New worktree branch" ? "New: " + (L.branchName || "lane") : "Current (main)", chev: true, onClick: () => EV.openSheet("pickBranch", {}) })}
      </div>
      <div class="glabel">Agent</div>
      <div class="group">
        ${h(EV.Gi, { icon: I.cpu({ s: 17 }), iconBg: "#2F6F8F", label: "Model", sub: m.provider, value: m.name, chev: true, onClick: () => EV.openSheet("model", { target: "launch" }) })}
        <div class="gi static" style="display:block;padding:10px 0 12px"><div style="padding:0 14px 8px"><div style="font-size:17px">Effort</div><div style="color:var(--ink-low);font-size:13px">How long it thinks before acting</div></div>
          ${h(EV.Seg, { options: ["low", "medium", "high", "xhigh", "max"], value: L.effort, onChange: (e) => { L.effort = e; L.recipe = null; EV.log("launch_effort", { effort: e }); EV.update(); }, disabled: ["low", "medium", "high", "xhigh", "max"].filter((e) => !m.efforts.includes(e)) })}</div>
        ${h(EV.Gi, { icon: I.puzzle({ s: 17 }), iconBg: "#3C7A5A", label: "Plugins", value: L.plugins.length + " of " + S.plugins.length, chev: true, onClick: () => EV.openSheet("pickPlugins", {}) })}
        ${h(EV.Gi, { icon: I.shield({ s: 17 }), iconBg: "#7A5C3C", label: "Access", sub: { "Full access": "Read and write anywhere", "Workspace write": "Writes inside the project; asks first elsewhere", "Read-only": "Reads only", "Restricted": "Only allowed tools; no network" }[L.access], value: L.access, chev: true, onClick: () => EV.openSheet("pickAccess", {}) })}
      </div>
      <div class="gfoot">Host, plugins and access are fixed once the session starts. Model and effort can change later.</div>
      <div class="group" style="margin-top:14px">${h(EV.Gi, { label: "More options", sub: "Context strategy, subagent depth, turn limit", chev: true, onClick: () => EV.openSheet("moreOptions", {}) })}</div>
      <div style="padding:20px 16px 0"><button class="btn primary big" disabled=${!L.prompt.trim()} onClick=${start}>Start session</button></div>
    </${EV.Sheet}>`;
  };

  const back = html`<button class="text-btn strong" onClick=${() => EV.closeSheet()}>Done</button>`;

  EV.sheets.pickHost = function ({ stacked }) {
    const S = EV.S;
    const L = S.launch;
    const pick = (x) => {
      if (x.state === "offline") return;
      L.host = x.id; L.recipe = null; L.error = null;
      const avail = EV.projectsOn(x.id);
      if (!avail.find((p) => p.id === L.project)) {
        const old = L.project;
        L.project = avail[0].id;
        L.note = old + " isn't on " + x.id + ", so the project changed to " + L.project + ".";
      } else L.note = null;
      EV.log("launch_host", { host: x.id });
      EV.closeSheet();
    };
    return html`<${EV.Sheet} title="Host" right=${back} size=${stacked ? "stacked" : "medium"}>
      <div class="group">${S.hosts.map((x) => html`<button class=${"gi" + (x.state === "offline" ? " disabled" : "")} key=${x.id} onClick=${() => pick(x)}>
        <span style="width:22px;display:flex;color:var(--accent)">${L.host === x.id ? I.check({ s: 18 }) : null}</span>
        <span class="gl">${x.id}<small>${x.state === "offline" ? "Offline" : x.os + " · " + S.sessions.filter((s) => s.live && s.host === x.id).length + " live sessions"}</small></span>
        <span class="gv">${x.state === "offline" ? html`<span class="mini-btn" role="button" onClick=${(e) => { e.stopPropagation(); EV.reconnectHost(x.id); }}>Connect</span>` : html`<span class="conn-dot"></span>`}</span>
      </button>`)}</div>
    </${EV.Sheet}>`;
  };

  EV.sheets.pickProject = function ({ stacked }) {
    const S = EV.S;
    const L = S.launch;
    const [q, setQ] = useState("");
    const [browse, setBrowse] = useState(false);
    const list = EV.projectsOn(L.host).filter((p) => !q || p.id.includes(q.toLowerCase()));
    const pick = (id) => { L.project = id; L.recipe = null; L.error = null; L.note = null; EV.log("launch_project", { project: id }); EV.closeSheet(); };
    const root = (EV.host(L.host).roots || ["~"])[0];
    return html`<${EV.Sheet} title="Project" right=${back} size=${stacked ? "stacked" : "large"}>
      <div class="search-field">${I.search({ s: 16 })}<input placeholder=${"Projects on " + L.host} value=${q} onInput=${(e) => setQ(e.currentTarget.value)} aria-label="Search projects" /></div>
      <div class="glabel">Recent on ${L.host}</div>
      <div class="group">${list.map((p) => html`<button class="gi" key=${p.id} onClick=${() => pick(p.id)}><span style="width:22px;display:flex;color:var(--accent)">${L.project === p.id ? I.check({ s: 18 }) : null}</span><span class="gl">${p.id}<small style="font-family:var(--mono);font-size:12px">~/${p.path || ""}</small></span><span></span></button>`)}</div>
      <div class="group" style="margin-top:16px">${h(EV.Gi, { label: "Browse folders on " + L.host + "…", cls: "accent", onClick: () => setBrowse(!browse) })}</div>
      ${browse ? html`<div class="glabel" style="text-transform:none;font-family:var(--mono)">${root}</div><div class="group">${["prime-radiant-inc", "c-to-wasm", "superpowers", "house", "scratch"].map((d) => h(EV.Gi, { key: d, icon: I.folder({ s: 16 }), iconBg: "#8A6D3B", label: d, chev: true, onClick: () => { if (!S.projects.find((p) => p.id === d)) S.projects.push({ id: d, path: "git/" + d }); pick(d); } }))}</div>` : null}
    </${EV.Sheet}>`;
  };

  EV.sheets.pickPlugins = function ({ stacked }) {
    const S = EV.S;
    const L = S.launch;
    const mps = S.marketplaces.map((m) => m.id);
    const [q, setQ] = useState("");
    const ok = (p) => !q || (p.id + " " + p.desc + " " + p.mp).toLowerCase().includes(q.toLowerCase());
    const toggle = (id) => { L.plugins = L.plugins.includes(id) ? L.plugins.filter((x) => x !== id) : L.plugins.concat(id); L.recipe = null; EV.log("launch_plugin_toggle", { plugin: id, on: L.plugins.includes(id) }); EV.update(); };
    return html`<${EV.Sheet} title="Plugins for this session" right=${back} left=${html`<span style="display:flex"><button class="text-btn" onClick=${() => { L.plugins = S.plugins.map((p) => p.id); EV.log("launch_plugins_all", {}); EV.update(); }}>All</button><button class="text-btn" onClick=${() => { L.plugins = []; EV.log("launch_plugins_none", {}); EV.update(); }}>None</button></span>`} size=${stacked ? "stacked" : "large"}>
      <div class="search-field">${I.search({ s: 16 })}<input placeholder="Search plugins" value=${q} onInput=${(e) => setQ(e.currentTarget.value)} aria-label="Search plugins" /></div>
      <div class="gfoot" style="padding:2px 32px 8px">${L.plugins.length} of ${S.plugins.length} on${L.plugins.length ? ": " + L.plugins.join(", ") : ""}. Plugins can't be changed after the session starts.</div>
      ${mps.filter((mp) => S.plugins.some((p) => p.mp === mp && ok(p))).map((mp) => html`<div class="glabel">${mp}</div><div class="group">${S.plugins.filter((p) => p.mp === mp && ok(p)).map((p) => {
        const warn = p.id === "superpowers-chrome" && L.host === "magic-kingdom" && L.plugins.includes(p.id);
        return html`<div class="gi static" key=${p.id} onClick=${() => toggle(p.id)} style="cursor:pointer">
          <span></span>
          <span class="gl">${p.id}<small>${p.desc}</small><small style="color:var(--ink-low)">${p.counts}</small>${warn ? html`<small><span class="tag amber">Needs Chrome on ${L.host}</span></small>` : null}</span>
          <span class="gv">${h(EV.Switch, { on: L.plugins.includes(p.id), onChange: () => toggle(p.id), label: p.id })}</span>
        </div>`;
      })}</div>`)}
    </${EV.Sheet}>`;
  };

  EV.sheets.pickAccess = function ({ stacked }) {
    const L = EV.S.launch;
    const opts = [["Full access", "Read and write anywhere; run anything"], ["Workspace write", "Write inside the project; ask before anything outside"], ["Read-only", "Read files and run read-only commands"], ["Restricted", "Only the tools you allow; no network"]];
    return html`<${EV.Sheet} title="Access" right=${back} size=${stacked ? "stacked" : "medium"}>
      <div class="group">${opts.map(([k, d]) => html`<button class="gi" key=${k} onClick=${() => { L.access = k; L.recipe = null; EV.log("launch_access", { access: k }); EV.closeSheet(); }}><span style="width:22px;display:flex;color:var(--accent)">${L.access === k ? I.check({ s: 18 }) : null}</span><span class="gl">${k}<small>${d}</small></span><span></span></button>`)}</div>
    </${EV.Sheet}>`;
  };

  EV.sheets.pickBranch = function ({ stacked }) {
    const L = EV.S.launch;
    const [name, setName] = useState(L.branchName || "");
    return html`<${EV.Sheet} title="Branch" right=${html`<button class="text-btn strong" onClick=${() => { L.branchName = name.trim(); EV.log("launch_branch", { branch: L.branch, name: name.trim() }); EV.closeSheet(); }}>Done</button>`} size=${stacked ? "stacked" : "medium"}>
      <div class="group">
        <button class="gi" onClick=${() => { L.branch = "Current branch"; EV.update(); }}><span style="width:22px;display:flex;color:var(--accent)">${L.branch !== "New worktree branch" ? I.check({ s: 18 }) : null}</span><span class="gl">Current branch<small>Work on main in the project folder</small></span><span></span></button>
        <button class="gi" onClick=${() => { L.branch = "New worktree branch"; EV.update(); }}><span style="width:22px;display:flex;color:var(--accent)">${L.branch === "New worktree branch" ? I.check({ s: 18 }) : null}</span><span class="gl">New worktree branch<small>An isolated copy on its own branch</small></span><span></span></button>
      </div>
      ${L.branch === "New worktree branch" ? html`<div class="field" style="margin-top:14px"><input placeholder="branch-name" value=${name} onInput=${(e) => setName(e.currentTarget.value)} style="font-family:var(--mono)" aria-label="Branch name" /></div>` : null}
    </${EV.Sheet}>`;
  };

  EV.sheets.moreOptions = function ({ stacked }) {
    const L = EV.S.launch;
    L.more = L.more || { strategy: "Hub default", depth: "Hub default", turns: "Hub default" };
    const row = (label, key, opts) => html`<div class="glabel">${label}</div>${h(EV.Seg, { options: opts, value: L.more[key], onChange: (v) => { L.more[key] = v; EV.log("launch_more", { key, value: v }); EV.update(); } })}`;
    return html`<${EV.Sheet} title="More options" right=${back} size=${stacked ? "stacked" : "large"}>
      ${row("Context strategy", "strategy", ["Hub default", "compact", "session-log", "ooda"])}
      ${row("Max subagent depth", "depth", ["Hub default", "1", "2", "3", "5"])}
      ${row("Turn limit", "turns", ["Hub default", "100", "500", "none"])}
      <div class="gfoot" style="padding-top:14px">Everything else uses the hub's launch defaults. Edit those in the web app.</div>
    </${EV.Sheet}>`;
  };

  EV.sheets.saveRecipe = function () {
    const S = EV.S;
    const L = S.launch;
    const [name, setName] = useState("");
    const save = () => {
      const id = "r" + Date.now();
      S.recipes.push({ id, name: name.trim(), host: L.host, project: L.project, model: L.model, effort: L.effort, plugins: L.plugins.slice(), access: L.access, branch: L.branch });
      L.recipe = id;
      EV.log("recipe_save", { name: name.trim() });
      EV.closeSheet();
      EV.toast("Saved as " + name.trim());
    };
    return html`<${EV.Sheet} title="Save as recipe" left=${html`<button class="text-btn" onClick=${EV.closeSheet}>Cancel</button>`} right=${html`<button class="text-btn strong" disabled=${!name.trim()} onClick=${save}>Save</button>`} size="medium">
      <div class="field" style="margin-top:8px"><input placeholder="Recipe name" value=${name} onInput=${(e) => setName(e.currentTarget.value)} aria-label="Recipe name" /></div>
      <div class="gfoot">${L.host} · ${L.project} · ${L.model} · ${L.effort} · ${L.plugins.length} plugins · ${L.access}</div>
    </${EV.Sheet}>`;
  };
})();
