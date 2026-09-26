// Live simulation: pulse meters, rotating activity lines, subagent churn,
// and scripted events (a new question, a failure, a host going offline,
// connection loss) that the usability moderator can trigger by name.
(function () {
  const EV = window.EV;
  const scripts = {
    "s-pr2138": ["Waiting on 31 subagents", "Reading a subagent report", "Waiting on 30 subagents"],
    "s-tasklist": ["Running go test ./cmd/evener-hub/...", "Editing TaskCard.tsx", "Running npm test -- TaskCard", "Reading TasksPanel.tsx"],
    "s-stumble": ["Editing agent/tool_repair.go", "Running go test ./agent/internal/tool/...", "Reading llm/types.go"],
    "s-gateway": ["Thinking", "Reading auth/token.go", "Sketching the command surface"],
    "s-resume": ["Reading agent/session_resume.go", "Running go test ./agent -run Resume"],
    "s-readintent": ["Running make lint", "Editing ToolRow.tsx", "Running npm test"],
    "s-landing": ["Writing site/index.md", "Reading the style guide"],
    "s-sdk": ["Running npm test in appwire-client/typescript", "Editing askDock.ts"],
  };
  let tick = 0;

  function step() {
    const S = EV.S;
    if (!S) return;
    tick++;
    const now = Date.now();
    if (S.conn === "live") {
      for (const s of S.sessions) {
        if (s.state !== "working" || s.stuck || !s.live) continue;
        const host = EV.host(s.host);
        if (host && host.state === "offline") continue;
        const burst = Math.random() < 0.85 ? 0.3 + Math.random() * 0.7 : 0;
        const p = s.pulse.slice();
        p[6] = Math.min(1, Math.max(p[6] * 0.6, burst));
        if (tick % 5 === 0) { p.shift(); p.push(burst * 0.6); }
        s.pulse = p;
        if (burst) s.updatedAt = now;
        if (tick % 9 === (s.id.length % 9) && scripts[s.id]) {
          const list = scripts[s.id];
          const i = (list.indexOf(s.activity) + 1) % list.length;
          s.activity = list[i];
          s.actAt = now;
        }
        s.usage.in += 12000 + Math.round(Math.random() * 40000);
        s.usage.out += 400 + Math.round(Math.random() * 900);
      }
      // A little churn in the big swarm so it reads as alive.
      if (tick % 12 === 0) {
        const swarm = S.subagents["s-pr2138"] || [];
        const run = swarm.filter((g) => g.state === "running" && !g.children);
        if (run.length > 26) {
          const g = run[(tick / 12) % run.length | 0];
          g.state = "done"; g.line = "Tests pass under -race"; g.ago = 5;
          const pr = EV.sess("s-pr2138");
          if (pr) { pr.activity = "Waiting on " + (run.length - 1) + " subagents"; pr.actAt = now; }
        }
      }
    }
    EV.update();
  }

  function flash(s) { EV.S.board.flash[s.id] = Date.now(); }

  const events = {
    question() {
      const s = EV.sess("s-gateway");
      if (!s || s.state === "question") return;
      s.state = "question";
      s.why = "Asks: where should the token command store tokens?";
      s.updatedAt = Date.now();
      s.pulse = [0, 0, 0, 0, 0, 0, 0];
      EV.addItem(s.id, { t: "agent", md: "Before I write the command I need one decision from you." });
      EV.addItem(s.id, { t: "ask", id: "q-gateway" });
      flash(s);
      EV.alert({ kind: "question", sessionId: s.id, why: s.why });
    },
    failure() {
      const s = EV.sess("s-readintent");
      if (!s || s.state === "failed") return;
      s.state = "failed";
      s.why = "Failed: make lint found 3 errors after 3 attempts";
      s.updatedAt = Date.now();
      s.pulse = [0, 0, 0, 0, 0, 0, 0];
      EV.addItem(s.id, { t: "act", steps: [{ i: "Ran the linters", g: "make lint", s: "fail", out: "ToolRow.tsx:88:7  lint/a11y/useButtonType  Provide an explicit type prop\nToolRow.tsx:112:3 lint/style/noNonNullAssertion\nToolRow.test.tsx:41:9 lint/suspicious/noArrayIndexKey\nFound 3 errors." }] });
      EV.addItem(s.id, { t: "err", e: "make lint found 3 errors after 3 attempts", d: "The session stopped because it couldn't make the linters pass. Retry, or send it guidance first.", acts: ["retry"] });
      flash(s);
      EV.alert({ kind: "failed", sessionId: s.id, why: s.why });
    },
    finish() {
      const s = EV.sess("s-resume");
      if (!s || s.state !== "working") return;
      s.state = "yourmove";
      s.unseen = true;
      s.why = "Fixed: resuming a session now replays the original prompt. Tests added.";
      s.updatedAt = Date.now();
      s.pulse = [0, 0, 0, 0, 0, 0, 0];
      EV.addItem(s.id, { t: "agent", md: "Fixed: resuming a session now replays the original prompt. I added two tests that cover resume after a crash and after an upgrade." });
      EV.alert({ kind: "finished", sessionId: s.id, why: s.why });
    },
    approval() {
      const s = EV.sess("s-landing");
      if (!s || s.state !== "working") return;
      s.state = "approval";
      s.why = "Wants to run a command with network access: npm view";
      s.updatedAt = Date.now();
      EV.S.approvals["ap-landing"] = { what: "Wants to use the network", tool: "shell", target: "npm view @astrojs/starlight version", mode: "Sandbox: workspace write · network off" };
      EV.addItem(s.id, { t: "appr", id: "ap-landing" });
      flash(s);
      EV.alert({ kind: "approval", sessionId: s.id, why: s.why });
    },
    many() { events.question(); EV.later(900, events.failure); },
    "host-offline"() {
      const x = EV.host("paradise-park");
      x.state = "offline";
      x.lastError = "ssh: connect to host paradise-park port 22: Operation timed out";
      EV.alert({ kind: "notice", title: "paradise-park is offline", why: EV.liveOnHost(x.id) + " sessions can't be reached", focus: "hosts" });
    },
    "host-online"() { const x = EV.host("paradise-park"); x.state = "connected"; x.lastError = null; },
    reconnecting() {
      EV.S.conn = "reconnecting"; EV.S.connSince = Date.now();
      EV.later(6000, () => { if (EV.S.conn === "reconnecting") { EV.S.conn = "live"; flushOutbox(); EV.update(); } });
    },
    offline() { EV.S.conn = "offline"; EV.S.connSince = Date.now(); },
    online() { EV.S.conn = "live"; flushOutbox(); },
  };

  function flushOutbox() {
    const S = EV.S;
    const box = S.outbox || [];
    S.outbox = [];
    box.forEach((m) => { const s = EV.sess(m.sid); if (s) EV.sendMessage(s, m.text, m.mode); });
    if (box.length) EV.toast("Sent " + box.length + " waiting message" + (box.length > 1 ? "s" : ""));
  }

  const presets = {
    default() {},
    reading(S) {
      S.nav = [{ name: "board", key: 1 }, { name: "session", id: "s-hier", key: 2, from: "board" }, { name: "reader", path: "docs/superpowers/plans/2026-09-25-host-project-hierarchy.md", sessionId: "s-hier", key: 3 }];
      S.navSeq = 3;
      EV.sess("s-hier").unseen = false;
      S.reading = "reader";
    },
    "in-tasklist"(S) { S.nav = [{ name: "board", key: 1 }, { name: "session", id: "s-tasklist", key: 2, from: "board" }]; S.navSeq = 2; },
    "in-pr2138"(S) { S.nav = [{ name: "board", key: 1 }, { name: "session", id: "s-pr2138", key: 2, from: "board" }]; S.navSeq = 2; },
    "host-offline"(S) { const x = S.hosts.find((h) => h.id === "paradise-park"); x.state = "offline"; x.lastError = "ssh: connect to host paradise-park port 22: Operation timed out"; },
  };

  EV.sim = {
    start() { clearInterval(EV.sim.timer); EV.sim.timer = setInterval(step, 2000); },
    trigger(name) {
      const f = events[name];
      EV.log("event", { name, known: !!f });
      if (f) { f(); EV.update(); }
    },
    preset(name, S) { (presets[name] || presets.default)(S); },
    events: Object.keys(events),
    presets: Object.keys(presets),
  };
})();
