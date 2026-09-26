// Subagents, the read-only subagent transcript, files, the Reader (plans and
// documents with comments and review), and the artifact viewer with proposals.
(function () {
  const EV = window.EV;
  const { html, h, useRef, useEffect, useLayoutEffect, useState, Done, Cancel } = EV;
  const I = EV.I;

  // Back for the quiet screens (the Reader, the artifact viewer): alerts that
  // arrive while you read are held, and a small amber dot on Back says so.
  EV.HeldBack = function () {
    const held = EV.S.held.length;
    return html`<button class="icon-btn back-btn" onClick=${EV.pop} aria-label=${held ? "Back. " + held + " new while you read" : "Back"}>${I.chevL({ s: 22 })}${held ? html`<span class="badge">${held}</span>` : null}</button>`;
  };

  // ---------- subagents ----------
  function subMark(g) {
    // One pulse meter per view: here that's the session's, so a running
    // subagent gets a plain running mark.
    if (g.state === "running") return html`<span class="mk"><span class="run-dot"></span></span>`;
    if (g.state === "failed") return html`<span class="mk danger">${I.failed()}</span>`;
    return html`<span class="mk low">${I.check({ s: 16 })}</span>`;
  }

  function SubRow({ g, sessionId, parent }) {
    const tap = () => { EV.log("subagent_open", { sessionId, subagentId: g.id, from: "subagents" }); EV.push("subagent", { sessionId, subId: g.id }); };
    const stopping = EV.S.stopRequests[g.id];
    const body = html`<div class="row">
      <div class="mark">${subMark(g)}</div>
      <div style="min-width:0">
        <div class="l1"><span class="title" style="font-size:16px">${g.title}</span><span class="age">${EV.subTime(g)}</span></div>
        <div class=${"why" + (g.state === "failed" ? " danger" : "")}>${stopping && g.state !== "stopped" ? "Stop requested from the coordinator" : g.line}</div>
        <div class="meta">${parent ? html`<span class="from">from ${parent}</span><span class="sep"></span>` : null}${g.model !== (EV.sess(sessionId) || {}).model ? html`<span>${EV.modelLabel(g.model)}</span><span class="sep"></span>` : null}${g.lane ? html`<span class="host">${I.branch({ s: 12 })}<span class="mono">${g.lane}</span></span><span class="sep"></span>` : null}<span>${g.tokens} tokens</span></div>
      </div>
    </div>`;
    return h(EV.SwipeRow, { onTap: tap }, body);
  }

  function Subagents({ sessionId }) {
    const S = EV.S;
    const s = EV.sess(sessionId);
    const list = S.subagents[sessionId] || [];
    const t = EV.tally(s) || { run: 0, fail: 0, done: 0 };
    const [filter, setFilter] = useState("all");
    const [q, setQ] = useState("");
    const [doneOpen, setDoneOpen] = useState(false);
    const ok = (g) => !q || g.title.toLowerCase().includes(q.toLowerCase());
    const flat = [];
    const walk = (arr, parent) => (arr || []).forEach((g) => { flat.push({ g, parent }); walk(g.children, g.title); });
    walk(list, null);
    const by = (st) => flat.filter((x) => x.g.state === st && ok(x.g));
    const running = by("running").sort((a, b) => a.g.ago - b.g.ago);
    const groups = { failed: by("failed"), running, done: by("done").concat(by("stopped")) };
    const chip = (k, label, n) => html`<button class=${"chip" + (filter === k ? " on" : "")} onClick=${() => { setFilter(k); EV.log("subagent_filter", { sessionId, filter: k }); }}>${label} <span class="n">${n}</span></button>`;
    const show = (k) => filter === "all" || filter === k;
    return html`<div style="display:flex;flex-direction:column;height:100%">
      <div class="nav"><div class="nav-row">
        <div class="lead"><button class="icon-btn back-btn" onClick=${EV.pop} aria-label="Back">${I.chevL({ s: 22 })}</button></div>
        <div class="nav-title" style="cursor:default"><div class="t">Subagents · ${EV.tallyTotal(t)}</div><div class="s">${s.title}</div></div>
        <div class="trail"></div>
      </div></div>
      <div class="scroll">
        <div style="padding:12px 16px 4px">${h(EV.Strip, { t, wide: true })}</div>
        <div class="chips" style="top:0">${chip("all", "All", EV.tallyTotal(t))}${chip("failed", html`<span class="lg fail"></span>Failed`, t.fail)}${chip("running", html`<span class="lg run"></span>Running`, t.run)}${chip("done", html`<span class="lg done"></span>Done`, t.done)}</div>
        ${list.length > 8 ? html`<div class="search-field">${I.search({ s: 16 })}<input placeholder="Filter subagents" value=${q} onInput=${(e) => setQ(e.currentTarget.value)} aria-label="Filter subagents" /></div>` : null}
        ${show("failed") && groups.failed.length ? html`<div class="sec-h">Failed <span class="n">${groups.failed.length}</span></div>${groups.failed.map((x) => html`<${SubRow} key=${x.g.id} g=${x.g} parent=${x.parent} sessionId=${sessionId} />`)}` : null}
        ${show("running") && groups.running.length ? html`<div class="sec-h">Running <span class="n">${groups.running.length}</span></div>${groups.running.map((x) => html`<${SubRow} key=${x.g.id} g=${x.g} parent=${x.parent} sessionId=${sessionId} />`)}` : null}
        ${show("done") && groups.done.length ? (filter === "done" ? html`<div class="sec-h">Done <span class="n">${groups.done.length}</span></div>${groups.done.map((x) => html`<${SubRow} key=${x.g.id} g=${x.g} parent=${x.parent} sessionId=${sessionId} />`)}`
          : html`<button class=${"fold" + (doneOpen ? " open" : "")} style="margin-top:8px" onClick=${() => setDoneOpen(!doneOpen)}><span class="lbl">Done <span class="cnt">${groups.done.length}</span></span><span class="chev">${I.chevR()}</span></button>${doneOpen ? groups.done.map((x) => html`<${SubRow} key=${x.g.id} g=${x.g} parent=${x.parent} sessionId=${sessionId} />`) : null}`) : null}
        <div style="height:30px"></div>
      </div>
    </div>`;
  }
  EV.screens.subagents = Subagents;

  function subTranscript(g, parent) {
    const items = [
      { t: "sys", text: "Started by “" + parent.title + "”" },
      { t: "user", text: "Mandate: " + g.title + ". Report what you find; don't change unrelated code." },
      { t: "agent", md: "Starting. I'll look at the relevant code first." },
      { t: "act", live: g.state === "running", steps: [
        { i: "Read the relevant files", g: "agent/retirement.go, agent/retirement_test.go", s: "ok" },
        { i: g.state === "failed" ? "Ran the tests" : g.line, g: g.state === "failed" ? "go test ./agent/... -run Retirement -count=3" : "", s: g.state === "failed" ? "fail" : g.state === "running" ? "run" : "ok",
          out: g.state === "failed" ? "--- FAIL: TestRetirementTreeSettleDrainsPendingRootAttention (0.44s)\n    retirement_test.go:212: settle finished before drain\nFAIL (attempt 3 of 3)" : null },
      ] },
    ];
    if (g.state === "failed") items.push({ t: "agent", md: "The fix I tried moves the lock, but the test still fails on the third run. I think the drain signal is lost when settle holds the lock. I'm out of attempts." });
    if (g.state === "done") items.push({ t: "agent", md: "**Report:** " + g.line + "." });
    if (g.state === "stopped") items.push({ t: "sys", text: "Stopped by the coordinator at your request" });
    return items;
  }

  function SubagentScreen({ sessionId, subId }) {
    const S = EV.S;
    const s = EV.sess(sessionId);
    const g = EV.findSub(sessionId, subId) || { title: "Subagent", state: "done", line: "", model: s.model, ago: 60, elapsed: 60, tokens: "0" };
    const tr = subTranscript(g, s);
    const canStop = ["running", "failed"].includes(g.state) && !S.stopRequests[subId];
    const lbl = { running: "Running", failed: "Failed", done: "Done", stopped: "Stopped" }[g.state];
    return html`<div style="display:flex;flex-direction:column;height:100%">
      <div class="nav"><div class="nav-row">
        <div class="lead"><button class="icon-btn back-btn" onClick=${EV.pop} aria-label="Back">${I.chevL({ s: 22 })}</button></div>
        <div class="nav-title" style="cursor:default"><div class="t">${g.title}</div><div class="s">${subMark(g)}<span>${lbl}</span></div></div>
        <div class="trail"></div>
      </div></div>
      <div class="scroll">
        <div class="ro-banner">Subagent of <b>${s.title}</b>. Subagents take direction from their coordinator, so you talk to it through the coordinator.</div>
        <div class="tx">${tr.map((it, i) => h(SubItem, { key: i, it }))}</div>
      </div>
      <div class="bottom"><div class="readonly-bar">
        ${canStop ? html`<button class="btn" onClick=${() => { EV.log("subagent_stop_sheet", { sessionId, subagentId: subId }); EV.openSheet("stopSub", { sessionId, subId }); }}>${I.stop({ s: 12 })} ${g.state === "running" ? "Ask coordinator to stop it" : "Ask coordinator to stop retrying it"}</button>` : S.stopRequests[subId] ? html`<span class="btn" style="flex:1;border-color:transparent;color:var(--ink-mid)">Stop requested</span>` : null}
        <button class="btn primary" onClick=${() => { EV.log("open_coordinator", { sessionId, from: subId }); EV.openSession(sessionId, { from: "subagent" }); }}>Open coordinator</button>
      </div></div>
    </div>`;
  }
  function SubItem({ it }) {
    // Reuse the session renderer's look without session-specific behavior.
    if (it.t === "user") return html`<div class="u-msg"><div class="u-bubble">${it.text}</div><div class="u-cap">From the coordinator</div></div>`;
    if (it.t === "agent") return html`<div class="a-msg" dangerouslySetInnerHTML=${{ __html: EV.md(it.md) }}></div>`;
    if (it.t === "sys") return html`<div class="sys">${I.diamond({ s: 12 })}<span>${it.text}</span></div>`;
    if (it.t === "act") {
      return html`<div class="act open"><div class="act-h"><span class="chev">${I.chevR({ s: 12 })}</span><span>${it.live ? html`<span style="color:var(--alive-ink);font-weight:600">Working</span> · ` : null}${it.steps.length} steps</span></div>
        <div class="steps">${it.steps.map((st) => html`<div><div class="step"><span class="st">${st.s === "fail" ? html`<span class="mk danger" style="width:14px;height:14px">${I.failed({ s: 13 })}</span>` : st.s === "run" ? h(EV.Pulse, { values: [0.3, 0.6, 0.4, 0.9, 0.5, 0.8, 0.7] }) : html`<span style="color:var(--ink-low);display:flex">${I.check({ s: 13 })}</span>`}</span><span><span class="in">${st.i}</span>${st.g ? html`<br /><span class="tg">${st.g}</span>` : null}</span></div>
        ${st.out ? html`<div class="evidence">${st.out.split("\n").map((l) => html`<div class=${/FAIL|fail/.test(l) ? "fail" : ""}>${l}</div>`)}</div>` : null}</div>`)}</div></div>`;
    }
    return null;
  }
  EV.screens.subagent = SubagentScreen;

  EV.sheets.stopSub = function ({ sessionId, subId }) {
    const S = EV.S;
    const s = EV.sess(sessionId);
    const g = EV.findSub(sessionId, subId);
    const [txt, setTxt] = useState("Stop subagent “" + g.title + "”" + (g.state === "failed" ? ": it has failed three times. Tell me what you'll do instead." : ": it's no longer needed."));
    const working = EV.inTurn(s);
    const go = (mode) => {
      EV.sendMessage(s, txt, mode);
      S.stopRequests[subId] = true;
      EV.log("subagent_stop_request", { sessionId, subagentId: subId, mode, text: txt });
      EV.closeSheet();
      EV.toast("Asked the coordinator to stop it");
      EV.later(3000, () => { g.state = "stopped"; g.line = "Stopped at your request"; EV.log("subagent_stopped", { sessionId, subagentId: subId }); EV.toast("“" + g.title + "” stopped"); EV.update(); });
    };
    return html`<${EV.Sheet} title="Stop subagent" left=${h(Cancel)} size="medium">
      <div class="gfoot" style="padding:4px 32px 10px">Subagents take direction from their coordinator. This sends the coordinator a message asking it to stop “${g.title}”.</div>
      <div class="field"><textarea rows="4" value=${txt} onInput=${(e) => setTxt(e.currentTarget.value)} aria-label="Message to the coordinator"></textarea></div>
      <div style="display:flex;gap:8px;justify-content:flex-end;padding:14px 16px">
        ${working ? html`<button class="btn" onClick=${() => go("queue")}>Queue</button><button class="btn primary" onClick=${() => go("steer")}>Steer now</button>` : html`<button class="btn primary" onClick=${() => go("send")}>Send to coordinator</button>`}
      </div>
      ${working ? html`<div class="gfoot">Steer reaches the coordinator at its next step. Queue waits until its current turn ends.</div>` : null}
    </${EV.Sheet}>`;
  };

  // ---------- files ----------
  EV.sheets.files = function ({ sessionId }) {
    const S = EV.S;
    const tr = S.transcripts[sessionId] || [];
    const seen = new Set();
    const items = [];
    [...tr].reverse().forEach((x) => {
      if (x.t === "doc" && !seen.has(x.path)) { seen.add(x.path); items.push({ kind: x.kind, path: x.path }); }
      if (x.t === "art" && !seen.has(x.id)) { seen.add(x.id); items.push({ kind: "Artifact", id: x.id }); }
    });
    const open = (it) => {
      EV.log("file_open", { sessionId, target: it.path || it.id, from: "files" });
      EV.closeAllSheets();
      if (it.kind === "Artifact") EV.push("artifact", { id: it.id, sessionId });
      else EV.push("reader", { path: it.path, sessionId });
    };
    return html`<${EV.Sheet} title="Files & artifacts" right=${h(Done)} size="medium">
      <div class="group">${items.map((it) => {
        if (it.kind === "Artifact") {
          const a = S.artifacts[it.id];
          return h(EV.Gi, { key: it.id, icon: I.artifact({ s: 17 }), label: a.title, sub: "Artifact · v" + a.version + " · updated " + EV.fmtAgo(a.ago * 1000) + " ago", chev: true, onClick: () => open(it) });
        }
        const d = S.docs[it.path] || { lines: 0, ago: 0, changed: [] };
        return h(EV.Gi, { key: it.path, icon: I.doc({ s: 17 }), label: EV.docTitle(it.path), sub: it.kind + " · " + it.path.split("/").pop() + " · " + EV.fmtAgo(d.ago * 1000) + " ago", value: EV.docChanged(it.path) ? html`<span class="dot"></span>` : null, chev: true, onClick: () => open(it) });
      })}</div>
      ${!items.length ? html`<div class="empty">This session hasn't written or linked any files yet.</div>` : null}
    </${EV.Sheet}>`;
  };

  // ---------- reader ----------
  function blocksOf(md) {
    const toks = marked.lexer(md);
    const out = [];
    toks.forEach((tk) => {
      if (tk.type === "space") return;
      const arr = [tk];
      arr.links = toks.links;
      out.push({ html: marked.parser(arr), text: tk.raw, type: tk.type, depth: tk.depth, title: tk.type === "heading" ? tk.text : null });
    });
    return out;
  }
  // A block's text as a comment quotes it: a heading without its #s.
  const blockText = (b) => b.text.replace(/^#+\s*/, "");
  // What a comment quotes: the text it was left on, else its whole block.
  const quoteOf = (blocks, c) => c.quote || blockText(blocks[c.block]);

  function RBlock({ b, i, path, sessionId, changed, comments, onComment }) {
    const count = comments.filter((c) => c.item == null).length;
    const perItem = comments.filter((c) => c.item != null).map((c) => c.item);
    const box = useRef(null);
    // A comment on a list item marks that item, not the list's first line.
    useLayoutEffect(() => {
      const el = box.current;
      if (!el) return;
      el.querySelectorAll(".cmark.li").forEach((m) => m.remove());
      const items = el.querySelectorAll("li");
      [...new Set(perItem)].forEach((k) => {
        const li = items[k];
        if (!li) return;
        const m = document.createElement("button");
        m.className = "cmark li";
        m.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 5.5A2.5 2.5 0 0 1 6.5 3h11A2.5 2.5 0 0 1 20 5.5v8a2.5 2.5 0 0 1-2.5 2.5H10l-4.5 4v-4A2.5 2.5 0 0 1 4 13.5z" /></svg> ' + perItem.filter((x) => x === k).length;
        m.setAttribute("aria-label", "Comments on this item");
        m.addEventListener("pointerdown", (e) => e.stopPropagation());
        m.addEventListener("pointerup", (e) => e.stopPropagation());
        m.addEventListener("click", (e) => { e.stopPropagation(); EV.openSheet("comments", { path, sessionId }); });
        li.style.position = "relative";
        li.appendChild(m);
      });
    });
    const lp = EV.useLongPress((e) => {
      // In a list, comments attach to the bullet under the finger, not the whole list.
      const t = e && e.target && e.target.closest ? e.target : null;
      const li = t ? t.closest("li") : null;
      const blk = t ? t.closest(".rblock") : null;
      let item = null;
      let quote = blockText(b);
      if (li && blk) { item = [...blk.querySelectorAll("li")].indexOf(li); quote = li.textContent.trim(); li.classList.add("li-sel"); }
      EV.log("block_menu", { path, block: i, item });
      EV.openMenu({ kind: "list", top: 280, title: item != null ? "List item" : "Paragraph", block: i, path, liEl: item != null ? li : null, preview: html`<div class="preview"><div class="px" style="border:0;margin:0;padding:0">${quote.replace(/[#*`>|]/g, "").slice(0, 220)}</div></div>`, items: [
        { label: "Comment", icon: I.bubble({ s: 18 }), run: () => onComment(i, item, quote) },
        { label: "Quote in reply", icon: I.quote({ s: 18 }), run: () => { EV.log("quote_from_doc", { path, block: i, item }); EV.quoteIntoDraft(sessionId, quote); } },
        { label: "Copy", icon: I.doc({ s: 18 }), run: () => EV.toast("Copied") },
      ] });
    });
    const pressed = EV.S.menu && EV.S.menu.path === path && EV.S.menu.block === i && !EV.S.menu.liEl;
    return html`<div class=${"rblock" + (changed ? " changed" : "") + (pressed ? " sel" : "")} data-block=${i} ...${lp}>
      <div ref=${box} dangerouslySetInnerHTML=${{ __html: b.html }}></div>
      ${count ? html`<button class="cmark" onPointerDown=${(e) => e.stopPropagation()} onPointerUp=${(e) => e.stopPropagation()} onClick=${(e) => { e.stopPropagation(); EV.openSheet("comments", { path, sessionId }); }}>${I.bubble({ s: 12 })} ${count}</button>` : null}
    </div>`;
  }

  function Reader({ path, sessionId }) {
    const S = EV.S;
    const doc = S.docs[path];
    const s = EV.sess(sessionId);
    const scrollRef = useRef(null);
    const firstRead = useRef(!S.readDocs[path]);
    const [ci, setCi] = useState(-1);
    // The document's own heading is its title, so the nav bar shows the title
    // only once that heading has scrolled out of view.
    const [pastTitle, setPastTitle] = useState(false);
    const blocks = EV.useMemoBlocks(path, doc.md);
    const title = EV.docTitle(path);
    const comments = S.comments[path] || [];
    const changed = firstRead.current ? doc.changed : [];
    useEffect(() => { S.readDocs[path] = true; EV.log("doc_read", { path }); }, []);
    useEffect(() => {
      if (S.jumpBlock != null && scrollRef.current) {
        const el = scrollRef.current.querySelector('[data-block="' + S.jumpBlock + '"]');
        S.jumpBlock = null;
        if (el) { scrollRef.current.scrollTo({ top: el.offsetTop - 60, behavior: "smooth" }); el.classList.remove("flashc"); void el.offsetWidth; el.classList.add("flashc"); }
      }
    });
    const onComment = (i, item, quote) => EV.openSheet("comment", { path, sessionId, block: i, item, quote });
    const goChange = (dir) => {
      const n = (ci + dir + changed.length) % changed.length;
      setCi(n);
      S.jumpBlock = changed[n];
      EV.log("next_change", { path, index: n });
      EV.update();
    };
    // Remember the reading position, and leave a "Continue reading" trail on
    // the Board when you leave a document before the end.
    useLayoutEffect(() => {
      const el = scrollRef.current;
      if (el && S.readerPos[path]) el.scrollTop = S.readerPos[path];
      return () => {
        if (!el) return;
        const pct = el.scrollHeight <= el.clientHeight ? 1 : (el.scrollTop + el.clientHeight) / el.scrollHeight;
        S.readerPos[path] = el.scrollTop;
        S.lastRead = pct < 0.97 ? { path, sessionId, title, pct, at: Date.now() } : null;
      };
    }, [path]);
    const hint = !comments.length && !S.readerHintDismissed;
    return html`<div style="display:flex;flex-direction:column;height:100%">
      <div class="nav"><div class="nav-row">
        <div class="lead">${h(EV.HeldBack)}</div>
        <div class="nav-title" style="cursor:default">${pastTitle ? html`<div class="t">${title}</div><div class="s"><span style="overflow:hidden;text-overflow:ellipsis;min-width:0">${doc.kind} · updated ${EV.fmtAgo(doc.ago * 1000)} ago</span></div>` : null}</div>
        <div class="trail">
          <button class="icon-btn" aria-label="Outline" onClick=${() => EV.openSheet("outline", { path })}>${I.outline()}</button>
          <button class="icon-btn" aria-label="Document menu" onClick=${() => EV.openMenu({ kind: "list", top: 96, right: true, title: "Document", items: [
            { label: "Open session", icon: I.chevR({ s: 16 }), run: () => EV.openSession(sessionId, { from: "reader" }) },
            { label: "Copy path", icon: I.doc({ s: 18 }), run: () => EV.toast("Path copied") },
            { label: "Copy text", icon: I.doc({ s: 18 }), run: () => EV.toast("Text copied") },
          ] })}>${I.dots()}</button>
        </div>
      </div></div>
      <div class="scroll" ref=${scrollRef} onScroll=${(e) => { const past = e.currentTarget.scrollTop > 64; if (past !== pastTitle) setPastTitle(past); }}>
        <div class="doc-cap">${doc.kind} · updated ${EV.fmtAgo(doc.ago * 1000)} ago${changed.length ? html` · <span class="chg">${changed.length} change${changed.length > 1 ? "s" : ""} since you read it yesterday</span>` : null}</div>
        <article class="reader" aria-label=${path}>
          ${blocks.map((b, i) => h(RBlock, { key: i, b, i, path, sessionId, changed: changed.includes(i), comments: comments.filter((c) => c.block === i), onComment }))}
        </article>
      </div>
      <div class="bottom">${comments.length ? null : html`<div class="rb-tip">${I.bubble({ s: 14 })} Touch and hold a paragraph to comment on it</div>`}<div class="review-bar">
        ${comments.length ? html`<button class="btn quiet" onClick=${() => EV.openSheet("comments", { path, sessionId })}>${I.bubble({ s: 16 })} ${comments.length}</button>` : html`<span></span>`}
        ${changed.length ? html`<span class="chg-step"><button class="icon-btn" aria-label="Previous change" onClick=${() => goChange(-1)}>${I.chevL({ s: 16 })}</button><span>${ci < 0 ? changed.length + " changes" : "Change " + (ci + 1) + " of " + changed.length}</span><button class="icon-btn" aria-label="Next change" onClick=${() => goChange(1)}>${I.chevR({ s: 16 })}</button></span>` : null}
        <span style="display:flex;gap:6px">
          <button class="btn primary" onClick=${() => { EV.log("review_sheet", { path, comments: comments.length }); EV.openSheet("review", { path, sessionId }); }}>Send review</button>
        </span>
      </div></div>
    </div>`;
  }
  const blockCache = new Map();
  EV.useMemoBlocks = (path, md) => { if (!blockCache.has(path)) blockCache.set(path, blocksOf(md)); return blockCache.get(path); };
  EV.screens.reader = Reader;

  EV.sheets.outline = function ({ path }) {
    const S = EV.S;
    const blocks = EV.useMemoBlocks(path, S.docs[path].md);
    return html`<${EV.Sheet} title="Outline" right=${h(Done)} size="medium">
      <div class="group outline-list">${blocks.map((b, i) => (b.type === "heading" ? h(EV.Gi, { key: i, cls: b.depth > 2 ? "l2" : "", label: b.title, onClick: () => { S.jumpBlock = i; EV.log("outline_jump", { path, block: i }); EV.closeSheet(); } }) : null))}</div>
    </${EV.Sheet}>`;
  };

  EV.sheets.comment = function ({ path, sessionId, block, item, quote }) {
    const S = EV.S;
    const blocks = EV.useMemoBlocks(path, S.docs[path].md);
    const [txt, setTxt] = useState("");
    const ref = useRef(null);
    useEffect(() => { ref.current && ref.current.focus(); }, []);
    const save = () => {
      if (!txt.trim()) return;
      (S.comments[path] = S.comments[path] || []).push({ block, item, quote, text: txt.trim() });
      EV.log("comment_add", { path, block, item, text: txt.trim() });
      EV.closeSheet();
      EV.toast("Comment added");
    };
    return html`<${EV.Sheet} title="Comment" left=${h(Cancel)} right=${html`<button class="text-btn strong" disabled=${!txt.trim()} onClick=${save}>Add</button>`} size="medium">
      <div class="comment-q">${(quote || blockText(blocks[block])).slice(0, 280)}</div>
      <div class="field"><textarea ref=${ref} rows="4" placeholder="What should change?" value=${txt} onInput=${(e) => setTxt(e.currentTarget.value)} aria-label="Comment"></textarea></div>
      <div class="gfoot">Comments stay with this document until you send your review.</div>
    </${EV.Sheet}>`;
  };

  EV.sheets.comments = function ({ path, sessionId }) {
    const S = EV.S;
    const blocks = EV.useMemoBlocks(path, S.docs[path].md);
    const list = S.comments[path] || [];
    return html`<${EV.Sheet} title=${"Comments · " + list.length} right=${h(Done)} size="medium">
      ${!list.length ? html`<div class="empty"><b>No comments yet</b>Touch and hold a paragraph to comment on it.</div>` : html`<div class="group">${list.map((c, i) => html`<div class="gi static" key=${i} style="display:block">
        <div style="font:14px/19px var(--read-font);color:var(--ink-mid);border-left:2px solid var(--edge-strong);padding-left:8px">${quoteOf(blocks, c).slice(0, 140)}</div>
        <div style="margin-top:6px">${c.text}</div>
        <div style="display:flex;gap:8px;margin-top:6px"><button class="mini-btn" onClick=${() => { S.jumpBlock = c.block; EV.closeSheet(); }}>Show</button><button class="mini-btn danger" onClick=${() => { list.splice(i, 1); EV.log("comment_delete", { path }); EV.update(); }}>Delete</button></div>
      </div>`)}</div>`}
      ${list.length ? html`<div style="padding:14px 16px"><button class="btn primary big" onClick=${() => { EV.closeSheet(); EV.openSheet("review", { path, sessionId }); }}>Send review</button></div>` : null}
    </${EV.Sheet}>`;
  };

  EV.sheets.review = function ({ path, sessionId }) {
    const S = EV.S;
    const s = EV.sess(sessionId);
    const blocks = EV.useMemoBlocks(path, S.docs[path].md);
    const list = S.comments[path] || [];
    const [verdict, setVerdict] = useState(null);
    const [note, setNote] = useState("");
    const working = EV.inTurn(s);
    const build = () => {
      let m = "Review of " + path + ": " + (verdict === "Comment" ? "comments" : verdict.toLowerCase()) + ".";
      list.forEach((c) => { m += "\n\n> " + quoteOf(blocks, c).split("\n")[0].slice(0, 160) + "\n" + c.text; });
      if (note.trim()) m += "\n\nOverall: " + note.trim();
      return m;
    };
    const send = (mode) => {
      const text = build();
      EV.sendMessage(s, text, mode, "review");
      EV.log("review_sent", { path, sessionId, verdict, comments: list.length, mode, note: note.trim() });
      S.comments[path] = [];
      EV.closeAllSheets();
      EV.toast("Review sent");
      const t = EV.top();
      if (t.name === "reader") EV.pop();
    };
    return html`<${EV.Sheet} title="Review" left=${h(Cancel)} size="large">
      <div class="gfoot" style="padding:4px 32px 10px">To “${s.title}” · ${path.split("/").pop()}</div>
      ${h(EV.Seg, { options: ["Approve", "Request changes", "Comment"], value: verdict, onChange: setVerdict })}
      ${verdict ? null : html`<div class="gfoot" style="padding-top:6px">Choose one to send your review.</div>`}
      <div class="glabel">Overall note</div>
      <div class="field"><textarea rows="3" placeholder=${verdict === "Approve" ? "Optional: anything to keep in mind" : "Optional: the gist of what to change"} value=${note} onInput=${(e) => setNote(e.currentTarget.value)} aria-label="Overall note"></textarea></div>
      <div class="glabel">Comments · ${list.length}</div>
      ${list.length ? html`<div class="group">${list.map((c, i) => html`<div class="gi static" key=${i} style="display:block"><div style="font:14px/19px var(--read-font);color:var(--ink-mid);border-left:2px solid var(--edge-strong);padding-left:8px">${quoteOf(blocks, c).slice(0, 120)}</div><div style="margin-top:5px">${c.text}</div></div>`)}</div>`
        : html`<div class="gfoot">No comments. Touch and hold a paragraph in the document to add one.</div>`}
      <div style="display:flex;gap:8px;justify-content:flex-end;padding:18px 16px 8px">
        ${working ? html`<button class="btn" disabled=${!verdict} onClick=${() => send("queue")}>Queue</button><button class="btn primary" disabled=${!verdict} onClick=${() => send("steer")}>Steer now</button>` : html`<button class="btn primary big" disabled=${!verdict} onClick=${() => send("send")}>Send review</button>`}
      </div>
      ${working ? html`<div class="gfoot">The session is working. Steer reaches it at its next step; Queue waits until its turn ends.</div>` : null}
    </${EV.Sheet}>`;
  };

  // ---------- artifact viewer ----------
  function ArtifactViewer({ id, sessionId }) {
    const S = EV.S;
    const a = S.artifacts[id];
    const ref = useRef(null);
    const [ready, setReady] = useState(false);
    const [failed, setFailed] = useState(false);
    const [saved, setSaved] = useState(0);
    useEffect(() => {
      const onMsg = (e) => {
        if (!ref.current || e.source !== ref.current.contentWindow) return;
        const d = e.data || {};
        if (d.type === "ready") setReady(true);
        if (d.type === "state") { S.artifactState[id] = Object.assign({}, S.artifactState[id], d.state); setSaved(Date.now()); EV.log("artifact_state", { id, state: d.state }); }
        if (d.type === "ui/message") { EV.log("artifact_proposal_shown", { id, text: d.text }); EV.openSheet("proposal", { artifactId: id, sessionId, text: d.text }); }
      };
      window.addEventListener("message", onMsg);
      const t = setTimeout(() => setFailed((f) => f || !ref.current || !ref.current.dataset.ready), 3000);
      return () => { window.removeEventListener("message", onMsg); clearTimeout(t); };
    }, []);
    useEffect(() => { if (ready && ref.current) ref.current.dataset.ready = "1"; });
    const showSaved = saved && Date.now() - saved < 2500;
    if (showSaved) setTimeout(() => EV.update(), 2600);
    return html`<div style="display:flex;flex-direction:column;height:100%">
      <div class="nav"><div class="nav-row">
        <div class="lead">${h(EV.HeldBack)}</div>
        <div class="nav-title" style="cursor:default"><div class="t">${a.title}</div><div class="s">${showSaved ? html`<span class="saved">Saved</span>` : html`<span>Artifact · v${a.version} · updated ${EV.fmtAgo(a.ago * 1000)} ago</span>`}</div></div>
        <div class="trail"><button class="icon-btn" aria-label="Artifact menu" onClick=${() => EV.openMenu({ kind: "list", top: 96, right: true, title: "Artifact", items: [
          { label: "About this artifact", icon: I.artifact({ s: 18 }), run: () => EV.openSheet("aboutArtifact", { id }) },
          { label: "Open session", icon: I.chevR({ s: 16 }), run: () => EV.openSession(sessionId, { from: "artifact" }) },
        ] })}>${I.dots()}</button></div>
      </div></div>
      ${failed && !ready ? html`<div class="scroll"><div class="empty"><b>${a.title}</b>${a.summary}<div style="margin-top:12px">This artifact can't run over this connection. Open it on a computer on the same network as the hub.</div></div></div>`
        : html`<iframe ref=${ref} class="art-frame" title=${a.title} sandbox="allow-scripts" srcdoc=${a.html}></iframe>`}
    </div>`;
  }
  EV.screens.artifact = ArtifactViewer;

  EV.sheets.aboutArtifact = function ({ id }) {
    const a = EV.S.artifacts[id];
    return html`<${EV.Sheet} title="About this artifact" right=${h(Done)} size="medium">
      <div class="group">${h(EV.Gi, { label: a.title, sub: a.summary })}${h(EV.Gi, { label: "Version", value: "v" + a.version })}${h(EV.Gi, { label: "Updated", value: EV.fmtAgo(a.ago * 1000) + " ago" })}${h(EV.Gi, { label: "Diagnostics", value: "None" })}</div>
      <div class="gfoot">Artifacts run in a sandbox with no network access. They can save their own state and propose messages, but nothing is sent until you choose.</div>
    </${EV.Sheet}>`;
  };

  EV.sheets.proposal = function ({ artifactId, sessionId, text }) {
    const S = EV.S;
    const s = EV.sess(sessionId);
    const [txt, setTxt] = useState(text);
    const working = EV.inTurn(s);
    const act = (mode) => {
      EV.log("artifact_proposal", { id: artifactId, action: mode, text: txt });
      if (mode === "discard") { EV.closeSheet(); EV.toast("Discarded"); return; }
      EV.sendMessage(s, txt, mode, "artifact");
      const m = /layout ([ABC])/.exec(txt);
      S.artifactState[artifactId] = Object.assign({}, S.artifactState[artifactId], { sent: m ? m[1] : "an option" });
      EV.closeAllSheets();
      EV.toast(mode === "queue" ? "Queued for the session" : "Sent to the session");
      EV.pop();
    };
    return html`<${EV.Sheet} title="From the artifact" left=${html`<button class="text-btn" onClick=${() => act("discard")}>Discard</button>`} size="medium">
      <div class="gfoot" style="padding:4px 32px 10px">“${S.artifacts[artifactId].title}” wants to send this to “${s.title}”. Nothing is sent until you choose.</div>
      <div class="field"><textarea rows="4" value=${txt} onInput=${(e) => setTxt(e.currentTarget.value)} aria-label="Proposed message"></textarea></div>
      <div style="display:flex;gap:8px;justify-content:flex-end;padding:14px 16px">
        ${working ? html`<button class="btn" onClick=${() => act("queue")}>Queue</button><button class="btn primary" onClick=${() => act("steer")}>Steer now</button>` : html`<button class="btn primary big" onClick=${() => act("send")}>Send to session</button>`}
      </div>
    </${EV.Sheet}>`;
  };
})();
