// Core runtime for the Evener for iPhone prototype: state, navigation,
// sheets, menus, banners, toasts, gestures, icons and shared components.
// Every screen reads the single global state object EV.S and calls
// EV.update() after changing it; Preact diffs the whole tree.
(function () {
  const { h, render } = preact;
  const { useState, useEffect, useRef, useLayoutEffect } = preactHooks;
  const html = htm.bind(h);
  const EV = (window.EV = window.EV || {});
  Object.assign(EV, { h, html, render, useState, useEffect, useRef, useLayoutEffect });

  // ---------- logging (the moderator's ground truth) ----------
  const proto = (window.__proto = window.__proto || { log: [] });
  EV.log = (type, detail) => {
    proto.log.push(Object.assign({ t: +((Date.now() - (EV.S ? EV.S.t0 : Date.now())) / 1000).toFixed(1), type }, detail || {}));
  };

  // ---------- state ----------
  EV.freshState = function () {
    const D = structuredClone(window.EV_DATA);
    const now = Date.now();
    const S = {
      t0: now,
      conn: "live",
      connSince: now,
      hosts: D.hosts, providers: D.providers, models: D.models, plugins: D.plugins, marketplaces: D.marketplaces,
      recipes: D.recipes, projects: D.projects, categories: D.categories, defaultPlugins: D.defaultPlugins,
      sessions: D.sessions.map((s) => Object.assign(s, {
        updatedAt: now - s.ago * 1000,
        startedAt: now - (s.started || s.ago + 600) * 1000,
        pulse: s.state === "working" && !s.stuck ? [0.2, 0.5, 0.3, 0.7, 0.4, 0.8, 0.6].map((v, i) => Math.min(1, v + ((s.id.length * (i + 3)) % 5) / 10)) : [0, 0, 0, 0, 0, 0, 0],
      })),
      subagents: D.subagents, transcripts: D.transcripts, asks: D.asks, approvals: D.approvals,
      docs: D.docs, artifacts: D.artifacts, search: D.search, archivedTotal: D.archivedTotal,
      nav: [{ name: "board", key: 1 }],
      navAnim: null,
      navSeq: 1,
      sheets: [],
      sheetSeq: 1,
      menu: null,
      banner: null,
      held: [],
      recent: [],
      toast: null,
      board: { collapsed: { idle: true, testruns: true, archived: true }, open: { "proj:evener": true, "host:evener:magic-kingdom": true }, organize: "project", selecting: false, selected: {}, searching: false, query: "", scope: "all", flash: {} },
      prefs: { detail: {}, defaultDetail: "Intent", readFont: "Serif", showModel: false, theme: "System",
        alerts: { failures: true, questions: true, finished: false, hold: true, haptics: true }, hintUses: 0 },
      drafts: {},
      noteDrafts: {},
      composeOpen: {},
      noteState: {},
      images: {},
      queue: {},
      steering: {},
      answers: {},
      comments: {},
      readDocs: {},
      readerPos: {},
      lastRead: null,
      openDocChanges: {},
      stopRequests: {},
      artifactState: {},
      dockMin: {},
      expanded: {},
      reading: null,
      typing: false,
    };
    return S;
  };

  let rerender = () => {};
  let pending = false;
  EV.update = function (fn) {
    if (fn) fn(EV.S);
    if (!pending) {
      pending = true;
      requestAnimationFrame(() => { pending = false; rerender(); });
    }
  };

  // ---------- helpers ----------
  EV.sess = (id) => EV.S.sessions.find((s) => s.id === id);
  EV.host = (id) => EV.S.hosts.find((x) => x.id === id);
  // Every effort level, lowest first; each model supports some of them.
  EV.EFFORTS = ["low", "medium", "high", "xhigh", "max"];
  EV.model = (id) => EV.S.models.find((m) => m.id === id) || { id, name: id, provider: "", ctx: 0, efforts: EV.EFFORTS };
  // A model is called by its display name wherever people read it, with
  // the effort after it: "GLM 5.3 Vision · XHigh", never the raw id.
  EV.modelLabel = (id, effort) => EV.model(id).name + (effort ? " · " + EV.cap(effort) : "");
  EV.multiHost = () => EV.S.hosts.length > 1;
  const mode = (xs) => { const n = {}; let best = null; xs.forEach((x) => { n[x] = (n[x] || 0) + 1; if (best == null || n[x] > n[best]) best = x; }); return best; };
  EV.usual = function () {
    const live = EV.S.sessions.filter((s) => s.live && !s.archived);
    return { project: mode(live.map((s) => s.project)), host: mode(live.map((s) => s.host)) };
  };
  // How many live sessions run on a host, or use a provider's models.
  const liveCount = (pred) => EV.S.sessions.filter((s) => s.live && !s.archived && pred(s)).length;
  EV.liveOnHost = (hostId) => liveCount((s) => s.host === hostId);
  EV.liveOnProvider = (providerId) => liveCount((s) => EV.model(s.model).provider === providerId);

  EV.fmtAgo = function (ms) {
    const s = Math.max(0, Math.round(ms / 1000));
    if (s < 45) return "now";
    const m = Math.round(s / 60);
    if (m < 60) return m + "m";
    const hh = Math.round(m / 60);
    if (hh < 24) return hh + "h";
    return Math.round(hh / 24) + "d";
  };
  EV.fmtDur = function (ms) {
    const s = Math.max(0, Math.round(ms / 1000));
    if (s < 60) return s + "s";
    const m = Math.floor(s / 60);
    if (m < 60) return m + "m";
    const hh = Math.floor(m / 60);
    return hh + "h " + (m % 60) + "m";
  };
  EV.fmtTok = function (n) {
    if (n >= 1e9) return (n / 1e9).toFixed(n >= 1e10 ? 0 : 1) + "B";
    if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 0 : 1) + "M";
    if (n >= 1e3) return (n / 1e3).toFixed(n >= 1e4 ? 0 : 1) + "K";
    return String(n);
  };

  // Phone-facing state of a top-level session.
  EV.stateOf = function (s) {
    if (s.state === "working") {
      if (s.stuck) return "stuck";
      const quiet = Date.now() - s.updatedAt;
      if (quiet > 10 * 60 * 1000) return "stuck";
      return "working";
    }
    return s.state;
  };
  // In a turn: working, or working but maybe stuck. Stop, Steer and Queue
  // apply only then.
  EV.inTurn = function (s) {
    const st = EV.stateOf(s);
    return st === "working" || st === "stuck";
  };
  // Delayed work belongs to the state it was scheduled in. A reset (every
  // usability task starts with one) swaps in a fresh state, and anything still
  // pending from before is dropped instead of landing in the next task's
  // transcript, toasts or action log. Returns the timer for clearTimeout.
  EV.later = function (ms, fn) {
    const S = EV.S;
    return setTimeout(() => { if (EV.S === S) fn(); }, ms);
  };
  // The scripted agent's follow-up to something you did: fn runs after ms
  // only if the same turn is still going (setWorking starts a new one), so a
  // Stop, a shutdown or a newer turn in between wins over the script.
  EV.laterInTurn = function (s, ms, fn) {
    const turn = s.turn;
    return EV.later(ms, () => {
      if (s.turn !== turn || !EV.inTurn(s)) return;
      fn();
      EV.update();
    });
  };
  const NEEDS = ["failed", "question", "approval", "warning", "restart"];
  EV.isNeeds = (s) => NEEDS.includes(s.state);
  EV.band = function (s) {
    if (!s.live || s.archived) return null;
    if (EV.isNeeds(s)) return "needs";
    if (s.state === "yourmove") return s.unseen ? "yourmove" : "idle";
    if (s.state === "working") return "working";
    if (s.state === "idle") return "idle";
    return null;
  };
  EV.needsCount = (exceptId) => EV.S.sessions.filter((s) => s.live && !s.archived && EV.isNeeds(s) && s.id !== exceptId).length;
  EV.needsOrder = function () {
    const rank = { failed: 0, question: 1, approval: 1, warning: 1, restart: 1 };
    return EV.S.sessions.filter((s) => s.live && !s.archived && EV.isNeeds(s)).sort((a, b) => rank[a.state] - rank[b.state] || a.updatedAt - b.updatedAt);
  };
  EV.liveOrder = function () {
    const needs = EV.needsOrder();
    const your = EV.S.sessions.filter((s) => EV.band(s) === "yourmove").sort((a, b) => b.updatedAt - a.updatedAt);
    const work = EV.S.sessions.filter((s) => EV.band(s) === "working").sort((a, b) => (EV.stateOf(b) === "stuck") - (EV.stateOf(a) === "stuck") || b.startedAt - a.startedAt);
    const idle = EV.S.sessions.filter((s) => EV.band(s) === "idle").sort((a, b) => b.updatedAt - a.updatedAt);
    return needs.concat(your, work, idle);
  };
  EV.tally = function (s) {
    const list = EV.S.subagents[s.id];
    if (!list) return s.subs ? Object.assign({}, s.subs) : null;
    const t = { run: 0, fail: 0, done: 0 };
    const walk = (arr) => arr.forEach((g) => {
      if (g.state === "running") t.run++; else if (g.state === "failed") t.fail++; else t.done++;
      if (g.children) walk(g.children);
    });
    walk(list);
    return t;
  };
  EV.tallyTotal = (t) => (t ? t.run + t.fail + t.done : 0);
  EV.subTime = (g) => (g.state === "running" ? EV.fmtDur(g.elapsed * 1000) : EV.fmtAgo(g.ago * 1000));

  // A working session's line: what it's doing, or how long it's been quiet.
  // (A stuck one says so instead; see EV.whyParts.)
  EV.activityLine = function (s) {
    const quiet = Date.now() - s.updatedAt;
    if (quiet > 3 * 60 * 1000) return { text: "Quiet " + EV.fmtAgo(quiet), cls: "activity" };
    return { text: s.activity || "Working", cls: "activity" };
  };

  // ---------- navigation ----------
  EV.push = function (name, props) {
    const S = EV.S;
    S.navSeq++;
    S.nav.push(Object.assign({ name, key: S.navSeq }, props || {}));
    S.navAnim = { type: "push", key: S.navSeq, until: Date.now() + 330 };
    S.menu = null;
    EV.log("open", Object.assign({ screen: name }, props || {}));
    EV.onScreenChange();
    EV.update();
    setTimeout(() => EV.update(), 340);
  };
  EV.pop = function () {
    const S = EV.S;
    if (S.nav.length < 2) return;
    const popped = S.nav.pop();
    S.navAnim = { type: "pop", popped, until: Date.now() + 290 };
    EV.log("back", { from: popped.name });
    EV.onScreenChange();
    EV.update();
    setTimeout(() => { if (S.navAnim && S.navAnim.popped === popped) S.navAnim = null; EV.update(); }, 300);
  };
  EV.replaceTop = function (name, props) {
    const S = EV.S;
    S.navSeq++;
    S.nav[S.nav.length - 1] = Object.assign({ name, key: S.navSeq }, props || {});
    S.navAnim = { type: "lateral", key: S.navSeq, until: Date.now() + 310 };
    EV.log("open", Object.assign({ screen: name, lateral: true }, props || {}));
    EV.onScreenChange();
    EV.update();
    setTimeout(() => EV.update(), 320);
  };
  EV.top = () => EV.S.nav[EV.S.nav.length - 1];
  EV.popToBoard = function () {
    EV.S.nav = [EV.S.nav[0]];
    EV.S.navAnim = null;
    EV.onScreenChange();
    EV.update();
  };
  // Opens a document or artifact from the Board with its session under it,
  // so Back goes to the session rather than straight back to the Board.
  EV.openOverSession = function (sessionId, from, screen) {
    const S = EV.S;
    S.nav = [S.nav[0], { name: "session", id: sessionId, key: ++S.navSeq, from }, Object.assign(screen, { key: ++S.navSeq })];
    S.navAnim = { type: "push", key: S.navSeq, until: Date.now() + 330 };
    EV.onScreenChange();
    EV.update();
  };
  EV.onScreenChange = function () {
    const t = EV.top();
    EV.S.reading = t && (t.name === "reader" || t.name === "artifact") ? t.name : null;
    if (!EV.S.reading) EV.releaseHeld();
  };

  // ---------- sheets, menus, toasts ----------
  EV.openSheet = function (kind, props) {
    EV.S.sheetSeq++;
    EV.S.sheets.push(Object.assign({ kind, key: EV.S.sheetSeq }, props || {}));
    EV.S.menu = null;
    EV.log("sheet", Object.assign({ kind }, props || {}));
    EV.update();
  };
  EV.closeSheet = function () { EV.S.sheets.pop(); EV.update(); };
  EV.closeAllSheets = function () { EV.S.sheets = []; EV.update(); };
  EV.openMenu = function (menu) { EV.S.menu = Object.assign({ openedAt: Date.now() }, menu); EV.update(); };
  EV.closeMenu = function () { if (EV.S.menu && EV.S.menu.liEl) EV.S.menu.liEl.classList.remove("li-sel"); EV.S.menu = null; EV.update(); };

  let toastTimer = null;
  EV.toast = function (text, undo) {
    EV.S.toast = { text, undo, key: Date.now() };
    clearTimeout(toastTimer);
    toastTimer = EV.later(undo ? 8000 : 2600, () => { EV.S.toast = null; EV.update(); });
    EV.update();
  };

  // ---------- in-app alerts ----------
  let bannerTimer = null;
  EV.alert = function (a) {
    const S = EV.S;
    const pref = S.prefs.alerts;
    if (a.kind === "failed" && !pref.failures) return;
    if ((a.kind === "question" || a.kind === "approval") && !pref.questions) return;
    if (a.kind === "finished" && !pref.finished) return;
    const t = EV.top();
    if (t && t.name === "session" && t.id === a.sessionId) return;
    // Whatever alerted you last is what Next goes to first, shown or held.
    if (a.sessionId) S.recent = [a.sessionId].concat(S.recent.filter((id) => id !== a.sessionId));
    // A finished result never joins or replaces an alert about sessions that
    // need you: coalescing would read "3 sessions need you". It already
    // lands in Finished on the Board.
    if (a.kind === "finished" && S.banner) return;
    if ((S.reading || S.typing) && pref.hold) {
      S.held.push(a);
      EV.log("banner_held", { kind: a.kind, sessionId: a.sessionId });
      EV.update();
      return;
    }
    showBanner(a);
  };
  function showBanner(a) {
    const S = EV.S;
    if (S.banner && Date.now() - S.banner.at < 5000 && S.banner.sessionId !== a.sessionId) {
      const n = (S.banner.count || 1) + 1;
      S.banner = { kind: "many", count: n, at: Date.now(), key: Date.now() };
    } else {
      S.banner = Object.assign({ at: Date.now(), key: Date.now() }, a);
    }
    EV.log("banner_shown", { kind: S.banner.kind, sessionId: S.banner.sessionId || null });
    armBannerTimer();
    EV.update();
  }
  // Alerts stay 8 seconds, and never go away while a finger is on them.
  function armBannerTimer() {
    clearTimeout(bannerTimer);
    bannerTimer = EV.later(8000, () => { if (EV.S.bannerHeld) { armBannerTimer(); return; } EV.S.banner = null; EV.update(); });
  }
  EV.releaseHeld = function () {
    const S = EV.S;
    if (!S.held.length) return;
    const list = S.held;
    S.held = [];
    if (list.length === 1) showBanner(list[0]);
    else { S.banner = { kind: "many", count: list.length, at: Date.now(), key: Date.now() }; EV.log("banner_shown", { kind: "many", count: list.length }); armBannerTimer(); }
  };
  EV.dismissBanner = function () { EV.S.banner = null; EV.update(); };

  // ---------- opening sessions ----------
  EV.openSession = function (id, opts) {
    const s = EV.sess(id);
    if (!s) return;
    EV.S.recent = EV.S.recent.filter((x) => x !== id);
    const t = EV.top();
    if (opts && opts.lateral && t && t.name === "session") EV.replaceTop("session", { id, from: opts.from || "next" });
    else EV.push("session", { id, from: (opts && opts.from) || "board" });
    EV.markSeen(s);
  };
  EV.markSeen = function (s) {
    if (s.state === "yourmove" && s.unseen) { s.unseen = false; EV.log("seen", { sessionId: s.id }); }
  };

  // ---------- icons (SF Symbols stand-ins) ----------
  const sv = (d, o) => html`<svg width=${(o && o.s) || 20} height=${(o && o.s) || 20} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width=${(o && o.w) || 2} stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${d}</svg>`;
  const P = (d) => html`<path d=${d} />`;
  EV.I = {
    chevL: (o) => sv(P("M15 4.5 7.5 12l7.5 7.5"), Object.assign({ w: 2.4 }, o)),
    chevR: (o) => sv(P("M9 5l7 7-7 7"), Object.assign({ s: 14, w: 2.4 }, o)),
    chevD: (o) => sv(P("M5 9l7 7 7-7"), Object.assign({ s: 14, w: 2.4 }, o)),
    search: (o) => sv(html`<circle cx="10.5" cy="10.5" r="6.5" /><path d="M15.5 15.5 21 21" />`, o),
    compose: (o) => sv(html`<path d="M11 4H6a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-5" /><path d="M17.5 3.5a2.1 2.1 0 0 1 3 3L12 15l-4 1 1-4z" />`, o),
    dots: (o) => sv(html`<circle cx="5" cy="12" r="1.6" fill="currentColor" /><circle cx="12" cy="12" r="1.6" fill="currentColor" /><circle cx="19" cy="12" r="1.6" fill="currentColor" />`, o),
    failed: (o) => html`<svg width=${(o && o.s) || 18} height=${(o && o.s) || 18} viewBox="0 0 24 24" aria-hidden="true"><path d="M8 2h8l6 6v8l-6 6H8l-6-6V8z" fill="currentColor" /><path d="M8.5 8.5l7 7M15.5 8.5l-7 7" stroke="#fff" stroke-width="2.4" stroke-linecap="round" /></svg>`,
    question: (o) => html`<svg width=${(o && o.s) || 18} height=${(o && o.s) || 18} viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="10.5" fill="currentColor" /><path d="M9.2 9.3a2.9 2.9 0 1 1 4.1 2.6c-.8.4-1.3 1-1.3 1.9v.6" stroke="#fff" stroke-width="2.3" fill="none" stroke-linecap="round" /><circle cx="12" cy="17.6" r="1.35" fill="#fff" /></svg>`,
    hand: (o) => html`<svg width=${(o && o.s) || 18} height=${(o && o.s) || 18} viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="10.5" fill="currentColor" /><g transform="translate(3.9 3.6) scale(.68)"><path d="M7.4 12.2V6.3a1.5 1.5 0 0 1 3 0v5M10.4 11.3V4.7a1.5 1.5 0 0 1 3 0v6.6M13.4 11.3V5.6a1.5 1.5 0 0 1 3 0v7.2M16.4 12.8V8.9a1.5 1.5 0 0 1 3 0v5.4c0 4.1-3 7.2-6.9 7.2-2.4 0-4.1-.9-5.5-2.8L4.3 14.9a1.5 1.5 0 0 1 2.3-1.9l.8 1" fill="none" stroke="#fff" stroke-width="2.9" stroke-linecap="round" stroke-linejoin="round" /></g></svg>`,
    warn: (o) => html`<svg width=${(o && o.s) || 18} height=${(o && o.s) || 18} viewBox="0 0 24 24" aria-hidden="true"><path d="M10.3 3.4a2 2 0 0 1 3.4 0l8.3 14.4a2 2 0 0 1-1.7 3H3.7a2 2 0 0 1-1.7-3z" fill="currentColor" /><path d="M12 9v4.6" stroke="#fff" stroke-width="2.3" stroke-linecap="round" /><circle cx="12" cy="17" r="1.3" fill="#fff" /></svg>`,
    restart: (o) => html`<svg width=${(o && o.s) || 18} height=${(o && o.s) || 18} viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="10.5" fill="currentColor" /><path d="M7.9 10.5A4.4 4.4 0 0 1 16.1 10.5M16.1 13.5A4.4 4.4 0 0 1 7.9 13.5" stroke="#fff" stroke-width="2" fill="none" stroke-linecap="round" /><path d="M17.7 9.9 14.5 11.1 16.9 12.6zM6.3 14.1 9.5 12.9 7.1 11.4z" fill="#fff" stroke="#fff" stroke-width=".8" stroke-linejoin="round" /></svg>`,
    pin: (o) => sv(html`<path d="M12 16v5" /><path d="M8.5 3.5h7l-1 6 3.5 3.5v1.5H6V13l3.5-3.5z" />`, o),
    archive: (o) => sv(html`<rect x="3" y="4" width="18" height="5" rx="1.5" /><path d="M5 9v9a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V9M10 13h4" />`, o),
    stop: (o) => html`<svg width=${(o && o.s) || 14} height=${(o && o.s) || 14} viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="5" width="14" height="14" rx="2.5" fill="currentColor" /></svg>`,
    up: (o) => sv(html`<path d="M12 19V5M5.5 11.5 12 5l6.5 6.5" />`, Object.assign({ w: 2.6, s: 18 }, o)),
    down: (o) => sv(html`<path d="M12 5v14M5.5 12.5 12 19l6.5-6.5" />`, Object.assign({ w: 2.4, s: 16 }, o)),
    plus: (o) => sv(P("M12 5v14M5 12h14"), Object.assign({ w: 2.2 }, o)),
    cpu: (o) => sv(html`<rect x="6" y="6" width="12" height="12" rx="2" /><path d="M9 2.5v3.5M15 2.5v3.5M9 18v3.5M15 18v3.5M2.5 9H6M2.5 15H6M18 9h3.5M18 15h3.5" />`, o),
    host: (o) => sv(html`<rect x="3" y="4" width="18" height="7" rx="2" /><rect x="3" y="13" width="18" height="7" rx="2" /><path d="M7 7.5h.01M7 16.5h.01" />`, o),
    folder: (o) => sv(P("M3 7.5A2.5 2.5 0 0 1 5.5 5h3.8l2 2.2h7.2A2.5 2.5 0 0 1 21 9.7v7.8a2.5 2.5 0 0 1-2.5 2.5h-13A2.5 2.5 0 0 1 3 17.5z"), o),
    puzzle: (o) => sv(P("M9 4.5a2 2 0 1 1 4 0V6h3.5A1.5 1.5 0 0 1 18 7.5V11h1.5a2 2 0 1 1 0 4H18v3.5a1.5 1.5 0 0 1-1.5 1.5H13v-1.5a2 2 0 1 0-4 0V20H5.5A1.5 1.5 0 0 1 4 18.5V15h1.5a2 2 0 1 0 0-4H4V7.5A1.5 1.5 0 0 1 5.5 6H9z"), o),
    shield: (o) => sv(html`<path d="M12 3 5 6v5.5c0 4.4 3 8 7 9.5 4-1.5 7-5.1 7-9.5V6z" /><rect x="9.5" y="10.5" width="5" height="4.5" rx="1" /><path d="M10.5 10.5V9a1.5 1.5 0 0 1 3 0v1.5" />`, o),
    doc: (o) => sv(html`<path d="M7 3h7l5 5v11a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z" /><path d="M14 3v5h5M8.5 13h7M8.5 16.5h5" />`, o),
    artifact: (o) => sv(html`<path d="M12 3 3.5 7.5 12 12l8.5-4.5z" /><path d="M3.5 12 12 16.5 20.5 12" /><path d="M3.5 16.5 12 21l8.5-4.5" />`, o),
    people: (o) => sv(html`<circle cx="9" cy="8" r="3.2" /><path d="M3 19.5c.6-3.2 3-5 6-5s5.4 1.8 6 5" /><circle cx="17" cy="9" r="2.6" /><path d="M16.5 14.5c2.4 0 4 1.5 4.5 4" />`, o),
    checklist: (o) => sv(html`<path d="M4 6.5l1.6 1.6L8.5 5M4 12.5l1.6 1.6 2.9-3.1M4 18.5l1.6 1.6 2.9-3.1M11.5 7h8.5M11.5 13h8.5M11.5 19h8.5" />`, o),
    target: (o) => sv(html`<circle cx="12" cy="12" r="8.5" /><circle cx="12" cy="12" r="4.5" /><circle cx="12" cy="12" r="1" fill="currentColor" />`, o),
    note: (o) => sv(html`<rect x="4.5" y="3.5" width="15" height="17" rx="2" /><path d="M8 8h8M8 12h8M8 16h5" />`, o),
    hub: (o) => sv(html`<circle cx="12" cy="12.5" r="2.6" /><circle cx="12" cy="4" r="1.9" /><circle cx="4.6" cy="18" r="1.9" /><circle cx="19.4" cy="18" r="1.9" /><path d="M12 6v3.9M6.2 16.8l3.6-2.6M17.8 16.8l-3.6-2.6" />`, o),
    command: (o) => sv(html`<path d="M9 6.5a2.5 2.5 0 1 0-2.5 2.5H9zM9 9v6M15 9h2.5A2.5 2.5 0 1 0 15 6.5zM15 9v6M9 15H6.5A2.5 2.5 0 1 0 9 17.5zM15 15v2.5a2.5 2.5 0 1 0 2.5-2.5zM9 9h6M9 15h6" />`, o),
    power: (o) => sv(html`<path d="M12 3.5v8" /><path d="M7.1 6.6a7.5 7.5 0 1 0 9.8 0" />`, o),
    copy: (o) => sv(html`<rect x="8.5" y="8.5" width="11" height="12" rx="2" /><path d="M15.5 8.5V5.5a2 2 0 0 0-2-2h-7a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h2" />`, o),
    person: (o) => sv(html`<circle cx="12" cy="8" r="3.6" /><path d="M5 20c.8-3.6 3.6-5.6 7-5.6s6.2 2 7 5.6" />`, o),
    sparkle: (o) => sv(P("M12 3.5c.6 4.3 2.7 6.4 7 7-4.3.6-6.4 2.7-7 7-.6-4.3-2.7-6.4-7-7 4.3-.6 6.4-2.7 7-7z"), o),
    link: (o) => sv(html`<path d="M10 14a4 4 0 0 0 5.7 0l3.1-3.1a4 4 0 0 0-5.7-5.7l-1.2 1.2" /><path d="M14 10a4 4 0 0 0-5.7 0l-3.1 3.1a4 4 0 0 0 5.7 5.7l1.2-1.2" />`, o),
    globe: (o) => sv(html`<circle cx="12" cy="12" r="8.5" /><path d="M3.5 12h17M12 3.5c2.3 2.4 3.4 5.2 3.4 8.5s-1.1 6.1-3.4 8.5c-2.3-2.4-3.4-5.2-3.4-8.5s1.1-6.1 3.4-8.5z" />`, o),
    quote: (o) => sv(html`<path d="M5 17c2.5-.8 4-2.9 4-6V7H4.5v5H8M15 17c2.5-.8 4-2.9 4-6V7h-4.5v5H18" />`, o),
    bubble: (o) => sv(P("M4 5.5A2.5 2.5 0 0 1 6.5 3h11A2.5 2.5 0 0 1 20 5.5v8a2.5 2.5 0 0 1-2.5 2.5H10l-4.5 4v-4A2.5 2.5 0 0 1 4 13.5z"), o),
    check: (o) => sv(P("M5 12.5l4.5 4.5L19 7"), Object.assign({ w: 2.5 }, o)),
    x: (o) => sv(P("M6 6l12 12M18 6 6 18"), Object.assign({ w: 2.3 }, o)),
    outline: (o) => sv(html`<path d="M4 6h.01M8 6h12M6 12h.01M10 12h10M6 18h.01M10 18h10" />`, o),
    diamond: (o) => sv(P("M12 3.5 19 12l-7 8.5L5 12z"), Object.assign({ s: 14 }, o)),
    retry: (o) => sv(html`<path d="M20 11.5A8 8 0 1 1 17.7 6" /><path d="M20 4v5h-5" />`, o),
    key: (o) => sv(html`<circle cx="8" cy="15" r="4" /><path d="M11 12l8.5-8.5M16 7l2.5 2.5M14 9l2 2" />`, o),
    photo: (o) => sv(html`<rect x="3" y="5" width="18" height="14" rx="2.5" /><circle cx="9" cy="10" r="1.8" /><path d="M21 16l-5-5-8 8" />`, o),
    camera: (o) => sv(html`<path d="M4 8.5A2.5 2.5 0 0 1 6.5 6h1.8l1.5-2h4.4l1.5 2h1.8A2.5 2.5 0 0 1 20 8.5v8a2.5 2.5 0 0 1-2.5 2.5h-11A2.5 2.5 0 0 1 4 16.5z" /><circle cx="12" cy="12.5" r="3.5" />`, o),
    branch: (o) => sv(html`<circle cx="6" cy="5.5" r="2" /><circle cx="6" cy="18.5" r="2" /><circle cx="18" cy="8" r="2" /><path d="M6 7.5v9M18 10c0 4-6 3.5-11 7" />`, o),
    gauge: (o) => sv(html`<path d="M4.5 17a8 8 0 1 1 15 0" /><path d="M12 13l3.5-4" />`, o),
    wifi: () => html`<svg width="17" height="12" viewBox="0 0 17 12" aria-hidden="true"><path d="M8.5 11.2 6.4 9a3 3 0 0 1 4.2 0zM3.8 6.4a6.7 6.7 0 0 1 9.4 0L11.6 8a4.4 4.4 0 0 0-6.2 0zM1.2 3.8a10.4 10.4 0 0 1 14.6 0l-1.6 1.6a8.1 8.1 0 0 0-11.4 0z" fill="currentColor" /></svg>`,
    battery: () => html`<svg width="27" height="13" viewBox="0 0 27 13" aria-hidden="true"><rect x="0.5" y="0.5" width="22" height="12" rx="3.5" fill="none" stroke="currentColor" opacity=".4" /><rect x="2" y="2" width="17" height="9" rx="2" fill="currentColor" /><path d="M24 4.5v4c.8-.3 1.3-1.1 1.3-2s-.5-1.7-1.3-2z" fill="currentColor" opacity=".4" /></svg>`,
    signal: () => html`<svg width="18" height="12" viewBox="0 0 18 12" aria-hidden="true"><rect x="0" y="8" width="3" height="4" rx="1" fill="currentColor" /><rect x="5" y="5.5" width="3" height="6.5" rx="1" fill="currentColor" /><rect x="10" y="3" width="3" height="9" rx="1" fill="currentColor" /><rect x="15" y="0" width="3" height="12" rx="1" fill="currentColor" /></svg>`,
  };
  const I = EV.I;

  // ---------- shared components ----------
  // The pulse meter: one bar per minute, oldest on the left and faintest.
  // Every meter shares one fixed scale (the hub reports activity on a
  // fleet-wide log scale, 0 to 1), so two meters can be compared. A minute
  // with nothing in it leaves only the baseline, so a session going quiet
  // shows a flat right end instead of a shorter glyph.
  EV.Pulse = function ({ values, off, flat, stuck }) {
    const vals = values || [0, 0, 0, 0, 0, 0, 0];
    const isFlat = flat || vals.every((v) => v < 0.05);
    const last = vals.length - 1;
    return html`<span class=${"pulse" + (stuck ? " stuck" : off ? " off" : isFlat ? " flat" : "")} aria-hidden="true">${vals.map((v, i) => html`<i style=${"height:" + (stuck ? 2 : v < 0.05 ? 0 : Math.max(2, Math.round(Math.min(1, v) * 13))) + "px;opacity:" + (0.45 + (0.55 * i) / last).toFixed(2)}></i>`)}</span>`;
  };
  // The whole fleet's activity, for the Board header: each minute is the
  // working sessions' activity summed and put back on the same scale.
  EV.fleetPulse = function () {
    const working = EV.S.sessions.filter((s) => s.live && !s.archived && s.state === "working");
    return [0, 1, 2, 3, 4, 5, 6].map((i) => Math.min(1, working.reduce((a, s) => a + (s.pulse[i] || 0), 0) / 6));
  };

  // still: a working session gets a plain running dot instead of its pulse
  // meter, for places that aren't watching it (pinned sections, the
  // Projects tree, the session's own title).
  EV.Mark = function ({ s, size, still }) {
    const st = EV.stateOf(s);
    const offline = EV.S.conn !== "live" || (EV.host(s.host) && EV.host(s.host).state === "offline");
    if (st === "failed") return html`<span class="mk danger" title="Failed">${I.failed({ s: size })}</span>`;
    if (st === "question") return html`<span class="mk attention" title="Question">${I.question({ s: size })}</span>`;
    if (st === "approval") return html`<span class="mk attention" title="Approval">${I.hand({ s: size })}</span>`;
    if (st === "warning") return html`<span class="mk attention" title="Warning">${I.warn({ s: size })}</span>`;
    if (st === "restart") return html`<span class="mk attention" title="Restart needed">${I.restart({ s: size })}</span>`;
    if (st === "stuck") return html`<span class="mk" title="May be stuck">${h(EV.Pulse, { values: [0, 0, 0, 0, 0, 0, 0], stuck: true })}</span>`;
    if (st === "working") return still ? html`<span class="mk" title="Working"><span class=${"run-dot" + (offline ? " off" : "")}></span></span>` : html`<span class="mk" title="Working">${h(EV.Pulse, { values: s.pulse, off: offline })}</span>`;
    if (st === "yourmove" && s.unseen) return html`<span class="mk" title="Finished, not yet seen"><span class="dot"></span></span>`;
    return html`<span class="mk"></span>`;
  };

  // The subagent strip, in the Subagents list's order: failures in red
  // (never thinner than 3pt, so 2 of 55 still shows), running work in
  // green, then finished work in the lightest neutral. Once everything is
  // done the strip says nothing, so it isn't drawn.
  EV.Strip = function ({ t, wide }) {
    if (!t || (!t.run && !t.fail)) return null;
    const seg = (n, c, min) => (n ? html`<i class=${c} style=${"flex:" + n + " 1 " + (min ? "3px" : "0px") + ";min-width:" + (min ? "3px" : "1px")}></i>` : null);
    return html`<span class=${"strip" + (wide ? " wide" : "")} aria-hidden="true">${seg(t.fail, "s-fail", true)}${seg(t.run, "s-run")}${seg(t.done, "s-done")}</span>`;
  };

  EV.Switch = function ({ on, onChange, label }) {
    return html`<button class=${"switch" + (on ? " on" : "")} role="switch" aria-checked=${on ? "true" : "false"} aria-label=${label} onClick=${(e) => { e.stopPropagation(); onChange(!on); }}></button>`;
  };

  EV.Seg = function ({ options, value, onChange, disabled }) {
    return html`<div class="seg" role="radiogroup">${options.map((o) => html`<button role="radio" aria-checked=${o === value ? "true" : "false"} class=${o === value ? "on" : ""} disabled=${disabled && disabled.includes(o)} onClick=${() => onChange(o)}>${cap(o)}</button>`)}</div>`;
  };
  const cap = (s) => (s === "xhigh" ? "XHigh" : s.charAt(0).toUpperCase() + s.slice(1));
  EV.cap = cap;
  // Effort as a segmented control: the efforts this model can't do are off.
  EV.EffortSeg = function ({ model, value, onChange }) {
    return h(EV.Seg, { options: EV.EFFORTS, value, onChange, disabled: EV.EFFORTS.filter((e) => !model.efforts.includes(e)) });
  };

  EV.Gi = function ({ icon, label, sub, value, chev, onClick, cls, right }) {
    return html`<button class=${"gi" + (icon ? " icon" : "") + (cls ? " " + cls : "") + (onClick ? "" : " static")} onClick=${onClick}>
      ${icon ? html`<span class="gic">${icon}</span>` : html`<span></span>`}
      <span class="gl">${label}${sub ? html`<small>${sub}</small>` : null}</span>
      <span class="gv">${value}${right}${chev ? html`<span class="chev">${I.chevR()}</span>` : null}</span>
    </button>`;
  };

  // Sheet frame. size: "large" | "medium"
  EV.Sheet = function ({ title, left, right, size, children, onClose, flat }) {
    const [dy, setDy] = useState(0);
    const start = useRef(null);
    const close = onClose || EV.closeSheet;
    const onDown = (e) => { start.current = e.clientY; };
    const onMove = (e) => { if (start.current != null) setDy(Math.max(0, e.clientY - start.current)); };
    const onUp = () => { if (dy > 110) close(); setDy(0); start.current = null; };
    return html`<div class=${"sheet " + (size || "large")} style=${dy ? "transform:translateY(" + dy + "px);transition:none" : ""} role="dialog" aria-label=${title}>
      <div class="grab" onPointerDown=${onDown} onPointerMove=${onMove} onPointerUp=${onUp} onPointerCancel=${onUp} style="touch-action:none;cursor:grab;padding:4px 0"></div>
      ${flat ? null : html`<div class="sheet-h">${left || html`<span></span>`}<div class="ttl">${title}</div>${right || html`<span></span>`}</div>`}
      <div class="sheet-b">${children}</div>
    </div>`;
  };
  // The standard sheet-header buttons; both just close the sheet.
  EV.Done = () => html`<button class="text-btn strong" onClick=${EV.closeSheet}>Done</button>`;
  EV.Cancel = () => html`<button class="text-btn" onClick=${EV.closeSheet}>Cancel</button>`;

  // ---------- gestures ----------
  // Swipeable, long-pressable row wrapper.
  EV.SwipeRow = function ({ lead, trail, onTap, onLong, children, disabled }) {
    const [dx, setDx] = useState(0);
    const st = useRef({});
    const W = 76;
    const leadW = (lead || []).length * W, trailW = (trail || []).length * W;
    const reset = () => { setDx(0); st.current.open = 0; };
    const down = (e) => {
      if (disabled) return;
      // A swipe that starts in the screen's edge zone is the system back
      // gesture, never a row action: an edge swipe must not archive anything.
      const appLeft = document.getElementById("app").getBoundingClientRect().left;
      st.current = { x: e.clientX, y: e.clientY, dx0: dx, moved: false, horiz: null, long: false, id: e.pointerId, edge: e.clientX - appLeft < 24 };
      clearTimeout(st.current.lt);
      st.current.lt = setTimeout(() => {
        if (!st.current.moved && onLong) { st.current.long = true; EV.S.interacting = false; EV.swallowNextClick(); onLong(); }
      }, 520);
      EV.S.interacting = true;
    };
    const move = (e) => {
      const c = st.current;
      if (c.x == null || c.id !== e.pointerId) return;
      const mx = e.clientX - c.x, my = e.clientY - c.y;
      if (!c.moved && Math.hypot(mx, my) > 8) { c.moved = true; clearTimeout(c.lt); c.horiz = Math.abs(mx) > Math.abs(my) && !c.edge; if (c.horiz) { try { e.currentTarget.setPointerCapture(e.pointerId); } catch (_) {} } }
      if (c.moved && c.horiz) {
        let nx = c.dx0 + mx;
        if (!lead || !lead.length) nx = Math.min(0, nx);
        if (!trail || !trail.length) nx = Math.max(0, nx);
        setDx(nx);
      }
    };
    const up = () => {
      const c = st.current;
      clearTimeout(c.lt);
      setTimeout(() => { EV.S.interacting = false; EV.update(); }, 250);
      if (c.x == null) return;
      c.x = null;
      if (c.long) return;
      if (c.moved && c.horiz) {
        if (dx > Math.max(leadW + 60, 190) && lead && lead[0]) { reset(); lead[0].run(); return; }
        if (dx > leadW / 2 && leadW) { setDx(leadW); return; }
        if (-dx > trailW / 2 && trailW) { setDx(-trailW); return; }
        reset();
        return;
      }
      if (!c.moved) {
        if (Math.abs(dx) > 4) { reset(); return; }
        onTap && onTap();
      }
    };
    return html`<div class="row-wrap">
      ${dx > 0 && lead ? html`<div class="swipe-actions lead" style=${"width:" + Math.max(dx, leadW) + "px"}>${lead.map((a) => html`<button class=${a.cls} style=${lead.length === 1 ? "width:100%;align-items:flex-start;padding-left:22px" : ""} onClick=${() => { reset(); a.run(); }}>${a.icon}${a.label}</button>`)}</div>` : null}
      ${dx < 0 && trail ? html`<div class="swipe-actions trail">${trail.map((a) => html`<button class=${a.cls} onClick=${() => { reset(); a.run(); }}>${a.icon}${a.label}</button>`)}</div>` : null}
      <div class=${"row-inner" + (st.current.x != null ? " dragging" : "")} style=${"transform:translateX(" + dx + "px);transition:" + (st.current.x != null ? "none" : "transform 220ms cubic-bezier(.2,.8,.2,1)")}
        onPointerDown=${down} onPointerMove=${move} onPointerUp=${up} onPointerCancel=${up} onContextMenu=${(e) => e.preventDefault()}
        role="button" tabindex="0" onKeyDown=${(e) => { if (e.key === "Enter") onTap && onTap(); }}>${children}</div>
    </div>`;
  };

  // Long-press only (no swipe), used on transcript items and doc blocks.
  EV.useLongPress = function (onLong, onTap) {
    const st = useRef({});
    return {
      onPointerDown: (e) => {
        st.current = { x: e.clientX, y: e.clientY, fired: false };
        st.current.t = setTimeout(() => { st.current.fired = true; EV.swallowNextClick(); onLong(e); }, 520);
      },
      onPointerMove: (e) => { if (st.current.t && Math.hypot(e.clientX - st.current.x, e.clientY - st.current.y) > 8) { clearTimeout(st.current.t); st.current.t = null; } },
      onPointerUp: () => { clearTimeout(st.current.t); const fired = st.current.fired; st.current = {}; if (!fired && onTap) onTap(); },
      onPointerCancel: () => { clearTimeout(st.current.t); st.current = {}; },
      onContextMenu: (e) => e.preventDefault(),
    };
  };

  // ---------- chrome ----------
  function StatusBar() {
    const d = new Date();
    const t = d.getHours() % 12 || 12;
    return html`<div class="statusbar"><span>${t}:${String(d.getMinutes()).padStart(2, "0")}</span><span class="island"></span><span class="sb-right">${I.signal()}${I.wifi()}${I.battery()}</span></div>`;
  }

  function Banner() {
    const b = EV.S.banner;
    if (!b) return null;
    const s = b.sessionId && EV.sess(b.sessionId);
    const tap = () => {
      EV.log("banner_tap", { kind: b.kind, sessionId: b.sessionId || null });
      EV.S.banner = null;
      if (b.kind === "many") { EV.popToBoard(); EV.S.board.jump = "live"; EV.update(); return; }
      if (b.kind === "notice") { EV.openSheet("hub", { focus: b.focus }); return; }
      EV.closeAllSheets();
      EV.openSession(b.sessionId, { from: "banner" });
    };
    const startY = useRef(null);
    let title = "", why = "", mark = null;
    if (b.kind === "many") { title = b.count + " sessions need you"; why = "Tap to see them on the Board."; mark = html`<span class="mk attention">${I.question()}</span>`; }
    else if (b.kind === "notice") { title = b.title; why = b.why; mark = html`<span class="mk attention">${I.warn()}</span>`; }
    else if (s) { title = s.title; const w = EV.whyParts && EV.whyParts(s); why = w ? h(EV.WhyText, { w }) : b.why || s.why; mark = h(EV.Mark, { s }); }
    // In-app alerts drop in just below the nav bar, so they never cover the
    // Back button, the title or the ask dock. Swipe up to dismiss.
    return html`<button class=${"banner" + (b.kind === "finished" ? " finished" : "")} key=${b.key} onClick=${tap}
      onPointerDown=${(e) => { startY.current = e.clientY; EV.S.bannerHeld = true; }}
      onPointerUp=${(e) => { EV.S.bannerHeld = false; if (startY.current != null && startY.current - e.clientY > 30) { e.preventDefault(); EV.log("banner_dismiss", {}); EV.dismissBanner(); } startY.current = null; }}
      onPointerCancel=${() => { EV.S.bannerHeld = false; }}
      aria-live="polite">
      ${mark}
      <span><span class="bt"><span>${title}</span><span class="w">now</span></span><span class=${"bw why " + ((s && EV.whyParts && (EV.whyParts(s) || {}).cls) || "")}>${why}</span></span>
    </button>`;
  }

  function Toast() {
    const t = EV.S.toast;
    if (!t) return null;
    return html`<div class=${"toast" + (t.undo ? "" : " solo")} key=${t.key} role="status">${t.text}${t.undo ? html`<button onClick=${() => { t.undo(); EV.S.toast = null; EV.update(); }}>Undo</button>` : null}</div>`;
  }

  function MenuLayer() {
    const m = EV.S.menu;
    if (!m) return null;
    const Comp = EV.menus && EV.menus[m.kind];
    const onScrim = () => EV.closeMenu();
    return html`<div class="menu-layer"><div class="menu-scrim" onClick=${onScrim}></div>${Comp ? h(Comp, m) : null}</div>`;
  }

  EV.ListMenu = function ({ items, top, right, preview, title, header }) {
    return html`<div class="menu-wrap" style=${"top:" + (top || 140) + "px"}>
      ${preview || null}
      <div class=${"menu" + (right ? " right" : "") + (header ? " wide" : "")} role="menu" aria-label=${title || "Menu"}>
        ${header ? html`<div class="mh">${header}</div>` : null}
        ${items.filter(Boolean).map((it) => html`<button role="menuitem" class=${"mi" + (it.danger ? " danger" : "") + (it.sep ? " sepd" : "") + (it.sub ? " two" : "")} onClick=${() => { if (!it.keep) EV.closeMenu(); it.run(); }}>
          ${it.checked != null ? html`<span class="chk lead">${it.checked ? I.check({ s: 17 }) : null}</span>` : null}<span style="flex:1;min-width:0">${it.label}${it.sub ? html`<small>${it.sub}</small>` : null}</span>${it.checked == null && it.icon ? html`<span class="ic">${it.icon}</span>` : null}
        </button>`)}
      </div>
    </div>`;
  };
  EV.menus = {
    list: (m) => h(EV.ListMenu, m),
  };

  function ScreenStack() {
    const S = EV.S;
    const anim = S.navAnim && Date.now() < S.navAnim.until + 40 ? S.navAnim : null;
    const out = [];
    const n = S.nav.length;
    S.nav.forEach((sc, i) => {
      const isTop = i === n - 1;
      let cls = "screen";
      if (anim && anim.type === "push" && isTop && anim.key === sc.key) cls += " push-in";
      else if (anim && anim.type === "push" && i === n - 2) cls += " under";
      else if (anim && anim.type === "lateral" && isTop && anim.key === sc.key) cls += " lateral-in";
      let style = "";
      if (!isTop && !(anim && anim.type === "push" && i === n - 2)) style = "visibility:hidden;pointer-events:none";
      const Comp = EV.screens[sc.name];
      out.push(html`<div class=${cls} style=${style} key=${sc.key} data-screen=${sc.name} aria-hidden=${isTop ? "false" : "true"}>${Comp ? h(Comp, sc) : null}</div>`);
    });
    if (anim && anim.type === "pop" && anim.popped) {
      const Comp = EV.screens[anim.popped.name];
      out.push(html`<div class="screen pop-out" key=${anim.popped.key} data-screen=${anim.popped.name}>${Comp ? h(Comp, anim.popped) : null}</div>`);
    }
    return out;
  }

  function SheetLayer() {
    const S = EV.S;
    if (!S.sheets.length) return null;
    return S.sheets.map((sh, i) => {
      const Comp = EV.sheets[sh.kind];
      const isTop = i === S.sheets.length - 1;
      return html`<div class="layer" key=${sh.key} style=${isTop ? "" : "pointer-events:none"}>
        ${i === 0 ? html`<div class="scrim" onClick=${() => { if (!sh.modal) EV.closeSheet(); }}></div>` : null}
        ${Comp ? h(Comp, Object.assign({ stacked: i > 0 }, sh)) : html`<div class="sheet large"><div class="empty">Missing sheet: ${sh.kind}</div></div>`}
      </div>`;
    });
  }

  EV.screens = {};
  EV.sheets = {};

  // Edge-swipe back, anywhere in the app. Inside a stacked sheet (a picker
  // opened from the launch sheet) it goes back to the sheet underneath, the
  // way a UINavigationController inside a sheet does; with no sheet open it
  // pops the screen. A lone sheet is dismissed by dragging it down, not by
  // this gesture.
  EV.edgeBack = () => { if (EV.S.sheets.length > 1) EV.closeSheet(); else EV.pop(); };
  function useEdgeBack(ref) {
    useEffect(() => {
      const el = ref.current;
      if (!el) return;
      const canStart = (x) => x - el.getBoundingClientRect().left < 22 && !EV.S.menu
        && (EV.S.sheets.length > 1 || (EV.S.nav.length > 1 && !EV.S.sheets.length));
      const finish = (start, x, y) => {
        const dx = x - start.x, dy = Math.abs(y - start.y);
        if (dx > 70 && dy < 80) { EV.log("gesture", { name: "edge_back" }); EV.edgeBack(); }
      };
      // A touch fires both streams (pointerup, then touchend). Scroll areas
      // take the pointer stream (pointercancel) as soon as a horizontal drag
      // starts, so the touch stream is the backup; whichever ends first
      // clears both starts so one swipe never goes back twice.
      let st = null, tst = null;
      const down = (e) => { st = canStart(e.clientX) ? { x: e.clientX, y: e.clientY } : null; };
      const up = (e) => {
        if (!st) return;
        const start = st;
        st = tst = null;
        finish(start, e.clientX, e.clientY);
      };
      const tdown = (e) => {
        const t = e.touches[0];
        tst = t && canStart(t.clientX) ? { x: t.clientX, y: t.clientY } : null;
      };
      const tup = (e) => {
        const t = e.changedTouches[0];
        if (!tst || !t) return;
        const start = tst;
        st = tst = null;
        finish(start, t.clientX, t.clientY);
      };
      el.addEventListener("pointerdown", down, true);
      el.addEventListener("pointerup", up, true);
      el.addEventListener("pointercancel", () => { st = null; }, true);
      el.addEventListener("touchstart", tdown, { capture: true, passive: true });
      el.addEventListener("touchend", tup, { capture: true, passive: true });
      return () => { el.removeEventListener("pointerdown", down, true); el.removeEventListener("pointerup", up, true); el.removeEventListener("touchstart", tdown, true); el.removeEventListener("touchend", tup, true); };
    }, []);
  }

  // A long-press opens its menu while the finger is still down. Lifting that
  // finger then produces a click wherever it is, which would close the menu
  // (on the scrim) or trigger whatever item appeared under it. iOS never
  // does that, so swallow exactly that one click; any new touch clears it.
  EV.swallowNextClick = () => { EV.S.swallowClick = true; };
  function useLiftGuard(ref) {
    useEffect(() => {
      const el = ref.current;
      if (!el) return;
      const click = (e) => { if (EV.S.swallowClick) { EV.S.swallowClick = false; e.stopPropagation(); e.preventDefault(); } };
      const down = () => { EV.S.swallowClick = false; };
      el.addEventListener("click", click, true);
      el.addEventListener("pointerdown", down, true);
      return () => { el.removeEventListener("click", click, true); el.removeEventListener("pointerdown", down, true); };
    }, []);
  }

  function App() {
    const [, setV] = useState(0);
    const ref = useRef(null);
    rerender = () => setV((v) => v + 1);
    useEdgeBack(ref);
    useLiftGuard(ref);
    const framed = EV.framed;
    return html`<div ref=${ref} style="position:absolute;inset:0">
      ${framed ? h(StatusBar) : null}
      ${h(ScreenStack)}
      ${h(SheetLayer)}
      ${h(MenuLayer)}
      ${h(Banner)}
      ${h(Toast)}
      ${framed ? html`<div class="homebar"></div>` : null}
    </div>`;
  }
  EV.App = App;

  EV.applyTheme = function () {
    const t = EV.S.prefs.theme;
    const root = document.documentElement;
    if (t === "Light") root.setAttribute("data-theme", "light");
    else if (t === "Dark") root.setAttribute("data-theme", "dark");
    else if (EV.S.prefs.themeTouched) root.removeAttribute("data-theme");
    document.documentElement.style.setProperty("--read-font", EV.S.prefs.readFont === "Sans" ? "var(--sans)" : "var(--serif)");
  };
})();
