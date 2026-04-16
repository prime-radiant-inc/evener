// Task structure view — hierarchical delegation tree, no tool-call detail.

import { h } from 'https://esm.sh/preact@10.25.4';
import { useState, useEffect } from 'https://esm.sh/preact@10.25.4/hooks';
import htm from 'https://esm.sh/htm@3.1.1';

import { fetchJSON, fmtScore, fmtModel, RepToggles } from './components/shared.js';

const html = htm.bind(h);

// ---------------------------------------------------------------------------
// Trajectory → structure extraction
// ---------------------------------------------------------------------------

function parseToolArgs(args) {
    if (!args) return {};
    if (typeof args === 'object') return args;
    if (typeof args === 'string') {
        try { return JSON.parse(args); } catch { return {}; }
    }
    return {};
}

function communicateKind(name, args) {
    if (name !== 'communicate') return 'final';
    const kind = typeof args.kind === 'string' ? args.kind.trim().toLowerCase() : '';
    return kind || 'final';
}

function parseTaskListResult(content) {
    // Tool result content is a JSON array of task entries with full fields.
    if (typeof content !== 'string') return null;
    try {
        const parsed = JSON.parse(content);
        return Array.isArray(parsed) ? parsed : null;
    } catch { return null; }
}

function extractStructureFromTrajectory(trajectory, seedTaskList) {
    // Single chronological event stream — task_list, delegations,
    // and communicate calls all mixed by round order (walk order == time).
    const events = [];
    let finalResponse = '';
    let initialTaskList = null;
    const tasksById = new Map();  // id → latest known task entry

    // Seed with server-provided initial_task_list (parsed from STEERING)
    // so updates can be enriched even when no task_list result JSON exists.
    if (Array.isArray(seedTaskList) && seedTaskList.length > 0) {
        initialTaskList = seedTaskList;
        for (const t of seedTaskList) {
            if (t.id != null) tasksById.set(t.id, { ...t });
        }
    }

    for (const round of (trajectory || [])) {
        // Scan tool_results for task_list to capture the full task data.
        // Serf's task_list tool returns every task with description,
        // prompt, type, reasoning_effort, status, etc. Updates only carry
        // id/status/notes so we cross-reference by id.
        for (const tr of (round.tool_results || [])) {
            const name = (tr.name || '').toLowerCase();
            if (name.includes('task_list')) {
                const parsed = parseTaskListResult(tr.content);
                if (parsed) {
                    // Prefer the more complete view for initialTaskList.
                    if (!initialTaskList || parsed.length > initialTaskList.length) {
                        initialTaskList = parsed;
                    }
                    for (const t of parsed) {
                        if (t.id != null) {
                            tasksById.set(t.id, { ...(tasksById.get(t.id) || {}), ...t });
                        }
                    }
                }
            }
        }

        for (const tc of (round.tool_calls || [])) {
            const name = (tc.name || '').toLowerCase();
            const args = parseToolArgs(tc.arguments);

            if (name.includes('task_list')) {
                let enrichedUpdates = null;
                if (Array.isArray(args.updates)) {
                    enrichedUpdates = args.updates.map(u => {
                        const base = u.id != null ? tasksById.get(u.id) : null;
                        return base ? { ...base, ...u } : u;
                    });
                }
                events.push({
                    kind: 'task_list',
                    round: round.round,
                    action: args.action || 'unknown',
                    tasks: Array.isArray(args.tasks) ? args.tasks : null,
                    updates: enrichedUpdates,
                });
            } else if (name === 'spawn_agent') {
                events.push({
                    kind: 'delegation',
                    round: round.round,
                    tool_call_id: tc.id || '',
                    agent_type: args.agent_type || args.agent || '(unknown)',
                    task: args.task || args.prompt || '',
                    task_list: Array.isArray(args.task_list) ? args.task_list : null,
                    model: args.model || '',
                    max_turns: args.max_turns || '',
                    reasoning_effort: args.reasoning_effort || '',
                });
            } else if (name === 'communicate' || name.includes('submit') || name === 'report_result' || name === 'finish') {
                const kind = communicateKind(name, args);
                events.push({
                    kind: 'communicate',
                    round: round.round,
                    name: tc.name || '',
                    communicate_kind: kind,
                    args: args,
                });
                const message = typeof args.message === 'string' ? args.message.trim() : '';
                if (message) {
                    finalResponse = message;
                }
            }
        }
        if (round.text && round.text.trim()) {
            finalResponse = round.text;
        }
    }

    return { events, finalResponse, initialTaskList };
}

