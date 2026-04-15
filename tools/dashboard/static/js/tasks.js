// Tasks list page — shows all tasks with performance history.

import { h } from 'https://esm.sh/preact@10.25.4';
import { useState, useEffect, useMemo } from 'https://esm.sh/preact@10.25.4/hooks';
import htm from 'https://esm.sh/htm@3.1.1';

import {
    fetchJSON, fmtScore, fmtDate, fmtModel,
    ScoreBar,
} from './components/shared.js';

const html = htm.bind(h);

// Color for a score value (thresholds: 30/60/90%)
function scoreColor(score) {
    if (score >= 0.9) return '#22c55e';  // green: >= 90%
    if (score >= 0.6) return '#eab308';  // yellow: 60-90%
    if (score >= 0.3) return '#f97316';  // orange: 30-60%
    return '#ef4444';  // red: < 30%
}

// Mini sparkline component showing recent scores
function Sparkline({ scores, width = 80, height = 20 }) {
    if (!scores || scores.length === 0) return null;

    const padding = 2;
    const w = width - padding * 2;
    const h = height - padding * 2;

    // Build points with positions and colors
    const points = scores.map((score, i) => ({
        x: padding + (i / Math.max(scores.length - 1, 1)) * w,
        y: padding + (1 - score) * h,
        color: scoreColor(score),
    }));

    // Gray connecting line
    const pathD = `M ${points.map(p => `${p.x},${p.y}`).join(' L ')}`;

    return html`
        <svg width=${width} height=${height} style=${{ display: 'block' }}>
            <path d=${pathD} fill="none" stroke="#ddd" stroke-width="1" />
            ${points.map(p => html`
                <circle cx=${p.x} cy=${p.y} r="2" fill=${p.color} />
            `)}
        </svg>
    `;
}

