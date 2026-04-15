// Run detail page — single experiment/wave with task table.

import { h } from 'https://esm.sh/preact@10.25.4';
import { useState, useEffect, useMemo } from 'https://esm.sh/preact@10.25.4/hooks';
import htm from 'https://esm.sh/htm@3.1.1';

import {
    fetchJSON, fmtScore, fmtDate, fmtModel,
    ScoreBar, RepDots, StatCard, FilterBar,
} from './components/shared.js';

const html = htm.bind(h);

export default function RunDetail({ runId }) {
    const [run, setRun] = useState(null);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    const [filter, setFilter] = useState('all');
    const [sortCol, setSortCol] = useState('task');
    const [sortAsc, setSortAsc] = useState(true);

    useEffect(() => {
        setLoading(true);
        fetchJSON(`/api/experiments/${encodeURIComponent(runId)}`)
            .then(d => { setRun(d); setLoading(false); })
            .catch(e => { setError(e.message); setLoading(false); });
    }, [runId]);

    const tasks = useMemo(() => {
        if (!run || !run.results) return [];
        return Object.entries(run.results).map(([name, info]) => ({
            name,
            score: info.score ?? info.mean_score ?? 0,
            reps: info.reps || [],
        }));
    }, [run]);

    const stats = useMemo(() => {
        if (!tasks.length) return { mean: 0, passed: 0, failed: 0, perfect: 0, total: 0 };
        const total = tasks.length;
        const passed = tasks.filter(t => t.score >= 1.0).length;
        const failed = total - passed;
        const mean = tasks.reduce((s, t) => s + t.score, 0) / total;
        const perfect = run ? (run.perfect_count ?? passed) : passed;
        return { mean, passed, failed, perfect, total };
    }, [tasks, run]);

    const filtered = useMemo(() => {
        if (filter === 'pass') return tasks.filter(t => t.score >= 1.0);
        if (filter === 'fail') return tasks.filter(t => t.score < 1.0);
        return tasks;
    }, [tasks, filter]);

    const sorted = useMemo(() => {
        const copy = [...filtered];
        copy.sort((a, b) => {
            let va, vb;
            if (sortCol === 'task') { va = a.name; vb = b.name; }
            else { va = a.score; vb = b.score; }
            if (va < vb) return sortAsc ? -1 : 1;
            if (va > vb) return sortAsc ? 1 : -1;
            return 0;
        });
        return copy;
    }, [filtered, sortCol, sortAsc]);

    function toggleSort(col) {
        if (sortCol === col) setSortAsc(!sortAsc);
        else { setSortCol(col); setSortAsc(col === 'task'); }
    }

    function sortIndicator(col) {
        if (sortCol !== col) return '';
        return sortAsc ? ' \u25B2' : ' \u25BC';
    }

    const filterOpts = [
        { value: 'all', label: 'All', count: tasks.length },
        { value: 'pass', label: 'Pass', count: tasks.filter(t => t.score >= 1.0).length },
        { value: 'fail', label: 'Fail', count: tasks.filter(t => t.score < 1.0).length },
    ];

    if (loading) return html`<div class="loading">Loading run...</div>`;
    if (error) return html`<div class="error-msg">Error: ${error}</div>`;
    if (!run) return html`<div class="error-msg">No data</div>`;

    return html`
        <div>
            <h2>${run.variant || runId}</h2>
            <p style=${{ color: '#888', margin: '0 0 16px', fontSize: '13px' }}>
                SHA: <code>${(run.git_sha || '').slice(0, 7)}</code>
                ${' \u00B7 '}${fmtDate(run.date)}
                ${' \u00B7 '}${fmtModel(run.model)}
            </p>
            <div class="stats-row">
                <${StatCard} label="Mean Score" value=${fmtScore(stats.mean)} />
                <${StatCard} label="Passed" value=${`${stats.passed}/${stats.total}`} />
                <${StatCard} label="Failed" value=${stats.failed} />
                <${StatCard} label="Perfect" value=${stats.perfect} />
            </div>
            <${FilterBar} options=${filterOpts} active=${filter} onSelect=${setFilter} />
            <table>
                <thead>
                    <tr>
                        <th style=${{ cursor: 'pointer' }} onClick=${() => toggleSort('task')}>Task${sortIndicator('task')}</th>
                        <th style=${{ cursor: 'pointer' }} onClick=${() => toggleSort('score')}>Score${sortIndicator('score')}</th>
                        <th>Reps</th>
                    </tr>
                </thead>
                <tbody>
                    ${sorted.map(t => html`
                        <tr
                            style=${{ cursor: 'pointer' }}
                            onClick=${() => { location.hash = `#/experiments/${encodeURIComponent(runId)}/tasks/${encodeURIComponent(t.name)}`; }}
                        >
                            <td>${t.name}</td>
                            <td>
                                <span style=${{ marginRight: '6px' }}>${fmtScore(t.score)}</span>
                                <${ScoreBar} score=${t.score} width=${60} />
                            </td>
                            <td><${RepDots} reps=${t.reps} /></td>
                        </tr>
                    `)}
                </tbody>
            </table>
        </div>
    `;
}
