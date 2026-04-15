// Task detail page — deep dive into a single task within a run.

import { h } from 'https://esm.sh/preact@10.25.4';
import { useState, useEffect } from 'https://esm.sh/preact@10.25.4/hooks';
import htm from 'https://esm.sh/htm@3.1.1';

import {
    fetchJSON, fmtScore, fmtDate, fmtWallTime, fmtTokens,
    ScoreBar, RepDots, StatCard, RepToggles,
} from './components/shared.js';

const html = htm.bind(h);

// ---------------------------------------------------------------------------
// Action color mapping for trajectory rendering
// ---------------------------------------------------------------------------

const ACTION_COLORS = {
    EXPLORE:  '#4898f0',
    EDIT:     '#d4a020',
    EXEC:     '#888',
    SPAWN:    '#b050b8',
    SUBMIT:   '#2dd66a',
    REVIEW:   '#e8a040',
    TASK:     '#20b2aa',
    ERROR:    '#e84444',
    STEERING: '#0891b2',  // cyan for steering/system messages
    USER:     '#059669',  // green for user input
};

function actionColor(action) {
    if (!action) return '#888';
    const key = action.toUpperCase();
    return ACTION_COLORS[key] || '#888';
}

// ---------------------------------------------------------------------------
// JSON syntax highlighting
// ---------------------------------------------------------------------------