// ---------------------------------------------------------------------------
// Small visual pieces
// ---------------------------------------------------------------------------

function OutcomeBadge({ status, reward, score }) {
    let cls = 'queued', text = 'NO REWARD';
    if (status === 'running') { cls = 'running'; text = 'RUNNING'; }
    else if (status === 'queued') { cls = 'queued'; text = 'QUEUED'; }
    else {
        // Prefer harbor reward; fall back to experiment score
        const r = reward != null ? Number(reward) : (score != null ? Number(score) : null);
        if (r == null || Number.isNaN(r)) { cls = 'queued'; text = 'NO REWARD'; }
        else if (r >= 1.0) { cls = 'pass'; text = `PASS \u00b7 ${r.toFixed(2)}`; }
        else if (r <= 0) { cls = 'fail'; text = `FAIL \u00b7 ${r.toFixed(2)}`; }
        else { cls = 'partial'; text = `PARTIAL \u00b7 ${r.toFixed(2)}`; }
    }
    return html`<span class=${`result-badge ${cls}`}>${text}</span>`;
}

function CollapsibleSection({ title, defaultOpen, children }) {
    const [open, setOpen] = useState(defaultOpen);
    return html`
        <div class=${`structure-section${open ? '' : ' collapsed'}`}>
            <div class="structure-section-header" onClick=${() => setOpen(!open)}>
                <span class="structure-toggle">${open ? '\u25be' : '\u25b8'}</span>
                <span class="structure-section-title">${title}</span>
            </div>
            ${open && html`<div class="structure-section-body">${children}</div>`}
        </div>
    `;
}

// Known keys that get rendered with their own labels/slots.
const TASK_KNOWN_KEYS = new Set([
    'id', 'description', 'prompt', 'depends_on', 'type', 'status', 'notes',
    'reasoning_effort', 'model', 'max_turns', 'agent',
]);

