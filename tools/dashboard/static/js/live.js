// Live monitor page — real-time EC2 instance state for active runs.

import { h } from 'https://esm.sh/preact@10.25.4';
import { useState, useEffect, useRef, useMemo } from 'https://esm.sh/preact@10.25.4/hooks';
import htm from 'https://esm.sh/htm@3.1.1';

import {
    fetchJSON,
    StatCard,
} from './components/shared.js';

const html = htm.bind(h);

// ---------------------------------------------------------------------------
// AWS state colors
// ---------------------------------------------------------------------------

const STATE_COLORS = {
    'running':        '#2dd66a',
    'pending':        '#4898f0',
    'terminated':     '#888',
    'stopped':        '#d4a020',
    'stopping':       '#d4a020',
    'shutting-down':  '#d4a020',
    'unknown':        '#888',
};

function stateColor(state) {
    return STATE_COLORS[state] || STATE_COLORS.unknown;
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function truncId(id) {
    if (!id) return '';
    return id.length > 12 ? id.slice(0, 8) + '...' + id.slice(-4) : id;
}

function fmtUptime(launchTime) {
    if (!launchTime) return '\u2014';
    const launched = new Date(launchTime).getTime();
    if (isNaN(launched)) return '\u2014';
    const secs = Math.max(0, (Date.now() - launched) / 1000);
    const h = Math.floor(secs / 3600);
    const m = Math.floor((secs % 3600) / 60);
    if (h > 0) return `${h}h ${m}m`;
    return `${m}m`;
}

function countByState(instances) {
    const counts = {
        running: 0, pending: 0, terminated: 0, other: 0,
        passed: 0, failed: 0,
    };
    for (const i of instances) {
        const s = i.aws_state;
        if (s === 'running') counts.running++;
        else if (s === 'pending') counts.pending++;
        else if (s === 'terminated') counts.terminated++;
        else counts.other++;

        const r = i.reward;
        if (r != null) {
            if (r >= 1.0) counts.passed++;
            else counts.failed++;
        }
    }
    return counts;
}

// Sort order: running first, then other non-terminal, then terminated
// grouped by reward (pass > partial > fail > no-result).
function sortInstances(instances) {
    function bucket(inst) {
        const s = inst.aws_state;
        if (s === 'running') return 0;
        if (s === 'pending') return 1;
        if (s !== 'terminated' && s !== 'stopped'
            && s !== 'stopping' && s !== 'shutting-down') return 2;
        // terminated / stopped buckets — group by reward
        const r = inst.reward;
        if (r == null) return 6;           // no result yet
        if (r >= 1.0) return 3;            // pass
        if (r > 0) return 4;               // partial
        return 5;                          // fail
    }
    return [...instances].sort((a, b) => {
        const diff = bucket(a) - bucket(b);
        if (diff !== 0) return diff;
        // Stable secondary sort by task then rep
        const taskCmp = (a.task || '').localeCompare(b.task || '');
        if (taskCmp !== 0) return taskCmp;
        return String(a.rep || '').localeCompare(String(b.rep || ''));
    });
}

function rewardCell(reward) {
    if (reward == null) {
        return { text: '\u2014', color: '#666' };
    }
    if (reward >= 1.0) {
        return { text: '\u2713 ' + reward.toFixed(1), color: '#2dd66a' };
    }
    if (reward === 0.0) {
        return { text: '\u2717 0.0', color: '#e74c3c' };
    }
    return { text: reward.toFixed(2), color: '#d4a020' };
}

// ---------------------------------------------------------------------------
// StateBadge — colored label for AWS state
// ---------------------------------------------------------------------------

function StateBadge({ state }) {
    return html`
        <span style=${{
            display: 'inline-block',
            padding: '2px 8px',
            borderRadius: '3px',
            background: stateColor(state),
            color: '#fff',
            fontSize: '11px',
            fontWeight: '500',
            textTransform: 'uppercase',
            letterSpacing: '0.3px',
        }}>${state || 'unknown'}</span>
    `;
}

// ---------------------------------------------------------------------------
// InstanceTable
// ---------------------------------------------------------------------------

function InstanceTable({ instances }) {
    if (!instances || instances.length === 0) {
        return html`
            <div style=${{
                padding: '16px', background: '#1a2a3a', borderRadius: '6px',
                color: '#88bbdd', fontSize: '13px', marginTop: '16px',
            }}>
                No instances recorded for this run.
            </div>
        `;
    }

    const sorted = sortInstances(instances);
    return html`
        <table style=${{
            width: '100%', borderCollapse: 'collapse', marginTop: '16px',
            fontSize: '13px',
        }}>
            <thead>
                <tr style=${{ borderBottom: '1px solid #444', textAlign: 'left' }}>
                    <th style=${{ padding: '8px 6px' }}>Instance</th>
                    <th style=${{ padding: '8px 6px' }}>Task</th>
                    <th style=${{ padding: '8px 6px', width: '50px' }}>Rep</th>
                    <th style=${{ padding: '8px 6px', width: '120px' }}>State</th>
                    <th style=${{ padding: '8px 6px', width: '90px' }}>Result</th>
                    <th style=${{ padding: '8px 6px', width: '140px' }}>Public IP</th>
                    <th style=${{ padding: '8px 6px', width: '80px' }}>Uptime</th>
                </tr>
            </thead>
            <tbody>
                ${sorted.map((inst, idx) => {
                    const rc = rewardCell(inst.reward);
                    return html`
                    <tr key=${inst.instance_id + '-' + idx}
                        style=${{ borderBottom: '1px solid #2a2a2a' }}>
                        <td style=${{ padding: '6px', fontFamily: 'monospace', color: '#aaa' }}>
                            ${truncId(inst.instance_id)}
                        </td>
                        <td style=${{ padding: '6px' }}>${inst.task}</td>
                        <td style=${{ padding: '6px', color: '#888' }}>${inst.rep}</td>
                        <td style=${{ padding: '6px' }}>
                            <${StateBadge} state=${inst.aws_state} />
                        </td>
                        <td style=${{ padding: '6px', color: rc.color, fontWeight: '500' }}>
                            ${rc.text}
                        </td>
                        <td style=${{ padding: '6px', fontFamily: 'monospace', color: '#aaa' }}>
                            ${inst.aws_state === 'running' && inst.public_ip ? inst.public_ip : '\u2014'}
                        </td>
                        <td style=${{ padding: '6px', color: '#888' }}>
                            ${inst.aws_state === 'running' ? fmtUptime(inst.launch_time) : '\u2014'}
                        </td>
                    </tr>
                `;})}
            </tbody>
        </table>
    `;
}

// ---------------------------------------------------------------------------
// RunHeader
// ---------------------------------------------------------------------------

function RunHeader({ run }) {
    if (!run) return null;
    return html`
        <div style=${{
            padding: '12px 16px', background: '#1a1a1a', borderRadius: '6px',
            marginTop: '8px', marginBottom: '16px',
            display: 'flex', flexWrap: 'wrap', gap: '24px', fontSize: '13px',
        }}>
            <div>
                <div style=${{ color: '#888', fontSize: '11px', textTransform: 'uppercase' }}>Run ID</div>
                <div style=${{ fontFamily: 'monospace', color: '#eee' }}>${run.run_id}</div>
            </div>
            <div>
                <div style=${{ color: '#888', fontSize: '11px', textTransform: 'uppercase' }}>Model</div>
                <div style=${{ color: '#eee' }}>${run.model}</div>
            </div>
            <div>
                <div style=${{ color: '#888', fontSize: '11px', textTransform: 'uppercase' }}>Instance Type</div>
                <div style=${{ color: '#eee' }}>${run.instance_type}</div>
            </div>
            <div>
                <div style=${{ color: '#888', fontSize: '11px', textTransform: 'uppercase' }}>Launched</div>
                <div style=${{ color: '#eee' }}>${run.launched_at}</div>
            </div>
        </div>
    `;
}

// ---------------------------------------------------------------------------
// Live — main component
// ---------------------------------------------------------------------------

export default function Live() {
    const [runs, setRuns] = useState([]);
    const [selectedRunId, setSelectedRunId] = useState('');
    const [run, setRun] = useState(null);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    const esRef = useRef(null);

    // Load run list on mount
    useEffect(() => {
        fetchJSON('/api/live/runs')
            .then(d => {
                setRuns(d);
                if (d.length > 0) setSelectedRunId(d[0].run_id);
                setLoading(false);
            })
            .catch(e => { setError(e.message); setLoading(false); });
    }, []);

    // Load run data + connect SSE when run changes
    useEffect(() => {
        if (!selectedRunId) {
            setRun(null);
            return;
        }

        if (esRef.current) {
            esRef.current.close();
            esRef.current = null;
        }

        // Fetch initial enriched state
        fetchJSON(`/api/live/runs/${encodeURIComponent(selectedRunId)}`)
            .then(d => setRun(d))
            .catch(() => setRun(null));

        // Open SSE stream
        const es = new EventSource(`/api/live/runs/${encodeURIComponent(selectedRunId)}/stream`);
        esRef.current = es;

        es.addEventListener('state_update', (e) => {
            try {
                const data = JSON.parse(e.data);
                setRun(data);
            } catch (_) {}
        });

        es.addEventListener('error', () => {
            // SSE handles reconnects automatically; let it retry.
        });

        return () => {
            es.close();
            esRef.current = null;
        };
    }, [selectedRunId]);

    const instances = run?.instances || [];
    const counts = useMemo(() => countByState(instances), [instances]);

    if (loading) return html`<div class="loading">Loading runs...</div>`;
    if (error) return html`<div class="error-msg">Error: ${error}</div>`;

    if (!runs.length) {
        return html`
            <div>
                <h2>Live Monitor</h2>
                <div style=${{
                    padding: '16px', background: '#1a2a3a', borderRadius: '6px',
                    color: '#88bbdd', fontSize: '13px',
                }}>
                    No runs found in harbor-runner state directory.
                </div>
            </div>
        `;
    }

    return html`
        <div>
            <h2>Live Monitor</h2>

            <div style=${{ marginBottom: '8px' }}>
                <select
                    value=${selectedRunId}
                    onChange=${e => setSelectedRunId(e.target.value)}
                    style=${{
                        padding: '6px 10px', background: '#2a2a2a', color: '#eee',
                        border: '1px solid #444', borderRadius: '4px',
                        minWidth: '320px', fontFamily: 'monospace', fontSize: '12px',
                    }}
                >
                    ${runs.map(r => html`
                        <option value=${r.run_id}>${r.run_id}</option>
                    `)}
                </select>
            </div>

            <${RunHeader} run=${run} />

            <div class="stats-row">
                <${StatCard} label="Running"     value=${counts.running} />
                <${StatCard} label="Pending"     value=${counts.pending} />
                <${StatCard} label="Terminated"  value=${counts.terminated} />
                <${StatCard} label="Passed"      value=${counts.passed} />
                <${StatCard} label="Failed"      value=${counts.failed} />
                <${StatCard} label="Total"       value=${instances.length} />
            </div>

            <${InstanceTable} instances=${instances} />
        </div>
    `;
}
