// The Board: home. Live bands (needs you, finished, working, idle),
// pinned categories, the Projects/Hosts tree, test runs, archived,
// search, select mode and hub notices.
(function () {
  const EV = window.EV;
  const { html, h, useRef, useEffect, useState } = EV;
  const I = EV.I;

  let frozen = null; // order snapshot while a finger is on the list

  function notices() {
    const S = EV.S;
    const out = [];
    for (const p of S.providers) {
      if (p.status === "expired") {
        const n = S.sessions.filter((s) => s.live && !s.archived && EV.model(s.model).provider === p.id).length;
        out.push({ id: "p:" + p.id, open: () => EV.openSheet("provider", { providerId: p.id }), text: html`<b>${p.id}</b> sign-in expired${n ? " · " + n + (n === 1 ? " session" : " sessions") : ""}`, act: "Sign in", run: () => { EV.log("notice_action", { notice: "signin", provider: p.id }); EV.openSheet("signin", { provider: p.id }); } });
      }
    }
    for (const x of S.hosts) {
      if (x.state === "offline") {
        const n = S.sessions.filter((s) => s.live && !s.archived && s.host === x.id).length;
        out.push({ id: "h:" + x.id, open: () => EV.openSheet("host", { hostId: x.id }), text: html`<b>${x.id}</b> is offline${n ? " · " + n + " sessions can't be reached" : ""}`, act: "Reconnect", run: () => { EV.log("notice_action", { notice: "reconnect", host: x.id }); EV.reconnectHost(x.id); } });
      }
    }
    return out;
  }

  EV.reconnectHost = function (id) {
    const x = EV.host(id);
    if (!x) return;
    x.state = "connecting";
    EV.toast("Connecting to " + id + "…");
    EV.update();
    setTimeout(() => { x.state = "connected"; x.lastError = null; EV.toast("Connected to " + id); EV.update(); }, 1800);
  };

  // ---------- row ----------
  // Why a session is on the Board. Rows that need you lead with a word for
  // the kind of need, so a question, an approval, a failure and a restart
  // are told apart by words as well as by their marks. Only that word takes
  // the state's color; the reason, which is what you read, stays ink.
  EV.whyParts = function (s) {
    const st = EV.stateOf(s);
    const rest = (s.why || "").replace(/^(Failed|Asks):\s*/, "");
    if (st === "failed") return { label: "Failed", text: rest, cls: "danger", two: true };
    if (st === "question") return { label: "Question", text: rest, cls: "attention", two: true };
    if (st === "approval") return { label: "Approval", text: rest.replace(/^Wants to /, "wants to "), cls: "attention", two: true };
    if (st === "restart") return { label: "Restart needed", text: "restart this session to pick up the hub's update", cls: "attention" };
    if (st === "warning") return { label: "Warning", text: rest, cls: "attention", two: true };
    if (st === "stuck") return { label: "May be stuck", text: "no updates for " + EV.fmtAgo(Date.now() - s.updatedAt), cls: "attention" };
    if (st === "working") return EV.activityLine(s);
    if (s.state === "yourmove" && s.unseen) return { text: s.why, cls: "excerpt", two: true };
    return null;
  };
  const whyFor = EV.whyParts;
  EV.WhyText = function ({ w }) {
    return w.label ? html`<b class="wl">${w.label}</b> · ${w.text}` : w.text;
  };

  // A row's last line says where the session is in its own plan: its task
  // list, done of total, and the task it's on. That says more than a
  // subagent count, which is the same at minute 2 and minute 40, so
  // subagents appear only as an exception, when some failed. The strip and
  // the full counts live on the session's Subagents chip.
  function Meta({ s }) {
    const t = EV.tally(s);
    const k = s.tasks;
    const parts = [];
    if (k && k.done < k.total) parts.push(html`<span class="task">${I.checklist({ s: 13 })}<b>Task ${k.done + 1} of ${k.total}</b>${k.current ? html`<span class="tt">${k.current}</span>` : null}</span>`);
    if (t && t.fail) parts.push(html`<span class="bad">${t.fail} subagent${t.fail === 1 ? "" : "s"} failed</span>`);
    // Project and host only when they aren't the fleet's usual ones, so the
    // ones that do print stand out.
    const u = EV.usual();
    if (s.project !== u.project) parts.push(html`<span class="host">${I.folder({ s: 12 })}${s.project}</span>`);
    if (EV.multiHost() && s.host !== u.host) parts.push(html`<span class="host">${I.host({ s: 12 })}${s.host}</span>`);
    if (EV.S.prefs.showModel) parts.push(html`<span>${EV.modelLabel(s.model)}</span>`);
    if (!parts.length) return null;
    return html`<div class="meta">${parts.map((p, i) => (i === 0 ? p : html`<span class="sep"></span>${p}`))}</div>`;
  }

  EV.openFileFromBoard = function (s, a) {
    const S = EV.S;
    const file = a.kind === "Artifact" ? { name: "artifact", id: a.id, sessionId: s.id } : { name: "reader", path: a.path, sessionId: s.id };
    S.nav = [S.nav[0], { name: "session", id: s.id, key: ++S.navSeq, from: "board" }, Object.assign(file, { key: ++S.navSeq })];
    S.navAnim = { type: "push", key: S.navSeq, until: Date.now() + 330 };
    EV.onScreenChange();
    EV.update();
  };

  function Attachments({ s }) {
    if (!s.attachments || !s.attachments.length || !(s.state === "yourmove")) return null;
    const stop = (e) => e.stopPropagation();
    return html`<div class="atts">${s.attachments.slice(0, 2).map((a) => html`<button class="att" onPointerDown=${stop} onPointerUp=${stop}
      onClick=${(e) => { e.stopPropagation(); EV.log("attachment_open", { sessionId: s.id, kind: a.kind, target: a.path || a.id, from: "board" }); EV.markSeen(s); EV.openFileFromBoard(s, a); }}>
      <span style="display:flex;color:var(--ink-mid)">${a.kind === "Artifact" ? I.artifact({ s: 15 }) : I.doc({ s: 15 })}</span><b>${a.kind}</b><span class="p">${a.kind === "Artifact" ? a.title : EV.docTitle(a.path)}</span></button>`)}</div>`;
  }

  EV.rowActions = function (s) {
    const lead = [];
    const trail = [];
    if (s.archived) lead.push({ label: "Unarchive", cls: "sa-archive", icon: I.archive({ s: 20 }), run: () => EV.setArchived(s, false) });
    else lead.push({ label: "Archive", cls: "sa-archive", icon: I.archive({ s: 20 }), run: () => EV.setArchived(s, true) });
    if (EV.stateOf(s) === "working" || EV.stateOf(s) === "stuck") trail.push({ label: "Stop", cls: "sa-stop", icon: I.stop({ s: 16 }), run: () => EV.stopTurn(s, "row_swipe") });
    if (!s.archived) trail.push({ label: s.category ? "Unpin" : "Pin", cls: "sa-pin", icon: I.pin({ s: 20 }), run: () => (s.category ? EV.unpin(s) : EV.pinMenu(s)) });
    trail.push({ label: "More", cls: "sa-more", icon: I.dots({ s: 20 }), run: () => EV.rowMenu(s) });
    return { lead, trail };
  };

  EV.BoardRow = function ({ s, quiet, context }) {
    const S = EV.S;
    const sel = S.board.selecting;
    const why = quiet ? null : whyFor(s);
    const st = EV.stateOf(s);
    const flash = S.board.flash[s.id] && Date.now() - S.board.flash[s.id] < 1300;
    const acts = EV.rowActions(s);
    const draft = S.drafts[s.id] && S.drafts[s.id].trim();
    const age = st === "working" || st === "stuck" ? EV.fmtAgo(Date.now() - s.startedAt) : EV.fmtAgo(Date.now() - s.updatedAt);
    const tap = () => {
      if (sel) { S.board.selected[s.id] = !S.board.selected[s.id]; EV.update(); return; }
      EV.log("row_tap", { sessionId: s.id, context: context || "live" });
      EV.openSession(s.id, { from: context || "board" });
    };
    const body = html`<div class=${"row" + (quiet ? " quiet" : "") + (EV.isNeeds(s) && !quiet ? " two-line" : "") + (flash ? " flash" : "") + (sel && S.board.selected[s.id] ? " selected" : "")} data-session=${s.id}>
      <div class="mark">${sel ? html`<span class=${"sel-box" + (S.board.selected[s.id] ? " on" : "")}>${S.board.selected[s.id] ? I.check({ s: 14 }) : null}</span>` : h(EV.Mark, { s, still: !!context && (context === "projects" || context.startsWith("category:")) })}</div>
      <div style="min-width:0">
        <div class="l1"><span class="title">${s.title}</span><span class="age">${draft ? html`<span class="draft">Draft</span>` : null}${age}</span></div>
        ${why ? html`<div class=${"why" + (why.cls ? " " + why.cls : "") + (why.two ? " two" : "")}>${h(EV.WhyText, { w: why })}</div>` : null}
        ${quiet ? null : h(Attachments, { s })}
        ${quiet ? null : h(Meta, { s })}
      </div>
    </div>`;
    return h(EV.SwipeRow, { lead: sel ? null : acts.lead, trail: sel ? null : acts.trail, onTap: tap, onLong: sel ? null : () => EV.rowMenu(s), disabled: false }, body);
  };

  // ---------- actions ----------
  EV.setArchived = function (s, val) {
    s.archived = val;
    EV.log(val ? "archive" : "unarchive", { sessionId: s.id });
    EV.toast(val ? "Archived" : "Unarchived", () => { s.archived = !val; EV.log(val ? "unarchive" : "archive", { sessionId: s.id, undo: true }); EV.update(); });
    EV.update();
  };
  EV.stopTurn = function (s, how) {
    if (s.state !== "working") return;
    s.state = "idle";
    s.unseen = false;
    s.updatedAt = Date.now();
    s.pulse = [0, 0, 0, 0, 0, 0, 0];
    (EV.S.transcripts[s.id] = EV.S.transcripts[s.id] || []).push({ t: "sys", text: "Stopped by you" });
    EV.log("stop", { sessionId: s.id, how });
    EV.toast("Stopped");
    EV.update();
  };
  EV.shutdown = function (s) {
    s.state = "shutdown";
    s.live = false;
    s.updatedAt = Date.now();
    EV.log("shutdown", { sessionId: s.id });
    EV.toast("Session shut down");
    EV.update();
  };
  EV.pin = function (s, catId) {
    s.category = catId;
    const c = EV.S.categories.find((x) => x.id === catId);
    EV.log("pin", { sessionId: s.id, category: c ? c.name : catId });
    EV.toast("Pinned to " + (c ? c.name : catId), () => { s.category = null; EV.log("unpin", { sessionId: s.id, undo: true }); EV.update(); });
    EV.update();
  };
  EV.unpin = function (s) {
    const prev = s.category;
    s.category = null;
    EV.log("unpin", { sessionId: s.id });
    EV.toast("Unpinned", () => { s.category = prev; EV.update(); });
    EV.update();
  };
  EV.pinMenu = function (s) {
    const S = EV.S;
    const items = S.categories.map((c) => ({ label: c.name, checked: s.category === c.id, run: () => EV.pin(s, c.id) }));
    items.push({ label: "New category…", icon: I.plus({ s: 18 }), sep: true, run: () => EV.openSheet("newCategory", { sessionId: s.id }) });
    if (s.category) items.push({ label: "Unpin", icon: I.pin({ s: 18 }), run: () => EV.unpin(s) });
    EV.openMenu({ kind: "list", title: "Pin to category", items, top: 220, preview: html`<div class="preview"><div class="pt">Pin to category</div><div class="pw">${s.title}</div></div>` });
  };
  EV.rowMenu = function (s) {
    const st = EV.stateOf(s);
    const t = EV.tally(s);
    const tr = EV.S.transcripts[s.id] || [];
    const lastAgent = [...tr].reverse().find((x) => x.t === "agent");
    const why = whyFor(s);
    EV.log("row_menu", { sessionId: s.id });
    const items = [
      !s.archived ? { label: s.category ? "Change category…" : "Pin to category…", icon: I.pin({ s: 18 }), run: () => setTimeout(() => EV.pinMenu(s), 30) } : null,
      s.state === "yourmove" ? { label: s.unseen ? "Mark as read" : "Mark as unread", icon: I.check({ s: 18 }), run: () => { s.unseen = !s.unseen; EV.log(s.unseen ? "mark_unread" : "mark_read", { sessionId: s.id }); EV.update(); } } : null,
      s.state === "idle" ? { label: "Mark as unread", icon: I.check({ s: 18 }), run: () => { s.state = "yourmove"; s.unseen = true; EV.log("mark_unread", { sessionId: s.id }); EV.update(); } } : null,
      st === "working" || st === "stuck" ? { label: "Stop this turn", icon: I.stop({ s: 14 }), run: () => EV.stopTurn(s, "menu") } : null,
      { label: "Rename", icon: I.compose({ s: 18 }), run: () => EV.openSheet("rename", { sessionId: s.id }) },
      { label: "Copy link", icon: I.link({ s: 18 }), run: () => { EV.log("copy_link", { sessionId: s.id }); EV.toast("Link copied"); } },
      { label: s.archived ? "Unarchive" : "Archive", icon: I.archive({ s: 18 }), sep: true, run: () => EV.setArchived(s, !s.archived) },
      s.live ? { label: "Shut down…", icon: I.power({ s: 18 }), danger: true, run: () => setTimeout(() => EV.confirmShutdown(s), 30) } : null,
    ];
    const preview = html`<div class="preview" role="button" tabindex="0" style="cursor:pointer" onClick=${() => { EV.closeMenu(); EV.log("row_menu_open", { sessionId: s.id }); EV.openSession(s.id, { from: "menu" }); }}>
      <div style="display:flex;gap:8px;align-items:center">${h(EV.Mark, { s })}<div class="pt">${s.title}</div></div>
      ${why ? html`<div class=${"pw" + (why.cls ? " why " + why.cls : "")}>${h(EV.WhyText, { w: why })}</div>` : null}
      <div class="pm">${s.tasks ? html`<span>${s.tasks.done}/${s.tasks.total} tasks</span><span>·</span>` : null}${t ? html`<span>${EV.tallyTotal(t)} subagents${t.fail ? ", " + t.fail + " failed" : ""}</span><span>·</span>` : null}<span>${s.project}</span><span>·</span><span>${s.host}</span><span>·</span><span>${EV.modelLabel(s.model, s.effort)}</span></div>
      ${lastAgent ? html`<div class="px">${EV.plain(lastAgent.md, 240)}</div>` : null}
    </div>`;
    EV.openMenu({ kind: "list", items, preview, top: 90, title: "Session actions" });
  };
  EV.confirmShutdown = function (s) {
    EV.openMenu({ kind: "list", top: 260, title: "Shut down",
      preview: html`<div class="preview"><div class="pt">Shut down “${s.title}”?</div><div class="pw">The session stops and frees its host. Sending it a message later resumes it where it left off.</div></div>`,
      items: [{ label: "Shut down", danger: true, icon: I.x({ s: 18 }), run: () => EV.shutdown(s) }, { label: "Cancel", run: () => {} }] });
  };

  // ---------- sections ----------
  function SectionHead({ id, label, n, big, action }) {
    return html`<div class=${"sec-h" + (big ? " big" : "")} id=${id}><span class="h-left">${label}${n != null ? html`<span class="n">${n}</span>` : null}</span>${action || null}</div>`;
  }

  function Fold({ id, label, n, children, mark }) {
    const S = EV.S;
    const open = !S.board.collapsed[id];
    return html`<div>
      <button class=${"fold" + (open ? " open" : "")} aria-expanded=${open ? "true" : "false"} onClick=${() => { S.board.collapsed[id] = open; EV.log("fold", { id, open: !open }); EV.update(); }}>
        <span class="lbl">${mark || null}${label} <span class="cnt">${n}</span></span><span class="chev">${I.chevR()}</span>
      </button>
      ${open ? children : null}
    </div>`;
  }

  function orderedLive() {
    const list = EV.liveOrder();
    if (EV.S.interacting && frozen) {
      const ids = new Set(list.map((s) => s.id));
      const keep = frozen.filter((id) => ids.has(id));
      const extra = list.filter((s) => !keep.includes(s.id)).map((s) => s.id);
      return keep.concat(extra).map((id) => EV.sess(id));
    }
    frozen = list.map((s) => s.id);
    return list;
  }

  function LiveSection() {
    const all = orderedLive();
    const needs = all.filter((s) => EV.band(s) === "needs");
    const your = all.filter((s) => EV.band(s) === "yourmove");
    const work = all.filter((s) => EV.band(s) === "working");
    const idle = all.filter((s) => EV.band(s) === "idle");
    // The whole fleet in one line, so the first screen says what's working
    // even when Needs you fills it. Each count jumps to its band.
    const go = (k, band) => { EV.log("jump", { to: k, from: "summary" }); if (k === "idle") EV.S.board.collapsed.idle = false; EV.S.board.jump = band; EV.update(); };
    const sum = [
      needs.length ? html`<button class="ls needs" onClick=${() => go("needs", "needs")}><b>${needs.length}</b> need you</button>` : null,
      your.length ? html`<button class="ls" onClick=${() => go("finished", "your")}><b>${your.length}</b> finished</button>` : null,
      work.length ? html`<button class="ls work" onClick=${() => go("working", "work")}>${h(EV.Pulse, { values: EV.fleetPulse(), off: EV.S.conn !== "live" })}<b>${work.length}</b> working</button>` : null,
      idle.length ? html`<button class="ls" onClick=${() => go("idle", "idle")}><b>${idle.length}</b> idle</button>` : null,
    ].filter(Boolean);
    return html`<section aria-label="Live">
      <div id="sec-live"></div>
      ${sum.length > 1 ? html`<div class="live-sum" role="navigation" aria-label="Live sessions by state">${sum}</div>` : null}
      ${needs.length ? html`<${SectionHead} id="band-needs" label="Needs you" n=${needs.length} />${needs.map((s) => html`<${EV.BoardRow} key=${s.id} s=${s} context="needs" />`)}` : null}
      ${your.length ? html`<${SectionHead} id="band-your" label="Finished" n=${your.length} />${your.map((s) => html`<${EV.BoardRow} key=${s.id} s=${s} context="yourmove" />`)}` : null}
      ${work.length ? html`<${SectionHead} id="band-work" label="Working" n=${work.length} />${work.map((s) => html`<${EV.BoardRow} key=${s.id} s=${s} context="working" />`)}` : null}
      ${idle.length ? html`<div style="margin-top:8px" id="band-idle"><${Fold} id="idle" label="Idle" n=${idle.length}>${idle.map((s) => html`<${EV.BoardRow} key=${s.id} s=${s} quiet=${true} context="idle" />`)}</${Fold}></div>` : null}
      ${!all.length ? html`<div class="empty"><b>Nothing's running</b>Start a session to put an agent to work.<div style="margin-top:14px"><button class="btn primary" onClick=${() => EV.openNew("empty")}>New session</button></div></div>` : null}
    </section>`;
  }

  // Pinned sessions live in the user's named categories. Each category is a
  // section of its own (as on the web's rail), never one generic "Pinned" bucket.
  function CategorySections() {
    const S = EV.S;
    return S.categories.map((c) => {
      const list = S.sessions.filter((x) => x.category === c.id && !x.archived);
      const id = "cat:" + c.id;
      const open = !S.board.collapsed[id];
      return html`<section aria-label=${c.name} key=${c.id} id=${"sec-cat-" + c.id}>
        <div class="sec-h big cat-h">
          <button class="h-left cat-toggle" aria-expanded=${open ? "true" : "false"} onClick=${() => { S.board.collapsed[id] = open; EV.log("fold", { id, open: !open }); EV.update(); }}>
            <span class="cat-pin">${I.pin({ s: 13 })}</span>${c.name}<span class="n">${list.length}</span><span class=${"cat-chev" + (open ? " open" : "")}>${I.chevR({ s: 11 })}</span>
          </button>
          <button class="h-act" aria-label=${"Category actions for " + c.name} onClick=${() => EV.categoryMenu(c)}>${I.dots({ s: 18 })}</button>
        </div>
        ${open ? (list.length ? list.map((x) => html`<${EV.BoardRow} key=${x.id} s=${x} quiet=${true} context=${"category:" + c.name} />`) : html`<div class="fold" style="cursor:default;font-size:14px;color:var(--ink-low)">No sessions pinned here yet. Touch and hold a session and choose Pin to category.</div>`) : null}
      </section>`;
    });
  }

  EV.categoryMenu = function (c) {
    EV.openMenu({ kind: "list", top: 300, title: c.name, preview: html`<div class="preview"><div class="pt">${c.name}</div><div class="pw">Category of pinned sessions</div></div>`, items: [
      { label: "Rename", icon: I.compose({ s: 18 }), run: () => EV.openSheet("renameCategory", { categoryId: c.id }) },
      { label: "Delete category", danger: true, icon: I.x({ s: 18 }), run: () => {
        const pinned = EV.S.sessions.filter((s) => s.category === c.id);
        pinned.forEach((s) => { s.category = null; });
        EV.S.categories = EV.S.categories.filter((x) => x.id !== c.id);
        EV.log("category_delete", { name: c.name, unpinned: pinned.length });
        EV.toast("Deleted " + c.name + "; sessions unpinned");
        EV.update();
      } },
    ] });
  };

  function projSessions(pid, hostId) {
    return EV.S.sessions.filter((s) => s.project === pid && !s.test && (!hostId || s.host === hostId));
  }
  function liveN(list) { return list.filter((s) => s.live && !s.archived).length; }

  function SessionTiers({ list }) {
    const now = Date.now();
    const act = list.filter((s) => !s.archived);
    const today = act.filter((s) => now - s.updatedAt < 24 * 3600e3).sort((a, b) => b.updatedAt - a.updatedAt);
    const recent = act.filter((s) => now - s.updatedAt >= 24 * 3600e3).sort((a, b) => b.updatedAt - a.updatedAt);
    const arch = list.filter((s) => s.archived);
    return html`<div>
      ${today.length ? html`<div class="tier">Today</div>${today.map((s) => html`<${EV.BoardRow} key=${s.id} s=${s} quiet=${true} context="projects" />`)}` : null}
      ${recent.length ? html`<div class="tier">Recent</div>${recent.map((s) => html`<${EV.BoardRow} key=${s.id} s=${s} quiet=${true} context="projects" />`)}` : null}
      ${arch.length ? html`<div class="tier">Archived · ${arch.length}</div>${arch.map((s) => html`<${EV.BoardRow} key=${s.id} s=${s} quiet=${true} context="projects" />`)}` : null}
    </div>`;
  }

  function TreeHead({ id, level, name, sub, count, pinned, dot, onLong }) {
    const S = EV.S;
    const open = !!S.board.open[id];
    const lp = EV.useLongPress(() => onLong && onLong(), () => { S.board.open[id] = !open; EV.log("tree_toggle", { id, open: !open }); EV.update(); });
    return html`<button class=${"tree-h" + (level === 2 ? " l2" : "") + (open ? " open" : "")} aria-expanded=${open ? "true" : "false"} ...${lp}>
      <span style="display:flex;justify-content:center;color:var(--ink-mid)">${dot || (level === 2 ? I.host({ s: 16 }) : I.folder({ s: 18 }))}</span>
      <span style="min-width:0"><span class="nm">${pinned ? html`<span style="color:var(--ink-low);display:flex">${I.pin({ s: 14 })}</span>` : null}${name}</span>${sub ? html`<span class="sub">${sub}</span>` : null}</span>
      <span class="ct">${count}</span>
      <span class="chev">${I.chevR()}</span>
    </button>`;
  }

  function ProjectsSection() {
    const S = EV.S;
    const byHost = S.board.organize === "host";
    const projs = S.projects.filter((p) => projSessions(p.id).length).sort((a, b) => (b.pinned ? 1 : 0) - (a.pinned ? 1 : 0) || Math.max(...projSessions(b.id).map((s) => s.updatedAt)) - Math.max(...projSessions(a.id).map((s) => s.updatedAt)));
    const toggle = EV.multiHost() ? html`<button class="h-act" onClick=${() => EV.openMenu({ kind: "list", top: 360, title: "Organize by", preview: html`<div class="preview"><div class="pt">Organize by</div><div class="pw">How the Projects section nests sessions</div></div>`, items: [
      { label: "Project, then host", checked: !byHost, run: () => { S.board.organize = "project"; EV.log("organize", { mode: "project" }); EV.update(); } },
      { label: "Host, then project", checked: byHost, run: () => { S.board.organize = "host"; EV.log("organize", { mode: "host" }); EV.update(); } },
    ] })}>${byHost ? "Host, then project" : "Project, then host"} ${I.chevD({ s: 12 })}</button>` : null;
    const projMenu = (p) => EV.openMenu({ kind: "list", top: 300, title: p.id, preview: html`<div class="preview"><div class="pt">${p.id}</div><div class="pw" style="font-family:var(--mono);font-size:13px">~/${p.path}</div></div>`, items: [
      { label: p.pinned ? "Unpin project" : "Pin project to top", icon: I.pin({ s: 18 }), run: () => { p.pinned = !p.pinned; EV.log(p.pinned ? "pin_project" : "unpin_project", { project: p.id }); EV.update(); } },
      { label: "New session here", icon: I.compose({ s: 18 }), run: () => EV.openNew("project_menu", { project: p.id }) },
      { label: "Archive project", icon: I.archive({ s: 18 }), run: () => { projSessions(p.id).forEach((s) => { s.archived = true; }); EV.log("archive_project", { project: p.id }); EV.toast("Archived " + p.id); EV.update(); } },
    ] });
    let body;
    if (!byHost) {
      body = projs.map((p) => {
        const list = projSessions(p.id);
        const pid = "proj:" + p.id;
        const hostsWith = S.hosts.filter((x) => list.some((s) => s.host === x.id));
        return html`<div key=${p.id}>
          <${TreeHead} id=${pid} name=${p.id} count=${liveN(list) ? liveN(list) + " live" : ""} pinned=${p.pinned} onLong=${() => projMenu(p)} />
          ${S.board.open[pid] ? (EV.multiHost() ? hostsWith.map((x) => {
            const hid = "host:" + p.id + ":" + x.id;
            const hl = list.filter((s) => s.host === x.id);
            return html`<div key=${hid}><${TreeHead} id=${hid} level=${2} name=${x.id} count=${x.state === "offline" ? html`<span class="tag amber">Offline</span>` : liveN(hl) ? liveN(hl) + " live" : ""} />
              ${S.board.open[hid] ? html`<div class="subrow">${h(SessionTiers, { list: hl })}</div>` : null}</div>`;
          }) : h(SessionTiers, { list })) : null}
        </div>`;
      });
    } else {
      body = S.hosts.map((x) => {
        const hid = "host:" + x.id;
        const hl = S.sessions.filter((s) => s.host === x.id && !s.test);
        const hp = projs.filter((p) => hl.some((s) => s.project === p.id));
        return html`<div key=${hid}>
          <${TreeHead} id=${hid} name=${x.id} sub=${x.os} count=${x.state === "offline" ? html`<span class="tag amber">Offline</span>` : liveN(hl) ? liveN(hl) + " live" : ""} dot=${I.host({ s: 18 })} />
          ${S.board.open[hid] ? hp.map((p) => {
            const pid = "hp:" + x.id + ":" + p.id;
            const pl = hl.filter((s) => s.project === p.id);
            return html`<div key=${pid}><${TreeHead} id=${pid} level=${2} name=${p.id} count=${liveN(pl) ? liveN(pl) + " live" : ""} pinned=${p.pinned} dot=${I.folder({ s: 16 })} onLong=${() => projMenu(p)} />
              ${S.board.open[pid] ? html`<div class="subrow">${h(SessionTiers, { list: pl })}</div>` : null}</div>`;
          }) : null}
        </div>`;
      });
    }
    return html`<section aria-label=${byHost ? "Hosts" : "Projects"}>
      <${SectionHead} id="sec-projects" label=${byHost ? "Hosts" : "Projects"} big=${true} action=${toggle} />
      ${body}
    </section>`;
  }

  function TestRuns() {
    const list = EV.S.sessions.filter((s) => s.test);
    if (!list.length) return null;
    return html`<div style="margin-top:10px;border-top:0.5px solid var(--edge)"><${Fold} id="testruns" label="Test runs" n=${list.length}>${list.map((s) => html`<${EV.BoardRow} key=${s.id} s=${s} quiet=${true} context="testruns" />`)}</${Fold}></div>`;
  }

  function Archived() {
    const S = EV.S;
    const list = S.sessions.filter((s) => s.archived).sort((a, b) => b.updatedAt - a.updatedAt);
    const extra = S.archivedTotal - 5;
    return html`<div id="sec-archived" style="border-top:0.5px solid var(--edge)"><${Fold} id="archived" label="Archived" n=${list.length + extra}>
      ${list.map((s) => html`<${EV.BoardRow} key=${s.id} s=${s} quiet=${true} context="archived" />`)}
      <div class="device-note" style="text-align:left;padding:10px 16px 16px">${extra} older archived sessions. Search finds them.</div>
    </${Fold}></div>`;
  }

  // ---------- search ----------
  function Search() {
    const S = EV.S;
    const q = S.board.query.trim().toLowerCase();
    const inputRef = useRef(null);
    useEffect(() => { inputRef.current && inputRef.current.focus(); }, []);
    let sessions = [], hits = [], projs = [];
    if (q) {
      const words = q.split(/\s+/);
      const match = (txt) => words.every((w) => (txt || "").toLowerCase().includes(w));
      const scopeOk = (s) => S.board.scope === "all" || (S.board.scope === "live" ? s.live && !s.archived : s.archived);
      sessions = S.sessions.filter((s) => !s.test && scopeOk(s) && match(s.title + " " + (s.why || "")));
      for (const x of S.search) {
        const s = EV.sess(x.id);
        if (!s || !scopeOk(s)) continue;
        if (x.kind === "session" && match(x.title + " " + x.prompt) && !sessions.includes(s)) sessions.push(s);
        if (x.kind === "hit" && match(x.snippet)) hits.push(x);
      }
      projs = S.projects.filter((p) => match(p.id));
    }
    const onInput = (e) => {
      S.board.query = e.currentTarget.value;
      clearTimeout(Search.t);
      Search.t = setTimeout(() => EV.log("search", { query: S.board.query, scope: S.board.scope }), 500);
      EV.update();
    };
    const openHit = (x) => {
      EV.log("search_open", { sessionId: x.id, kind: x.kind, query: S.board.query });
      const s = EV.sess(x.id);
      S.board.searching = false;
      EV.push("session", { id: x.id, from: "search", highlight: x.kind === "hit" ? x.snippet : null });
      if (s) EV.markSeen(s);
    };
    const hl = (text) => {
      const w = q.split(/\s+/)[0];
      const i = w ? text.toLowerCase().indexOf(w) : -1;
      if (i < 0) return text;
      return html`${text.slice(0, i)}<mark class="hit">${text.slice(i, i + w.length)}</mark>${text.slice(i + w.length)}`;
    };
    const around = (text) => {
      const w = q.split(/\s+/)[0];
      const i = w ? text.toLowerCase().indexOf(w) : -1;
      return i > 60 ? "…" + text.slice(text.lastIndexOf(" ", i - 40) + 1) : text;
    };
    return html`<div style="display:flex;flex-direction:column;height:100%">
      <div class="nav"><div style="display:flex;align-items:center;gap:4px;padding:6px 8px 0 0">
        <div class="search-field" style="flex:1;margin-right:0">${I.search({ s: 17 })}<input ref=${inputRef} id="board-search" type="search" placeholder="Search sessions and messages" value=${S.board.query} onInput=${onInput} aria-label="Search sessions" /></div>
        <button class="text-btn" onClick=${() => { S.board.searching = false; S.board.query = ""; EV.log("search_cancel", {}); EV.update(); }}>Cancel</button>
      </div>
      <div class="chips" style="position:static;background:none;padding-top:2px">${[["all", "All"], ["live", "Live"], ["archived", "Archived"]].map(([k, l]) => html`<button class=${"chip" + (S.board.scope === k ? " on" : "")} onClick=${() => { S.board.scope = k; EV.update(); }}>${l}</button>`)}</div></div>
      <div class="scroll">
        ${!q ? html`<div class="sec-h">Recent searches</div>${["retry loop", "settle race"].map((r) => html`<button class="fold" onClick=${() => { S.board.query = r; EV.log("search", { query: r, recent: true }); EV.update(); }}><span class="lbl" style="color:var(--ink-hi)">${I.search({ s: 15 })} ${r}</span></button>`)}` : null}
        ${q && !sessions.length && !hits.length && !projs.length ? html`<div class="empty"><b>No matches</b>Nothing in live or archived sessions matches “${S.board.query}”.</div>` : null}
        ${sessions.length ? html`<div class="sec-h">Sessions <span class="n">${sessions.length}</span></div>${sessions.map((s) => h(EV.SwipeRow, { key: "s" + s.id, onTap: () => openHit({ id: s.id, kind: "session" }) }, html`<div class="row"><div class="mark">${h(EV.Mark, { s })}</div><div style="min-width:0"><div class="l1"><span class="title">${hl(s.title)}</span><span class="age">${EV.fmtAgo(Date.now() - s.updatedAt)}</span></div><div class="meta"><span>${s.archived ? "Archived" : s.live ? "Live" : "Shut down"}</span><span class="sep"></span><span>${s.project}</span></div></div></div>`))}` : null}
        ${hits.length ? html`<div class="sec-h">In sessions <span class="n">${hits.length}</span></div>${hits.map((x) => h(EV.SwipeRow, { key: "h" + x.id + x.snippet.length, onTap: () => openHit(x) }, html`<div class="row"><div class="mark"><span class="mk low">${I.bubble({ s: 17 })}</span></div><div style="min-width:0"><div class="l1"><span class="title" style="font-size:15px">${x.title}</span><span class="age">${EV.fmtAgo(x.ago * 1000)}</span></div><div class="why two" style="font-family:var(--read-font)">${hl(around(x.snippet))}</div></div></div>`))}` : null}
        ${projs.length ? html`<div class="sec-h">Projects</div>${projs.map((p) => html`<button class="fold" onClick=${() => { S.board.searching = false; S.board.open["proj:" + p.id] = true; S.board.jump = "projects"; EV.update(); }}><span class="lbl" style="color:var(--ink-hi)">${I.folder({ s: 16 })} ${p.id}</span><span class="chev">${I.chevR()}</span></button>`)}` : null}
      </div>
    </div>`;
  }

  // ---------- board screen ----------
  function Board({ top }) {
    const S = EV.S;
    const scrollRef = useRef(null);
    useEffect(() => {
      if (!S.board.jump || !scrollRef.current) return;
      const target = S.board.jump.startsWith("cat:") ? "sec-cat-" + S.board.jump.slice(4) : { live: "sec-live", needs: "band-needs", your: "band-your", work: "band-work", idle: "band-idle", projects: "sec-projects", archived: "sec-archived" }[S.board.jump];
      S.board.jump = null;
      const el = target && scrollRef.current.querySelector("#" + target);
      if (el) scrollRef.current.scrollTo({ top: Math.max(0, el.offsetTop - 50), behavior: "smooth" });
    });
    if (S.board.searching) return h(Search);
    const hub = EV.host("magic-kingdom");
    const needs = EV.needsCount();
    const live = S.sessions.filter((s) => s.live && !s.archived && !s.test).length;
    const projN = S.projects.filter((p) => projSessions(p.id).length).length;
    const archN = S.sessions.filter((s) => s.archived).length + S.archivedTotal - 5;
    const jump = (k) => { EV.log("jump", { to: k }); S.board.jump = k; EV.update(); };
    const conn = S.conn;
    const selN = Object.values(S.board.selected).filter(Boolean).length;
    const ns = notices();
    return html`<div style="display:flex;flex-direction:column;height:100%">
      <div class="nav"><div class="nav-row board-nav">
        <div class="lead"><button class="hub-btn" onClick=${() => EV.openSheet("hub", {})} aria-label=${"Hub: magic-kingdom, " + (conn === "live" ? "connected" : conn === "reconnecting" ? "reconnecting" : "offline")}>
          magic-kingdom<span style="color:var(--ink-low);display:flex">${I.chevD({ s: 12 })}</span></button></div>
        <div class="trail"><button class="icon-btn" aria-label="Search" onClick=${() => { S.board.searching = true; EV.log("search_open_field", {}); EV.update(); }}>${I.search()}</button></div>
      </div></div>
      <div class="scroll" ref=${scrollRef}>
        <div class="chips" role="navigation" aria-label="Sections">
          <button class="chip" onClick=${() => jump("live")}>Live <span class="n">${live}</span>${needs ? html`<span class="badge" aria-label=${needs + " need you"}>${needs}</span>` : null}</button>
          ${S.categories.map((c) => html`<button class="chip" key=${c.id} onClick=${() => jump("cat:" + c.id)}><span style="display:flex;color:var(--ink-mid)">${I.pin({ s: 12 })}</span>${c.name} <span class="n">${S.sessions.filter((x) => x.category === c.id && !x.archived).length}</span></button>`)}
          <button class="chip" onClick=${() => jump("projects")}>Projects <span class="n">${projN}</span></button>
          <button class="chip" onClick=${() => { S.board.collapsed.archived = false; jump("archived"); }}>Archived <span class="n">${archN}</span></button>
        </div>
        ${ns.map((n) => html`<div class="notice" key=${n.id} role="button" tabindex="0" style="cursor:pointer" onClick=${() => { EV.log("notice_open", { notice: n.id }); n.open(); }}><span class="ic">${I.warn({ s: 18 })}</span><span class="txt">${n.text}</span><button class="act" onClick=${(e) => { e.stopPropagation(); n.run(); }}>${n.act}</button></div>`)}
        ${S.lastRead && Date.now() - S.lastRead.at < 2 * 3600e3 ? html`<button class="continue" onClick=${() => {
          const r = S.lastRead;
          EV.log("continue_reading", { path: r.path });
          S.nav = [S.nav[0], { name: "session", id: r.sessionId, key: ++S.navSeq, from: "continue" }, { name: "reader", path: r.path, sessionId: r.sessionId, key: ++S.navSeq }];
          S.navAnim = { type: "push", key: S.navSeq, until: Date.now() + 330 };
          EV.onScreenChange();
          EV.update();
        }}>
          <span style="display:flex;color:var(--accent)">${I.doc({ s: 18 })}</span>
          <span style="min-width:0"><span class="k">Continue reading · ${Math.round(S.lastRead.pct * 100)}%</span><span class="t">${S.lastRead.title}</span></span>
          <span style="display:flex;color:var(--ink-low)">${I.chevR()}</span>
        </button>` : null}
        ${h(LiveSection)}
        ${h(CategorySections)}
        ${h(ProjectsSection)}
        ${h(TestRuns)}
        ${h(Archived)}
        <div style="height:24px"></div>
      </div>
      <div class="toolbar"><div class="toolbar-row">
        ${S.board.selecting
          ? html`<button class="text-btn" style="justify-self:start" onClick=${() => { S.board.selecting = false; S.board.selected = {}; EV.update(); }}>Done</button>
                 <span class="status">${selN} selected</span>
                 <button class="text-btn strong" style="justify-self:end" disabled=${!selN} onClick=${() => EV.bulkMenu()}>Actions</button>`
          : html`<button class="text-btn" style="justify-self:start" onClick=${() => { S.board.selecting = true; EV.log("select_mode", {}); EV.update(); }}>Select</button>
                 <span class="status">${conn === "live" ? null : conn === "reconnecting" ? html`<span class="conn-dot reconnecting"></span>Reconnecting…` : html`<span class="conn-dot offline"></span>Offline · updated ${EV.fmtAgo(Date.now() - S.connSince)} ago`}</span>
                 <span style="justify-self:end"><button class="icon-btn compose-btn" aria-label="New session" onClick=${() => EV.openNew("board")}>${I.compose({ s: 24 })}</button></span>`}
      </div></div>
    </div>`;
  }

  EV.bulkMenu = function () {
    const S = EV.S;
    const ids = Object.keys(S.board.selected).filter((k) => S.board.selected[k]);
    const done = () => { S.board.selecting = false; S.board.selected = {}; EV.update(); };
    EV.openMenu({ kind: "list", top: 420, title: "Selected sessions", preview: html`<div class="preview"><div class="pt">${ids.length} selected</div></div>`, items: [
      { label: "Archive", icon: I.archive({ s: 18 }), run: () => { ids.forEach((id) => { EV.sess(id).archived = true; }); EV.log("bulk_archive", { ids }); EV.toast("Archived " + ids.length, () => { ids.forEach((id) => { EV.sess(id).archived = false; }); EV.update(); }); done(); } },
      { label: "Mark as read", icon: I.check({ s: 18 }), run: () => { ids.forEach((id) => { const s = EV.sess(id); if (s.state === "yourmove") s.unseen = false; }); EV.log("bulk_read", { ids }); done(); } },
      { label: "Pin to Release", icon: I.pin({ s: 18 }), run: () => { ids.forEach((id) => { EV.sess(id).category = "release"; }); EV.log("bulk_pin", { ids, category: "Release" }); EV.toast("Pinned " + ids.length + " to Release"); done(); } },
    ] });
  };

  EV.screens.board = Board;
})();