function TaskEntry({ entry, isUpdate }) {
    const id = entry.id != null ? entry.id : '';
    const description = entry.description || '';
    const prompt = entry.prompt || '';
    const status = entry.status || '';
    const notes = entry.notes || '';
    const type = entry.type || '';
    const reasoningEffort = entry.reasoning_effort || '';
    const model = entry.model || '';
    const maxTurns = entry.max_turns || '';
    const agent = entry.agent || '';
    const dependsOn = Array.isArray(entry.depends_on) ? entry.depends_on : [];
    // Surface any additional fields verbatim.
    const extras = Object.entries(entry)
        .filter(([k, v]) => !TASK_KNOWN_KEYS.has(k) && v !== '' && v != null && !(Array.isArray(v) && v.length === 0))
        .map(([k, v]) => [k, typeof v === 'object' ? JSON.stringify(v) : String(v)]);

    return html`
        <li class=${`structure-task-entry${isUpdate ? ' structure-task-update' : ''}`}>
            <div class="structure-task-head">
                ${id !== '' && html`<span class="structure-task-id">#${id}</span>`}
                ${status && html`<span class=${`structure-update-status status-${status}`}>${status}</span>`}
                ${type && html`<span class="structure-task-type">${type}</span>`}
                ${description && html`<span class="structure-task-desc">${description}</span>`}
            </div>
            ${(reasoningEffort || model || maxTurns || agent) && html`
                <div class="structure-task-meta">
                    ${agent && html`<span><span class="structure-field-label">agent:</span> ${agent}</span>`}
                    ${model && html`<span><span class="structure-field-label">model:</span> ${fmtModel(model)}</span>`}
                    ${reasoningEffort && html`<span><span class="structure-field-label">effort:</span> ${reasoningEffort}</span>`}
                    ${maxTurns && html`<span><span class="structure-field-label">max_turns:</span> ${maxTurns}</span>`}
                </div>
            `}
            ${dependsOn.length > 0 && html`
                <div class="structure-task-meta"><span class="structure-field-label">depends on:</span> ${dependsOn.join(', ')}</div>
            `}
            ${prompt && html`<div class="structure-task-prompt">${prompt}</div>`}
            ${notes && html`<div class="structure-task-notes"><span class="structure-field-label">notes:</span> ${notes}</div>`}
            ${extras.length > 0 && html`
                <div class="structure-task-extras">
                    ${extras.map(([k, v]) => html`<span><span class="structure-field-label">${k}:</span> <span class="structure-field-value">${v}</span></span>`)}
                </div>
            `}
        </li>
    `;
}

function TaskListContent({ tasks }) {
    return html`
        <ol class="structure-tasklist">
            ${tasks.map(t => html`<${TaskEntry} entry=${t} isUpdate=${false} />`)}
        </ol>
    `;
}

function TaskUpdateContent({ updates }) {
    return html`
        <ol class="structure-tasklist">
            ${updates.map(u => html`<${TaskEntry} entry=${u} isUpdate=${true} />`)}
        </ol>
    `;
}

function TaskListEvent({ event }) {
    const [open, setOpen] = useState(true);
    const count = event.tasks ? event.tasks.length : (event.updates ? event.updates.length : 0);
    return html`
        <div class="structure-tasklist-event">
            <div class="structure-delegation-head" onClick=${() => setOpen(!open)}>
                <span class="structure-expander">${open ? '\u25be' : '\u25b8'}</span>
                <span class="structure-event-round">R${event.round}</span>
                <span class=${`structure-event-action action-${event.action}`}>task_list ${event.action}</span>
                ${count > 0 && html`<span class="structure-badge">${count} ${count === 1 ? 'entry' : 'entries'}</span>`}
            </div>
            ${open && html`
                <div class="structure-event-body">
                    ${event.tasks && html`<${TaskListContent} tasks=${event.tasks} />`}
                    ${event.updates && html`<${TaskUpdateContent} updates=${event.updates} />`}
                </div>
            `}
        </div>
    `;
}

function CommunicateEvent({ event }) {
    const [open, setOpen] = useState(true);
    const argsStr = JSON.stringify(event.args, null, 2);
    const label = event.name === 'communicate'
        ? `${event.name}:${event.communicate_kind || 'final'}`
        : event.name;
    return html`
        <div class="structure-communicate-call">
            <div class="structure-delegation-head" onClick=${() => setOpen(!open)}>
                <span class="structure-expander">${open ? '\u25be' : '\u25b8'}</span>
                <span class="structure-event-round">R${event.round}</span>
                <span class="structure-event-action action-communicate">${label}</span>
            </div>
            ${open && html`
                <div class="structure-event-body">
                    <pre class="card-body-pre">${argsStr}</pre>
                </div>
            `}
        </div>
    `;
}

function DelegationRow({ del, sessionMap, runId, task, trialHash, rep }) {
    const [open, setOpen] = useState(false);

    // Find matching child session
    let childSession = null;
    for (const s of sessionMap.values()) {
        if ((s.parent_tool_call_id || '') === del.tool_call_id) {
            childSession = s;
            break;
        }
    }

    const used = childSession && Array.isArray(childSession.trajectory)
        ? childSession.trajectory.length : 0;
    const max = del.max_turns;
    const turnsLabel = max ? `${used}/${max} turns` : (used ? `${used} turns` : '');

    const badges = [];
    if (del.model) badges.push(fmtModel(del.model));
    if (turnsLabel) badges.push(turnsLabel);
    if (del.reasoning_effort) badges.push(del.reasoning_effort);

    return html`
        <div class="structure-delegation">
            <div class="structure-delegation-head" onClick=${() => setOpen(!open)}>
                <span class="structure-expander">${open ? '\u25be' : '\u25b8'}</span>
                <span class="structure-event-round">R${del.round}</span>
                <span class="structure-event-action action-delegate">delegate</span>
                <span class="structure-delegation-type">\u2192 ${del.agent_type}</span>
                ${badges.map(b => html`<span class="structure-badge">${b}</span>`)}
            </div>
            ${open && html`
                <div class="structure-delegation-body">
                    ${del.task && html`
                        <div class="structure-delegation-label">task:</div>
                        <pre class="card-body-pre">${del.task}</pre>
                    `}
                    ${del.task_list && html`
                        <div class="structure-delegation-label">task list:</div>
                        <${TaskListContent} tasks=${del.task_list} />
                    `}
                    ${childSession
                        ? html`<${SessionCard}
                                    session=${childSession}
                                    sessionMap=${sessionMap}
                                    runId=${runId}
                                    task=${task}
                                    trialHash=${trialHash}
                                    rep=${rep}
                                    agentType=${del.agent_type}
                                    nested=${true} />`
                        : html`<div class="structure-empty">(no matching child session)</div>`
                    }
                </div>
            `}
        </div>
    `;
}

function SessionCard({ session, sessionMap, runId, task, trialHash, rep, agentType, nested }) {
    const sid = (session.session_id || '').slice(0, 8);
    const model = fmtModel(session.model || '?');
    const label = agentType || 'coordinator';
    const trajParams = new URLSearchParams();
    if (rep) trajParams.set('rep', String(rep));
    if (trialHash) trajParams.set('trial', trialHash);
    const trajQs = trajParams.toString();
    const trajHref = `#/experiments/${encodeURIComponent(runId)}/tasks/${encodeURIComponent(task)}${trajQs ? '?' + trajQs : ''}`;
    const structure = extractStructureFromTrajectory(
        session.trajectory, session.initial_task_list
    );

    return html`
        <div class=${`card structure-card${nested ? ' structure-nested' : ''}`}>
            <div class="card-header">
                <span class="card-title">${label}</span>
                <span class="structure-meta">${sid} \u00b7 ${model}</span>
                <a href=${trajHref} class="structure-traj-link" onClick=${e => e.stopPropagation()}>full trajectory \u2192</a>
            </div>
            <div class="card-body">
                ${session.system_prompt && html`
                    <${CollapsibleSection} title="System Prompt" defaultOpen=${false}>
                        <pre class="card-body-pre">${session.system_prompt}</pre>
                    <//>
                `}
                ${session.initial_prompt && html`
                    <${CollapsibleSection} title="Initial Prompt" defaultOpen=${true}>
                        <pre class="card-body-pre">${session.initial_prompt}</pre>
                    <//>
                `}
                ${structure.initialTaskList && structure.initialTaskList.length > 0 && html`
                    <${CollapsibleSection} title=${`Initial Task List (${structure.initialTaskList.length})`} defaultOpen=${true}>
                        <${TaskListContent} tasks=${structure.initialTaskList} />
                    <//>
                `}
                ${structure.events.length > 0 && html`
                    <${CollapsibleSection} title=${`Events (${structure.events.length})`} defaultOpen=${true}>
                        <div class="structure-events">
                            ${structure.events.map(e => {
                                if (e.kind === 'delegation') {
                                    return html`<${DelegationRow}
                                        del=${e}
                                        sessionMap=${sessionMap}
                                        runId=${runId}
                                        task=${task}
                                        trialHash=${trialHash}
                                        rep=${rep} />`;
                                }
                                if (e.kind === 'task_list') {
                                    return html`<${TaskListEvent} event=${e} />`;
                                }
                                if (e.kind === 'communicate') {
                                    return html`<${CommunicateEvent} event=${e} />`;
                                }
                                return null;
                            })}
                        </div>
                    <//>
                `}
                <${CollapsibleSection} title="Final Response" defaultOpen=${true}>
                    ${structure.finalResponse
                        ? html`<pre class="card-body-pre structure-final">${structure.finalResponse}</pre>`
                        : html`<div class="structure-empty">(no text output)</div>`
                    }
                <//>
            </div>
        </div>
    `;
}

