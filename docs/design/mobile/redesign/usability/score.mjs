#!/usr/bin/env node
// score.mjs — score a usability round from the prototype's own action logs.
//
// Each participant's driver writes task-<ID>-log.json (the prototype's
// window.__proto.log for that task) and summary.json (claims and action
// counts). This applies the moderator's success criteria to every task log
// and prints one matrix: participant × task → success / partial / fail,
// with the action count. Criteria live here, in code, so every round is
// scored the same way; participants never see this file.
//
// Usage: node score.mjs <round-dir>            (expects <round-dir>/p1, p2, ...)
//        node score.mjs <round-dir> --detail   (also prints the evidence line)

import fs from 'node:fs';
import path from 'node:path';

const PLAN = 'docs/superpowers/plans/2026-09-25-host-project-hierarchy.md';
const has = (L, type, pred) => L.some((e) => e.type === type && (!pred || pred(e)));
const find = (L, type, pred) => L.find((e) => e.type === type && (!pred || pred(e)));

// Each returns [verdict, evidence].
const CRITERIA = {
  T1: (L) => has(L, 'answer', (e) => e.sessionId === 's-audit') ? ['success', 'answered s-audit via ' + find(L, 'answer', (e) => e.sessionId === 's-audit').how]
    : has(L, 'open', (e) => e.id === 's-audit') ? ['partial', 'opened s-audit, no answer'] : ['fail', 'never opened s-audit'],
  T2: (L) => {
    const rev = find(L, 'review_sent', (e) => e.path === PLAN);
    const texts = L.filter((e) => e.type === 'comment_add' && e.path === PLAN).map((e) => e.text).concat(rev ? [rev.note || ''] : []);
    const oneHost = texts.some((t) => /(one|single|1|only)\s+host/i.test(t));
    if (rev && oneHost) return ['success', `review ${rev.verdict}, ${rev.comments} comments, ${rev.mode}`];
    if (rev) return ['partial', `review ${rev.verdict} without the one-host change`];
    if (has(L, 'send', (e) => /(one|single|only)\s+host/i.test(e.text || ''))) return ['partial', 'sent the change as a plain message'];
    return ['fail', has(L, 'doc_read', (e) => e.path === PLAN) ? 'read the plan, sent nothing' : 'never opened the plan'];
  },
  T3: (L) => {
    const p = find(L, 'artifact_proposal', (e) => e.action !== 'discard' && /layout C/i.test(e.text || ''));
    if (p) return ['success', 'proposal ' + p.action];
    if (has(L, 'artifact_proposal')) return ['partial', 'proposal ' + find(L, 'artifact_proposal').action + ': ' + find(L, 'artifact_proposal').text];
    return has(L, 'open', (e) => e.screen === 'artifact') ? ['partial', 'opened the artifact only'] : ['fail', 'never opened the artifact'];
  },
  T4: (L) => {
    const s = [...L].reverse().find((e) => e.type === 'start_session');
    if (!s) return ['fail', 'no session started'];
    const want = { host: 'paradise-park', project: 'evener', model: 'glm-5.3-vision', effort: 'max', plugins: 'go,superpowers' };
    const got = { host: s.host, project: s.project, model: s.model, effort: s.effort, plugins: (s.plugins || []).join(',') };
    const wrong = Object.keys(want).filter((k) => want[k] !== got[k]).map((k) => `${k}=${got[k]}`);
    return [wrong.length === 0 ? 'success' : wrong.length <= 2 ? 'partial' : 'fail', wrong.length ? 'wrong: ' + wrong.join('; ') : 'all five settings right'];
  },
  T5: (L) => has(L, 'subagent_stop_request', (e) => e.subagentId === 'g-settle') ? ['success', 'stop requested via ' + find(L, 'subagent_stop_request').mode]
    : has(L, 'subagent_stop_request') ? ['partial', 'stopped the wrong subagent: ' + find(L, 'subagent_stop_request').subagentId]
    : has(L, 'subagent_open', (e) => e.subagentId === 'g-settle') ? ['partial', 'found it, did not stop it'] : ['fail', 'never found it'],
  T6: (L) => has(L, 'send', (e) => e.sessionId === 's-tasklist' && e.mode === 'queue') ? ['success', 'queued']
    : has(L, 'send', (e) => e.sessionId === 's-tasklist') ? ['partial', 'sent as ' + find(L, 'send', (e) => e.sessionId === 's-tasklist').mode] : ['fail', 'nothing sent'],
  T7: (L) => {
    const signed = has(L, 'provider_signin', (e) => e.provider === 'codex-jesse-fsck.com');
    const retried = has(L, 'retry', (e) => e.sessionId === 's-retry' && e.providerOk);
    if (signed && retried) return ['success', 'signed in, then retried'];
    if (signed || retried || has(L, 'retry')) return ['partial', signed ? 'signed in, no successful retry' : 'retried without fixing sign-in'];
    return ['fail', 'neither'];
  },
  T8: (L) => {
    const pinned = has(L, 'pin', (e) => e.sessionId === 's-retry' && e.category === 'Release') || has(L, 'bulk_pin', (e) => (e.ids || []).includes('s-retry'));
    const org = has(L, 'organize', (e) => e.mode === 'host');
    return pinned && org ? ['success', 'pinned and organized by host'] : pinned || org ? ['partial', pinned ? 'pinned only' : 'organized only'] : ['fail', 'neither'];
  },
  T9: (L) => has(L, 'search_open', (e) => e.sessionId === 's-gocache') ? ['success', 'opened from search (' + find(L, 'search_open', (e) => e.sessionId === 's-gocache').kind + ')']
    : has(L, 'open', (e) => e.id === 's-gocache') ? ['success', 'opened it another way'] : has(L, 'search') ? ['partial', 'searched, did not open it'] : ['fail', 'never searched'],
  // Round 1 scored T10 on answering; from round 2 it is scored on getting
  // back to the document after the interruption, which is what the design
  // is for. Answering or deliberately deferring are both fine.
  T10: (L) => {
    const answered = has(L, 'answer', (e) => e.sessionId === 's-gateway');
    const alertAt = (find(L, 'banner_shown') || find(L, 'banner_held') || {}).t;
    const leftAt = (find(L, 'back', (e) => e.from === 'reader') || {}).t;
    const returned = leftAt != null && L.some((e) => (e.type === 'doc_read' || e.type === 'continue_reading') && e.t > leftAt);
    const stayed = leftAt == null;
    if (alertAt == null) return ['fail', 'no alert raised'];
    if (returned || stayed) return ['success', (answered ? 'answered it' : 'did not answer') + ', ' + (stayed ? 'never left the document' : 'came back to the document')];
    return ['partial', (answered ? 'answered it' : 'did not answer') + ', never came back to the document'];
  },
  T2b: (L) => {
    const rev = find(L, 'review_sent', (e) => e.path === PLAN);
    const texts = L.filter((e) => e.type === 'comment_add' && e.path === PLAN).map((e) => e.text).concat(rev ? [rev.note || ''] : []);
    const answered = texts.some((t) => /archiv/i.test(t));
    if (rev && answered) return ['success', `review ${rev.verdict}, ${rev.comments} comments, ${rev.mode}`];
    if (rev) return ['partial', `review ${rev.verdict} without answering the archived question`];
    if (has(L, 'send', (e) => /archiv/i.test(e.text || ''))) return ['partial', 'answered it as a plain message'];
    return ['fail', has(L, 'doc_read', (e) => e.path === PLAN) ? 'read the plan, sent nothing' : 'never opened the plan'];
  },
  T11: (L) => has(L, 'approval', (e) => e.sessionId === 's-mirror' && (e.decision === 'allow' || e.decision === 'allow_scope')) ? ['success', find(L, 'approval', (e) => e.decision !== 'deny').decision]
    : has(L, 'approval', (e) => e.sessionId === 's-mirror') ? ['partial', 'denied instead'] : has(L, 'open', (e) => e.id === 's-mirror') ? ['partial', 'opened, did not decide'] : ['fail', 'never found it'],
  T12: (L) => has(L, 'detail_level', (e) => ['Tools', 'Activity', 'Full'].includes(e.level)) ? ['success', 'detail ' + find(L, 'detail_level', (e) => ['Tools', 'Activity', 'Full'].includes(e.level)).level]
    : has(L, 'evidence_toggle', (e) => e.open) ? ['success', 'expanded step output by hand'] : has(L, 'activity_toggle') ? ['partial', 'expanded an activity run only'] : ['fail', 'no change'],
  T13: (L) => has(L, 'host_reconnect', (e) => e.host === 'paradise-park') || has(L, 'notice_action', (e) => e.notice === 'reconnect') ? ['success', 'reconnected paradise-park']
    : has(L, 'sheet', (e) => ['hub', 'hosts', 'host'].includes(e.kind)) ? ['partial', 'looked at hosts, did not reconnect'] : ['fail', 'no action'],
  T14: (L) => has(L, 'model_change', (e) => e.model === 'claude-sonnet-5' && e.effort === 'high') ? ['success', 'sonnet 5, high']
    : has(L, 'model_change') ? ['partial', 'changed to ' + find(L, 'model_change').model + ' / ' + find(L, 'model_change').effort] : ['fail', 'no change'],
};

