// Experiments list page — landing page for the dashboard.

import { h } from 'https://esm.sh/preact@10.25.4';
import { useState, useEffect, useMemo, useRef } from 'https://esm.sh/preact@10.25.4/hooks';
import htm from 'https://esm.sh/htm@3.1.1';

import {
    fetchJSON, fmtScore, fmtDate, fmtModel,
    ScoreBar, FilterBar,
} from './components/shared.js';

const html = htm.bind(h);

// Batch size for S3 score fetches (balance between latency and parallelism)
const SCORE_BATCH_SIZE = 10;

// Extract run name prefix by stripping timestamp suffix (YYYYMMDD-HHMM)
function getRunPrefix(runId) {
    return runId.replace(/-\d{8}-\d{4,6}$/, '');
}

// Aggregate runs with same prefix+model into single entries
function aggregateRuns(runs) {
    const groups = new Map(); // key: prefix+model -> {latest, allRuns}

    for (const run of runs) {
        const prefix = getRunPrefix(run.run_id);
        const key = `${prefix}|${run.model || 'unknown'}`;

        if (!groups.has(key)) {
            groups.set(key, { latest: run, allRuns: [run] });
        } else {
            const group = groups.get(key);
            group.allRuns.push(run);
            // Keep the one with more tasks, or latest date if tied
            if ((run.task_count || 0) > (group.latest.task_count || 0) ||
                ((run.task_count || 0) === (group.latest.task_count || 0) &&
                 (run.date || '') > (group.latest.date || ''))) {
                group.latest = run;
            }
        }
    }

    // Return aggregated runs with combined metadata
    return Array.from(groups.values()).map(({ latest, allRuns }) => {
        if (allRuns.length === 1) return latest;

        // Combine task counts and compute aggregate score
        const allTasks = {};
        for (const run of allRuns) {
            const tasks = run.tasks || {};
            for (const [task, score] of Object.entries(tasks)) {
                if (!(task in allTasks)) {
                    allTasks[task] = score;
                }
            }
        }
        const taskCount = Object.keys(allTasks).length || latest.task_count || 0;
        const scores = Object.values(allTasks);
        const meanScore = scores.length > 0 ? scores.reduce((a, b) => a + b, 0) / scores.length : (latest.mean_score || 0);
        const perfectCount = scores.filter(s => s === 1.0).length || latest.perfect_count || 0;

        return {
            ...latest,
            task_count: taskCount,
            mean_score: meanScore,
            perfect_count: perfectCount,
            aggregated_count: allRuns.length,
            aggregated_runs: allRuns.map(r => r.run_id),
        };
    });
}

