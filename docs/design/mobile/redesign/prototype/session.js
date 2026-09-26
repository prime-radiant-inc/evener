// Session: the workbench. Nav with lateral swipe, context chips, the
// transcript at five detail levels, the status tray with Next, the ask dock
// for questions and approvals, and the steer/queue composer. Also the
// session-level sheets (session info, model, commands, queue, tasks, goal,
// notes, find, aside, rename, categories).
(function () {
  const EV = window.EV;
  const { html, h, useRef, useEffect, useLayoutEffect, useState } = EV;
  const I = EV.I;
  const LEVELS = ["Chat", "Intent", "Tools", "Activity", "Full"];
  const mdCache = new Map();
  EV.md = function (src) {
    if (!mdCache.has(src)) mdCache.set(src, marked.parse(src, { gfm: true }));
    return mdCache.get(src);
  };
  EV.level = (id) => EV.S.prefs.detail[id] || EV.S.prefs.defaultDetail;
  // Plain prose from markdown for previews: drop tables, code and markup.
  EV.plain = function (md, max) {
    const text = md.split(/\n{2,}/).filter((p) => !/^\s*(\||```|    )/.test(p)).map((p) => p.replace(/^#+\s*/, "").replace(/[*_`>]/g, "").replace(/\s+/g, " ").trim()).filter(Boolean).join(" ");
    return max && text.length > max ? text.slice(0, max).replace(/\s+\S*$/, "") + "…" : text;
  };
  // A document's own title (its first heading), falling back to the file name.
  EV.docTitle = function (path) {
    const d = EV.S.docs[path];
    const m = d && /^#\s+(.+)$/m.exec(d.md);
    return m ? m[1].trim() : path.split("/").pop();
  };
  const lvl = (id) => LEVELS.indexOf(EV.level(id));

  // ---------- transcript items ----------
  function actSummary(steps) {
    const fails = steps.filter((x) => x.s === "fail").length;
    const names = steps.map((x) => x.i.charAt(0).toLowerCase() + x.i.slice(1));
    let txt = names.slice(0, 3).join(", ");
    if (names.length > 3) txt += ", and " + (names.length - 3) + " more";
    return { n: steps.length, txt, fails };
  }

  function Evidence({ st }) {
    const S = EV.S;
    if (st.diff) {
      const d = st.diff;
      return html`<div class="diff"><div class="dh"><span style="font-family:var(--mono)">${d.file}</span><span><span style="color:var(--alive-ink)">+${d.add}</span> <span style="color:var(--danger-ink)">−${d.del}</span></span></div>
        ${(d.lines || []).map((l) => html`<div class=${"dl " + (l[0] === "add" ? "add" : l[0] === "del" ? "del" : "")}>${(l[0] === "add" ? "+ " : l[0] === "del" ? "− " : "  ") + l[1]}</div>`)}
        ${!d.lines ? html`<div class="dl" style="color:var(--ink-mid);font-family:var(--sans);padding:6px 10px">${d.add + d.del} changed lines</div>` : null}</div>`;
    }
    if (st.out) return html`<div class="evidence">${st.out.split("\n").map((l) => html`<div class=${/FAIL|fail|error|401/.test(l) ? "fail" : ""}>${l}</div>`)}${st.out.split("\n").length > 3 ? html`<button class="more" onClick=${() => EV.toast("Opens the full output (412 lines)")}>Show all output</button>` : null}</div>`;
    return html`<div class="evidence" style="font-family:var(--sans);color:var(--ink-mid)">No output</div>`;
  }

  function Act({ it, sid, idx }) {
    const S = EV.S;
    const L = lvl(sid);
    const key = sid + ":" + idx;
    const defOpen = it.live || L >= 2;
    const open = S.expanded[key] != null ? S.expanded[key] : defOpen;
    const sum = actSummary(it.steps);
    const toggle = () => { S.expanded[key] = !open; EV.log("activity_toggle", { sessionId: sid, index: idx, open: !open }); EV.update(); };
    return html`<div class=${"act" + (open ? " open" : "")}>
      <button class="act-h" onClick=${toggle} aria-expanded=${open ? "true" : "false"}>
        <span class="chev">${I.chevR({ s: 12 })}</span>
        <span>${it.live ? html`<span style="color:var(--alive-ink);font-weight:600">Working</span> · ` : null}${sum.n} step${sum.n === 1 ? "" : "s"} · ${sum.txt}${sum.fails ? html` <span class="bad">(${sum.fails} failed)</span>` : null}</span>
      </button>
      ${open ? html`<div class="steps">${it.steps.map((st, j) => {
        const ek = key + ":" + j;
        const eopen = S.expanded[ek] != null ? S.expanded[ek] : L >= 4;
        return html`<div>
          <button class="step" onClick=${() => { S.expanded[ek] = !eopen; EV.log("evidence_toggle", { sessionId: sid, step: st.i, open: !eopen }); EV.update(); }}>
            <span class="st">${st.s === "fail" ? html`<span class="mk danger" style="width:14px;height:14px">${I.failed({ s: 13 })}</span>` : st.s === "run" ? h(EV.Pulse, { values: [0.3, 0.6, 0.4, 0.9, 0.5, 0.8, 0.7] }) : html`<span style="color:var(--ink-low);display:flex">${I.check({ s: 13 })}</span>`}</span>
            <span><span class="in">${st.i}</span>${st.g ? html`<br /><span class="tg">${st.g}</span>` : null}</span>
          </button>
          ${eopen && (st.out || st.diff) ? h(Evidence, { st }) : null}
        </div>`;
      })}</div>` : null}
    </div>`;
  }

  function UserMsg({ it, s }) {
    const lp = EV.useLongPress(() => EV.openMenu({ kind: "list", top: 250, title: "Message", preview: html`<div class="preview"><div class="pw" style="color:var(--ink-hi)">${it.text}</div></div>`, items: [
      { label: "Copy", icon: I.doc({ s: 18 }), run: () => EV.toast("Copied") },
      { label: "Quote in reply", icon: I.quote({ s: 18 }), run: () => EV.quoteIntoDraft(s.id, it.text) },
      { label: "Fork from here", icon: I.branch({ s: 18 }), run: () => EV.fork(s, it.text) },
    ] }));
    return html`<div class="u-msg" ...${lp}><div class="u-bubble">${it.text}</div>${it.kind ? html`<div class="u-cap">${{ steer: "Steered", queue: "Queued", answer: "Answer", review: "Review", artifact: "From the artifact" }[it.kind] || ""}</div>` : null}</div>`;
  }

  function AgentMsg({ it, s }) {
    const lp = EV.useLongPress(() => EV.openMenu({ kind: "list", top: 250, title: "Message", preview: html`<div class="preview"><div class="px" style="border:0;margin:0;padding:0">${EV.plain(it.md, 260)}</div></div>`, items: [
      { label: "Copy", icon: I.doc({ s: 18 }), run: () => EV.toast("Copied") },
      { label: "Quote in reply", icon: I.quote({ s: 18 }), run: () => EV.quoteIntoDraft(s.id, it.md.replace(/[*#`]/g, "").split("\n")[0]) },
    ] }));
    return html`<div class="a-msg" ...${lp} dangerouslySetInnerHTML=${{ __html: EV.md(it.md) }}></div>`;
  }

  function Item({ it, s, idx }) {
    const S = EV.S;
    const L = lvl(s.id);
    switch (it.t) {
      case "time": return html`<div class="tmark">${it.label}</div>`;
      case "user": return h(UserMsg, { it, s });
      case "agent": return h(AgentMsg, { it, s });
      case "act": return h(Act, { it, sid: s.id, idx });
      case "think": return it.live && s.state === "working" ? html`<div class="think live">${I.diamond({ s: 12 })} Thinking… · ~1.2K tokens</div>` : html`<div class="think">${I.diamond({ s: 12 })} Thought for ${it.secs || 12}s</div>`;
      case "sub": {
        const g = EV.findSub(s.id, it.id) || it;
        const cls = { running: "run", failed: "fail", done: "done", waiting: "wait" }[g.state] || "done";
        const label = { running: "running", failed: "failed", done: "done", waiting: "waiting" }[g.state] || g.state;
        return html`<button class="sub-item" onClick=${() => { EV.log("subagent_open", { sessionId: s.id, subagentId: it.id, from: "transcript" }); EV.push("subagent", { sessionId: s.id, subId: it.id }); }}>
          <span style="padding-top:2px;color:var(--ink-mid);display:flex">${I.people({ s: 17 })}</span>
          <span style="min-width:0"><span class="nm">${g.title}</span><br /><span class="ln">${g.line || it.line}</span></span>
          <span class=${"pill " + cls}>${label}</span>
        </button>`;
      }
      case "doc": {
        const d = S.docs[it.path] || { lines: 0, ago: 0, changed: [] };
        const changed = d.changed && d.changed.length && !S.readDocs[it.path];
        return html`<button class="doc-chip" onClick=${() => { EV.log("doc_open", { sessionId: s.id, path: it.path, from: "transcript" }); EV.push("reader", { path: it.path, sessionId: s.id }); }}>
          <span class="ic">${I.doc({ s: 20 })}</span>
          <span style="min-width:0"><span class="k">${it.kind}${changed ? html` · <span style="color:var(--accent-ink)">changed since you last read</span>` : null}</span><br /><span class="t">${EV.docTitle(it.path)}</span><br /><span class="m">${it.path.split("/").pop()} · ${d.lines} lines · ${EV.fmtAgo(d.ago * 1000)} ago</span></span>
          <span style="display:flex;gap:6px;align-items:center">${changed ? html`<span class="dot"></span>` : null}<span style="color:var(--ink-low);display:flex">${I.chevR()}</span></span>
        </button>`;
      }
      case "art": {
        const a = S.artifacts[it.id];
        if (!a) return null;
        const st = S.artifactState[a.id];
        return html`<button class="art-card" onClick=${() => { EV.log("artifact_open", { sessionId: s.id, id: a.id, from: "transcript" }); EV.push("artifact", { id: a.id, sessionId: s.id }); }}>
          <div class="pv">${h(ArtPreview, { pick: (st && (st.sent || st.selected)) || "B" })}</div>
          <div class="bd"><div class="nm">${a.title}</div><div class="sm">${a.summary}</div>
          <div class="ft"><span>Artifact · v${a.version}${st && st.sent ? " · you chose " + st.sent : ""}</span><span class="open">Open</span></div></div>
        </button>`;
      }
      case "noteSet": return html`<div class="note-set"><div class="ns-l">${I.person({ s: 12 })} You updated your note</div>${it.text ? html`<div class="ns-t">${it.text}</div>` : html`<div class="ns-t dim">Cleared</div>`}</div>`;
      case "sys": return L >= 1 || /compacted|Model changed|Stopped/.test(it.text) ? html`<div class="sys">${I.diamond({ s: 12 })}<span>${it.text}</span></div>` : null;
      case "qhist": return html`<div class="q-hist">${it.qs.map((q, i) => html`<div style=${i ? "margin-top:8px" : ""}><div class="q">${q.q}</div><div class="a">You answered: ${q.a}</div></div>`)}</div>`;
      case "err": return html`<div class="err"><div class="e">${it.e}</div><div class="d">${it.d}</div>${it.done ? null : html`<div class="acts">${(it.acts || []).map((a) => a === "signin"
        ? html`<button class="btn" onClick=${() => { EV.log("error_action", { sessionId: s.id, action: "signin" }); EV.openSheet("signin", { provider: EV.model(s.model).provider, sessionId: s.id }); }}>${I.key({ s: 16 })} Sign in</button>`
        : a === "retry" ? html`<button class="btn primary" onClick=${() => EV.retry(s)}>${I.retry({ s: 16 })} Retry</button>`
        : html`<button class="btn primary" onClick=${() => EV.restart(s)}>${I.retry({ s: 16 })} Restart session</button>`)}</div>`}</div>`;
      case "ask": {
        const qs = S.asks[it.id] || [];
        return html`<div class="q-hist"><div class="q">${qs[0] ? qs[0].q : "Question"}</div><div class="a">${qs.length > 1 ? qs.length + " questions · " : ""}Answer below</div></div>`;
      }
      case "appr": {
        const a = S.approvals[it.id];
        return html`<div class="q-hist"><div class="q">Approval needed</div><div class="a">${a.what}: <span style="font-family:var(--mono);font-size:13px">${a.target}</span></div></div>`;
      }
      default: return null;
    }
  }

  function ArtPreview({ pick }) {
    return html`<div style="position:absolute;inset:12px;display:grid;grid-template-columns:repeat(3,1fr);gap:8px">
      ${["A", "B", "C"].map((k) => html`<div style=${"border-radius:8px;background:var(--surface);border:" + (k === pick ? "2px solid var(--accent)" : "1px solid var(--edge)") + ";padding:6px;display:flex;flex-direction:column;gap:4px"}>
        <div style="font:600 11px var(--sans);color:var(--ink-mid)">${k}</div>
        ${[0, 1, 2, 3].map((i) => html`<div style=${"height:5px;border-radius:3px;background:var(--edge);margin-left:" + (k === "A" ? (i % 2) * 8 : k === "C" ? (i ? 8 : 0) : 0) + "px;width:" + (70 - i * 8) + "%"}></div>`)}
      </div>`)}
    </div>`;
  }

  EV.findSub = function (sid, gid) {
    const walk = (arr) => { for (const g of arr || []) { if (g.id === gid) return g; const c = walk(g.children); if (c) return c; } return null; };
    return walk(EV.S.subagents[sid]);
  };

  // ---------- actions ----------
  EV.addItem = (sid, it) => { (EV.S.transcripts[sid] = EV.S.transcripts[sid] || []).push(it); };
  EV.setWorking = function (s, activity) {
    s.state = "working";
    s.live = true;
    s.stuck = false;
    s.unseen = false;
    s.activity = activity || "Thinking";
    s.updatedAt = Date.now();
    s.actAt = Date.now();
    s.pulse = [0.2, 0.4, 0.3, 0.6, 0.5, 0.8, 0.9];
  };
  EV.quoteIntoDraft = function (sid, text) {
    const q = "> " + text.trim().slice(0, 240).split("\n").join("\n> ") + "\n\n";
    EV.S.drafts[sid] = q + (EV.S.drafts[sid] || "");
    EV.log("quote", { sessionId: sid });
    const t = EV.top();
    if (!(t.name === "session" && t.id === sid)) EV.push("session", { id: sid, from: "quote" });
    EV.S.focusComposer = sid;
    EV.update();
  };
  EV.fork = function (s, text) {
    const id = "s-fork-" + Date.now();
    const ns = Object.assign({}, s, { id, title: s.title + " (fork)", state: "idle", unseen: false, category: null, updatedAt: Date.now(), startedAt: Date.now(), subs: null });
    EV.S.sessions.push(ns);
    EV.S.transcripts[id] = (EV.S.transcripts[s.id] || []).filter((x) => x.t !== "ask" && x.t !== "appr").slice(0, 3).concat([{ t: "sys", text: "Forked from “" + s.title + "”" }]);
    EV.S.drafts[id] = text || "";
    EV.log("fork", { from: s.id, to: id });
    EV.toast("Forked. Edit the message and send.");
    EV.openSession(id, { from: "fork" });
  };
  EV.retry = function (s) {
    const p = EV.S.providers.find((x) => x.id === EV.model(s.model).provider);
    const tr = EV.S.transcripts[s.id] || [];
    tr.forEach((x) => { if (x.t === "err") x.done = true; });
    EV.log("retry", { sessionId: s.id, providerOk: !p || p.status !== "expired" });
    if (p && p.status === "expired") {
      EV.addItem(s.id, { t: "err", e: p.id + " sign-in is still expired", d: "Sign in to " + p.id + " first, then retry.", acts: ["signin", "retry"] });
      EV.toast("Sign-in still expired");
      EV.update();
      return;
    }
    EV.setWorking(s, "Retrying: running go test ./llm/...");
    EV.addItem(s.id, { t: "sys", text: "Retried by you" });
    EV.addItem(s.id, { t: "act", live: true, steps: [{ i: "Re-ran the retry tests", g: "go test ./llm/... -run Retry", s: "run" }] });
    EV.toast("Retrying");
    EV.update();
  };
  EV.restart = function (s) {
    (EV.S.transcripts[s.id] || []).forEach((x) => { if (x.t === "err") x.done = true; });
    EV.setWorking(s, "Restarting on 0.9.412");
    EV.addItem(s.id, { t: "sys", text: "Restarted by you on the hub's version" });
    EV.log("restart", { sessionId: s.id });
    EV.toast("Restarting");
    EV.update();
  };

  EV.sendMessage = function (s, text, mode, kind) {
    const S = EV.S;
    text = text.trim();
    if (!text && !(S.images[s.id] || []).length) return;
    const imgs = (S.images[s.id] || []).length;
    S.images[s.id] = [];
    S.drafts[s.id] = "";
    const st = EV.stateOf(s);
    if (S.conn !== "live") {
      (S.outbox = S.outbox || []).push({ sid: s.id, text, mode });
      EV.log("send", { sessionId: s.id, mode: "outbox", text });
      EV.toast("Will send when you're back online");
      EV.update();
      return;
    }
    if (s.state === "question") {
      const ask = (S.transcripts[s.id] || []).find((x) => x.t === "ask");
      EV.resolveAsk(s, ask, [{ q: (S.asks[ask.id][0] || {}).q, a: text }], text, "typed");
      return;
    }
    if (st === "working" || st === "stuck") {
      if (mode === "queue") {
        (S.queue[s.id] = S.queue[s.id] || []).push({ id: "q" + Date.now(), text });
        EV.log("send", { sessionId: s.id, mode: "queue", text, images: imgs });
        EV.toast("Queued for when this turn ends");
      } else {
        S.steering[s.id] = { text, at: Date.now() };
        EV.log("send", { sessionId: s.id, mode: "steer", text, images: imgs });
        setTimeout(() => {
          if (!S.steering[s.id]) return;
          delete S.steering[s.id];
          EV.addItem(s.id, { t: "user", text, kind: "steer" });
          s.updatedAt = Date.now();
          EV.update();
          setTimeout(() => { EV.addItem(s.id, { t: "agent", md: "Got it. Adjusting course: " + text.charAt(0).toLowerCase() + text.slice(1).replace(/[.!]?$/, ".") }); s.updatedAt = Date.now(); EV.update(); }, 2600);
        }, 2200);
      }
      S.prefs.hintUses++;
      EV.update();
      return;
    }
    EV.addItem(s.id, { t: "user", text: text + (imgs ? "\n[" + imgs + " image" + (imgs > 1 ? "s" : "") + "]" : ""), kind });
    const resumed = s.state === "shutdown";
    EV.setWorking(s, resumed ? "Resuming" : "Thinking");
    EV.log("send", { sessionId: s.id, mode: resumed ? "resume" : "send", text, images: imgs, kind: kind || null });
    setTimeout(() => {
      EV.addItem(s.id, { t: "agent", md: EV.replyFor(s, text) });
      s.activity = "Working on it";
      s.updatedAt = Date.now();
      EV.update();
    }, 3000);
    EV.update();
  };
  EV.replyFor = function (s, text) {
    if (/^review of/i.test(text)) return "Thanks for the review. I'll work through your comments one at a time and update the plan.";
    if (/^go with layout/i.test(text)) return "Going with your choice. I'll update the plan to make it the default and start on step 1.";
    if (/stop subagent/i.test(text)) return "Stopping that subagent now.";
    return "On it. I'll start by reading the relevant code and report back.";
  };

  EV.resolveAsk = function (s, ask, qa, text, how) {
    const S = EV.S;
    const tr = S.transcripts[s.id];
    const i = tr.indexOf(ask);
    tr[i] = { t: "qhist", qs: qa };
    EV.addItem(s.id, { t: "user", text, kind: "answer" });
    EV.setWorking(s, "Thinking");
    s.why = "";
    EV.log("answer", { sessionId: s.id, askId: ask.id, answers: qa.map((x) => x.a), how });
    EV.toast("Answer sent");
    setTimeout(() => {
      const first = (qa[0] && qa[0].a) || "";
      EV.addItem(s.id, { t: "agent", md: /drop/i.test(first) ? "Dropping the implied options from all 14 descriptions. I'll start the next audit when that's done." : /keychain/i.test(first) ? "Keychain it is, with a file fallback on headless hosts." : "Thanks. Continuing with that." });
      s.activity = "Editing agent/internal/tool/definitions.go";
      s.updatedAt = Date.now();
      EV.update();
    }, 3200);
    EV.update();
  };

  // decision: true (this one action), "scope" (everything under the folder,
  // for this session) or false. "Allow once" really is one action, so a batch
  // job asks again for its next file; the scoped choice ends the prompts.
  EV.decideApproval = function (s, decision) {
    const S = EV.S;
    const tr = S.transcripts[s.id];
    const i = tr.findIndex((x) => x.t === "appr");
    const a = S.approvals[tr[i].id];
    const label = decision === "scope" ? "Allowed all writes in " + a.scope + " for this session" : decision ? "Allowed once: " + a.tool + " " + a.target : "Denied: " + a.tool + " " + a.target;
    tr[i] = { t: "sys", text: label };
    s.why = "";
    EV.log("approval", { sessionId: s.id, decision: decision === "scope" ? "allow_scope" : decision ? "allow" : "deny" });
    EV.toast(decision === "scope" ? "Allowed for " + a.scope : decision ? "Allowed once" : "Denied");
    if (decision === "scope" || !a.next) EV.setWorking(s, decision ? (a.after || "Continuing") : "Thinking");
    else {
      EV.setWorking(s, "Writing " + a.target);
      setTimeout(() => {
        if (s.state !== "working") return;
        const id = tr[i] && a.next ? a.next.id : null;
        if (!id) return;
        S.approvals[id] = Object.assign({}, a, a.next, { next: null });
        s.state = "approval";
        s.why = "Wants to write outside the workspace: " + a.scope;
        s.updatedAt = Date.now();
        EV.addItem(s.id, { t: "appr", id });
        EV.alert({ kind: "approval", sessionId: s.id, why: s.why });
        EV.update();
      }, 5000);
    }
    if (!decision) setTimeout(() => { EV.addItem(s.id, { t: "agent", md: "Understood. I'll write the mirror inside the workspace at `./mirror` instead." }); EV.update(); }, 2500);
    EV.update();
  };

  EV.goNext = function (fromId) {
    // Alerts that arrived while you were reading come first: that's what "new" promised.
    const heldIds = EV.S.held.map((a) => a.sessionId);
    const list = EV.needsOrder().filter((x) => x.id !== fromId).sort((a, b) => heldIds.includes(b.id) - heldIds.includes(a.id));
    EV.log("next", { from: fromId, to: list[0] ? list[0].id : null });
    if (!list.length) { EV.toast("Nothing else needs you"); return; }
    EV.S.held = [];
    EV.openSession(list[0].id, { lateral: true, from: "next" });
  };
  EV.goAdjacent = function (fromId, dir) {
    const order = EV.liveOrder().map((x) => x.id);
    const i = order.indexOf(fromId);
    const j = i < 0 ? 0 : i + dir;
    EV.log("lateral_swipe", { from: fromId, dir });
    if (j < 0 || j >= order.length) { EV.toast(dir > 0 ? "Last live session" : "First live session"); return; }
    EV.openSession(order[j], { lateral: true, from: "swipe" });
  };

  // ---------- ask dock ----------
  function AskDock({ s }) {
    const S = EV.S;
    const tr = S.transcripts[s.id] || [];
    if (s.state === "approval") {
      const it = tr.find((x) => x.t === "appr");
      if (!it) return null;
      const a = S.approvals[it.id];
      return html`<div class="dock appr" role="region" aria-label="Approval needed">
        <div class="dock-h"><span>Approval needed</span></div>
        <div class="dock-b">
          <div class="what">${a.what}</div>
          <div class="tgt">${a.tool}  ${a.target}</div>
          <div class="md">${a.explain || a.mode}</div>
        </div>
        <div class="appr-choices">
          ${a.scope ? html`<button class="choice" onClick=${() => EV.decideApproval(s, "scope")}><b>Allow all of <span class="mono">${a.scope}</span></b><small>For the rest of this session</small></button>` : null}
          <button class="choice" onClick=${() => EV.decideApproval(s, true)}><b>${a.scope ? "Allow this file only" : "Allow once"}</b><small>${a.scope ? "It will ask again for the next one" : "Just this action"}</small></button>
          <button class="choice deny" onClick=${() => EV.decideApproval(s, false)}><b>Deny</b></button>
        </div>
      </div>`;
    }
    const ask = tr.find((x) => x.t === "ask");
    if (!ask) return null;
    const qs = S.asks[ask.id] || [];
    const st = (S.answers[ask.id] = S.answers[ask.id] || { i: 0, sel: {} });
    const q = qs[st.i];
    if (S.dockMin[s.id]) {
      return html`<div class="dock"><button class="dock-min" onClick=${() => { S.dockMin[s.id] = false; EV.update(); }}><span>Answer ${qs.length} question${qs.length > 1 ? "s" : ""}</span>${I.chevD({ s: 14 })}</button></div>`;
    }
    const sel = st.sel[st.i] || [];
    const pick = (label) => {
      let next;
      if (q.multi) next = sel.includes(label) ? sel.filter((x) => x !== label) : sel.concat(label);
      else next = [label];
      st.sel[st.i] = next;
      EV.log("option_select", { sessionId: s.id, question: st.i + 1, option: label });
      EV.update();
    };
    const last = st.i === qs.length - 1;
    const send = () => {
      const qa = qs.map((qq, k) => ({ q: qq.q, a: (st.sel[k] || []).join(", ") || "(no answer)" }));
      const text = qs.map((qq, k) => qq.header + ": " + ((st.sel[k] || []).join(", ") || "no answer")).join("\n");
      EV.resolveAsk(s, ask, qa, text, "dock");
    };
    return html`<div class="dock" role="region" aria-label="Question from the agent">
      <div class="dock-h"><span>${qs.length > 1 ? "Question " + (st.i + 1) + " of " + qs.length : "Question"}</span><button class="icon-btn" style="height:30px;min-width:30px;color:var(--ink-mid)" aria-label="Collapse question" onClick=${() => { S.dockMin[s.id] = true; EV.update(); }}>${I.chevD({ s: 14 })}</button></div>
      <div class="dock-b">
        <div class="q">${q.q}</div>
        ${q.why ? html`<div class="why">${q.why}</div>` : null}
        ${q.multi ? html`<div class="why" style="margin-top:6px;color:var(--ink-low)">Choose any that apply.</div>` : null}
        <div class="opts">${q.opts.map((o) => {
          const on = sel.includes(o.l);
          return html`<button class=${"opt" + (on ? " on" : "")} role=${q.multi ? "checkbox" : "radio"} aria-checked=${on ? "true" : "false"} onClick=${() => pick(o.l)}>
            <span class=${"rb" + (q.multi ? " sq" : "")}>${on ? I.check({ s: 13 }) : null}</span>
            <span><span class="lb">${o.l}${o.rec ? html`<span class="rec">Recommended</span>` : null}</span>${o.d ? html`<span class="dt" style="display:block">${o.d}</span>` : null}</span>
          </button>`;
        })}</div>
      </div>
      <div class="dock-f">
        <span style="display:flex;gap:4px">${st.i > 0 ? html`<button class="btn quiet" onClick=${() => { st.i--; EV.update(); }}>Back</button>` : html`<button class="btn quiet" onClick=${() => { EV.log("other_answer", { sessionId: s.id }); S.focusComposer = s.id; EV.update(); }}>Other answer…</button>`}</span>
        ${last ? html`<button class="btn primary" disabled=${!sel.length} onClick=${send}>${qs.length > 1 ? "Send answers" : "Send answer"}</button>`
          : html`<button class="btn primary" disabled=${!sel.length} onClick=${() => { st.i++; EV.update(); }}>Next question</button>`}
      </div>
    </div>`;
  }

  // ---------- tray ----------
  function Tray({ s }) {
    const st = EV.stateOf(s);
    const others = EV.needsCount(s.id);
    let left;
    if (st === "working") {
      const since = EV.fmtDur(Date.now() - (s.actAt || s.updatedAt));
      left = html`<button class="live" onClick=${() => EV.S.scrollBottom = s.id}>${h(EV.Pulse, { values: s.pulse, off: EV.S.conn !== "live" })}<span class="t">${s.activity}${s.activity === "Thinking" ? "…" : ""} · ${since}</span></button>`;
    } else if (st === "stuck") left = html`<span class="live amber"><span class="hollow"></span><span class="t">May be stuck · no updates for ${EV.fmtAgo(Date.now() - s.updatedAt)}</span></span>`;
    else if (st === "failed") left = html`<span class="live" style="color:var(--danger-ink)"><span class="t">Failed · details above</span></span>`;
    else if (st === "shutdown") left = html`<span class="live"><span class="t">Shut down · sending a message resumes it</span></span>`;
    else if (st === "restart") left = html`<span class="live amber"><span class="t">Needs a restart · see above</span></span>`;
    else left = html`<span class="live"><span class="t">Finished ${EV.fmtAgo(Date.now() - s.updatedAt)} ago</span></span>`;
    return html`<div class="tray">${left}</div>`;
  }

  // Other sessions that need you, and a way to go to the next one. Sits above
  // the tray or ask dock (and the Reader's review bar) so it can't be mistaken
  // for part of this session. Alerts held while you read are counted as "new".
  EV.NextBar = function NextBar({ exceptId }) {
    const others = EV.needsCount(exceptId);
    const held = EV.S.held.length;
    if (!others) return null;
    return html`<div class="next-bar">
      <button class="nb-list" onClick=${() => { EV.log("needs_list_open", { from: exceptId }); EV.openSheet("needsList", { exceptId }); }} aria-label=${"See the " + others + " other sessions that need you"}>${held ? html`<b>${held} new</b> · ` : null}${others} other session${others === 1 ? " needs" : "s need"} you</button>
      <button class="nb-next" onClick=${() => EV.goNext(exceptId)} aria-label="Go to the next session that needs you">Next ${I.chevR({ s: 12 })}</button>
    </div>`;
  };

  // What needs you, as a short list to choose from, newest alerts first.
  EV.sheets.needsList = function ({ exceptId }) {
    const S = EV.S;
    const heldIds = S.held.map((a) => a.sessionId);
    const list = EV.needsOrder().filter((x) => x.id !== exceptId).sort((a, b) => heldIds.includes(b.id) - heldIds.includes(a.id));
    return html`<${EV.Sheet} title="Needs you" right=${html`<button class="text-btn strong" onClick=${EV.closeSheet}>Done</button>`} size="medium">
      ${list.map((x) => html`<button class="row" key=${x.id} style="text-align:left" onClick=${() => { EV.log("needs_list_pick", { sessionId: x.id }); S.held = S.held.filter((a) => a.sessionId !== x.id); EV.closeAllSheets(); EV.openSession(x.id, { from: "needs_list" }); }}>
        <div class="mark">${h(EV.Mark, { s: x })}</div>
        <div style="min-width:0"><div class="l1"><span class="title">${x.title}</span><span class="age">${heldIds.includes(x.id) ? html`<span class="draft" style="color:var(--attention-ink);border-color:var(--attention-edge)">New</span>` : null}${EV.fmtAgo(Date.now() - x.updatedAt)}</span></div>
        ${EV.whyParts(x) ? html`<div class=${"why two " + (EV.whyParts(x).cls || "")}>${h(EV.WhyText, { w: EV.whyParts(x) })}</div>` : null}</div>
      </button>`)}
    </${EV.Sheet}>`;
  };

  // ---------- composer ----------
  function Composer({ s }) {
    const S = EV.S;
    const ta = useRef(null);
    const text = S.drafts[s.id] || "";
    const st = EV.stateOf(s);
    const working = st === "working" || st === "stuck";
    const has = text.trim().length > 0 || (S.images[s.id] || []).length > 0;
    useEffect(() => {
      if (S.focusComposer === s.id && ta.current) { S.focusComposer = null; ta.current.focus(); }
      if (ta.current) { ta.current.style.height = "auto"; ta.current.style.height = Math.min(140, ta.current.scrollHeight) + "px"; }
    });
    const ph = s.state === "question" ? "Answer or ask…" : working ? "Tell the agent something…" : s.state === "shutdown" ? "Message to resume" : "Message";
    const m = EV.model(s.model);
    const offline = S.conn !== "live";
    let primary;
    if (working && !has) primary = html`<button class="stop" aria-label="Stop this turn" onClick=${() => EV.stopTurn(s, "composer")}>${I.stop({ s: 13 })}</button>`;
    else if (working && has && !offline) primary = html`<span style="display:flex;gap:6px"><button class="queue" onClick=${() => EV.sendMessage(s, text, "queue")}>Queue</button><button class="steer" onClick=${() => EV.sendMessage(s, text, "steer")}>Steer ${I.up({ s: 15 })}</button></span>`;
    else if (offline && has) primary = html`<button class="steer" onClick=${() => EV.sendMessage(s, text, "send")}>Send later</button>`;
    else primary = html`<button class="send" disabled=${!has} aria-label="Send" onClick=${() => EV.sendMessage(s, text, "send")}>${I.up()}</button>`;
    const imgs = S.images[s.id] || [];
    return html`<div class="composer">
      ${working && has && S.prefs.hintUses < 2 && !offline ? html`<div class="hint">${I.diamond({ s: 12 })}<span><b>Steer</b> arrives at the agent's next step. <b>Queue</b> waits until this turn ends.</span></div>` : null}
      <div class="cbox">
        ${imgs.length ? html`<div class="thumbs">${imgs.map((x, i) => html`<div class="th"><button aria-label="Remove image" onClick=${() => { imgs.splice(i, 1); EV.update(); }}>${I.x({ s: 10 })}</button></div>`)}</div>` : null}
        <textarea ref=${ta} id=${"composer-" + s.id} rows="1" placeholder=${ph} value=${text} aria-label="Message"
          onInput=${(e) => { S.drafts[s.id] = e.currentTarget.value; EV.update(); }}
          onFocus=${() => { S.typing = true; }} onBlur=${() => { S.typing = false; setTimeout(() => { if (!S.typing) EV.releaseHeld(); }, 200); }}></textarea>
        <div class="crow">
          <button class="cbtn" aria-label="Attach" onClick=${() => EV.openMenu({ kind: "list", top: 470, title: "Attach", items: [
            { label: "Photo library", icon: I.photo({ s: 18 }), run: () => { imgs.push(1); S.images[s.id] = imgs; EV.log("attach", { sessionId: s.id, source: "library" }); EV.update(); } },
            { label: "Camera", icon: I.camera({ s: 18 }), run: () => { imgs.push(1); S.images[s.id] = imgs; EV.log("attach", { sessionId: s.id, source: "camera" }); EV.update(); } },
          ] })}>${I.plus({ s: 20 })}</button>
          <button class="cbtn model" aria-label=${"Model " + m.name + ", effort " + s.effort} onClick=${() => EV.openSheet("model", { target: "session", sessionId: s.id })}>${m.name} · ${EV.cap(s.effort)}</button>
          <button class="cbtn" aria-label="Commands and skills" style="font:600 17px var(--mono)" onClick=${() => EV.openSheet("commands", { sessionId: s.id })}>/</button>
          <span class="sp"></span>
          ${primary}
        </div>
      </div>
    </div>`;
  }

  function Ghosts({ s }) {
    const S = EV.S;
    const q = S.queue[s.id] || [];
    const steer = S.steering[s.id];
    return html`${steer ? html`<div class="u-msg ghost"><div class="u-bubble">${steer.text}</div><div class="u-cap">Steering · arrives at the next step</div></div>` : null}
      ${q.map((m) => html`<div class="u-msg ghost" key=${m.id}><div class="u-bubble">${m.text}</div><div class="u-cap">Queued · sends when this turn ends</div>
        <div class="ghost-row"><button class="mini-btn" onClick=${() => { S.queue[s.id] = q.filter((x) => x !== m); S.steering[s.id] = { text: m.text, at: Date.now() }; EV.log("queue_promote", { sessionId: s.id }); EV.update(); setTimeout(() => { if (S.steering[s.id]) { delete S.steering[s.id]; EV.addItem(s.id, { t: "user", text: m.text, kind: "steer" }); EV.update(); } }, 2200); }}>Steer now</button>
        <button class="mini-btn" onClick=${() => { S.queue[s.id] = q.filter((x) => x !== m); S.drafts[s.id] = m.text; S.focusComposer = s.id; EV.log("queue_edit", { sessionId: s.id }); EV.update(); }}>Edit</button>
        <button class="mini-btn danger" onClick=${() => { S.queue[s.id] = q.filter((x) => x !== m); EV.log("queue_cancel", { sessionId: s.id }); EV.toast("Removed from queue"); EV.update(); }}>Cancel</button></div></div>`)}`;
  }

  // ---------- session screen ----------
  function Session({ id, top, highlight, key }) {
    const S = EV.S;
    const s = EV.sess(id);
    const scrollRef = useRef(null);
    const atBottom = useRef(true);
    const lastLen = useRef(0);
    const [newCount, setNewCount] = useState(0);
    const titleSw = useRef(null);
    const tr = (s && S.transcripts[id]) || [];

    useLayoutEffect(() => {
      const el = scrollRef.current;
      if (!el || !s) return;
      let target = null;
      if (highlight) {
        const needle = highlight.replace(/…/g, "").trim().slice(0, 40).toLowerCase();
        target = Array.from(el.querySelectorAll(".a-msg,.u-bubble")).find((n) => n.textContent.toLowerCase().includes(needle));
        if (target) { target.classList.add("rblock", "flashc"); }
      } else if (s.state === "yourmove" || s.state === "idle") {
        const msgs = el.querySelectorAll(".a-msg");
        target = msgs[msgs.length - 1];
      }
      if (target) el.scrollTop = Math.max(0, target.offsetTop - 70);
      else el.scrollTop = el.scrollHeight;
      lastLen.current = tr.length;
    }, [id]);

    useEffect(() => {
      const el = scrollRef.current;
      if (!el) return;
      if (S.scrollBottom === id) { S.scrollBottom = null; el.scrollTo({ top: el.scrollHeight, behavior: "smooth" }); }
      if (tr.length > lastLen.current) {
        if (atBottom.current) el.scrollTop = el.scrollHeight;
        else setNewCount(newCount + (tr.length - lastLen.current));
        lastLen.current = tr.length;
      }
    });

    if (!s) return html`<div class="empty">Session not found</div>`;
    const t = EV.tally(s);
    const tot = EV.tallyTotal(t);
    const needs = EV.needsCount(s.id);
    const st = EV.stateOf(s);
    const files = tr.filter((x) => x.t === "doc" || x.t === "art").length;
    const fileNew = tr.some((x) => x.t === "doc" && S.docs[x.path] && S.docs[x.path].changed.length && !S.readDocs[x.path]);
    const q = S.queue[s.id] || [];
    const subtitle = { working: "Working · " + EV.fmtAgo(Date.now() - s.startedAt), stuck: "May be stuck", failed: "Failed", question: "Asks a question", approval: "Needs approval", warning: "Warning", restart: "Needs a restart", yourmove: "Finished", idle: "Idle", shutdown: "Shut down" }[st] || "";

    const onTitleDown = (e) => { titleSw.current = { x: e.clientX, y: e.clientY }; };
    const onTitleUp = (e) => {
      const c = titleSw.current; titleSw.current = null;
      if (!c) return;
      const dx = e.clientX - c.x;
      if (Math.abs(dx) > 50 && Math.abs(e.clientY - c.y) < 40) { e.preventDefault(); EV.goAdjacent(s.id, dx < 0 ? 1 : -1); return; }
      if (Math.abs(dx) < 8) { EV.log("session_sheet_open", { sessionId: s.id }); EV.openSheet("session", { sessionId: s.id }); }
    };
    const menu = () => EV.openMenu({ kind: "list", top: 96, right: true, title: "Session menu", items: [
      { label: "Detail: " + EV.level(s.id), icon: I.outline({ s: 18 }), run: () => setTimeout(() => EV.detailMenu(s), 30) },
      { label: "Find in session", icon: I.search({ s: 18 }), run: () => EV.openSheet("find", { sessionId: s.id }) },
      files ? { label: "Files & artifacts", icon: I.doc({ s: 18 }), run: () => EV.openSheet("files", { sessionId: s.id }) } : null,
      tot ? { label: "Subagents", icon: I.people({ s: 18 }), run: () => EV.push("subagents", { sessionId: s.id }) } : null,
      { label: "Notes & links", icon: I.note({ s: 18 }), run: () => { EV.log("notes_open", { sessionId: s.id, from: "menu" }); EV.openSheet("notes", { sessionId: s.id }); } },
      { label: "Session info", icon: I.gauge({ s: 18 }), run: () => EV.openSheet("session", { sessionId: s.id }) },
      { label: "Ask aside…", icon: I.bubble({ s: 18 }), sep: true, run: () => EV.openSheet("aside", { sessionId: s.id }) },
      { label: s.category ? "Change category…" : "Pin to category…", icon: I.pin({ s: 18 }), run: () => setTimeout(() => EV.pinMenu(s), 30) },
      { label: "New session like this", icon: I.compose({ s: 18 }), run: () => EV.openNew("like", { like: s.id }) },
      { label: s.archived ? "Unarchive" : "Archive", icon: I.archive({ s: 18 }), run: () => { EV.setArchived(s, !s.archived); } },
      s.live ? { label: "Shut down…", icon: I.x({ s: 18 }), danger: true, run: () => setTimeout(() => EV.confirmShutdown(s), 30) } : null,
    ] });

    const onScroll = (e) => {
      const el = e.currentTarget;
      atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
      if (atBottom.current && newCount) setNewCount(0);
    };

    return html`<div style="display:flex;flex-direction:column;height:100%">
      <div class="nav">
        <div class="nav-row">
          <div class="lead"><button class="icon-btn back-btn" onClick=${EV.pop} aria-label=${"Back to Board" + (needs ? ", " + needs + " need you" : "")}>${I.chevL({ s: 22 })}${needs ? html`<span class="badge">${needs}</span>` : null}</button></div>
          <div class="nav-title" role="button" tabindex="0" aria-label=${s.title + ". " + subtitle + ". Session info"} onPointerDown=${onTitleDown} onPointerUp=${onTitleUp} onKeyDown=${(e) => { if (e.key === "Enter") EV.openSheet("session", { sessionId: s.id }); }} style="touch-action:pan-y">
            <div class="t">${s.title}</div>
            <div class="s">${st === "working" ? h(EV.Pulse, { values: s.pulse, off: S.conn !== "live" }) : h(EV.Mark, { s, size: 13 })}<span>${subtitle}</span><span style="color:var(--ink-low);display:flex" aria-hidden="true">${I.chevR({ s: 10 })}</span></div>
          </div>
          <div class="trail"><button class="icon-btn" aria-label="Session menu" onClick=${menu}>${I.dots()}</button></div>
        </div>
        ${S.conn !== "live" ? html`<div class="banner-thin"></div>` : null}
        <div class="ctx-chips">
          <button class="cchip view" onClick=${() => EV.detailMenu(s)} aria-label=${"Detail level " + EV.level(s.id)}>${I.outline({ s: 15 })} Detail: ${EV.level(s.id)}</button>
          ${tot ? html`<button class="cchip" onClick=${() => EV.push("subagents", { sessionId: s.id })}>${I.people({ s: 15 })} Subagents ${h(EV.Strip, { t })}<span class="n">${tot}</span>${t.fail ? html`<span class="rd"></span>` : null}</button>` : null}
          ${files ? html`<button class="cchip" onClick=${() => EV.openSheet("files", { sessionId: s.id })}>${I.doc({ s: 15 })} Files <span class="n">${files}</span>${fileNew ? html`<span class="bd"></span>` : null}</button>` : null}
          ${s.tasks ? html`<button class="cchip" onClick=${() => EV.openSheet("tasks", { sessionId: s.id })}>${I.checklist({ s: 15 })} Tasks <span class="n">${s.tasks.done}/${s.tasks.total}</span></button>` : null}
          ${s.goal ? html`<button class=${"cchip" + (s.goal.status === "blocked" ? " amber" : "")} onClick=${() => EV.openSheet("goal", { sessionId: s.id })}>${I.target({ s: 15 })} Goal</button>` : null}
          ${q.length ? html`<button class="cchip" onClick=${() => EV.openSheet("queue", { sessionId: s.id })}>Queue <span class="n">${q.length}</span></button>` : null}
        </div>
        ${h(EV.NotesBar, { s })}
      </div>
      <div class="scroll" ref=${scrollRef} onScroll=${onScroll}>
        <div class="tx">
          ${tr.map((it, i) => html`<${Item} key=${i} it=${it} s=${s} idx=${i} />`)}
          ${h(Ghosts, { s })}
        </div>
        ${newCount ? html`<button class="new-pill" style="position:sticky;bottom:10px" onClick=${() => { scrollRef.current.scrollTo({ top: scrollRef.current.scrollHeight, behavior: "smooth" }); setNewCount(0); }}>${I.down({ s: 14 })} ${newCount} new</button>` : null}
      </div>
      <div class="bottom">
        ${h(EV.NextBar, { exceptId: s.id })}
        ${s.state === "question" || s.state === "approval" ? h(AskDock, { s }) : h(Tray, { s })}
        ${h(Composer, { s })}
      </div>
    </div>`;
  }

  const LEVEL_HELP = {
    Chat: "Just the conversation",
    Intent: "Plus one line for each step the agent took",
    Tools: "Plus every command it ran; tap one for its output",
    Activity: "Plus system events, like compaction and model changes",
    Full: "Everything, with command output shown",
  };
  EV.detailMenu = function (s) {
    EV.openMenu({ kind: "list", top: 110, right: true, title: "Detail level",
      preview: html`<div class="preview"><div class="pt">Detail level</div><div class="pw">How much of the agent's work this conversation shows, including what's already there</div></div>`,
      items: LEVELS.map((l) => ({ label: l, sub: LEVEL_HELP[l], checked: EV.level(s.id) === l, run: () => { EV.S.prefs.detail[s.id] = l; EV.log("detail_level", { sessionId: s.id, level: l }); EV.update(); } })) });
  };

  EV.screens.session = Session;

  // ---------- sheets ----------
  const Done = () => html`<button class="text-btn strong" onClick=${EV.closeSheet}>Done</button>`;
  const Cancel = () => html`<button class="text-btn" onClick=${EV.closeSheet}>Cancel</button>`;

  EV.sheets.session = function ({ sessionId, stacked }) {
    const S = EV.S;
    const s = EV.sess(sessionId);
    const hst = EV.host(s.host);
    const m = EV.model(s.model);
    const u = s.usage;
    const total = u.in + u.out;
    const pct = Math.min(100, Math.round((s.ctx.used / s.ctx.window) * 100));
    const proj = S.projects.find((p) => p.id === s.project);
    return html`<${EV.Sheet} title="Session" right=${h(Done)} size=${stacked ? "stacked" : "large"}>
      <div style="padding:4px 20px 8px"><div style="font:600 20px/25px var(--sans)">${s.title}</div>
      <div style="display:flex;gap:6px;align-items:center;margin-top:4px;color:var(--ink-mid);font-size:14px">${h(EV.Mark, { s, size: 14 })}<span>${{ working: "Working", stuck: "May be stuck", failed: "Failed", question: "Asks a question", approval: "Needs approval", restart: "Needs a restart", yourmove: "Finished", idle: "Idle", shutdown: "Shut down", warning: "Warning" }[EV.stateOf(s)]}</span></div></div>
      <div class="glabel">Where</div>
      <div class="group">
        ${h(EV.Gi, { label: "Host", value: html`<span class=${"conn-dot" + (hst && hst.state === "offline" ? " offline" : "")}></span>${s.host}` })}
        ${h(EV.Gi, { label: "Project", value: s.project })}
        ${h(EV.Gi, { label: "Directory", value: html`<span class="mono">~/${proj ? proj.path : s.project}</span>` })}
        ${h(EV.Gi, { label: "Branch", value: html`<span class="mono">${s.branch}</span>` })}
      </div>
      <div class="glabel">Model</div>
      <div class="group">
        ${h(EV.Gi, { label: "Model", sub: m.provider, value: m.name, chev: true, onClick: () => EV.openSheet("model", { target: "session", sessionId: s.id }) })}
        ${h(EV.Gi, { label: "Effort", value: EV.cap(s.effort), chev: true, onClick: () => EV.openSheet("model", { target: "session", sessionId: s.id }) })}
      </div>
      <div class="gfoot">Model and effort changes apply from the next turn.</div>
      <div class="glabel">Plugins · chosen at start</div>
      <div class="group">${s.plugins.map((p) => h(EV.Gi, { key: p, label: p, value: html`<span class="tag gray">On</span>` }))}</div>
      <div class="gfoot">Plugins are chosen when a session starts. To change them, start a new session or fork this one.</div>
      <div class="glabel">Access</div>
      <div class="group">${h(EV.Gi, { label: s.access, value: "Network on" })}</div>
      <div class="glabel">Usage</div>
      <div class="group">
        ${h(EV.Gi, { label: "Tokens", sub: EV.fmtTok(u.in) + " in · " + EV.fmtTok(u.out) + " out · " + EV.fmtTok(u.cache) + " cached", value: EV.fmtTok(total) })}
        ${h(EV.Gi, { label: "Estimated cost", value: s.cost })}
        ${h(EV.Gi, { label: "Work time", value: EV.fmtDur(s.workSec * 1000) })}
        <div class="gi static" style="display:block">
          <div style="display:flex;justify-content:space-between;font-size:17px"><span>Context</span><span style="color:var(--ink-mid);font-size:15px">${s.ctx.used}K of ${s.ctx.window}K</span></div>
          <div style="position:relative;height:6px;border-radius:3px;background:var(--inset);margin:8px 0 4px;overflow:hidden"><i style=${"position:absolute;left:0;top:0;bottom:0;width:" + pct + "%;background:" + (pct > 80 ? "var(--attention)" : "var(--ink-mid)")}></i><i style="position:absolute;left:85%;top:-2px;bottom:-2px;width:2px;background:var(--edge-strong)"></i></div>
          <div style="font-size:12px;color:var(--ink-low)">Compacts automatically at 85%</div>
        </div>
        ${s.failedTools ? h(EV.Gi, { label: "Failed tool calls", value: html`<span style="color:var(--danger-ink)">${s.failedTools}</span>` }) : null}
      </div>
      <div class="glabel">Goal, tasks, notes and links</div>
      <div class="group">
        ${h(EV.Gi, { label: "Goal", value: s.goal ? EV.cap(s.goal.status) : "None", chev: true, onClick: () => EV.openSheet("goal", { sessionId: s.id }) })}
        ${h(EV.Gi, { label: "Tasks", value: s.tasks ? s.tasks.done + " of " + s.tasks.total : "None", chev: true, onClick: () => EV.openSheet("tasks", { sessionId: s.id }) })}
        ${h(EV.Gi, { label: "Notes & links", value: EV.notesSummary(s), chev: true, onClick: () => EV.openSheet("notes", { sessionId: s.id }) })}
      </div>
      <div class="glabel">Actions</div>
      <div class="group">
        ${h(EV.Gi, { label: "Ask aside…", sub: "A side session from the latest point", cls: "accent", onClick: () => EV.openSheet("aside", { sessionId: s.id }) })}
        ${h(EV.Gi, { label: "Fork from latest", cls: "accent", onClick: () => { EV.closeAllSheets(); EV.fork(s, ""); } })}
        ${h(EV.Gi, { label: "Compact context", cls: "accent", onClick: () => { EV.addItem(s.id, { t: "sys", text: "Context compacted · " + s.ctx.used + "K → " + Math.round(s.ctx.used / 4) + "K tokens" }); s.ctx.used = Math.round(s.ctx.used / 4); EV.log("compact", { sessionId: s.id }); EV.toast("Context compacted"); } })}
        ${h(EV.Gi, { label: "Rename", cls: "accent", onClick: () => EV.openSheet("rename", { sessionId: s.id }) })}
        ${h(EV.Gi, { label: s.archived ? "Unarchive" : "Archive", cls: "accent", onClick: () => { EV.closeAllSheets(); EV.setArchived(s, !s.archived); } })}
        ${s.live ? h(EV.Gi, { label: "Shut down…", cls: "danger", onClick: () => { EV.closeAllSheets(); setTimeout(() => EV.confirmShutdown(s), 50); } }) : null}
      </div>
    </${EV.Sheet}>`;
  };

  EV.sheets.model = function ({ target, sessionId, stacked }) {
    const S = EV.S;
    const s = sessionId ? EV.sess(sessionId) : null;
    const L = target === "launch" ? S.launch : null;
    const cur = { model: L ? L.model : s.model, effort: L ? L.effort : s.effort };
    const [sel, setSel] = useState(cur);
    const [q, setQ] = useState("");
    const m = EV.model(sel.model);
    const recentIds = [...new Set(S.sessions.filter((x) => x.live).map((x) => x.model))].slice(0, 5);
    const matches = (x) => !q || (x.name + " " + x.id + " " + x.provider).toLowerCase().includes(q.toLowerCase());
    const row = (x) => html`<button class="gi" key=${x.id} onClick=${() => setSel({ model: x.id, effort: x.efforts.includes(sel.effort) ? sel.effort : x.efforts[x.efforts.length - 1] })}>
      <span style="width:22px;display:flex;color:var(--accent)">${sel.model === x.id ? I.check({ s: 18 }) : null}</span>
      <span class="gl">${x.name}<small>${x.provider} · ${x.ctx >= 1000 ? x.ctx / 1000 + "M" : x.ctx + "K"} context · ${x.pin ? "$" + x.pin + " / $" + x.pout + " per M" : "free, local"}${x.vision ? " · vision" : ""}</small></span><span></span></button>`;
    const provs = [...new Set(S.models.map((x) => x.provider))];
    const apply = () => {
      if (L) { L.model = sel.model; L.effort = sel.effort; EV.log("launch_model", { model: sel.model, effort: sel.effort }); }
      else if (sel.model !== s.model || sel.effort !== s.effort) {
        const changedModel = sel.model !== s.model;
        s.model = sel.model; s.effort = sel.effort;
        EV.addItem(s.id, { t: "sys", text: changedModel ? "Model changed to " + sel.model + " · " + sel.effort : "Effort changed to " + sel.effort });
        EV.log("model_change", { sessionId: s.id, model: sel.model, effort: sel.effort });
        EV.toast("Applies from the next turn");
      }
      EV.closeSheet();
    };
    return html`<${EV.Sheet} title="Model" left=${h(Cancel)} right=${html`<button class="text-btn strong" onClick=${apply}>Done</button>`} size=${stacked ? "stacked" : "large"}>
      <div class="search-field">${I.search({ s: 16 })}<input placeholder="Search models" value=${q} onInput=${(e) => setQ(e.currentTarget.value)} aria-label="Search models" /></div>
      ${L ? null : html`<div class="glabel" style="padding-top:8px">Effort</div>
      ${h(EV.Seg, { options: ["low", "medium", "high", "xhigh", "max"], value: sel.effort, onChange: (e) => setSel({ model: sel.model, effort: e }), disabled: ["low", "medium", "high", "xhigh", "max"].filter((e) => !m.efforts.includes(e)) })}
      <div class="gfoot">How long it thinks before acting. ${m.name} supports ${m.efforts.map(EV.cap).join(", ")}.</div>`}
      ${!q ? html`<div class="glabel">Recent</div><div class="group">${recentIds.map((id) => row(EV.model(id)))}</div>` : null}
      ${provs.map((p) => { const list = S.models.filter((x) => x.provider === p && matches(x)); return list.length ? html`<div class="glabel">${p}</div><div class="group">${list.map(row)}</div>` : null; })}
    </${EV.Sheet}>`;
  };

  const SKILLS = {
    superpowers: ["brainstorming", "writing-plans", "test-driven-development", "systematic-debugging", "requesting-code-review"],
    "elements-of-style": ["writing-clearly-and-concisely"],
    go: ["go-testing", "go-profiling", "go-errors"],
    "shepherd-pr": ["shepherd"],
    "simplify-code": ["simplify"],
    "frontend-design": ["frontend-design"],
  };
  EV.sheets.commands = function ({ sessionId }) {
    const S = EV.S;
    const s = EV.sess(sessionId);
    const [q, setQ] = useState("");
    const ins = (name, kind) => { S.drafts[s.id] = "/" + name + " " + (S.drafts[s.id] || ""); S.focusComposer = s.id; EV.log("command_insert", { sessionId: s.id, name, kind }); EV.closeSheet(); };
    const builtins = [["goal", "Set or change the goal"], ["compact", "Compact the context now"], ["aside", "Ask something in a side session"], ["tasks", "Show the task list"], ["model", "Change the model"], ["effort", "Change the effort"], ["clear", "Start fresh in this session"]];
    const ok = (t) => !q || t.toLowerCase().includes(q.toLowerCase());
    return html`<${EV.Sheet} title="Commands and skills" left=${h(Cancel)} size="large">
      <div class="search-field">${I.search({ s: 16 })}<input placeholder="Search" value=${q} onInput=${(e) => setQ(e.currentTarget.value)} aria-label="Search commands and skills" /></div>
      <div class="glabel">Built in</div>
      <div class="group">${builtins.filter((b) => ok(b[0])).map((b) => h(EV.Gi, { key: b[0], label: html`<span style="font-family:var(--mono);font-size:15px">/${b[0]}</span>`, sub: b[1], onClick: () => ins(b[0], "builtin") }))}</div>
      ${s.plugins.filter((p) => SKILLS[p]).map((p) => { const list = SKILLS[p].filter(ok); return list.length ? html`<div class="glabel">${p}</div><div class="group">${list.map((k) => h(EV.Gi, { key: k, label: html`<span style="font-family:var(--mono);font-size:15px">/${k}</span>`, onClick: () => ins(k, "skill") }))}</div>` : null; })}
    </${EV.Sheet}>`;
  };

  EV.sheets.queue = function ({ sessionId }) {
    const S = EV.S;
    const q = S.queue[sessionId] || [];
    return html`<${EV.Sheet} title="Queue" right=${h(Done)} size="medium">
      <div class="gfoot" style="padding-top:4px">Queued messages send in order when the current turn ends.</div>
      <div class="group" style="margin-top:10px">${q.length ? q.map((m, i) => html`<div class="gi static" key=${m.id}><span class="gl" style="grid-column:1/3">${i + 1}. ${m.text}</span><span class="gv"><button class="mini-btn danger" onClick=${() => { S.queue[sessionId] = q.filter((x) => x !== m); EV.log("queue_cancel", { sessionId }); EV.update(); }}>Cancel</button></span></div>`) : html`<div class="gi static"><span class="gl" style="grid-column:1/4;color:var(--ink-mid)">Nothing queued</span></div>`}</div>
    </${EV.Sheet}>`;
  };

  EV.sheets.tasks = function ({ sessionId }) {
    const s = EV.sess(sessionId);
    const t = s.tasks || { done: 0, total: 0 };
    const names = ["Group the failures by cause", "Write the settle-race plan", "Start the -race subagents", "Fix the settle/drain race", "Run the flaky tests 200 times", "Confirm CI is green", "Summarize for review"];
    return html`<${EV.Sheet} title="Tasks" right=${h(Done)} size="large">
      ${!s.tasks ? html`<div class="empty">No tasks yet. The agent creates tasks as it plans.</div>` : html`
      <div class="group">${names.slice(0, t.total).map((n, i) => html`<div class="gi static" key=${i}><span style=${"display:flex;color:" + (i < t.done ? "var(--alive)" : i === t.done ? "var(--accent)" : "var(--ink-low)")}>${i < t.done ? I.check({ s: 18 }) : i === t.done ? h(EV.Pulse, { values: s.pulse }) : html`<span style="width:16px;height:16px;border-radius:8px;border:1.5px solid var(--edge-strong)"></span>`}</span>
        <span class="gl" style=${i < t.done ? "color:var(--ink-mid)" : i === t.done ? "font-weight:600" : ""}>${i === t.done && t.current ? t.current : n}</span><span></span></div>`)}</div>
      <div class="gfoot">${t.done} of ${t.total} done.</div>`}
    </${EV.Sheet}>`;
  };

  EV.sheets.goal = function ({ sessionId }) {
    const S = EV.S;
    const s = EV.sess(sessionId);
    const [txt, setTxt] = useState(s.goal ? s.goal.text : "");
    const save = () => { s.goal = txt.trim() ? { text: txt.trim(), status: s.goal ? s.goal.status : "active" } : null; EV.log("goal_set", { sessionId: s.id, text: txt.trim() }); EV.closeSheet(); EV.toast(txt.trim() ? "Goal set" : "Goal cleared"); };
    return html`<${EV.Sheet} title="Goal" left=${h(Cancel)} right=${html`<button class="text-btn strong" onClick=${save}>Save</button>`} size="medium">
      <div class="gfoot" style="padding:4px 32px 10px">The agent keeps working toward the goal across turns and says when it's done or blocked.</div>
      <div class="field"><textarea rows="3" placeholder="What does done look like?" value=${txt} onInput=${(e) => setTxt(e.currentTarget.value)} aria-label="Goal"></textarea></div>
      ${s.goal ? html`<div class="gfoot">Status: ${EV.cap(s.goal.status)}.</div><div style="padding:14px 16px"><button class="btn quiet" style="color:var(--danger-ink)" onClick=${() => { s.goal = null; EV.log("goal_clear", { sessionId: s.id }); EV.closeSheet(); EV.toast("Goal cleared"); }}>Clear goal</button></div>` : null}
    </${EV.Sheet}>`;
  };

  // Shared notes, as the web's Notes panel shows them: your note, the
  // agent's note, and the session's links (humanNote, agentNote and
  // sessionUrls on the thread). The phone writes two of them: your note
  // (notes/human/set) and link removal (urls/remove). Only the agent adds
  // links.
  const hasText = (t) => !!(t && t.trim());
  EV.notesOf = function (s) {
    const n = s.notes || {};
    const draft = EV.S.noteDrafts[s.id];
    return { human: draft != null ? draft : n.human || "", saved: n.human || "", agent: n.agent || "", urls: s.urls || [] };
  };
  EV.hasNotes = function (s) {
    const n = EV.notesOf(s);
    return hasText(n.human) || hasText(n.agent) || n.urls.length > 0;
  };
  const plural = (n, one) => n + " " + one + (n === 1 ? "" : "s");
  EV.notesSummary = function (s) {
    const n = EV.notesOf(s);
    const parts = [];
    if (hasText(n.human)) parts.push("your note");
    if (hasText(n.agent)) parts.push("agent note");
    if (n.urls.length) parts.push(plural(n.urls.length, "link"));
    return parts.length ? EV.cap(parts.join(" · ")) : "None";
  };

  // Saving your note is a steer: the daemon hands it to the agent at its next
  // step, and wakes an agent that isn't in a turn. So, like the web, leaving
  // the note schedules the save 10 seconds later and coming back to it
  // cancels that, and a burst of edits reaches the agent once. On the phone,
  // closing the sheet is leaving the note.
  const noteTimers = {};
  const NOTE_DELAY = 10000;
  const agentIdle = (s) => s.state === "idle" || s.state === "yourmove";
  EV.leaveNote = function (s) {
    const S = EV.S;
    const draft = S.noteDrafts[s.id];
    if (draft == null) return;
    if (draft === ((s.notes && s.notes.human) || "")) { delete S.noteDrafts[s.id]; S.noteState[s.id] = null; EV.update(); return; }
    // Leaving twice (the field blurs, then the sheet closes) is one leave.
    if (S.noteState[s.id] === "pending") return;
    S.noteState[s.id] = "pending";
    EV.log("note_leave", { sessionId: s.id });
    noteTimers[s.id] = setTimeout(() => {
      if (EV.S !== S) return;
      const text = S.noteDrafts[s.id];
      if (text == null) return;
      const woke = agentIdle(s);
      s.notes = Object.assign({}, s.notes || {}, { human: text.trim() });
      delete S.noteDrafts[s.id];
      S.noteState[s.id] = "saved";
      EV.addItem(s.id, { t: "noteSet", text: text.trim() });
      if (woke) {
        s.state = "working"; s.unseen = false; s.activity = "Reading your note";
        s.startedAt = s.updatedAt = Date.now();
        s.pulse = [0, 0, 0, 0, 0, 0.4, 0.8];
        EV.addItem(s.id, { t: "think", live: true });
      }
      EV.log("note_set", { sessionId: s.id, text: text.trim(), woke });
      EV.update();
    }, NOTE_DELAY);
    EV.update();
  };
  EV.focusNote = function (s) {
    clearTimeout(noteTimers[s.id]);
    if (EV.S.noteState[s.id] === "pending") { EV.S.noteState[s.id] = null; EV.update(); }
  };

  // Links: web pages open in an in-app browser (SFSafariViewController), and
  // file links open in the Reader when the file is a document the phone can
  // show. A file it can't show keeps its text but isn't tappable, as on the
  // web.
  EV.linkKind = function (u) {
    if (/^https?:\/\//.test(u.url)) return "web";
    if (/^file:\/\//.test(u.url)) return "file";
    return "other";
  };
  EV.linkDoc = function (u) {
    if (EV.linkKind(u) !== "file") return null;
    return Object.keys(EV.S.docs).find((p) => u.url.endsWith("/" + p)) || null;
  };
  EV.openLink = function (s, u, from) {
    const kind = EV.linkKind(u);
    const doc = EV.linkDoc(u);
    if (kind === "web") { EV.log("link_open", { sessionId: s.id, url: u.url, from }); EV.openSheet("browser", { url: u.url, label: u.label }); return; }
    if (doc) { EV.log("link_open", { sessionId: s.id, url: u.url, from }); EV.closeAllSheets(); EV.push("reader", { path: doc, sessionId: s.id }); }
  };
  EV.removeLink = function (s, u) {
    s.urls = (s.urls || []).filter((x) => x.id !== u.id);
    EV.log("link_remove", { sessionId: s.id, url: u.url });
    EV.toast("Removed " + (u.label || "the link") + ". Only the agent can add it back.");
    EV.update();
  };
  EV.linkMenu = function (s, u, e) {
    const doc = EV.linkDoc(u), kind = EV.linkKind(u);
    EV.openMenu({ kind: "list", top: e && e.clientY ? Math.max(90, e.clientY - 40) : 200, title: u.label || u.url, items: [
      kind === "web" || doc ? { label: kind === "web" ? "Open" : "Open in Reader", icon: kind === "web" ? I.globe({ s: 18 }) : I.doc({ s: 18 }), run: () => EV.openLink(s, u, "menu") } : null,
      { label: "Copy link", icon: I.link({ s: 18 }), run: () => { EV.log("link_copy", { sessionId: s.id, url: u.url }); EV.toast("Link copied"); } },
      s.live ? { label: "Remove link", icon: I.x({ s: 18 }), danger: true, sep: true, run: () => EV.removeLink(s, u) } : null,
    ] });
  };

  // The one-line bar under the session's chips, like the web's collapsed
  // notes bar: your note if there is one, else the agent's, else the links
  // (a lone link by its label, which says more than "1 link"). It only
  // appears when there is something to show; "Notes & links" in the session
  // menu is always there.
  EV.NotesBar = function ({ s }) {
    const n = EV.notesOf(s);
    const links = n.urls.length;
    if (!hasText(n.human) && !hasText(n.agent) && !links) return null;
    const [icon, who, text] = hasText(n.human) ? [I.person({ s: 14 }), "Your note", n.human] : hasText(n.agent) ? [I.sparkle({ s: 14 }), "Agent's note", n.agent] : [I.link({ s: 14 }), null, links === 1 ? n.urls[0].label || n.urls[0].url : plural(links, "link")];
    const count = links && who ? links : 0;
    const open = () => { EV.log("notes_open", { sessionId: s.id, from: "bar" }); EV.openSheet("notes", { sessionId: s.id }); };
    return html`<button class="notes-bar" onClick=${open} aria-label=${"Notes and links. " + (who ? who + ": " : "") + text.trim() + (count ? ". " + plural(count, "link") : "")}>
      <span class="nb-ic">${icon}</span><span class="nb-t">${text.trim()}</span>
      ${count ? html`<span class="nb-n">${I.link({ s: 13 })}${count}</span>` : null}
      <span class="nb-chev">${I.chevR({ s: 10 })}</span>
    </button>`;
  };

  function LinkRow({ s, u }) {
    const kind = EV.linkKind(u), doc = EV.linkDoc(u);
    const opens = kind === "web" || !!doc;
    const body = html`<div class=${"link-row" + (opens ? "" : " inert")}>
      <span class="lr-ic">${kind === "web" ? I.globe({ s: 17 }) : I.doc({ s: 17 })}</span>
      <span class="lr-t"><span class="lr-l">${u.label || u.url}</span>${u.label ? html`<span class="lr-u">${u.url}</span>` : null}</span>
      ${opens ? html`<span class="chev">${I.chevR()}</span>` : null}
    </div>`;
    const trail = s.live ? [{ label: "Remove", cls: "sa-stop", icon: I.x({ s: 18 }), run: () => EV.removeLink(s, u) }] : [];
    return h(EV.SwipeRow, { trail, onTap: () => EV.openLink(s, u, "notes"), onLong: (e) => EV.linkMenu(s, u, e) }, body);
  }

  EV.sheets.notes = function ({ sessionId }) {
    const S = EV.S;
    const s = EV.sess(sessionId);
    const n = EV.notesOf(s);
    const state = S.noteState[s.id];
    useEffect(() => () => EV.leaveNote(s), []);
    const status = state === "pending" ? "Saves in 10 seconds. Tap the note to keep editing."
      : state === "saved" && n.human === n.saved ? "Saved"
      : agentIdle(s) ? "Saving will wake the agent."
      : s.state === "working" ? "The agent gets your note at its next step."
      : "Saving sends your note to the agent.";
    const hasAny = hasText(n.human) || hasText(n.agent) || n.urls.length;
    return html`<${EV.Sheet} title="Notes & links" right=${html`<button class="text-btn strong" onClick=${EV.closeSheet}>Done</button>`} size="large">
      ${s.live ? html`<div class="glabel">Your note</div>
        <div class="field"><textarea class="note-editor" rows="4" placeholder="Make a note…" value=${n.human} aria-label="Your note"
          onFocus=${() => EV.focusNote(s)} onBlur=${() => EV.leaveNote(s)}
          onInput=${(e) => { S.noteDrafts[s.id] = e.currentTarget.value; S.noteState[s.id] = null; EV.update(); }}></textarea></div>
        <div class="gfoot" role="status">${status}</div>`
      : hasText(n.saved) ? html`<div class="glabel">Your note</div><div class="group"><div class="gi static"><span class="gl note-text">${n.saved}</span></div></div>` : null}
      ${hasText(n.agent) || s.live ? html`<div class="glabel">Agent</div>
        <div class="group"><div class="gi static"><span class=${"gl " + (hasText(n.agent) ? "note-text" : "dim")}>${hasText(n.agent) ? n.agent : "No agent note yet"}</span></div></div>` : null}
      ${n.urls.length || s.live ? html`<div class="glabel">Links</div>
        ${n.urls.length ? html`<div class="group">${n.urls.map((u) => html`<${LinkRow} key=${u.id} s=${s} u=${u} />`)}</div>
          <div class="gfoot">${s.live ? "The agent adds links as it works. Swipe left on one to remove it." : "Links the agent saved in this session."}</div>`
        : html`<div class="group"><div class="gi static"><span class="gl dim">No links yet</span></div></div>`}` : null}
      ${!s.live && !hasAny ? html`<div class="empty" style="padding-top:28px"><b>No shared notes</b>This session ended without any.</div>` : null}
    </${EV.Sheet}>`;
  };

  // A stand-in for the in-app browser (SFSafariViewController): the page
  // opens over the app and Done comes straight back.
  EV.sheets.browser = function ({ url, label }) {
    const host = url.replace(/^https?:\/\//, "").split("/")[0];
    return html`<${EV.Sheet} title=${host} left=${html`<button class="text-btn strong" onClick=${EV.closeSheet}>Done</button>`} size="large">
      <div class="browser-bar"><span class="mono">${url}</span></div>
      <div class="empty" style="padding-top:48px">${I.globe({ s: 34 })}<b style="margin-top:10px">${label || host}</b>The page loads here, inside the app. Done brings you back.</div>
      <div style="padding:8px 16px 0"><button class="btn big" onClick=${() => { EV.log("link_safari", { url }); EV.toast("Opens in Safari"); }}>Open in Safari</button></div>
    </${EV.Sheet}>`;
  };

  EV.sheets.find = function ({ sessionId }) {
    const S = EV.S;
    const [q, setQ] = useState("");
    const tr = S.transcripts[sessionId] || [];
    const res = q.length > 1 ? tr.map((it, i) => ({ it, i, text: it.t === "agent" ? it.md : it.t === "user" ? it.text : "" })).filter((x) => x.text.toLowerCase().includes(q.toLowerCase())) : [];
    return html`<${EV.Sheet} title="Find in session" right=${h(Done)} size="large">
      <div class="search-field">${I.search({ s: 16 })}<input placeholder="Find" value=${q} onInput=${(e) => setQ(e.currentTarget.value)} aria-label="Find in session" /></div>
      <div class="group">${res.map((r) => h(EV.Gi, { key: r.i, label: html`<span style="font-size:15px">${r.text.replace(/[*#`|]/g, "").slice(0, 120)}</span>`, sub: r.it.t === "user" ? "You" : "Agent", onClick: () => { EV.closeSheet(); EV.log("find_open", { sessionId, query: q }); const t = EV.top(); t.highlight = r.text.slice(0, 40); EV.replaceTop("session", { id: sessionId, highlight: r.text.slice(0, 40) }); } }))}</div>
      ${q.length > 1 && !res.length ? html`<div class="empty">No matches in this session.</div>` : null}
    </${EV.Sheet}>`;
  };

  EV.sheets.aside = function ({ sessionId }) {
    const s = EV.sess(sessionId);
    const [txt, setTxt] = useState("");
    const go = () => {
      const id = "s-aside-" + Date.now();
      EV.S.sessions.push(Object.assign({}, s, { id, title: "Aside: " + txt.trim().split(/\s+/).slice(0, 5).join(" "), state: "working", activity: "Thinking", category: null, subs: null, updatedAt: Date.now(), startedAt: Date.now(), unseen: false, tasks: null, goal: null, notes: null, attachments: [] }));
      EV.S.transcripts[id] = [{ t: "sys", text: "Aside from “" + s.title + "” with the same setup" }, { t: "user", text: txt.trim() }, { t: "think", live: true }];
      EV.log("aside", { from: s.id, to: id, text: txt.trim() });
      EV.closeAllSheets();
      EV.openSession(id, { from: "aside" });
    };
    return html`<${EV.Sheet} title="Ask aside" left=${h(Cancel)} right=${html`<button class="text-btn strong" disabled=${!txt.trim()} onClick=${go}>Start</button>`} size="medium">
      <div class="gfoot" style="padding:4px 32px 10px">Ask something in a side session with the same setup. “${s.title}” keeps going undisturbed.</div>
      <div class="field"><textarea rows="3" placeholder="What do you want to ask?" value=${txt} onInput=${(e) => setTxt(e.currentTarget.value)} aria-label="Aside question"></textarea></div>
    </${EV.Sheet}>`;
  };

  EV.sheets.rename = function ({ sessionId }) {
    const s = EV.sess(sessionId);
    const [txt, setTxt] = useState(s.title);
    const save = () => { const old = s.title; s.title = txt.trim() || old; EV.log("rename", { sessionId: s.id, title: s.title }); EV.closeSheet(); EV.toast("Renamed"); };
    return html`<${EV.Sheet} title="Rename" left=${h(Cancel)} right=${html`<button class="text-btn strong" onClick=${save}>Save</button>`} size="medium">
      <div class="field" style="margin-top:8px"><input value=${txt} onInput=${(e) => setTxt(e.currentTarget.value)} aria-label="Session name" /></div>
    </${EV.Sheet}>`;
  };

  EV.sheets.newCategory = function ({ sessionId }) {
    const s = sessionId && EV.sess(sessionId);
    const [txt, setTxt] = useState("");
    const save = () => {
      const name = txt.trim();
      if (!name) return;
      const id = name.toLowerCase().replace(/[^a-z0-9]+/g, "-");
      if (!EV.S.categories.find((c) => c.id === id)) EV.S.categories.push({ id, name });
      EV.log("category_create", { name });
      EV.closeSheet();
      if (s) EV.pin(s, id);
    };
    return html`<${EV.Sheet} title="New category" left=${h(Cancel)} right=${html`<button class="text-btn strong" disabled=${!txt.trim()} onClick=${save}>${s ? "Create and pin" : "Create"}</button>`} size="medium">
      <div class="field" style="margin-top:8px"><input placeholder="Category name" value=${txt} onInput=${(e) => setTxt(e.currentTarget.value)} aria-label="Category name" /></div>
      ${s ? html`<div class="gfoot">“${s.title}” will be pinned to it.</div>` : null}
    </${EV.Sheet}>`;
  };

  EV.sheets.renameCategory = function ({ categoryId }) {
    const c = EV.S.categories.find((x) => x.id === categoryId);
    const [txt, setTxt] = useState(c.name);
    const save = () => { c.name = txt.trim() || c.name; EV.log("category_rename", { id: c.id, name: c.name }); EV.closeSheet(); };
    return html`<${EV.Sheet} title="Rename category" left=${h(Cancel)} right=${html`<button class="text-btn strong" onClick=${save}>Save</button>`} size="medium">
      <div class="field" style="margin-top:8px"><input value=${txt} onInput=${(e) => setTxt(e.currentTarget.value)} aria-label="Category name" /></div>
    </${EV.Sheet}>`;
  };
})();