export default function Tasks() {
    const [data, setData] = useState([]);
    const [models, setModels] = useState([]);
    const [harnesses, setHarnesses] = useState([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    const [sortCol, setSortCol] = useState('task');
    const [sortAsc, setSortAsc] = useState(true);
    const [filter, setFilter] = useState('');
    const [modelFilter, setModelFilter] = useState('all');
    const [harnessFilter, setHarnessFilter] = useState('all');

    useEffect(() => {
        setLoading(true);
        const params = new URLSearchParams();
        if (modelFilter !== 'all') params.set('model', modelFilter);
        if (harnessFilter !== 'all') params.set('harness', harnessFilter);
        const url = '/api/experiments/tasks' + (params.toString() ? '?' + params : '');

        fetchJSON(url)
            .then(d => {
                setData(d.tasks);
                setModels(d.filters.models);
                setHarnesses(d.filters.harnesses);
                setLoading(false);
            })
            .catch(e => { setError(e.message); setLoading(false); });
    }, [modelFilter, harnessFilter]);

    const filtered = useMemo(() => {
        if (!filter) return data;
        const lc = filter.toLowerCase();
        return data.filter(t => t.task.toLowerCase().includes(lc));
    }, [data, filter]);

    const sorted = useMemo(() => {
        const copy = [...filtered];
        copy.sort((a, b) => {
            let va, vb;
            switch (sortCol) {
                case 'task':       va = a.task || ''; vb = b.task || ''; break;
                case 'runs':       va = a.run_count ?? 0; vb = b.run_count ?? 0; break;
                case 'pass_rate':  va = a.pass_rate ?? 0; vb = b.pass_rate ?? 0; break;
                case 'latest':     va = a.latest_score ?? 0; vb = b.latest_score ?? 0; break;
                case 'date':       va = a.latest_date || ''; vb = b.latest_date || ''; break;
                default:           va = ''; vb = '';
            }
            if (va < vb) return sortAsc ? -1 : 1;
            if (va > vb) return sortAsc ? 1 : -1;
            return 0;
        });
        return copy;
    }, [filtered, sortCol, sortAsc]);

    function toggleSort(col) {
        if (sortCol === col) {
            setSortAsc(!sortAsc);
        } else {
            setSortCol(col);
            setSortAsc(col === 'task'); // Ascending for task name, descending for numbers
        }
    }

    function sortIndicator(col) {
        if (sortCol !== col) return '';
        return sortAsc ? ' \u25B2' : ' \u25BC';
    }

    if (loading) return html`<div class="loading">Loading tasks...</div>`;
    if (error) return html`<div class="error-msg">Error: ${error}</div>`;

    return html`
        <div>
            <h2>Tasks <span style=${{ color: '#888', fontWeight: 400, fontSize: '14px' }}>(${sorted.length} tasks)</span></h2>

            <div style=${{ display: 'flex', gap: '16px', marginBottom: '16px', alignItems: 'center', flexWrap: 'wrap' }}>
                <input
                    type="text"
                    placeholder="Filter tasks..."
                    value=${filter}
                    onInput=${e => setFilter(e.target.value)}
                    style=${{ padding: '6px 12px', width: '200px', border: '1px solid #ccc', borderRadius: '4px' }}
                />

                <label style=${{ display: 'flex', alignItems: 'center', gap: '6px' }}>
                    Model:
                    <select value=${modelFilter} onChange=${e => setModelFilter(e.target.value)}
                            style=${{ padding: '4px 8px' }}>
                        <option value="all">All models</option>
                        ${models.map(m => html`<option value=${m}>${fmtModel(m)}</option>`)}
                    </select>
                </label>

                <label style=${{ display: 'flex', alignItems: 'center', gap: '6px' }}>
                    Harness:
                    <select value=${harnessFilter} onChange=${e => setHarnessFilter(e.target.value)}
                            style=${{ padding: '4px 8px' }}>
                        <option value="all">All harnesses</option>
                        ${harnesses.map(h => html`<option value=${h}>${h}</option>`)}
                    </select>
                </label>
            </div>

            <table>
                <thead>
                    <tr>
                        <th style=${{ cursor: 'pointer' }} onClick=${() => toggleSort('task')}>Task${sortIndicator('task')}</th>
                        <th style=${{ cursor: 'pointer' }} onClick=${() => toggleSort('runs')}>Runs${sortIndicator('runs')}</th>
                        <th style=${{ cursor: 'pointer' }} onClick=${() => toggleSort('pass_rate')}>Pass Rate${sortIndicator('pass_rate')}</th>
                        <th style=${{ cursor: 'pointer' }} onClick=${() => toggleSort('latest')}>Latest${sortIndicator('latest')}</th>
                        <th style=${{ cursor: 'pointer' }} onClick=${() => toggleSort('date')}>Last Run${sortIndicator('date')}</th>
                        <th>Trend</th>
                    </tr>
                </thead>
                <tbody>
                    ${sorted.map(task => html`
                        <tr
                            style=${{ cursor: 'pointer' }}
                            onClick=${() => { location.hash = `#/tasks/${encodeURIComponent(task.task)}`; }}
                        >
                            <td><code>${task.task}</code></td>
                            <td>${task.run_count}</td>
                            <td>
                                <span style=${{ marginRight: '6px' }}>${(task.pass_rate * 100).toFixed(0)}%</span>
                                <span style=${{ color: '#888', fontSize: '12px' }}>(${task.pass_count}/${task.run_count})</span>
                            </td>
                            <td>
                                <span style=${{ marginRight: '6px' }}>${fmtScore(task.latest_score)}</span>
                                <${ScoreBar} score=${task.latest_score} width=${60} />
                            </td>
                            <td>${fmtDate(task.latest_date)}</td>
                            <td><${Sparkline} scores=${task.recent_scores?.slice().reverse()} /></td>
                        </tr>
                    `)}
                </tbody>
            </table>
        </div>
    `;
}