export default function Experiments() {
    const [data, setData] = useState([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    const [modelFilter, setModelFilter] = useState('all');
    const [taskFilter, setTaskFilter] = useState('all');
    const [aggregate, setAggregate] = useState(true);
    const [sortCol, setSortCol] = useState('date');
    const [sortAsc, setSortAsc] = useState(false);
    const [loadingScores, setLoadingScores] = useState(new Set());
    const fetchingRef = useRef(false);

    useEffect(() => {
        fetchJSON('/api/experiments')
            .then(d => { setData(d); setLoading(false); })
            .catch(e => { setError(e.message); setLoading(false); });
    }, []);

    // Fetch S3 scores for runs that need them
    useEffect(() => {
        if (loading || fetchingRef.current) return;

        const needsFetch = data.filter(r => r.needs_s3_fetch && !loadingScores.has(r.run_id));
        if (needsFetch.length === 0) return;

        fetchingRef.current = true;

        // Mark all as loading
        setLoadingScores(prev => {
            const next = new Set(prev);
            needsFetch.forEach(r => next.add(r.run_id));
            return next;
        });

        // Fetch in batches
        async function fetchBatches() {
            for (let i = 0; i < needsFetch.length; i += SCORE_BATCH_SIZE) {
                const batch = needsFetch.slice(i, i + SCORE_BATCH_SIZE);
                const ids = batch.map(r => r.run_id).join(',');
                try {
                    const scores = await fetchJSON(`/api/experiments/scores?ids=${encodeURIComponent(ids)}`);
                    // Merge scores into data
                    setData(prev => prev.map(r => {
                        const s = scores[r.run_id];
                        if (s) {
                            return {
                                ...r,
                                task_count: s.task_count,
                                mean_score: s.mean_score,
                                perfect_count: s.perfect_count,
                                needs_s3_fetch: false,
                            };
                        }
                        return r;
                    }));
                } catch (e) {
                    console.error('Failed to fetch scores:', e);
                }
            }
            fetchingRef.current = false;
        }

        fetchBatches();
    }, [data, loading, loadingScores]);

    // Extract unique models for filter dropdown
    const models = useMemo(() => {
        const modelSet = new Set(data.map(r => r.model || 'unknown'));
        return ['all', ...Array.from(modelSet).sort()];
    }, [data]);

    // Apply filters and aggregation
    const processed = useMemo(() => {
        let result = data;

        // Model filter
        if (modelFilter !== 'all') {
            result = result.filter(r => (r.model || 'unknown') === modelFilter);
        }

        // Task count filter
        if (taskFilter === 'full') {
            result = result.filter(r => (r.task_count || 0) >= 80);
        } else if (taskFilter === 'partial') {
            result = result.filter(r => (r.task_count || 0) < 80 && (r.task_count || 0) > 0);
        }

        // Aggregate if enabled
        if (aggregate) {
            result = aggregateRuns(result);
        }

        return result;
    }, [data, modelFilter, taskFilter, aggregate]);

    const counts = useMemo(() => {
        const full = data.filter(r => (r.task_count || 0) >= 80).length;
        const partial = data.filter(r => (r.task_count || 0) < 80 && (r.task_count || 0) > 0).length;
        return { all: data.length, full, partial };
    }, [data]);

    const sorted = useMemo(() => {
        const copy = [...processed];
        copy.sort((a, b) => {
            let va, vb;
            switch (sortCol) {
                case 'date':       va = a.date || ''; vb = b.date || ''; break;
                case 'name':       va = a.run_id || ''; vb = b.run_id || ''; break;
                case 'sha':        va = a.git_sha || ''; vb = b.git_sha || ''; break;
                case 'model':      va = a.model || ''; vb = b.model || ''; break;
                case 'harness':    va = a.harness || ''; vb = b.harness || ''; break;
                case 'score':      va = a.mean_score ?? -1; vb = b.mean_score ?? -1; break;
                case 'tasks':      va = a.task_count ?? 0; vb = b.task_count ?? 0; break;
                case 'perfect':    va = a.perfect_count ?? 0; vb = b.perfect_count ?? 0; break;
                default:           va = ''; vb = '';
            }
            if (va < vb) return sortAsc ? -1 : 1;
            if (va > vb) return sortAsc ? 1 : -1;
            return 0;
        });
        return copy;
    }, [processed, sortCol, sortAsc]);

    function toggleSort(col) {
        if (sortCol === col) {
            setSortAsc(!sortAsc);
        } else {
            setSortCol(col);
            setSortAsc(false);
        }
    }

    function sortIndicator(col) {
        if (sortCol !== col) return '';
        return sortAsc ? ' \u25B2' : ' \u25BC';
    }

    if (loading) return html`<div class="loading">Loading experiments...</div>`;
    if (error) return html`<div class="error-msg">Error: ${error}</div>`;

    return html`
        <div>
            <h2>Experiments <span style=${{ color: '#888', fontWeight: 400, fontSize: '14px' }}>(${sorted.length} shown / ${counts.all} total)</span></h2>

            <div style=${{ display: 'flex', gap: '16px', marginBottom: '16px', alignItems: 'center', flexWrap: 'wrap' }}>
                <label style=${{ display: 'flex', alignItems: 'center', gap: '6px' }}>
                    Model:
                    <select value=${modelFilter} onChange=${e => setModelFilter(e.target.value)}
                            style=${{ padding: '4px 8px' }}>
                        ${models.map(m => html`<option value=${m}>${m === 'all' ? 'All models' : fmtModel(m)}</option>`)}
                    </select>
                </label>

                <label style=${{ display: 'flex', alignItems: 'center', gap: '6px' }}>
                    Tasks:
                    <select value=${taskFilter} onChange=${e => setTaskFilter(e.target.value)}
                            style=${{ padding: '4px 8px' }}>
                        <option value="all">All runs</option>
                        <option value="full">Full runs (80+)</option>
                        <option value="partial">Partial runs</option>
                    </select>
                </label>

                <label style=${{ display: 'flex', alignItems: 'center', gap: '6px', cursor: 'pointer' }}>
                    <input type="checkbox" checked=${aggregate} onChange=${e => setAggregate(e.target.checked)} />
                    Aggregate sequential runs
                </label>
            </div>

            <table>
                <thead>
                    <tr>
                        <th style=${{ cursor: 'pointer' }} onClick=${() => toggleSort('date')}>Date${sortIndicator('date')}</th>
                        <th style=${{ cursor: 'pointer' }} onClick=${() => toggleSort('name')}>Run${sortIndicator('name')}</th>
                        <th style=${{ cursor: 'pointer' }} onClick=${() => toggleSort('sha')}>SHA${sortIndicator('sha')}</th>
                        <th style=${{ cursor: 'pointer' }} onClick=${() => toggleSort('model')}>Model${sortIndicator('model')}</th>
                        <th style=${{ cursor: 'pointer' }} onClick=${() => toggleSort('harness')}>Harness${sortIndicator('harness')}</th>
                        <th style=${{ cursor: 'pointer' }} onClick=${() => toggleSort('score')}>Score${sortIndicator('score')}</th>
                        <th style=${{ cursor: 'pointer' }} onClick=${() => toggleSort('tasks')}>Tasks${sortIndicator('tasks')}</th>
                        <th style=${{ cursor: 'pointer' }} onClick=${() => toggleSort('perfect')}>Perfect${sortIndicator('perfect')}</th>
                    </tr>
                </thead>
                <tbody>
                    ${sorted.map(exp => html`
                        <tr
                            style=${{ cursor: 'pointer' }}
                            onClick=${() => { location.hash = `#/experiments/${encodeURIComponent(exp.run_id)}`; }}
                        >
                            <td>${fmtDate(exp.date)}</td>
                            <td>
                                ${getRunPrefix(exp.run_id)}
                                ${exp.aggregated_count > 1 ? html`
                                    <span style=${{ color: '#888', fontSize: '12px', marginLeft: '4px' }}
                                          title=${exp.aggregated_runs?.join('\n')}>
                                        (${exp.aggregated_count} runs)
                                    </span>
                                ` : ''}
                            </td>
                            <td><code>${(exp.git_sha || '').slice(0, 7) || '\u2014'}</code></td>
                            <td>${fmtModel(exp.model)}</td>
                            <td>${exp.harness || 'serf'}</td>
                            <td>
                                ${exp.needs_s3_fetch ? html`
                                    <span style=${{ color: '#888', fontStyle: 'italic' }}>loading...</span>
                                ` : html`
                                    <span style=${{ marginRight: '6px' }}>${fmtScore(exp.mean_score)}</span>
                                    <${ScoreBar} score=${exp.mean_score} width=${60} />
                                `}
                            </td>
                            <td>${exp.needs_s3_fetch ? '\u2014' : (exp.task_count ?? '\u2014')}</td>
                            <td>${exp.needs_s3_fetch ? '\u2014' : (exp.perfect_count ?? '\u2014')}</td>
                        </tr>
                    `)}
                </tbody>
            </table>
        </div>
    `;
}
