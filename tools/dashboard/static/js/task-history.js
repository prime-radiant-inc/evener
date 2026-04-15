// Task history page — shows all runs for a single task.

import { h } from 'https://esm.sh/preact@10.25.4';
import { useState, useEffect } from 'https://esm.sh/preact@10.25.4/hooks';
import htm from 'https://esm.sh/htm@3.1.1';

import {
    fetchJSON, fmtScore, fmtDate,
    ScoreBar, RepDots,
} from './components/shared.js';

const html = htm.bind(h);

export default function TaskHistory({ task }) {
    const [history, setHistory] = useState([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);

    useEffect(() => {
        setLoading(true);
        fetchJSON(`/api/experiments/tasks/${encodeURIComponent(task)}/history`)
            .then(d => { setHistory(d); setLoading(false); })
            .catch(e => { setError(e.message); setLoading(false); });
    }, [task]);

    if (loading) return html`<div class="loading">Loading history...</div>`;
    if (error) return html`<div class="error-msg">Error: ${error}</div>`;

    return html`
        <div>
            <h2>${task}</h2>
            <p style=${{ color: '#888', margin: '0 0 16px' }}>${history.length} run${history.length !== 1 ? 's' : ''}</p>
            <table>
                <thead>
                    <tr>
                        <th>Date</th>
                        <th>Run</th>
                        <th>SHA</th>
                        <th>Score</th>
                        <th>Reps</th>
                    </tr>
                </thead>
                <tbody>
                    ${history.map(r => html`
                        <tr>
                            <td>${fmtDate(r.date)}</td>
                            <td>
                                <a href=${`#/experiments/${encodeURIComponent(r.run_id)}/tasks/${encodeURIComponent(task)}`}>
                                    <code>${r.run_id.slice(0, 12)}</code>
                                </a>
                            </td>
                            <td><code>${(r.git_sha || '').slice(0, 7)}</code></td>
                            <td>
                                <span style=${{ marginRight: '6px' }}>${fmtScore(r.score)}</span>
                                <${ScoreBar} score=${r.score} width=${60} />
                            </td>
                            <td><${RepDots} reps=${r.reps} /></td>
                        </tr>
                    `)}
                </tbody>
            </table>
        </div>
    `;
}
