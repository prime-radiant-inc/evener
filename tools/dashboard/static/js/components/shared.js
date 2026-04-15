// Shared Preact components and helpers for the Serf dashboard.

import { h } from 'https://esm.sh/preact@10.25.4';
import htm from 'https://esm.sh/htm@3.1.1';

const html = htm.bind(h);

// ---------------------------------------------------------------------------
// Formatting helpers
// ---------------------------------------------------------------------------

export function fmtScore(score) {
    if (score == null) return '\u2014';
    return score.toFixed(3);
}

export function fmtPercent(n, total) {
    if (!total) return '0.0%';
    return ((n / total) * 100).toFixed(1) + '%';
}

export function fmtDate(dateStr) {
    if (dateStr == null) return '\u2014';
    return dateStr;
}

export function fmtModel(model) {
    if (!model) return '';
    const slashIdx = model.indexOf('/');
    return slashIdx >= 0 ? model.slice(slashIdx + 1) : model;
}

export function fmtWallTime(seconds) {
    if (seconds == null) return '\u2014';
    const m = Math.floor(seconds / 60);
    const s = Math.round(seconds % 60);
    return m > 0 ? `${m}m ${s}s` : `${s}s`;
}

export function fmtTokens(n) {
    if (n == null) return '\u2014';
    if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
    if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K';
    return String(n);
}

// ---------------------------------------------------------------------------
// Data fetching
// ---------------------------------------------------------------------------

export async function fetchJSON(url) {
    const resp = await fetch(url, { headers: { Accept: 'application/json' } });
    if (!resp.ok) throw new Error(`${resp.status}`);
    return resp.json();
}

// ---------------------------------------------------------------------------
// Components
// ---------------------------------------------------------------------------

/** Horizontal green/red pass-rate bar. */
export function ScoreBar({ score, width }) {
    const pct = score != null ? Math.round(score * 100) : 0;
    const barWidth = width || 80;
    return html`
        <div class="pass-bar" style=${{ width: barWidth + 'px' }}>
            <div class="pass-fill" style=${{ width: pct + '%' }}></div>
        </div>
    `;
}

/** Colored circles for each rep (pass=green, fail=red). */
export function RepDots({ reps }) {
    if (!reps || !reps.length) return null;
    return html`
        <span>
            ${reps.map((r, i) => html`
                <span
                    class=${`status-dot ${r ? 'pass' : 'fail'}`}
                    title=${`Rep ${i + 1}: ${r ? 'pass' : 'fail'}`}
                    style=${{ display: 'inline-block' }}
                ></span>
            `)}
        </span>
    `;
}

/** Colored status label using existing status-dot CSS. */
export function StatusBadge({ status }) {
    const dotClass = status || 'fail';
    return html`
        <span class="status-text">
            <span class=${`status-dot ${dotClass}`}></span>
            ${status || 'unknown'}
        </span>
    `;
}

/** Metric display card using existing stat-card CSS. */
export function StatCard({ label, value, sub }) {
    return html`
        <div class="stat-card">
            <div class="stat-label">${label}</div>
            <div class="stat-value">${value}</div>
            ${sub && html`<div class="stat-detail">${sub}</div>`}
        </div>
    `;
}

/** Multi-select rep toggle pills. Passes `enabled` set (1-indexed). */
export function RepToggles({ reps, enabled, onToggle }) {
    if (!reps || reps.length <= 1) return null;
    return html`
        <div class="rep-toggles">
            <span class="rep-toggles-label">Reps:</span>
            ${reps.map((r, i) => {
                const n = i + 1;
                const isEnabled = enabled.has(n);
                const passed = Boolean(r);
                return html`<button
                    class=${`rep-toggle${isEnabled ? ' enabled' : ''}`}
                    onClick=${() => onToggle(n)}
                    title=${`Rep ${n}: ${passed ? 'pass' : 'fail'}`}
                >
                    <span class=${`status-dot ${passed ? 'pass' : 'fail'}`}></span>
                    ${n}
                </button>`;
            })}
        </div>
    `;
}

/** Row of filter buttons. options: [{value, label, count}] */
export function FilterBar({ options, active, onSelect }) {
    return html`
        <div class="filter-bar">
            ${(options || []).map(opt => html`
                <button
                    class=${`filter-btn${opt.value === active ? ' active' : ''}`}
                    onClick=${() => onSelect(opt.value)}
                >
                    ${opt.label}
                    ${opt.count != null && html`<span class="count">${opt.count}</span>`}
                </button>
            `)}
        </div>
    `;
}

export { html };
