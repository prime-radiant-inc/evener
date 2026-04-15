// Scoreboard page — interactive task scoreboard with filtering.

import { h } from 'https://esm.sh/preact@10.25.4';
import { useState, useEffect, useMemo } from 'https://esm.sh/preact@10.25.4/hooks';
import htm from 'https://esm.sh/htm@3.1.1';

import {
    fetchJSON, fmtScore, fmtDate,
    ScoreBar, RepDots, StatCard, FilterBar,
} from './components/shared.js';

const html = htm.bind(h);

function scoreColor(score) {
    if (score == null) return '#888';
    if (score >= 1.0) return '#2dd66a';
    if (score >= 0.5) return '#d4a020';
    if (score > 0)    return '#e8a040';
    return '#e84444';
}

export default function Scoreboard() {
    const [data, setData] = useState(null);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    const [filter, setFilter] = useState('all');

    function load(f) {
        setLoading(true);
        const url = f === 'all' ? '/api/scoreboard' : `/api/scoreboard?filter=${f}`;
        fetchJSON(url)
            .then(d => { setData(d); setLoading(false); })
            .catch(e => { setError(e.message); setLoading(false); });
    }

    useEffect(() => { load(filter); }, [filter]);

    function handleFilter(f) {
        setFilter(f);
    }

    const tasks = useMemo(() => {
        if (!data || !data.tasks) return [];
        return Object.entries(data.tasks).map(([name, info]) => ({ name, ...info }));
    }, [data]);

    const stats = useMemo(() => {
        if (!tasks.length) return { solved: 0, failing: 0, zero: 0, mean: 0 };
        const solved = tasks.filter(t => t.score >= 1.0).length;
        const zero = tasks.filter(t => t.score === 0).length;
        const failing = tasks.filter(t => t.score < 1.0).length;
        const mean = tasks.reduce((sum, t) => sum + (t.score ?? 0), 0) / tasks.length;
        return { solved, failing, zero, mean };
    }, [tasks]);

    const filterOpts = [
        { value: 'all', label: 'All' },
        { value: 'failing', label: 'Failing' },
        { value: 'solved', label: 'Solved' },
    ];

    if (loading) return html`<div class="loading">Loading scoreboard...</div>`;
    if (error) return html`<div class="error-msg">Error: ${error}</div>`;

    return html`
        <div>
            <h2>Scoreboard</h2>
            <div class="stats-row">
                <${StatCard} label="Solved" value=${stats.solved} />
                <${StatCard} label="Failing" value=${stats.failing} />
                <${StatCard} label="Zero Score" value=${stats.zero} />
                <${StatCard} label="Mean" value=${fmtScore(stats.mean)} />
            </div>
            <${FilterBar} options=${filterOpts} active=${filter} onSelect=${handleFilter} />
            <table>
                <thead>
                    <tr>
                        <th>Task</th>
                        <th>Score</th>
                        <th>Reps</th>
                        <th>Last Run</th>
                        <th>Date</th>
                    </tr>
                </thead>
                <tbody>
                    ${tasks.map(t => html`
                        <tr>
                            <td>
                                <a href=${`#/experiments/tasks/${encodeURIComponent(t.name)}/history`}>
                                    ${t.name}
                                </a>
                            </td>
                            <td>
                                <span style=${{ color: scoreColor(t.score), fontWeight: 600 }}>
                                    ${fmtScore(t.score)}
                                </span>
                            </td>
                            <td><${RepDots} reps=${t.reps} /></td>
                            <td>
                                ${t.last_run
                                    ? html`<a href=${`#/experiments/${encodeURIComponent(t.last_run)}`}>
                                        <code>${t.last_run.slice(0, 12)}</code>
                                    </a>`
                                    : '\u2014'
                                }
                            </td>
                            <td>${fmtDate(t.last_date)}</td>
                        </tr>
                    `)}
                </tbody>
            </table>
        </div>
    `;
}
