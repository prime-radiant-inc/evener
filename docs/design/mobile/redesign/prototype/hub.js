// Hub: settings and fleet. Hosts, providers (with sign-in), plugins and
// marketplaces, recipes, display, in-app alerts, hubs, about.
(function () {
  const EV = window.EV;
  const { html, h, useState, useEffect } = EV;
  const I = EV.I;
  const Done = () => html`<button class="text-btn strong" onClick=${EV.closeSheet}>Done</button>`;
  const Back = (label) => html`<button class="text-btn" onClick=${EV.closeSheet}>${I.chevL({ s: 18 })}${label || "Back"}</button>`;

  EV.sheets.hub = function ({ stacked }) {
    const S = EV.S;
    const needSign = S.providers.filter((p) => p.status === "expired").length;
    const hubHost = EV.host("magic-kingdom");
    const mismatch = S.hosts.filter((x) => x.version !== hubHost.version).length;
    const offline = S.hosts.filter((x) => x.state === "offline").length;
    const updates = S.plugins.filter((p) => p.update).length;
    return html`<${EV.Sheet} title="magic-kingdom" right=${h(Done)} size=${stacked ? "stacked" : "large"}>
      <div style="padding:2px 20px 6px;display:flex;gap:8px;align-items:center;color:var(--ink-mid);font-size:14px">
        ${S.conn === "live" ? "Connected" : S.conn === "reconnecting" ? "Reconnecting…" : "Offline"} · evener ${hubHost.version} · up to date
      </div>
      <div class="glabel">Fleet</div>
      <div class="group">${h(EV.Gi, { icon: I.host({ s: 17 }), label: "Hosts", value: html`${S.hosts.length}${offline ? html` <span class="tag red">${offline} offline</span>` : mismatch ? html` <span class="tag gray">${mismatch} on another version</span>` : ""}`, chev: true, onClick: () => EV.openSheet("hosts", {}) })}</div>
      <div class="glabel">Setup</div>
      <div class="group">
        ${h(EV.Gi, { icon: I.key({ s: 17 }), label: "Providers", value: html`${S.providers.length}${needSign ? html` <span class="tag amber">${needSign} to sign in</span>` : ""}`, chev: true, onClick: () => EV.openSheet("providers", {}) })}
        ${h(EV.Gi, { icon: I.puzzle({ s: 17 }), label: "Plugins", value: html`${S.plugins.length}${updates ? html` <span class="tag blue">${updates} update</span>` : ""}`, chev: true, onClick: () => EV.openSheet("plugins", {}) })}
        ${h(EV.Gi, { icon: I.compose({ s: 17 }), label: "Recipes", value: S.recipes.length, chev: true, onClick: () => EV.openSheet("recipes", {}) })}
      </div>
      <div class="glabel">This phone</div>
      <div class="group">
        ${h(EV.Gi, { icon: I.outline({ s: 17 }), label: "Display", value: S.prefs.theme, chev: true, onClick: () => EV.openSheet("display", {}) })}
        ${h(EV.Gi, { icon: I.bubble({ s: 17 }), label: "In-app alerts", chev: true, onClick: () => EV.openSheet("alerts", {}) })}
        ${h(EV.Gi, { icon: I.hub({ s: 17 }), label: "Hubs", value: "1", chev: true, onClick: () => EV.openSheet("hubs", {}) })}
      </div>
      <div class="glabel">About</div>
      <div class="group">
        ${h(EV.Gi, { label: "Evener for iPhone", value: "2.0 prototype" })}
      </div>
      <div class="gfoot" style="padding-top:12px">More settings are in the web app: keyboard shortcuts, launch configuration, AGENTS.md, MCP servers and storage.</div>
    </${EV.Sheet}>`;
  };

  EV.sheets.hosts = function () {
    const S = EV.S;
    const hubV = EV.host("magic-kingdom").version;
    return html`<${EV.Sheet} title="Hosts" left=${Back("Hub")} size="stacked">
      <div class="group">${S.hosts.map((x) => h(EV.Gi, { key: x.id, icon: I.host({ s: 17 }),
        label: x.id, sub: (x.state === "offline" ? "Offline" : x.state === "connecting" ? "Connecting…" : "Connected") + " · " + x.os + " · " + S.sessions.filter((s) => s.live && !s.archived && s.host === x.id).length + " live",
        value: x.version !== hubV ? html`<span class="tag amber">${x.version}</span>` : x.state === "offline" ? html`<span class="tag red">Offline</span>` : null, chev: true,
        onClick: () => { EV.log("host_open", { host: x.id }); EV.openSheet("host", { hostId: x.id }); } }))}</div>
      <div class="gfoot">Hosts come from hub.toml or were added in the app. Add hosts from the web app; they need an SSH address and a key.</div>
    </${EV.Sheet}>`;
  };

  EV.sheets.host = function ({ hostId }) {
    const S = EV.S;
    const x = EV.host(hostId);
    const hubV = EV.host("magic-kingdom").version;
    return html`<${EV.Sheet} title=${x.id} left=${Back("Hosts")} size="stacked">
      <div class="group">
        ${h(EV.Gi, { label: "Status", value: x.state === "offline" ? html`<span class="tag amber">Offline</span>` : x.state === "connecting" ? "Connecting…" : "Connected" })}
        ${h(EV.Gi, { label: "Version", value: x.version !== hubV ? html`${x.version} <span class="tag gray">Hub runs ${hubV}</span>` : x.version })}
        ${h(EV.Gi, { label: "System", value: x.os })}
        ${h(EV.Gi, { label: "Resources", value: x.cpus + " CPUs · " + x.memGB + " GB" })}
        ${h(EV.Gi, { label: "Sessions", value: S.sessions.filter((s) => s.live && !s.archived && s.host === x.id).length + (x.state === "offline" ? " live, out of reach" : " live") })}
        ${h(EV.Gi, { label: "Project roots", value: html`<span class="mono">${x.roots.join(", ")}</span>` })}
        ${h(EV.Gi, { label: "Defined in", value: x.origin })}
      </div>
      ${x.lastError ? html`<div class="glabel">Last error</div><div class="group"><div class="gi static"><span class="gl" style="grid-column:1/4;font-family:var(--mono);font-size:13px;color:var(--danger-ink)">${x.lastError}</span></div></div>` : null}
      <div class="group" style="margin-top:16px">
        ${x.state === "connecting"
          ? h(EV.Gi, { label: "Connecting…", sub: "Trying ssh " + x.id, value: h(EV.Pulse, { values: [0.3, 0.6, 0.9, 0.6, 0.3, 0.6, 0.9] }) })
          : h(EV.Gi, { label: x.state === "offline" ? "Reconnect" : "Reconnect now", cls: "accent", onClick: () => { EV.log("host_reconnect", { host: x.id }); EV.reconnectHost(x.id); } })}
      </div>
      <div class="gfoot">${x.state === "offline" ? "This host is offline, so its sessions can't be reached. Reconnect to reach them. " : x.version !== hubV ? "This host runs a different version of Evener than the hub. Sessions keep working. Update Evener on the host when it's convenient. " : ""}${x.origin === "hub.toml" ? "This host is defined in hub.toml, so it can only be edited there." : "Hosts added in the app can be edited or removed here."}</div>
    </${EV.Sheet}>`;
  };

  EV.sheets.providers = function () {
    const S = EV.S;
    const tag = (p) => p.status === "expired" ? html`<span class="tag amber">Sign-in expired</span>` : p.status === "expiring" ? html`<span class="tag gray">${p.statusText}</span>` : p.status === "error" ? html`<span class="tag red">${p.statusText}</span>` : html`<span style="color:var(--ink-low);font-size:14px">${p.statusText}</span>`;
    return html`<${EV.Sheet} title="Providers" left=${Back("Hub")} size="stacked">
      <div class="group">${S.providers.map((p) => h(EV.Gi, { key: p.id, label: p.id, sub: p.base + (p.isDefault ? " · default" : ""), value: tag(p), chev: true, onClick: () => { EV.log("provider_open", { provider: p.id }); EV.openSheet("provider", { providerId: p.id }); } }))}</div>
      <div class="gfoot">Add providers from the web app.</div>
    </${EV.Sheet}>`;
  };

  EV.sheets.provider = function ({ providerId }) {
    const S = EV.S;
    const p = S.providers.find((x) => x.id === providerId);
    const n = S.sessions.filter((s) => s.live && !s.archived && EV.model(s.model).provider === p.id).length;
    return html`<${EV.Sheet} title=${p.id} left=${Back("Providers")} size="stacked">
      <div class="group">
        ${h(EV.Gi, { label: "Status", value: p.statusText })}
        ${h(EV.Gi, { label: "Type", value: p.base })}
        ${h(EV.Gi, { label: "Sign-in", value: p.auth === "oauth" ? "Account" : p.auth === "key" ? "API key" : "None" })}
        ${h(EV.Gi, { label: "Live sessions using it", value: n })}
      </div>
      <div class="glabel">Models</div>
      <div class="group">${p.models.map((m) => h(EV.Gi, { key: m, label: html`<span style="font-family:var(--mono);font-size:15px">${m}</span>` }))}</div>
      <div class="group" style="margin-top:16px">
        ${p.auth === "oauth" ? h(EV.Gi, { label: p.status === "ok" ? "Sign in again" : "Sign in", cls: "accent", onClick: () => EV.openSheet("signin", { provider: p.id }) }) : null}
        ${p.auth === "key" ? h(EV.Gi, { label: "Replace key", cls: "accent", onClick: () => EV.openSheet("replaceKey", { provider: p.id }) }) : null}
        ${h(EV.Gi, { label: "Test connection", cls: "accent", onClick: () => { EV.log("provider_test", { provider: p.id }); EV.toast(p.status === "expired" ? "Failed: sign-in expired" : p.status === "error" ? "Failed: key rejected" : "Works"); } })}
      </div>
    </${EV.Sheet}>`;
  };

  EV.sheets.signin = function ({ provider, sessionId, stacked }) {
    const S = EV.S;
    const p = S.providers.find((x) => x.id === provider);
    const [phase, setPhase] = useState(p.status === "ok" ? "done" : "start");
    const s = sessionId && EV.sess(sessionId);
    // The hub's device flow hands the app a page URL and a code, nothing
    // more, so the app copies the code on the way out instead of pretending
    // the page arrives filled in.
    const open = () => {
      EV.log("copy_code", { provider, auto: true });
      EV.log("provider_signin_start", { provider });
      setPhase("waiting");
      setTimeout(() => {
        p.status = "ok"; p.statusText = "Signed in";
        EV.log("provider_signin", { provider });
        setPhase("done");
        EV.update();
      }, 3200);
    };
    return html`<${EV.Sheet} title=${"Sign in to " + provider} left=${html`<button class="text-btn" onClick=${EV.closeSheet}>${phase === "done" ? "Close" : "Cancel"}</button>`} size=${stacked ? "stacked" : "medium"}>
      ${phase === "start" ? html`<div style="padding:4px 20px">
          <p style="margin:0 0 12px;font-size:15px;line-height:21px;color:var(--ink-mid)">The sign-in page opens inside the app, and this code is copied for you. Paste it when the page asks for it. The hub finishes signing in on its own.</p>
          <div style="display:flex;align-items:center;justify-content:space-between;background:var(--surface);border-radius:12px;padding:14px 16px">
            <span style="font:600 26px var(--mono);letter-spacing:.08em">WDJB-MJHT</span>
            <button class="btn" onClick=${() => { EV.log("copy_code", { provider }); EV.toast("Code copied"); }}>Copy code</button>
          </div>
          <div style="margin-top:16px"><button class="btn primary big" onClick=${open}>Open sign-in page</button></div>
          <p style="font-size:13px;color:var(--ink-low);margin:10px 2px">The code expires in 15 minutes.</p>
        </div>`
      : phase === "waiting" ? html`<div class="empty" style="padding-top:28px">${h(EV.Pulse, { values: [0.3, 0.6, 0.9, 0.6, 0.3, 0.6, 0.9] })}<b style="margin-top:12px">Waiting for you to finish signing in…</b>Your code is WDJB-MJHT. Close the page when it says you're done.</div>`
      : html`<div class="empty" style="padding-top:24px"><span style="color:var(--alive);display:inline-flex">${I.check({ s: 34 })}</span><b style="margin-top:8px">Signed in to ${provider}</b>Sessions using it can continue.
          ${s && s.state === "failed" ? html`<div style="margin-top:16px"><button class="btn primary" onClick=${() => { EV.closeAllSheets(); EV.retry(s); }}>${I.retry({ s: 16 })} Retry “${s.title}”</button></div>` : null}</div>`}
    </${EV.Sheet}>`;
  };

  EV.sheets.replaceKey = function ({ provider }) {
    const [v, setV] = useState("");
    return html`<${EV.Sheet} title="Replace key" left=${html`<button class="text-btn" onClick=${EV.closeSheet}>Cancel</button>`} right=${html`<button class="text-btn strong" disabled=${!v.trim()} onClick=${() => { EV.log("key_replace", { provider }); EV.closeSheet(); EV.toast("Key saved"); }}>Save</button>`} size="medium">
      <div class="field" style="margin-top:8px"><input type="password" placeholder="Paste the API key" value=${v} onInput=${(e) => setV(e.currentTarget.value)} aria-label="API key" /></div>
      <div class="gfoot">The key is stored on the hub, not on this phone.</div>
    </${EV.Sheet}>`;
  };

  EV.sheets.plugins = function () {
    const S = EV.S;
    const [tab, setTab] = useState("Installed");
    return html`<${EV.Sheet} title="Plugins" left=${Back("Hub")} size="stacked">
      ${h(EV.Seg, { options: ["Installed", "Marketplaces", "Browse"], value: tab, onChange: setTab })}
      ${tab === "Installed" ? html`<div class="gfoot" style="padding-top:12px">“On by default” sets which plugins new sessions start with. You can still choose per session.</div>
        ${S.marketplaces.map((mp) => html`<div class="glabel">${mp.id}</div><div class="group">${S.plugins.filter((p) => p.mp === mp.id).map((p) => html`<div class="gi static" key=${p.id}>
          <span></span><span class="gl">${p.id}${p.update ? html` <span class="tag blue">Update ${p.update}</span>` : null}<small>${p.version} · ${p.counts}</small></span>
          <span class="gv">${p.update ? html`<button class="mini-btn" onClick=${() => { p.version = p.update; p.update = null; EV.log("plugin_upgrade", { plugin: p.id }); EV.toast("Updated " + p.id); EV.update(); }}>Update</button>` : null}${h(EV.Switch, { on: p.on, label: p.id + " on by default", onChange: (v) => { p.on = v; S.defaultPlugins = S.plugins.filter((x) => x.on).map((x) => x.id); EV.log("plugin_default", { plugin: p.id, on: v }); EV.update(); } })}</span>
        </div>`)}</div>`)}`
      : tab === "Marketplaces" ? html`<div class="group" style="margin-top:14px">${S.marketplaces.map((mp) => h(EV.Gi, { key: mp.id, label: mp.id, sub: mp.source }))}</div>
        <div class="group" style="margin-top:16px">${h(EV.Gi, { label: "Add marketplace…", cls: "accent", onClick: () => EV.toast("Add by GitHub repo or URL") })}</div>`
      : html`<div class="group" style="margin-top:14px">${[["code-review", "claude-plugins-official", "Review changes for correctness"], ["pdf", "claude-plugins-official", "Read and write PDFs"], ["go-bench", "go-skills", "Benchmark and compare Go code"]].map(([id, mp, d]) => h(EV.Gi, { key: id, label: id, sub: mp + " · " + d, right: html`<button class="mini-btn" onClick=${() => { EV.log("plugin_install", { plugin: id }); EV.toast("Installing " + id + "…"); }}>Install</button>` }))}</div>`}
    </${EV.Sheet}>`;
  };

  EV.sheets.recipes = function () {
    const S = EV.S;
    return html`<${EV.Sheet} title="Recipes" left=${Back("Hub")} size="stacked">
      <div class="gfoot" style="padding-top:4px">Recipes set host, project, model, effort, plugins and access for a new session in one tap.</div>
      <div class="group" style="margin-top:10px">${S.recipes.map((r) => h(EV.Gi, { key: r.id, label: r.name, sub: r.host + " · " + r.project + " · " + r.model + " · " + r.effort + " · " + r.plugins.length + " plugins",
        right: html`<button class="mini-btn danger" onClick=${() => { S.recipes = S.recipes.filter((x) => x !== r); EV.log("recipe_delete", { name: r.name }); EV.update(); }}>Delete</button>` }))}</div>
      <div class="gfoot">Save a recipe from the New session screen.</div>
    </${EV.Sheet}>`;
  };

  EV.sheets.display = function () {
    const S = EV.S;
    const p = S.prefs;
    return html`<${EV.Sheet} title="Display" left=${Back("Hub")} size="stacked">
      <div class="glabel">Appearance</div>${h(EV.Seg, { options: ["System", "Light", "Dark"], value: p.theme, onChange: (v) => { p.theme = v; p.themeTouched = true; EV.applyTheme(); EV.log("pref", { theme: v }); EV.update(); } })}
      <div class="glabel">Reading font</div>${h(EV.Seg, { options: ["Serif", "Sans"], value: p.readFont, onChange: (v) => { p.readFont = v; EV.applyTheme(); EV.log("pref", { readFont: v }); EV.update(); } })}
      <div class="gfoot">For what agents write: messages, plans and documents.</div>
      <div class="glabel">Default detail level</div>${h(EV.Seg, { options: ["Chat", "Intent", "Tools", "Activity", "Full"], value: p.defaultDetail, onChange: (v) => { p.defaultDetail = v; EV.log("pref", { defaultDetail: v }); EV.update(); } })}
      <div class="gfoot">Each session can override this from its menu.</div>
      <div class="group" style="margin-top:18px"><div class="gi static"><span></span><span class="gl">Show model on Board rows</span><span class="gv">${h(EV.Switch, { on: p.showModel, label: "Show model on Board rows", onChange: (v) => { p.showModel = v; EV.log("pref", { showModel: v }); EV.update(); } })}</span></div></div>
    </${EV.Sheet}>`;
  };

  EV.sheets.alerts = function () {
    const a = EV.S.prefs.alerts;
    const row = (k, label, sub) => html`<div class="gi static"><span></span><span class="gl">${label}${sub ? html`<small>${sub}</small>` : null}</span><span class="gv">${h(EV.Switch, { on: a[k], label, onChange: (v) => { a[k] = v; EV.log("pref", { alert: k, on: v }); EV.update(); } })}</span></div>`;
    return html`<${EV.Sheet} title="In-app alerts" left=${Back("Hub")} size="stacked">
      <div class="glabel">Show a banner when</div>
      <div class="group">${row("failures", "A session fails")}${row("questions", "A session asks a question or needs approval")}${row("finished", "A session finishes", "Finished results always land in Finished on the Board")}</div>
      <div class="group" style="margin-top:16px">${row("hold", "Hold alerts while reading or typing", "They show when you leave the document or send")}${row("haptics", "Haptics")}</div>
      <div class="gfoot">Lock-screen notifications are coming later. Until then, alerts show while Evener is open.</div>
    </${EV.Sheet}>`;
  };

  EV.sheets.hubs = function () {
    return html`<${EV.Sheet} title="Hubs" left=${Back("Hub")} size="stacked">
      <div class="group"><button class="gi"><span style="width:22px;display:flex;color:var(--accent)">${I.check({ s: 18 })}</span><span class="gl">magic-kingdom<small>100.113.28.18:9180 · connected</small></span><span></span></button></div>
      <div class="glabel">Add a hub</div>
      <div class="group">${h(EV.Gi, { icon: I.camera({ s: 17 }), label: "Scan pairing code", onClick: () => EV.toast("Opens the camera in the app") })}${h(EV.Gi, { icon: I.doc({ s: 17 }), label: "Paste pairing link", onClick: () => EV.toast("Paste the link from Settings → Mobile app") })}</div>
      <div class="gfoot">In Evener on your computer, open Settings, then Mobile app, to show a pairing code.</div>
    </${EV.Sheet}>`;
  };
})();