function highlightJSON(obj) {
    if (typeof obj === 'string') {
        // Check if it's already a string (not JSON)
        try {
            JSON.parse(obj);
        } catch {
            // Not valid JSON, return as-is
            return obj;
        }
    }

    const json = typeof obj === 'string' ? obj : JSON.stringify(obj, null, 2);

    // Escape HTML first
    const escaped = json
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;');

    // Apply syntax highlighting
    return escaped
        // Strings (including keys) - must handle escaped quotes
        .replace(/"([^"\\]*(\\.[^"\\]*)*)"/g, (match, content) => {
            // Check if this is a key (followed by :)
            return `<span class="json-string">${match}</span>`;
        })
        // Numbers
        .replace(/\b(-?\d+\.?\d*(?:[eE][+-]?\d+)?)\b/g, '<span class="json-number">$1</span>')
        // Booleans
        .replace(/\b(true|false)\b/g, '<span class="json-boolean">$1</span>')
        // Null
        .replace(/\bnull\b/g, '<span class="json-null">null</span>');
}

function JsonBlock({ data, style }) {
    if (!data) return null;
    const highlighted = typeof data === 'string' && !data.trim().startsWith('{') && !data.trim().startsWith('[')
        ? data  // Plain text, not JSON
        : highlightJSON(data);

    return html`
        <pre
            style=${style}
            dangerouslySetInnerHTML=${{ __html: highlighted }}
        />
    `;
}

// ---------------------------------------------------------------------------
// TrajectoryRound — expandable round in a session
// ---------------------------------------------------------------------------

function TrajectoryRound({ round, index }) {
    const [expanded, setExpanded] = useState(false);

    const action = round.action || round.type || '';
    const summary = round.summary || round.content_preview || '';
    const toolCalls = round.tool_calls || [];
    const toolResults = round.tool_results || [];
    const toolCount = toolCalls.length;
    const tokens = round.usage?.output_tokens || round.tokens || round.token_count;
    const text = round.text || round.assistant_text || '';

    // Index tool results by tool_call_id for matching
    const resultsById = {};
    for (const tr of toolResults) {
        if (tr.tool_call_id) resultsById[tr.tool_call_id] = tr;
    }

    return html`
        <div
            style=${{
                borderLeft: `3px solid ${actionColor(action)}`,
                paddingLeft: '12px',
                marginBottom: '8px',
            }}
        >
            <div
                style=${{ cursor: 'pointer', display: 'flex', gap: '8px', alignItems: 'center' }}
                onClick=${() => setExpanded(!expanded)}
            >
                <span style=${{ color: '#888', fontSize: '12px', minWidth: '24px' }}>#${index + 1}</span>
                <span style=${{
                    color: actionColor(action),
                    fontWeight: 600,
                    fontSize: '12px',
                    textTransform: 'uppercase',
                }}>${action || 'STEP'}</span>
                <span style=${{ color: '#ccc', fontSize: '12px', flex: 1 }}>${summary}</span>
                <span style=${{ color: '#888', fontSize: '11px', whiteSpace: 'nowrap' }}>
                    ${toolCount > 0 ? `${toolCount} tools` : ''}
                    ${tokens ? ` \u00B7 ${fmtTokens(tokens)} tok` : ''}
                </span>
                <span style=${{ color: '#888', fontSize: '12px' }}>${expanded ? '\u25BC' : '\u25B6'}</span>
            </div>
            ${expanded && html`
                <div style=${{ marginTop: '8px', fontSize: '13px' }}>
                    ${text && html`
                        <div style=${{ background: '#f5f5f5', color: '#333', padding: '8px 12px', borderRadius: '4px', marginBottom: '8px', whiteSpace: 'pre-wrap', overflow: 'auto', border: '1px solid #e0e0e0' }}>
                            ${text}
                        </div>
                    `}
                    ${toolCalls.map(tc => {
                        const result = resultsById[tc.id];
                        const args = tc.arguments || tc.args;
                        const resultContent = result?.content;
                        return html`
                            <div style=${{ background: '#fafafa', padding: '10px 14px', borderRadius: '6px', marginBottom: '8px', border: '1px solid #e0e0e0' }}>
                                <div style=${{ color: '#2563eb', fontWeight: 600, fontSize: '13px', marginBottom: '6px' }}>
                                    ${tc.name || 'tool'}
                                    ${result?.is_error && html`<span style=${{ color: '#dc2626', marginLeft: '8px', fontWeight: 500 }}>(error)</span>`}
                                </div>
                                ${args && html`
                                    <details open>
                                        <summary style=${{ color: '#666', fontSize: '11px', cursor: 'pointer', marginBottom: '6px', fontWeight: 500 }}>Arguments</summary>
                                        <${JsonBlock}
                                            data=${args}
                                            style=${{ color: '#333', fontSize: '12px', margin: '4px 0', overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-word', background: '#fff', padding: '8px', borderRadius: '4px', border: '1px solid #e5e5e5' }}
                                        />
                                    </details>
                                `}
                                ${resultContent && html`
                                    <details open>
                                        <summary style=${{ color: '#666', fontSize: '11px', cursor: 'pointer', marginBottom: '6px', fontWeight: 500, borderTop: '1px solid #e0e0e0', paddingTop: '8px', marginTop: '8px' }}>Result</summary>
                                        <${JsonBlock}
                                            data=${resultContent}
                                            style=${{ color: '#444', fontSize: '12px', margin: '4px 0', overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-word', background: '#fff', padding: '8px', borderRadius: '4px', border: '1px solid #e5e5e5' }}
                                        />
                                    </details>
                                `}
                            </div>
                        `;
                    })}
                </div>
            `}
        </div>
    `;
}

// ---------------------------------------------------------------------------
// SessionTree — renders nested sessions with depth-based indentation
// ---------------------------------------------------------------------------

function SessionTree({ sessions, depth }) {
    const d = depth || 0;
    const borderColors = ['#4898f0', '#b050b8', '#2dd66a', '#d4a020', '#e8a040'];
    const borderColor = borderColors[d % borderColors.length];

    if (!sessions || !sessions.length) return null;

    return html`
        <div style=${{ marginLeft: d > 0 ? '16px' : '0', borderLeft: d > 0 ? `2px solid ${borderColor}` : 'none', paddingLeft: d > 0 ? '12px' : '0' }}>
            ${sessions.map(session => {
                const sessionRounds = session.trajectory || session.rounds || [];
                const children = session.children || [];
                const sid = (session.session_id || '').slice(0, 8);
                const sessionLabel = session.label || (sid ? `${sid} (${session.model || '?'})` : null);
                const systemPrompt = session.system_prompt || '';

                // Index children by the spawn_agent tool_call that created them
                const childrenByToolCall = new Map();
                for (const c of children) {
                    if (c.parent_tool_call_id) childrenByToolCall.set(c.parent_tool_call_id, c);
                }
                const matchedIds = new Set();

                return html`
                    <div style=${{ marginBottom: '12px' }}>
                        ${sessionLabel && html`
                            <div style=${{ color: borderColor, fontWeight: 600, fontSize: '12px', marginBottom: '6px' }}>${sessionLabel}</div>
                        `}
                        ${systemPrompt && html`
                            <details style=${{ marginBottom: '8px' }}>
                                <summary style=${{ cursor: 'pointer', color: '#666', fontSize: '12px', fontWeight: 600 }}>System Prompt</summary>
                                <pre style=${{ background: '#f8f8f8', color: '#333', padding: '10px 14px', borderRadius: '4px', marginTop: '4px', fontSize: '12px', overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-word', border: '1px solid #e0e0e0' }}>${systemPrompt}</pre>
                            </details>
                        `}
                        ${sessionRounds.map((r, i) => {
                            // Find subagents spawned in this round, inline them here
                            const inlineChildren = [];
                            for (const tc of (r.tool_calls || [])) {
                                if ((tc.name || '').toLowerCase() !== 'spawn_agent') continue;
                                const child = childrenByToolCall.get(tc.id);
                                if (child) {
                                    inlineChildren.push(child);
                                    matchedIds.add(tc.id);
                                }
                            }
                            return html`
                                <${TrajectoryRound} round=${r} index=${i} />
                                ${inlineChildren.length > 0 && html`
                                    <${SessionTree} sessions=${inlineChildren} depth=${d + 1} />
                                `}
                            `;
                        })}
                        ${(() => {
                            const orphans = children.filter(c => !matchedIds.has(c.parent_tool_call_id));
                            return orphans.length > 0
                                ? html`<${SessionTree} sessions=${orphans} depth=${d + 1} />`
                                : null;
                        })()}
                    </div>
                `;
            })}
        </div>
    `;
}

// ---------------------------------------------------------------------------
// TrajectoryRepColumn — fetches + renders one rep's trajectory
// ---------------------------------------------------------------------------

function outcomeClass(harbor) {
    if (!harbor) return { cls: 'queued', text: '—' };
    if (harbor.status === 'running') return { cls: 'running', text: 'RUNNING' };
    if (harbor.status === 'queued') return { cls: 'queued', text: 'QUEUED' };
    const r = harbor.reward;
    if (r == null) return { cls: 'queued', text: 'NO REWARD' };
    if (r >= 1.0) return { cls: 'pass', text: `PASS \u00b7 ${Number(r).toFixed(2)}` };
    if (r <= 0) return { cls: 'fail', text: `FAIL \u00b7 ${Number(r).toFixed(2)}` };
    return { cls: 'running', text: `PARTIAL \u00b7 ${Number(r).toFixed(2)}` };
}

function TrajectoryRepColumn({ runId, task, rep }) {
    const [harbor, setHarbor] = useState(null);
    const [loading, setLoading] = useState(true);

    useEffect(() => {
        setLoading(true);
        fetchJSON(`/api/runs/${encodeURIComponent(runId)}/tasks/${encodeURIComponent(task)}?rep=${rep}`)
            .then(h => { setHarbor(h); setLoading(false); })
            .catch(() => { setHarbor(null); setLoading(false); });
    }, [runId, task, rep]);

    const outcome = outcomeClass(harbor);
    const wallTime = harbor ? harbor.wall_time_sec : null;
    const rounds = harbor ? harbor.total_rounds : null;
    const tokens = harbor ? harbor.total_tokens : null;
    const trajectory = harbor ? harbor.trajectory : null;
    const testOutput = harbor ? harbor.test_output : null;

    const header = html`
        <div class="structure-column-header">
            <div class="structure-column-title">
                <span>Rep ${rep}</span>
                <span class=${`result-badge ${outcome.cls}`}>${outcome.text}</span>
            </div>
            ${harbor && html`
                <div class="structure-column-meta">
                    ${wallTime != null ? `${fmtWallTime(wallTime)}` : ''}
                    ${rounds != null ? ` \u00b7 ${rounds} rounds` : ''}
                    ${tokens != null ? ` \u00b7 ${fmtTokens(tokens)} tok` : ''}
                </div>
            `}
        </div>
    `;

    if (loading) return html`<div class="structure-column">${header}<div class="loading">Loading...</div></div>`;
    if (!harbor) return html`<div class="structure-column">${header}<div class="empty-state">Not available</div></div>`;

    return html`
        <div class="structure-column">
            ${header}
            ${trajectory && Array.isArray(trajectory) && trajectory.length > 0 && html`
                <div style=${{ marginTop: '12px' }}>
                    <${SessionTree} sessions=${trajectory} />
                </div>
            `}
            ${testOutput && html`
                <details style=${{ marginTop: '16px' }}>
                    <summary style=${{ cursor: 'pointer', fontWeight: 600, fontSize: '12px', color: '#666' }}>Verifier Output</summary>
                    <pre style=${{ background: '#f8f8f8', color: '#333', padding: '12px', borderRadius: '6px', marginTop: '6px', fontSize: '12px', overflow: 'auto', maxHeight: '400px', border: '1px solid #e0e0e0' }}>${testOutput}</pre>
                </details>
            `}
        </div>
    `;
}

// ---------------------------------------------------------------------------
// TaskDetail — main component
// ---------------------------------------------------------------------------

function computeEnabledReps({ rep, reps, allReps }) {
    if (rep != null) return new Set([rep]);
    if (reps && reps.length > 0) return new Set(reps);
    if (allReps.length > 0) return new Set(allReps.map((_, i) => i + 1));
    return new Set([1]);
}

export default function TaskDetail({ runId, task, rep, reps }) {
    const [run, setRun] = useState(null);
    const [history, setHistory] = useState([]);
    const [loading, setLoading] = useState(true);

    useEffect(() => {
        setLoading(true);
        const expP = fetchJSON(`/api/experiments/${encodeURIComponent(runId)}`).catch(() => null);
        const histP = fetchJSON(`/api/experiments/tasks/${encodeURIComponent(task)}/history`).catch(() => []);
        Promise.all([expP, histP])
            .then(([exp, hist]) => { setRun(exp); setHistory(hist); setLoading(false); })
            .catch(() => setLoading(false));
    }, [runId, task]);

    const taskData = run && run.results ? (run.results[task] || {}) : {};
    const score = taskData.score ?? taskData.mean_score ?? 0;
    const allReps = taskData.reps || [];
    const passCount = allReps.filter(Boolean).length;
    const enabledReps = computeEnabledReps({ rep, reps, allReps });
    const sortedEnabled = [...enabledReps].sort((a, b) => a - b);

    const toggleRep = (n) => {
        const next = new Set(enabledReps);
        if (next.has(n)) {
            if (next.size === 1) return;
            next.delete(n);
        } else {
            next.add(n);
        }
        const sorted = [...next].sort((a, b) => a - b);
        location.hash = `#/experiments/${encodeURIComponent(runId)}/tasks/${encodeURIComponent(task)}?reps=${sorted.join(',')}`;
    };

    return html`
        <div style=${{ display: 'grid', gridTemplateColumns: 'minmax(0,1fr) 240px', gap: '24px' }}>
            <div style=${{ minWidth: 0 }}>
                <h2>${task}</h2>
                <p style=${{ color: '#888', margin: '0 0 16px', fontSize: '13px' }}>
                    Run: <code>${runId.slice(0, 24)}</code>
                    ${run && run.date ? ` \u00B7 ${fmtDate(run.date)}` : ''}
                    ${' \u00B7 '}
                    <a
                        href=${`#/experiments/${encodeURIComponent(runId)}/tasks/${encodeURIComponent(task)}/structure${rep != null ? '?rep=' + rep : (reps ? '?reps=' + sortedEnabled.join(',') : '')}`}
                        style=${{ color: '#4898f0', textDecoration: 'none' }}
                    >View structure \u2192</a>
                </p>

                <div class="stats-row">
                    <${StatCard} label="Mean Score" value=${fmtScore(score)} />
                    <${StatCard} label="Passed" value=${`${passCount}/${allReps.length}`} />
                </div>

                ${allReps.length > 1 && html`
                    <${RepToggles}
                        reps=${allReps}
                        enabled=${enabledReps}
                        onToggle=${toggleRep} />
                `}
                ${allReps.length <= 1 && allReps.length > 0 && html`
                    <div style=${{ margin: '16px 0' }}>
                        <${RepDots} reps=${allReps} />
                    </div>
                `}

                <div class=${`structure-columns columns-${sortedEnabled.length}`} style=${{ marginTop: '16px' }}>
                    ${sortedEnabled.map(n => html`
                        <${TrajectoryRepColumn}
                            key=${n}
                            runId=${runId}
                            task=${task}
                            rep=${n} />
                    `)}
                </div>
            </div>

            <!-- History sidebar -->
            <div style=${{ borderLeft: '1px solid rgba(255,255,255,0.06)', paddingLeft: '16px' }}>
                <h3 style=${{ fontSize: '13px', color: '#888', marginBottom: '12px' }}>Recent Runs</h3>
                ${history.slice(0, 20).map(h => {
                    const isCurrent = h.run_id === runId;
                    return html`
                        <a
                            href=${`#/experiments/${encodeURIComponent(h.run_id)}/tasks/${encodeURIComponent(task)}`}
                            style=${{
                                display: 'block',
                                padding: '8px',
                                marginBottom: '4px',
                                borderRadius: '4px',
                                textDecoration: 'none',
                                background: isCurrent ? 'rgba(72,152,240,0.1)' : 'transparent',
                                border: isCurrent ? '1px solid rgba(72,152,240,0.3)' : '1px solid transparent',
                            }}
                        >
                            <div style=${{ fontSize: '12px', color: '#888' }}>${fmtDate(h.date)}</div>
                            <${ScoreBar} score=${h.score} width=${60} />
                        </a>
                    `;
                })}
            </div>
        </div>
    `;
}