// ---------------------------------------------------------------------------
// Per-rep column — fetches harbor data for one rep, renders its session cards
// ---------------------------------------------------------------------------

function StructureRepColumn({ runId, task, rep, trial }) {
    const [harbor, setHarbor] = useState(null);
    const [loading, setLoading] = useState(true);

    useEffect(() => {
        setLoading(true);
        const params = new URLSearchParams();
        params.set('rep', String(rep));
        if (trial) params.set('trial', trial);
        fetchJSON(`/api/runs/${encodeURIComponent(runId)}/tasks/${encodeURIComponent(task)}?${params}`)
            .then(h => { setHarbor(h); setLoading(false); })
            .catch(() => { setHarbor(null); setLoading(false); });
    }, [runId, task, rep, trial]);

    const reward = harbor ? harbor.reward : null;
    const status = harbor ? harbor.status : null;
    const model = harbor ? fmtModel(harbor.model || '') : '';

    const columnHeader = html`
        <div class="structure-column-header">
            <div class="structure-column-title">
                <span>Rep ${rep}</span>
                <${OutcomeBadge} status=${status} reward=${reward} />
            </div>
            ${model && html`<div class="structure-column-meta">${model}</div>`}
        </div>
    `;

    if (loading) {
        return html`<div class="structure-column">${columnHeader}<div class="loading">Loading...</div></div>`;
    }
    if (!harbor) {
        return html`<div class="structure-column">${columnHeader}<div class="empty-state">Not available</div></div>`;
    }
    if (!harbor.trajectory || harbor.trajectory.length === 0) {
        return html`<div class="structure-column">${columnHeader}<div class="empty-state">No sessions</div></div>`;
    }

    // Build flat session map for this rep
    const sessionMap = new Map();
    for (const root of harbor.trajectory) {
        sessionMap.set(root.session_id, root);
        for (const child of (root.children || [])) {
            sessionMap.set(child.session_id, child);
        }
    }

    return html`
        <div class="structure-column">
            ${columnHeader}
            ${harbor.trajectory.map(root => html`
                <${SessionCard}
                    session=${root}
                    sessionMap=${sessionMap}
                    runId=${runId}
                    task=${task}
                    trialHash=${trial}
                    rep=${rep}
                    agentType=${null}
                    nested=${false} />
            `)}
        </div>
    `;
}

