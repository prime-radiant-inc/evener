// Compare page — side-by-side experiment comparison.

import { h } from 'https://esm.sh/preact@10.25.4';
import { useState, useEffect, useMemo } from 'https://esm.sh/preact@10.25.4/hooks';
import htm from 'https://esm.sh/htm@3.1.1';

import {
    fetchJSON, fmtScore,
    ScoreBar, RepDots, StatCard,
} from './components/shared.js';

const html = htm.bind(h);

// ---------------------------------------------------------------------------
// CompareTable — shows task-level comparison for a category
// ---------------------------------------------------------------------------

function CompareTable({ tasks, labelA, labelB }) {
    if (!tasks || !tasks.length) return null;
    return html`
        <table>
            <thead>
                <tr>
                    <th>Task</th>
                    <th>${labelA || 'Run A'}</th>
                    <th>${labelB || 'Run B'}</th>
                </tr>
            </thead>
            <tbody>
                ${tasks.map(t => html`
                    <tr>
                        <td>${t.name}</td>
                        <td>
                            <span style=${{ marginRight: '4px' }}>${fmtScore(t.scoreA)}</span>
                            <${RepDots} reps=${t.repsA} />
                        </td>
                        <td>
                            <span style=${{ marginRight: '4px' }}>${fmtScore(t.scoreB)}</span>
                            <${RepDots} reps=${t.repsB} />
                        </td>
                    </tr>
                `)}
            </tbody>
        </table>
    `;
}

// ---------------------------------------------------------------------------
// Compare — main component
// ---------------------------------------------------------------------------