const dir = process.argv[2];
if (!dir || !fs.existsSync(dir)) { console.error('usage: node score.mjs <round-dir> [--detail]'); process.exit(2); }
const detail = process.argv.includes('--detail');
const parts = fs.readdirSync(dir).filter((d) => /^p\d+$/.test(d)).sort();
const totals = { success: 0, partial: 0, fail: 0 };
for (const p of parts) {
  const pdir = path.join(dir, p);
  const sumFile = path.join(pdir, 'summary.json');
  const summary = fs.existsSync(sumFile) ? JSON.parse(fs.readFileSync(sumFile, 'utf8')) : null;
  const logs = fs.readdirSync(pdir).filter((f) => /^task-.+-log\.json$/.test(f));
  console.log(`\n${p}${summary ? '' : ' (no summary.json yet)'}`);
  for (const f of logs.sort((a, b) => a.localeCompare(b, undefined, { numeric: true }))) {
    const id = f.replace(/^task-|-log\.json$/g, '');
    const L = JSON.parse(fs.readFileSync(path.join(pdir, f), 'utf8'));
    const crit = CRITERIA[id];
    const [verdict, evidence] = crit ? crit(L) : ['unscored', 'no criteria'];
    if (totals[verdict] != null) totals[verdict]++;
    const t = summary && (summary.tasks || summary.taskResults || []).find((x) => x.id === id || x.task === id);
    const actions = t ? (t.actions ?? t.actionCount ?? '?') : '?';
    console.log(`  ${id.padEnd(4)} ${verdict.padEnd(8)} ${String(actions).padStart(3)} actions${detail ? '  · ' + evidence : ''}`);
    if (detail && t && t.claim) console.log(`       claim: ${t.claim.slice(0, 220)}`);
  }
  if (summary && (summary.consoleErrors.length || summary.pageErrors.length)) console.log(`  !! ${summary.consoleErrors.length} console errors, ${summary.pageErrors.length} page errors`);
}
console.log(`\ntotal: ${totals.success} success, ${totals.partial} partial, ${totals.fail} fail`);