// ---------------------------------------------------------------------------
// TaskStructure — main page component (multi-rep columns)
// ---------------------------------------------------------------------------

function computeEnabledReps({ rep, reps, allReps }) {
    // Legacy single-rep mode
    if (rep != null) return new Set([rep]);
    // Explicit multi-rep param
    if (reps && reps.length > 0) return new Set(reps);
    // Default: all reps from experiment data, or rep 1 as fallback
    if (allReps.length > 0) {
        return new Set(allReps.map((_, i) => i + 1));
    }
    return new Set([1]);
}

function navigateWithReps(runId, task, repsArray) {
    const sorted = [...repsArray].sort((a, b) => a - b);
    location.hash = `#/experiments/${encodeURIComponent(runId)}/tasks/${encodeURIComponent(task)}/structure?reps=${sorted.join(',')}`;
}

export default function TaskStructure({ runId, task, trial, rep, reps }) {
    const [experiment, setExperiment] = useState(null);
    const [loading, setLoading] = useState(true);

    useEffect(() => {
        setLoading(true);
        fetchJSON(`/api/experiments/${encodeURIComponent(runId)}`)
            .then(e => { setExperiment(e); setLoading(false); })
            .catch(() => { setExperiment(null); setLoading(false); });
    }, [runId]);

    const taskData = experiment && experiment.results ? (experiment.results[task] || {}) : {};
    const allReps = (taskData.reps && Array.isArray(taskData.reps)) ? taskData.reps : [];
    const enabledReps = computeEnabledReps({ rep, reps, allReps });
    const sortedEnabled = [...enabledReps].sort((a, b) => a - b);

    const toggleRep = (n) => {
        const next = new Set(enabledReps);
        if (next.has(n)) {
            if (next.size === 1) return; // keep at least one
            next.delete(n);
        } else {
            next.add(n);
        }
        navigateWithReps(runId, task, [...next]);
    };

    const header = html`
        <div class="page-header">
            <h1>${task}</h1>
            <div class="subtitle">
                ${runId}
                ${trial ? ` \u00b7 ${trial}` : ''}
                ${experiment && experiment.model ? ` \u00b7 ${fmtModel(experiment.model)}` : ''}
            </div>
            <${RepToggles}
                reps=${allReps}
                enabled=${enabledReps}
                onToggle=${toggleRep} />
        </div>
    `;

    if (loading && !experiment) {
        return html`<div>${header}<div class="loading">Loading...</div></div>`;
    }

    return html`
        <div>
            ${header}
            <div class=${`structure-columns columns-${sortedEnabled.length}`}>
                ${sortedEnabled.map(n => html`
                    <${StructureRepColumn}
                        key=${n}
                        runId=${runId}
                        task=${task}
                        rep=${n}
                        trial=${trial} />
                `)}
            </div>
        </div>
    `;
}