export default function Compare() {
    const [experiments, setExperiments] = useState([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    const [runA, setRunA] = useState('');
    const [runB, setRunB] = useState('');
    const [dataA, setDataA] = useState(null);
    const [dataB, setDataB] = useState(null);
    const [comparing, setComparing] = useState(false);

    useEffect(() => {
        fetchJSON('/api/experiments')
            .then(d => { setExperiments(d); setLoading(false); })
            .catch(e => { setError(e.message); setLoading(false); });
    }, []);

    function doCompare() {
        if (!runA || !runB) return;
        setComparing(true);
        setDataA(null);
        setDataB(null);
        Promise.all([
            fetchJSON(`/api/experiments/${encodeURIComponent(runA)}`),
            fetchJSON(`/api/experiments/${encodeURIComponent(runB)}`),
        ])
            .then(([a, b]) => { setDataA(a); setDataB(b); setComparing(false); })
            .catch(e => { setError(e.message); setComparing(false); });
    }

    const comparison = useMemo(() => {
        if (!dataA || !dataB) return null;
        const resultsA = dataA.results || {};
        const resultsB = dataB.results || {};
        const allTasks = new Set([...Object.keys(resultsA), ...Object.keys(resultsB)]);

        const improved = [];
        const regressed = [];
        const stablePass = [];
        const stableFail = [];
        const onlyA = [];
        const onlyB = [];

        for (const name of allTasks) {
            const a = resultsA[name];
            const b = resultsB[name];

            if (!a && b) {
                onlyB.push({ name, scoreA: null, scoreB: b.score ?? b.mean_score ?? 0, repsA: [], repsB: b.reps || [] });
                continue;
            }
            if (a && !b) {
                onlyA.push({ name, scoreA: a.score ?? a.mean_score ?? 0, scoreB: null, repsA: a.reps || [], repsB: [] });
                continue;
            }

            const scoreA = a.score ?? a.mean_score ?? 0;
            const scoreB = b.score ?? b.mean_score ?? 0;
            const passA = scoreA >= 0.5;
            const passB = scoreB >= 0.5;
            const entry = { name, scoreA, scoreB, repsA: a.reps || [], repsB: b.reps || [] };

            if (!passA && passB) improved.push(entry);
            else if (passA && !passB) regressed.push(entry);
            else if (passA && passB) stablePass.push(entry);
            else stableFail.push(entry);
        }

        return { improved, regressed, stablePass, stableFail, onlyA, onlyB };
    }, [dataA, dataB]);

    function optionLabel(exp) {
        const date = exp.date || '';
        const variant = exp.variant || exp.run_id;
        const score = fmtScore(exp.mean_score);
        return `${date} \u2014 ${variant} (${score})`;
    }

    if (loading) return html`<div class="loading">Loading experiments...</div>`;
    if (error) return html`<div class="error-msg">Error: ${error}</div>`;

    return html`
        <div>
            <h2>Compare Runs</h2>

            <div style=${{ display: 'flex', gap: '12px', alignItems: 'center', marginBottom: '24px', flexWrap: 'wrap' }}>
                <select
                    value=${runA}
                    onChange=${e => setRunA(e.target.value)}
                    style=${{ padding: '6px 10px', background: '#2a2a2a', color: '#eee', border: '1px solid #444', borderRadius: '4px', minWidth: '280px' }}
                >
                    <option value="">-- Run A (baseline) --</option>
                    ${experiments.map(exp => html`
                        <option value=${exp.run_id}>${optionLabel(exp)}</option>
                    `)}
                </select>
                <select
                    value=${runB}
                    onChange=${e => setRunB(e.target.value)}
                    style=${{ padding: '6px 10px', background: '#2a2a2a', color: '#eee', border: '1px solid #444', borderRadius: '4px', minWidth: '280px' }}
                >
                    <option value="">-- Run B (candidate) --</option>
                    ${experiments.map(exp => html`
                        <option value=${exp.run_id}>${optionLabel(exp)}</option>
                    `)}
                </select>
                <button
                    onClick=${doCompare}
                    disabled=${!runA || !runB || comparing}
                    style=${{
                        padding: '6px 16px', borderRadius: '4px', border: 'none', cursor: 'pointer',
                        background: (!runA || !runB) ? '#444' : '#4898f0', color: '#fff', fontWeight: 600,
                    }}
                >
                    ${comparing ? 'Comparing...' : 'Compare'}
                </button>
            </div>

            ${comparison && html`
                <div>
                    <div class="stats-row">
                        <${StatCard} label="Improved" value=${comparison.improved.length} />
                        <${StatCard} label="Regressed" value=${comparison.regressed.length} />
                        <${StatCard} label="Stable Pass" value=${comparison.stablePass.length} />
                        <${StatCard} label="Stable Fail" value=${comparison.stableFail.length} />
                    </div>

                    ${comparison.improved.length > 0 && html`
                        <div style=${{ marginTop: '24px' }}>
                            <h3 style=${{ color: '#2dd66a' }}>Improved (${comparison.improved.length})</h3>
                            <${CompareTable} tasks=${comparison.improved} />
                        </div>
                    `}

                    ${comparison.regressed.length > 0 && html`
                        <div style=${{ marginTop: '24px' }}>
                            <h3 style=${{ color: '#e84444' }}>Regressed (${comparison.regressed.length})</h3>
                            <${CompareTable} tasks=${comparison.regressed} />
                        </div>
                    `}

                    ${comparison.stableFail.length > 0 && html`
                        <div style=${{ marginTop: '24px' }}>
                            <h3>Stable Fail (${comparison.stableFail.length})</h3>
                            <${CompareTable} tasks=${comparison.stableFail} />
                        </div>
                    `}

                    ${comparison.stablePass.length > 0 && html`
                        <details style=${{ marginTop: '24px' }}>
                            <summary style=${{ cursor: 'pointer', fontWeight: 600 }}>Stable Pass (${comparison.stablePass.length})</summary>
                            <${CompareTable} tasks=${comparison.stablePass} />
                        </details>
                    `}

                    ${comparison.onlyA.length > 0 && html`
                        <div style=${{ marginTop: '24px' }}>
                            <h3>Only in Run A (${comparison.onlyA.length})</h3>
                            <${CompareTable} tasks=${comparison.onlyA} />
                        </div>
                    `}

                    ${comparison.onlyB.length > 0 && html`
                        <div style=${{ marginTop: '24px' }}>
                            <h3>Only in Run B (${comparison.onlyB.length})</h3>
                            <${CompareTable} tasks=${comparison.onlyB} />
                        </div>
                    `}
                </div>
            `}
        </div>
    `;
}
