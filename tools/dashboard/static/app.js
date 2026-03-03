/* Eval Dashboard — client-side SPA */

// ---------------------------------------------------------------------------
// Data fetching
// ---------------------------------------------------------------------------

async function fetchJSON(url) {
    const resp = await fetch(url, { headers: { 'Accept': 'application/json' } });
    if (!resp.ok) throw new Error(`${resp.status}`);
    return resp.json();
}

// ---------------------------------------------------------------------------
// Router
// ---------------------------------------------------------------------------

function route() {
    const hash = location.hash || '#/';
    const app = document.getElementById('app');

    let m;
    if ((m = hash.match(/^#\/tasks\/([^/]+)\/history$/))) {
        renderTaskHistory(app, decodeURIComponent(m[1]));
    } else if ((m = hash.match(/^#\/runs\/([^/]+)\/tasks\/([^/?]+)(?:\?trial=([^&]+))?$/))) {
        renderTaskDetail(app, decodeURIComponent(m[1]), decodeURIComponent(m[2]), m[3] || null);
    } else if ((m = hash.match(/^#\/runs\/([^/]+)$/))) {
        renderRunDetail(app, decodeURIComponent(m[1]));
    } else if ((m = hash.match(/^#\/compare$/))) {
        renderComparison(app);
    } else {
        renderDashboard(app);
    }
}

window.addEventListener('hashchange', route);
window.addEventListener('DOMContentLoaded', route);

// Auto-refresh every 30 seconds
setInterval(route, 30000);

// ---------------------------------------------------------------------------
// Breadcrumb
// ---------------------------------------------------------------------------

function setBreadcrumb(items) {
    // items = [{label, href}, ...], last item has no href (current)
    const nav = document.getElementById('breadcrumb');
    nav.innerHTML = '';
    items.forEach((item, i) => {
        if (i > 0) {
            const sep = document.createElement('span');
            sep.className = 'sep';
            sep.textContent = '/';
            nav.appendChild(sep);
        }
        if (item.href) {
            const a = document.createElement('a');
            a.href = item.href;
            a.textContent = item.label;
            nav.appendChild(a);
        } else {
            const span = document.createElement('span');
            span.className = 'current';
            span.textContent = item.label;
            nav.appendChild(span);
        }
    });
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function h(tag, attrs, ...children) {
    const el = document.createElement(tag);
    if (attrs) {
        for (const [k, v] of Object.entries(attrs)) {
            if (k === 'className') el.className = v;
            else if (k.startsWith('on')) el.addEventListener(k.slice(2).toLowerCase(), v);
            else el.setAttribute(k, v);
        }
    }
    for (const child of children) {
        if (child == null) continue;
        if (typeof child === 'string' || typeof child === 'number') {
            el.appendChild(document.createTextNode(String(child)));
        } else if (Array.isArray(child)) {
            for (const c of child) {
                if (c != null) el.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
            }
        } else {
            el.appendChild(child);
        }
    }
    return el;
}

function escapeHtml(str) {
    const div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
}

function failureCategoryLabel(cat) {
    switch (cat) {
        case 'timeout': return 'Timeout';
        case 'wrong_answer': return 'Wrong Answer';
        case 'no_submit': return 'No Submit';
        case 'api_error': return 'API Error';
        default: return cat || '';
    }
}

function failureDotClass(cat) {
    switch (cat) {
        case 'timeout': return 'timeout';
        case 'no_submit': return 'no-submit';
        default: return 'fail';
    }
}

function formatTokens(usage) {
    if (!usage) return '';
    const inp = usage.input_tokens || 0;
    const out = usage.output_tokens || 0;
    if (inp === 0 && out === 0) return '';
    return `${(inp / 1000).toFixed(1)}k in / ${(out / 1000).toFixed(1)}k out`;
}

function truncate(str, maxLen) {
    if (!str) return '';
    if (str.length <= maxLen) return str;
    return str.slice(0, maxLen) + '...';
}

function formatWallTime(sec) {
    if (sec == null) return '-';
    const m = Math.floor(sec / 60);
    const s = Math.round(sec % 60);
    return m > 0 ? `${m}m ${s}s` : `${s}s`;
}

function formatSize(bytes) {
    if (bytes == null) return '-';
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function formatDate(isoStr) {
    if (!isoStr) return '-';
    try {
        const d = new Date(isoStr);
        return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' });
    } catch { return '-'; }
}

function stripProviderPrefix(model) {
    if (!model) return '-';
    const idx = model.indexOf('/');
    return idx >= 0 ? model.slice(idx + 1) : model;
}

function formatTaskTokens(tokIn, tokOut) {
    if (!tokIn && !tokOut) return '-';
    return `${(tokIn / 1000).toFixed(1)}k / ${(tokOut / 1000).toFixed(1)}k`;
}

// Extract a useful one-liner from tool call arguments
function toolCallOneLiner(tc) {
    const name = tc.name || 'tool';
    const args = parseArgs(tc);

    // Command execution: show the command
    for (const key of ['command', 'cmd', 'script']) {
        if (args[key]) return `${name}: ${truncate(args[key], 80)}`;
    }
    // File paths
    for (const key of ['path', 'file_path', 'file', 'directory']) {
        if (args[key]) return `${name}: ${args[key]}`;
    }
    // Search patterns
    if (args.pattern) {
        const path = args.path || '';
        return `${name}: ${args.pattern}` + (path ? ` in ${path}` : '');
    }
    // Spawn
    if (args.task) return `${name}: ${truncate(args.task, 60)}`;
    if (args.agent) return `${name}: ${args.agent}`;
    // Submit
    if (args.result != null) return `${name}("${truncate(String(args.result), 60)}")`;
    // Review
    if (args.reason) return `${name}: ${truncate(args.reason, 60)}`;
    if (args.message) return `${name}: ${truncate(args.message, 60)}`;
    // Patch
    if (args.patch) {
        const match = args.patch.match(/\+\+\+ b\/(.+)/);
        if (match) return `${name}: ${match[1]}`;
    }
    // Fallback
    return name;
}

function parseArgs(tc) {
    const raw = tc.arguments;
    if (typeof raw === 'string') {
        try { return JSON.parse(raw); } catch { return {}; }
    }
    return (raw && typeof raw === 'object') ? raw : {};
}

function metricBox(label, value) {
    return h('div', { className: 'metric-box' },
        h('div', { className: 'metric-label' }, label),
        h('div', { className: 'metric-value' }, String(value != null ? value : '-'))
    );
}

function twoStateSection(title, content, defaultOpen, accentColor, rawHref) {
    const oneLiner = (content || '').split('\n')[0].slice(0, 100);
    const section = h('div', { className: 'card two-state' + (accentColor ? ` accent-${accentColor}` : '') });
    const headerItems = [
        h('span', { className: 'card-title' }, title),
        h('span', { className: 'one-liner' }, oneLiner),
    ];
    if (rawHref) {
        headerItems.push(rawLink(rawHref));
    }
    const header = h('div', { className: 'card-header clickable' }, ...headerItems);
    const body = h('pre', { className: 'card-body-pre' }, content);
    section.appendChild(header);
    section.appendChild(body);

    if (!defaultOpen) {
        section.classList.add('collapsed');
    }

    header.addEventListener('click', () => {
        section.classList.toggle('collapsed');
    });
    return section;
}

function rawLink(href) {
    return h('a', {
        href: href,
        target: '_blank',
        className: 'raw-link',
        title: 'Open raw file',
        onClick: (e) => { e.stopPropagation(); }
    }, '\u2197');
}

// ---------------------------------------------------------------------------
// Dashboard page
// ---------------------------------------------------------------------------

async function renderDashboard(container) {
    setBreadcrumb([{ label: 'Dashboard' }]);
    container.innerHTML = '<div class="loading">Loading runs...</div>';

    try {
        const runs = await fetchJSON('/api/runs');

        if (!runs.length) {
            container.innerHTML = '<div class="empty-state">No eval runs found.</div>';
            return;
        }

        const compareLink = h('a', { href: '#/compare', className: 'compare-nav-link' }, 'Compare runs');

        const header = h('div', { className: 'page-header' },
            h('h1', null, 'Eval Runs', compareLink),
            h('div', { className: 'subtitle' }, `${runs.length} run${runs.length !== 1 ? 's' : ''}`)
        );

        const thead = h('thead', null,
            h('tr', null,
                h('th', null, 'Run'),
                h('th', null, 'Model'),
                h('th', null, 'Dataset'),
                h('th', null, 'Date'),
                h('th', null, 'Tasks'),
                h('th', null, 'Pass Rate'),
                h('th', null, '')
            )
        );

        const tbody = h('tbody');
        for (const run of runs) {
            const passRate = run.total_tasks > 0
                ? ((run.passed / run.total_tasks) * 100).toFixed(0)
                : 0;
            const dataset = run.dataset_name
                ? `${run.dataset_name} ${run.dataset_version || ''}`.trim()
                : '-';
            const inProgress = (run.running || 0) + (run.queued || 0);
            const statusBadges = [];
            if (run.running > 0) statusBadges.push(h('span', { className: 'running-badge' }, `${run.running} running`));
            if (run.queued > 0) statusBadges.push(h('span', { className: 'queued-badge' }, `${run.queued} queued`));
            const taskLabel = run.total_tasks > 0
                ? `${run.passed}/${run.total_tasks}`
                : (inProgress > 0 ? '...' : '0');

            const row = h('tr', null,
                h('td', null,
                    h('a', { className: 'table-link', href: `#/runs/${encodeURIComponent(run.job_name)}` },
                        run.job_name)
                ),
                h('td', null, stripProviderPrefix(run.model)),
                h('td', null, dataset),
                h('td', null, formatDate(run.started_at)),
                h('td', null, String(run.total_tasks), ...statusBadges),
                h('td', null,
                    h('div', { className: 'pass-info' },
                        h('span', { className: 'pass-fraction' }, taskLabel),
                        run.total_tasks > 0 ? h('span', { className: 'pass-pct' }, `${passRate}%`) : null
                    )
                ),
                h('td', null,
                    h('div', { className: 'pass-bar', style: 'width:120px' },
                        h('div', { className: 'pass-fill',
                            style: `width:${run.total_tasks > 0 ? (run.passed / run.total_tasks) * 100 : 0}%` })
                    )
                )
            );
            tbody.appendChild(row);
        }

        const table = h('table', null, thead, tbody);
        const card = h('div', { className: 'card' },
            h('div', { className: 'card-body table-wrap' }, table)
        );

        container.innerHTML = '';
        container.appendChild(header);
        container.appendChild(card);
    } catch (err) {
        container.innerHTML = `<div class="error-msg">Failed to load runs: ${escapeHtml(err.message)}</div>`;
    }
}

// ---------------------------------------------------------------------------
// Comparison page
// ---------------------------------------------------------------------------

async function renderComparison(container) {
    setBreadcrumb([
        { label: 'Dashboard', href: '#/' },
        { label: 'Compare' }
    ]);
    container.innerHTML = '<div class="loading">Loading runs...</div>';

    try {
        const runs = await fetchJSON('/api/runs');
        if (!runs.length) {
            container.innerHTML = '<div class="empty-state">No runs to compare.</div>';
            return;
        }

        const header = h('div', { className: 'page-header' },
            h('h1', null, 'Run Comparison')
        );

        // Dropdowns
        const selectA = h('select', { className: 'compare-select' });
        const selectB = h('select', { className: 'compare-select' });
        selectA.appendChild(h('option', { value: '' }, '-- Select Run A --'));
        selectB.appendChild(h('option', { value: '' }, '-- Select Run B --'));
        for (const run of runs) {
            selectA.appendChild(h('option', { value: run.job_name }, run.job_name));
            selectB.appendChild(h('option', { value: run.job_name }, run.job_name));
        }

        const selectRow = h('div', { className: 'compare-select-row' },
            h('div', null,
                h('label', { className: 'compare-label' }, 'Run A'),
                selectA
            ),
            h('div', null,
                h('label', { className: 'compare-label' }, 'Run B'),
                selectB
            )
        );

        const resultArea = h('div', { className: 'compare-results' });

        async function doCompare() {
            const a = selectA.value;
            const b = selectB.value;
            if (!a || !b) {
                resultArea.innerHTML = '';
                return;
            }
            if (a === b) {
                resultArea.innerHTML = '<div class="empty-state">Select two different runs.</div>';
                return;
            }
            resultArea.innerHTML = '<div class="loading">Comparing...</div>';

            try {
                const data = await fetchJSON(`/api/compare?a=${encodeURIComponent(a)}&b=${encodeURIComponent(b)}`);
                renderCompareResult(resultArea, data);
            } catch (err) {
                resultArea.innerHTML = `<div class="error-msg">Comparison failed: ${escapeHtml(err.message)}</div>`;
            }
        }

        selectA.addEventListener('change', doCompare);
        selectB.addEventListener('change', doCompare);

        container.innerHTML = '';
        container.appendChild(header);
        container.appendChild(selectRow);
        container.appendChild(resultArea);
    } catch (err) {
        container.innerHTML = `<div class="error-msg">Failed to load runs: ${escapeHtml(err.message)}</div>`;
    }
}

function renderCompareResult(container, data) {
    container.innerHTML = '';

    const delta = data.run_b.passed - data.run_a.passed;
    const deltaStr = delta > 0 ? `+${delta}` : String(delta);
    const pendingCount = (data.pending || []).length;
    const pendingSuffix = pendingCount > 0 ? `, ${pendingCount} pending` : '';
    const summary = h('div', { className: 'compare-summary' },
        `Run B: ${data.run_b.passed}/${data.run_b.total} (${deltaStr} vs Run A). `,
        `${data.improved.length} improved, ${data.regressed.length} regressed${pendingSuffix}.`
    );
    container.appendChild(summary);

    // Build ordered task list: regressed first, then improved, then pending, then stable
    const allRows = [
        ...data.regressed.map(r => ({ ...r, cat: 'regressed' })),
        ...data.improved.map(r => ({ ...r, cat: 'improved' })),
        ...(data.pending || []).map(r => ({ ...r, cat: 'pending' })),
        ...data.stable_pass.map(r => ({ ...r, cat: 'stable-pass' })),
        ...data.stable_fail.map(r => ({ ...r, cat: 'stable-fail' })),
    ];

    if (allRows.length > 0) {
        const thead = h('thead', null,
            h('tr', null,
                h('th', null, 'Task'),
                h('th', null, 'Run A'),
                h('th', null, 'Run B')
            )
        );
        const tbody = h('tbody');
        function compareDot(status) {
            return h('span', { className: `status-dot ${status}` });
        }
        function compareLabel(status) {
            if (status === 'pass') return 'Pass';
            if (status === 'running') return 'Running';
            if (status === 'queued') return 'Queued';
            return 'Fail';
        }
        for (const row of allRows) {
            const dotA = compareDot(row.a);
            const dotB = compareDot(row.b);
            const tr = h('tr', { className: `compare-row ${row.cat}` },
                h('td', null, row.task),
                h('td', null, h('span', { className: 'status-text' }, dotA, compareLabel(row.a))),
                h('td', null, h('span', { className: 'status-text' }, dotB, compareLabel(row.b)))
            );
            tbody.appendChild(tr);
        }
        const table = h('table', null, thead, tbody);
        const card = h('div', { className: 'card' },
            h('div', { className: 'card-body table-wrap' }, table)
        );
        container.appendChild(card);
    }

    // Tasks unique to one run
    if (data.only_a.length > 0 || data.only_b.length > 0) {
        const uniqueSection = h('div', { className: 'compare-unique' });
        if (data.only_a.length > 0) {
            uniqueSection.appendChild(h('h3', null, `Only in Run A (${data.only_a.length})`));
            const list = h('div', { className: 'unique-list' });
            for (const t of data.only_a) {
                list.appendChild(h('span', { className: 'unique-task' }, t.task));
            }
            uniqueSection.appendChild(list);
        }
        if (data.only_b.length > 0) {
            uniqueSection.appendChild(h('h3', null, `Only in Run B (${data.only_b.length})`));
            const list = h('div', { className: 'unique-list' });
            for (const t of data.only_b) {
                list.appendChild(h('span', { className: 'unique-task' }, t.task));
            }
            uniqueSection.appendChild(list);
        }
        container.appendChild(uniqueSection);
    }
}

// ---------------------------------------------------------------------------
// Task history page (one task across all runs)
// ---------------------------------------------------------------------------

async function renderTaskHistory(container, taskName) {
    setBreadcrumb([
        { label: 'Dashboard', href: '#/' },
        { label: `${taskName} History` }
    ]);
    container.innerHTML = '<div class="loading">Loading history...</div>';

    try {
        const history = await fetchJSON(`/api/tasks/${encodeURIComponent(taskName)}/history`);

        const header = h('div', { className: 'page-header' },
            h('h1', null, `${taskName}`),
            h('div', { className: 'subtitle' }, `History across ${history.length} run${history.length !== 1 ? 's' : ''}`)
        );

        if (!history.length) {
            container.innerHTML = '';
            container.appendChild(header);
            container.appendChild(h('div', { className: 'empty-state' }, 'No history found for this task.'));
            return;
        }

        const thead = h('thead', null,
            h('tr', null,
                h('th', null, 'Run'),
                h('th', null, 'Result'),
                h('th', null, 'Category'),
                h('th', null, 'Rounds'),
                h('th', null, 'Wasted'),
                h('th', null, 'Tokens'),
                h('th', null, 'Wall Time')
            )
        );

        const tbody = h('tbody');
        for (const entry of history) {
            const dotClass = entry.passed ? 'pass' : failureDotClass(entry.failure_category);
            const row = h('tr', null,
                h('td', null,
                    h('a', {
                        className: 'table-link',
                        href: `#/runs/${encodeURIComponent(entry.job_name)}/tasks/${encodeURIComponent(taskName)}`
                    }, entry.job_name)
                ),
                h('td', null,
                    h('span', { className: 'status-text' },
                        h('span', { className: `status-dot ${dotClass}` }),
                        entry.passed ? 'Pass' : 'Fail'
                    )
                ),
                h('td', null, failureCategoryLabel(entry.failure_category)),
                h('td', { className: 'numeric' }, String(entry.total_rounds || 0)),
                h('td', { className: 'numeric' }, String(entry.wasted_rounds || 0)),
                h('td', { className: 'numeric' }, formatTaskTokens(entry.total_tokens_in, entry.total_tokens_out)),
                h('td', { className: 'numeric' }, formatWallTime(entry.wall_time_sec))
            );
            tbody.appendChild(row);
        }

        const table = h('table', null, thead, tbody);
        const card = h('div', { className: 'card' },
            h('div', { className: 'card-body table-wrap' }, table)
        );

        container.innerHTML = '';
        container.appendChild(header);
        container.appendChild(card);
    } catch (err) {
        container.innerHTML = `<div class="error-msg">Failed to load history: ${escapeHtml(err.message)}</div>`;
    }
}

// ---------------------------------------------------------------------------
// Run detail page
// ---------------------------------------------------------------------------

async function renderRunDetail(container, jobName) {
    setBreadcrumb([
        { label: 'Dashboard', href: '#/' },
        { label: jobName }
    ]);
    container.innerHTML = '<div class="loading">Loading run...</div>';

    try {
        const [run, tasks] = await Promise.all([
            fetchJSON(`/api/runs/${encodeURIComponent(jobName)}`),
            fetchJSON(`/api/runs/${encodeURIComponent(jobName)}/tasks`)
        ]);

        const passRate = run.total_tasks > 0
            ? ((run.passed / run.total_tasks) * 100).toFixed(1)
            : '0';

        // Count failure categories
        const failCounts = {};
        let failTotal = 0;
        for (const t of tasks) {
            if (!t.passed && t.failure_category) {
                failCounts[t.failure_category] = (failCounts[t.failure_category] || 0) + 1;
                failTotal++;
            }
        }

        // Header
        const header = h('div', { className: 'page-header' },
            h('h1', null, jobName),
            h('div', { className: 'subtitle' }, `${run.total_tasks} tasks`)
        );

        // Metadata card
        const dataset = run.dataset_name
            ? `${run.dataset_name} ${run.dataset_version || ''}`.trim()
            : '-';
        const metadataCard = h('div', { className: 'summary-card' },
            h('div', { className: 'summary-metrics' },
                metricBox('Model', stripProviderPrefix(run.model)),
                metricBox('Dataset', dataset),
                metricBox('Git SHA', run.git_sha ? run.git_sha.slice(0, 7) : '-'),
                metricBox('Branch', run.git_branch || '-'),
                metricBox('Started', formatDate(run.started_at)),
                metricBox('Finished', formatDate(run.finished_at)),
                metricBox('Reps', run.reps != null ? run.reps : '-'),
                metricBox('Adapter', run.adapter || '-'),
            )
        );

        // Stat cards
        const stats = h('div', { className: 'stat-row' },
            h('div', { className: 'stat-card' },
                h('div', { className: 'stat-label' }, 'Pass Rate'),
                h('div', { className: 'stat-value' }, `${passRate}%`),
                h('div', { className: 'stat-detail' }, `${run.passed} of ${run.total_tasks}`)
            ),
            h('div', { className: 'stat-card' },
                h('div', { className: 'stat-label' }, 'Passed'),
                h('div', { className: 'stat-value', style: 'color:#18A34A' }, String(run.passed))
            ),
            h('div', { className: 'stat-card' },
                h('div', { className: 'stat-label' }, 'Failed'),
                h('div', { className: 'stat-value', style: 'color:#DC2626' }, String(run.total_tasks - run.passed))
            )
        );

        // Failure breakdown
        let breakdown = null;
        if (failTotal > 0) {
            const items = Object.entries(failCounts).map(([cat, count]) =>
                h('span', { className: 'breakdown-item' },
                    h('span', { className: 'breakdown-count' }, String(count)),
                    failureCategoryLabel(cat)
                )
            );
            breakdown = h('div', { className: 'breakdown-row' }, ...items);
        }

        // Filter bar
        const allCount = tasks.length;
        const passCount = tasks.filter(t => t.passed).length;
        const failCount = tasks.filter(t => !t.passed && t.status !== 'running' && t.status !== 'queued').length;
        const timeoutCount = tasks.filter(t => t.failure_category === 'timeout').length;
        const wrongCount = tasks.filter(t => t.failure_category === 'wrong_answer').length;
        const noSubmitCount = tasks.filter(t => t.failure_category === 'no_submit').length;

        const filters = [
            { key: 'all', label: 'All', count: allCount },
            { key: 'pass', label: 'Pass', count: passCount },
            { key: 'fail', label: 'Fail', count: failCount },
            { key: 'timeout', label: 'Timeout', count: timeoutCount },
            { key: 'wrong_answer', label: 'Wrong Answer', count: wrongCount },
            { key: 'no_submit', label: 'No Submit', count: noSubmitCount },
        ];

        let activeFilter = 'all';
        let searchQuery = '';
        let expandedReps = new Set();
        const filterBar = h('div', { className: 'filter-bar' });

        // Column definitions
        const columns = [
            { key: 'task_name', label: 'Task', sort: (a, b) => a.task_name.localeCompare(b.task_name) },
            { key: 'passed', label: 'Result', sort: (a, b) => (b.passed ? 1 : 0) - (a.passed ? 1 : 0) },
            { key: 'failure_category', label: 'Category', sort: (a, b) => (a.failure_category || '').localeCompare(b.failure_category || '') },
            { key: 'total_rounds', label: 'Rounds', sort: (a, b) => (a.total_rounds || 0) - (b.total_rounds || 0), numeric: true },
            { key: 'wasted_rounds', label: 'Wasted', sort: (a, b) => (a.wasted_rounds || 0) - (b.wasted_rounds || 0), numeric: true },
            { key: 'total_tokens_in', label: 'Tokens', sort: (a, b) => (a.total_tokens_in || 0) - (b.total_tokens_in || 0), numeric: true },
            { key: 'session_count', label: 'Sessions', sort: (a, b) => (a.session_count || 0) - (b.session_count || 0), numeric: true },
            { key: 'max_depth', label: 'Depth', sort: (a, b) => (a.max_depth || 0) - (b.max_depth || 0), numeric: true },
            { key: 'first_submit_round', label: 'Submit @', sort: (a, b) => (a.first_submit_round || 999) - (b.first_submit_round || 999), numeric: true },
            { key: 'wall_time_sec', label: 'Wall Time', sort: (a, b) => (a.wall_time_sec || 0) - (b.wall_time_sec || 0), numeric: true },
        ];

        // Sorting state
        let sortColKey = 'task_name';
        let sortAsc = true;

        function matchesFilter(task) {
            switch (activeFilter) {
                case 'pass': return task.passed;
                case 'fail': return !task.passed && task.status !== 'running' && task.status !== 'queued';
                case 'timeout': return task.failure_category === 'timeout';
                case 'wrong_answer': return task.failure_category === 'wrong_answer';
                case 'no_submit': return task.failure_category === 'no_submit';
                default: return true;
            }
        }

        function matchesSearch(task) {
            if (!searchQuery) return true;
            return task.task_name.toLowerCase().includes(searchQuery.toLowerCase());
        }

        function sortTasks(list) {
            const col = columns.find(c => c.key === sortColKey);
            if (!col) return list;
            return list.slice().sort((a, b) => {
                const cmp = col.sort(a, b);
                return sortAsc ? cmp : -cmp;
            });
        }

        function renderCell(task, col) {
            switch (col.key) {
                case 'task_name': {
                    const link = h('a', {
                        className: 'table-link',
                        href: `#/runs/${encodeURIComponent(jobName)}/tasks/${encodeURIComponent(task.task_name)}`
                    }, task.task_name);
                    if (task.trial_count > 1) {
                        const isExpanded = expandedReps.has(task.task_name);
                        const arrow = h('span', {
                            className: 'rep-toggle',
                            onClick: (e) => {
                                e.preventDefault();
                                e.stopPropagation();
                                if (expandedReps.has(task.task_name)) {
                                    expandedReps.delete(task.task_name);
                                } else {
                                    expandedReps.add(task.task_name);
                                }
                                renderTable();
                            }
                        }, isExpanded ? '\u25BC' : '\u25B6');
                        const badgeText = task.pass_count != null
                            ? `${task.pass_count}/${task.trial_count} pass`
                            : `${task.trial_count} reps`;
                        return h('span', null, arrow, link,
                            h('span', { className: 'rep-badge' }, badgeText));
                    }
                    return link;
                }
                case 'passed': {
                    if (task.status === 'queued') {
                        return h('span', { className: 'status-text' },
                            h('span', { className: 'status-dot queued' }),
                            'Queued');
                    }
                    if (task.status === 'running') {
                        return h('span', { className: 'status-text' },
                            h('span', { className: 'status-dot running' }),
                            'Running');
                    }
                    if (task.trial_count > 1 && task.pass_count != null) {
                        const dotClass = task.pass_count === task.trial_count ? 'pass'
                            : task.pass_count === 0 ? 'fail' : 'partial';
                        return h('span', { className: 'status-text' },
                            h('span', { className: `status-dot ${dotClass}` }),
                            `${task.pass_count}/${task.trial_count} Pass`);
                    }
                    const dotClass = task.passed ? 'pass' : failureDotClass(task.failure_category);
                    return h('span', { className: 'status-text' },
                        h('span', { className: `status-dot ${dotClass}` }),
                        task.passed ? 'Pass' : 'Fail');
                }
                case 'failure_category':
                    return document.createTextNode(failureCategoryLabel(task.failure_category));
                case 'total_rounds':
                    return document.createTextNode(String(task.total_rounds || 0));
                case 'wasted_rounds':
                    return document.createTextNode(String(task.wasted_rounds || 0));
                case 'total_tokens_in':
                    return document.createTextNode(formatTaskTokens(task.total_tokens_in, task.total_tokens_out));
                case 'session_count':
                    return document.createTextNode(String(task.session_count || 0));
                case 'max_depth':
                    return document.createTextNode(String(task.max_depth || 0));
                case 'first_submit_round':
                    return document.createTextNode(task.first_submit_round ? String(task.first_submit_round) : '-');
                case 'wall_time_sec':
                    return document.createTextNode(formatWallTime(task.wall_time_sec));
            }
        }

        // Search box
        const searchBox = h('input', {
            className: 'search-box',
            type: 'text',
            placeholder: 'Search tasks...',
            onInput: (e) => { searchQuery = e.target.value; renderTable(); }
        });

        // Table container
        const tableCard = h('div', { className: 'card' });
        const tableWrap = h('div', { className: 'card-body table-wrap' });
        tableCard.appendChild(tableWrap);

        function renderTable() {
            const filtered = tasks.filter(t => matchesFilter(t) && matchesSearch(t));
            const sorted = sortTasks(filtered);

            const headerRow = h('tr');
            for (const col of columns) {
                const isSorted = sortColKey === col.key;
                const thClass = 'sortable' + (isSorted ? ' sorted' : '');
                const arrow = isSorted ? (sortAsc ? '\u2191' : '\u2193') : '\u2195';
                const th = h('th', {
                    className: thClass,
                    onClick: () => { toggleSort(col.key); }
                }, col.label, h('span', { className: 'sort-arrow' }, arrow));
                headerRow.appendChild(th);
            }
            const thead = h('thead', null, headerRow);

            const tbody = h('tbody');
            for (const task of sorted) {
                const row = h('tr');
                for (const col of columns) {
                    const tdClass = col.numeric ? 'numeric' : null;
                    const td = h('td', tdClass ? { className: tdClass } : null);
                    td.appendChild(renderCell(task, col));
                    row.appendChild(td);
                }
                tbody.appendChild(row);

                // Expanded trial sub-rows for multi-rep tasks
                if (task.trial_count > 1 && expandedReps.has(task.task_name) && task.trials) {
                    for (const trial of task.trials) {
                        const subRow = h('tr', { className: 'trial-row' });
                        // Task name: indented hash linking to trial detail
                        const trialHref = `#/runs/${encodeURIComponent(jobName)}/tasks/${encodeURIComponent(task.task_name)}?trial=${trial.hash}`;
                        subRow.appendChild(h('td', null,
                            h('a', { className: 'trial-hash', href: trialHref }, trial.hash)));
                        // Result: pass/fail dot
                        const trialDotClass = trial.status === 'running' ? 'running'
                            : trial.status === 'queued' ? 'queued'
                            : trial.passed ? 'pass' : 'fail';
                        const trialLabel = trial.status === 'running' ? 'Running'
                            : trial.status === 'queued' ? 'Queued'
                            : trial.passed ? 'Pass' : 'Fail';
                        subRow.appendChild(h('td', null,
                            h('span', { className: 'status-text' },
                                h('span', { className: `status-dot ${trialDotClass}` }),
                                trialLabel)));
                        // Empty cells for remaining columns
                        for (let i = 2; i < columns.length; i++) {
                            subRow.appendChild(h('td'));
                        }
                        tbody.appendChild(subRow);
                    }
                }
            }

            const table = h('table', null, thead, tbody);
            tableWrap.innerHTML = '';
            tableWrap.appendChild(table);
        }

        function toggleSort(colKey) {
            if (sortColKey === colKey) {
                sortAsc = !sortAsc;
            } else {
                sortColKey = colKey;
                sortAsc = true;
            }
            renderTable();
        }

        function renderFilterBar() {
            filterBar.innerHTML = '';
            for (const f of filters) {
                if (f.count === 0 && f.key !== 'all') continue;
                const btn = h('button', {
                    className: `filter-btn${activeFilter === f.key ? ' active' : ''}`,
                    onClick: () => { activeFilter = f.key; renderFilterBar(); renderTable(); }
                }, f.label, h('span', { className: 'count' }, String(f.count)));
                filterBar.appendChild(btn);
            }
        }

        renderFilterBar();
        renderTable();

        // Pass rate bar
        const passBar = h('div', { className: 'pass-bar', style: 'height:8px;margin-bottom:24px' },
            h('div', { className: 'pass-fill',
                style: `width:${run.total_tasks > 0 ? (run.passed / run.total_tasks) * 100 : 0}%` })
        );

        container.innerHTML = '';
        container.appendChild(header);
        container.appendChild(metadataCard);
        container.appendChild(stats);
        container.appendChild(passBar);
        if (breakdown) container.appendChild(breakdown);
        container.appendChild(searchBox);
        container.appendChild(filterBar);
        container.appendChild(tableCard);
    } catch (err) {
        container.innerHTML = `<div class="error-msg">Failed to load run: ${escapeHtml(err.message)}</div>`;
    }
}

// ---------------------------------------------------------------------------
// Task detail page
// ---------------------------------------------------------------------------

async function renderTaskDetail(container, jobName, taskName, trialHash) {
    setBreadcrumb([
        { label: 'Dashboard', href: '#/' },
        { label: jobName, href: `#/runs/${encodeURIComponent(jobName)}` },
        { label: taskName }
    ]);
    container.innerHTML = '<div class="loading">Loading task...</div>';

    try {
        let taskUrl = `/api/runs/${encodeURIComponent(jobName)}/tasks/${encodeURIComponent(taskName)}`;
        if (trialHash) taskUrl += `?trial=${encodeURIComponent(trialHash)}`;
        const task = await fetchJSON(taskUrl);

        const rawFiles = task.raw_files || {};

        // Header with result badge
        let badgeClass, badgeText;
        if (task.status === 'running') {
            badgeClass = 'result-badge running';
            badgeText = 'RUNNING';
        } else if (task.status === 'queued') {
            badgeClass = 'result-badge queued';
            badgeText = 'QUEUED';
        } else {
            badgeClass = task.passed ? 'result-badge pass' : 'result-badge fail';
            badgeText = task.passed ? 'PASS' : 'FAIL';
        }
        const resultBadge = h('span', { className: badgeClass }, badgeText);

        const header = h('div', { className: 'page-header' },
            h('h1', null, taskName, resultBadge),
            h('div', { className: 'subtitle' },
                jobName,
                task.model ? ` \u00b7 ${task.model}` : '',
                task.failure_category ? ` \u00b7 ${failureCategoryLabel(task.failure_category)}` : '',
                rawFiles.result ? ' ' : null,
                rawFiles.result ? rawLink(`/raw/${rawFiles.result}`) : null,
                rawFiles.config ? rawLink(`/raw/${rawFiles.config}`) : null,
                h('a', {
                    href: `#/tasks/${encodeURIComponent(taskName)}/history`,
                    className: 'history-link'
                }, 'View history across runs')
            )
        );

        // ---------------------------------------------------------------
        // Trial picker for multi-rep tasks
        // ---------------------------------------------------------------
        let trialPicker = null;
        if (task.trial_count > 1 && task.trials) {
            const pills = task.trials.map(t => {
                const isActive = task.active_trial === t.hash;
                const dotClass = t.passed ? 'pass' : t.status === 'running' ? 'running' : 'fail';
                const href = `#/runs/${encodeURIComponent(jobName)}/tasks/${encodeURIComponent(taskName)}?trial=${t.hash}`;
                return h('a', {
                    className: `trial-pill${isActive ? ' active' : ''}`,
                    href: href
                },
                    h('span', { className: `status-dot ${dotClass}` }),
                    t.hash
                );
            });
            trialPicker = h('div', { className: 'trial-picker' },
                h('span', { className: 'trial-picker-label' }, 'Trials:'),
                ...pills
            );
        }

        // ---------------------------------------------------------------
        // Task instruction — always visible
        // ---------------------------------------------------------------
        let instructionSection = null;
        if (task.instruction) {
            instructionSection = h('div', { className: 'card' },
                h('div', { className: 'card-header' },
                    h('span', { className: 'card-title' }, 'Task Instruction'),
                ),
                h('pre', { className: 'card-body-pre' }, task.instruction),
            );
        }

        // ---------------------------------------------------------------
        // System prompt — collapsible, collapsed by default
        // ---------------------------------------------------------------
        let systemPromptSection = null;
        if (task.system_prompt) {
            systemPromptSection = twoStateSection('System Prompt', task.system_prompt, false);
        }

        // ---------------------------------------------------------------
        // Summary card — key metrics at a glance
        // ---------------------------------------------------------------
        const summaryCard = h('div', { className: 'summary-card' },
            h('div', { className: 'summary-metrics' },
                metricBox('Rounds', task.total_rounds),
                metricBox('Wasted', task.wasted_rounds),
                metricBox('Tokens In', task.total_tokens_in ? (task.total_tokens_in / 1000).toFixed(1) + 'k' : '-'),
                metricBox('Tokens Out', task.total_tokens_out ? (task.total_tokens_out / 1000).toFixed(1) + 'k' : '-'),
                metricBox('Sessions', task.session_count),
                metricBox('Depth', task.max_depth),
                metricBox('Wall Time', formatWallTime(task.wall_time_sec)),
                metricBox('Submit @', task.first_submit_round || '-'),
            ),
            task.submitted_value ? (() => {
                const sv = h('div', { className: 'submitted-value collapsed' });
                sv.appendChild(h('span', { className: 'label' }, 'Submitted: '));
                const codeEl = h('code', null, truncate(task.submitted_value, 200));
                sv.appendChild(codeEl);
                if (task.submitted_value.length > 200) {
                    sv.style.cursor = 'pointer';
                    sv.addEventListener('click', () => {
                        const isCollapsed = sv.classList.contains('collapsed');
                        sv.classList.toggle('collapsed');
                        codeEl.textContent = isCollapsed ? task.submitted_value : truncate(task.submitted_value, 200);
                    });
                }
                return sv;
            })() : null
        );

        // ---------------------------------------------------------------
        // Verifier output — two-state expand, default open
        // ---------------------------------------------------------------
        let verifierSection = null;
        if (task.test_output) {
            const accent = task.passed ? 'green' : 'red';
            const verifierRawHref = rawFiles.verifier ? `/raw/${rawFiles.verifier}` : null;
            verifierSection = twoStateSection('Verifier Output', task.test_output, true, accent, verifierRawHref);
        }

        // ---------------------------------------------------------------
        // Artifacts — collapsible list of agent-created files
        // ---------------------------------------------------------------
        let artifactSection = null;
        if (rawFiles.artifacts_base) {
            try {
                const artifacts = await fetchJSON(
                    `/api/runs/${encodeURIComponent(jobName)}/tasks/${encodeURIComponent(taskName)}/artifacts`
                );
                if (artifacts.length > 0) {
                    const list = h('div', { className: 'artifact-list' });
                    for (const a of artifacts) {
                        const row = h('div', { className: 'artifact-row' },
                            h('a', {
                                href: a.raw_url,
                                target: '_blank',
                                className: 'artifact-path',
                            }, a.path),
                            h('span', { className: 'artifact-size' }, formatSize(a.size)),
                        );
                        list.appendChild(row);
                    }
                    const section = h('div', { className: 'card two-state collapsed' });
                    const header = h('div', { className: 'card-header clickable' },
                        h('span', { className: 'card-title' }, `Artifacts (${artifacts.length} files)`),
                    );
                    header.addEventListener('click', () => section.classList.toggle('collapsed'));
                    section.appendChild(header);
                    const body = h('div', { className: 'card-body-pre artifact-body' });
                    body.appendChild(list);
                    section.appendChild(body);
                    artifactSection = section;
                }
            } catch (_) {
                // Artifact fetch failed — skip section
            }
        }

        // ---------------------------------------------------------------
        // Agent stdout — two-state expand, default collapsed
        // ---------------------------------------------------------------
        let stdoutSection = null;
        if (task.agent_stdout && task.agent_stdout.trim()) {
            const stdoutRawHref = rawFiles.stdout ? `/raw/${rawFiles.stdout}` : null;
            stdoutSection = twoStateSection('Agent Stdout', task.agent_stdout, false, null, stdoutRawHref);
        }

        // ---------------------------------------------------------------
        // Trajectory — full width
        // ---------------------------------------------------------------
        const trajSearch = h('input', {
            type: 'text',
            placeholder: 'Search rounds...',
            className: 'search-box traj-search',
        });

        const trajectorySection = h('div', { className: 'card' },
            h('div', { className: 'card-header' },
                h('span', { className: 'card-title' }, 'Trajectory'),
                h('div', { className: 'trajectory-controls' },
                    trajSearch,
                    h('button', {
                        className: 'toggle-btn',
                        onClick: () => {
                            const rounds = trajectorySection.querySelectorAll('.timeline-round');
                            const anyExpanded = Array.from(rounds).some(r => r.classList.contains('expanded'));
                            rounds.forEach(r => {
                                if (anyExpanded) r.classList.remove('expanded');
                                else r.classList.add('expanded');
                            });
                        }
                    }, 'Toggle All')
                )
            )
        );

        // Wire up search filtering after section is built
        trajSearch.addEventListener('input', (e) => {
            const term = e.target.value.toLowerCase();
            const rounds = trajectorySection.querySelectorAll('.timeline-round');
            rounds.forEach(r => {
                r.style.display = (!term || r.textContent.toLowerCase().includes(term)) ? '' : 'none';
            });
        });

        const trajectoryBody = h('div', { className: 'card-body-flush' });

        if (task.trajectory && task.trajectory.length > 0) {
            // Count rounds for summary
            let totalRounds = 0;
            let errorRounds = 0;
            for (const session of task.trajectory) {
                const traj = session.trajectory || [];
                for (const r of traj) {
                    totalRounds++;
                    if (r.action === 'ERROR') errorRounds++;
                }
            }

            const roundInfo = h('div', { className: 'trajectory-summary' },
                `${totalRounds} rounds`,
                errorRounds > 0 ? ` (${errorRounds} empty)` : '',
                task.trajectory.length > 1 ? ` across ${task.trajectory.length} transcript files` : ''
            );
            trajectoryBody.appendChild(roundInfo);

            for (const session of task.trajectory) {
                trajectoryBody.appendChild(renderSession(session, 0));
            }
        } else {
            trajectoryBody.appendChild(
                h('div', { className: 'empty-state' }, 'No trajectory data.')
            );
        }

        trajectorySection.appendChild(trajectoryBody);

        // ---------------------------------------------------------------
        // All files — collapsible directory listing
        // ---------------------------------------------------------------
        let filesSection = null;
        if (task.all_files && task.all_files.length > 0) {
            const groups = {};
            for (const f of task.all_files) {
                const topDir = f.path.includes('/') ? f.path.split('/')[0] : '(root)';
                if (!groups[topDir]) groups[topDir] = [];
                groups[topDir].push(f);
            }

            const list = h('div', { className: 'artifact-body' });
            for (const dir of Object.keys(groups).sort()) {
                list.appendChild(h('div', {
                    style: 'font-size:12px;font-weight:600;color:#6B6B6B;text-transform:uppercase;letter-spacing:0.5px;margin:12px 0 4px;'
                }, dir));
                const dirList = h('div', { className: 'artifact-list' });
                for (const f of groups[dir]) {
                    dirList.appendChild(h('div', { className: 'artifact-row' },
                        h('a', {
                            href: f.raw_url,
                            target: '_blank',
                            className: 'artifact-path',
                        }, f.path),
                        h('span', { className: 'artifact-size' }, formatSize(f.size)),
                    ));
                }
                list.appendChild(dirList);
            }

            filesSection = h('div', { className: 'card two-state collapsed' });
            const filesHeader = h('div', { className: 'card-header clickable' },
                h('span', { className: 'card-title' }, `Files (${task.all_files.length})`),
            );
            filesHeader.addEventListener('click', () => filesSection.classList.toggle('collapsed'));
            filesSection.appendChild(filesHeader);
            const filesBody = h('div', { className: 'card-body-pre' });
            filesBody.style.whiteSpace = 'normal';
            filesBody.appendChild(list);
            filesSection.appendChild(filesBody);
        }

        // Assemble page
        container.innerHTML = '';
        container.appendChild(header);
        if (trialPicker) container.appendChild(trialPicker);
        if (instructionSection) container.appendChild(instructionSection);
        if (systemPromptSection) container.appendChild(systemPromptSection);
        container.appendChild(summaryCard);
        if (verifierSection) container.appendChild(verifierSection);
        if (artifactSection) container.appendChild(artifactSection);
        if (stdoutSection) container.appendChild(stdoutSection);
        container.appendChild(trajectorySection);
        if (filesSection) container.appendChild(filesSection);
    } catch (err) {
        container.innerHTML = `<div class="error-msg">Failed to load task: ${escapeHtml(err.message)}</div>`;
    }
}

// ---------------------------------------------------------------------------
// Trajectory rendering
// ---------------------------------------------------------------------------

function renderSession(session, depth) {
    const block = h('div', { className: 'session-block' });

    // Session label for child sessions
    if (depth > 0) {
        const label = session.model
            ? `Subagent (${session.model}, depth ${session.depth || depth})`
            : `Subagent (depth ${session.depth || depth})`;
        block.appendChild(h('div', { className: 'session-label' }, label));
    }

    const timeline = h('div', { className: 'timeline' });
    const rounds = session.trajectory || [];
    const children = session.children || [];

    // Build lookup: parent_tool_call_id → child
    const childByToolCallId = {};
    const unmatchedChildren = [];
    for (const child of children) {
        if (child.parent_tool_call_id) {
            childByToolCallId[child.parent_tool_call_id] = child;
        } else {
            unmatchedChildren.push(child);
        }
    }

    // For positional fallback: queue of unmatched children to assign to SPAWN rounds
    let unmatchedIdx = 0;

    for (const round of rounds) {
        timeline.appendChild(renderRound(round));

        // After a SPAWN round, inline the child session that was spawned
        if (round.action === 'SPAWN' && round.tool_calls) {
            for (const tc of round.tool_calls) {
                const tcId = tc.id || tc.tool_call_id || '';
                // Try to match by tool call ID first
                if (tcId && childByToolCallId[tcId]) {
                    const child = childByToolCallId[tcId];
                    delete childByToolCallId[tcId];
                    timeline.appendChild(renderSession(child, (session.depth || 0) + 1));
                }
            }
            // Positional fallback: if no ID match happened, try next unmatched child
            if (unmatchedChildren.length > unmatchedIdx) {
                const matched = round.tool_calls.some(tc => {
                    const tcId = tc.id || tc.tool_call_id || '';
                    // Already consumed above — check if it was in the map
                    return tcId && !childByToolCallId[tcId] && children.some(
                        c => c.parent_tool_call_id === tcId
                    );
                });
                if (!matched) {
                    timeline.appendChild(
                        renderSession(unmatchedChildren[unmatchedIdx++], (session.depth || 0) + 1)
                    );
                }
            }
        }
    }

    // Any remaining unmatched children (no SPAWN round found) — append at end
    for (; unmatchedIdx < unmatchedChildren.length; unmatchedIdx++) {
        timeline.appendChild(renderSession(unmatchedChildren[unmatchedIdx], (session.depth || 0) + 1));
    }
    for (const child of Object.values(childByToolCallId)) {
        timeline.appendChild(renderSession(child, (session.depth || 0) + 1));
    }

    if (depth > 0) {
        const wrapper = h('div', { className: 'child-session' }, timeline);
        block.appendChild(wrapper);
    } else {
        block.appendChild(timeline);
    }

    return block;
}

function renderRound(round) {
    const action = round.action || 'PLAN';
    const roundNum = round.round || 0;
    const tokens = formatTokens(round.usage);

    const el = h('div', { className: 'timeline-round' });

    // Dot
    el.appendChild(h('div', { className: `timeline-dot ${action}` }));

    // Build rich single-line content
    const headerItems = [
        h('span', { className: 'round-num' }, `#${roundNum}`),
        h('span', { className: `round-action ${action}` }, action),
    ];

    // Show per-tool one-liners for maximum information density
    if (round.tool_calls && round.tool_calls.length > 0) {
        const toolLines = round.tool_calls.map(tc => toolCallOneLiner(tc));
        // Show first 3 tools inline, truncate rest
        const shown = toolLines.slice(0, 3);
        const remaining = toolLines.length - shown.length;
        const toolText = shown.join(' ; ') + (remaining > 0 ? ` (+${remaining} more)` : '');
        headerItems.push(h('span', { className: 'round-tools' }, toolText));
    } else if (round.summary) {
        headerItems.push(h('span', { className: 'round-summary' }, round.summary));
    }

    if (tokens) {
        headerItems.push(h('span', { className: 'round-tokens' }, tokens));
    }

    el.appendChild(h('div', { className: 'round-header' }, ...headerItems));

    // Detail (expanded on click)
    const detail = h('div', { className: 'round-detail' });

    // Assistant text
    if (round.text && round.text.trim()) {
        detail.appendChild(h('pre', { className: 'round-text' }, round.text));
    }

    // Tool calls with results side-by-side
    if (round.tool_calls && round.tool_calls.length > 0) {
        for (let i = 0; i < round.tool_calls.length; i++) {
            const tc = round.tool_calls[i];
            const tr = (round.tool_results && round.tool_results[i]) || null;
            detail.appendChild(renderToolCall(tc, tr));
        }
    }

    // Usage
    if (tokens) {
        detail.appendChild(h('div', { className: 'usage-line' }, tokens));
    }

    el.appendChild(detail);

    // Click to expand/collapse — but not if clicking inside detail area or buttons
    const headerEl = el.querySelector('.round-header');
    headerEl.style.cursor = 'pointer';
    headerEl.addEventListener('click', (e) => {
        el.classList.toggle('expanded');
    });

    return el;
}

function prettyPrintJSON(obj) {
    // Format JSON for human reading: string values with embedded \n and \t
    // are rendered with actual newlines/tabs so humans can read them.
    const raw = JSON.stringify(obj, null, 2);
    // Match JSON string values (respecting escaped quotes inside)
    return raw.replace(/"((?:[^"\\]|\\.)*)"/g, (match, content) => {
        if (!content.includes('\\n') && !content.includes('\\t')) return match;
        // Unescape the string content for display
        try {
            const unescaped = JSON.parse('"' + content + '"');
            // Use triple-quote style for multi-line values
            return '"""\n' + unescaped + '\n"""';
        } catch {
            return match;
        }
    });
}

function renderToolCall(tc, tr) {
    const block = h('div', { className: 'tool-block' });
    const name = tc.name || 'unknown';
    const argsRaw = tc.arguments;
    let argsStr = '';
    if (typeof argsRaw === 'string') {
        try { argsStr = prettyPrintJSON(JSON.parse(argsRaw)); }
        catch { argsStr = argsRaw; }
    } else if (argsRaw && typeof argsRaw === 'object') {
        argsStr = prettyPrintJSON(argsRaw);
    }

    block.appendChild(h('div', { className: 'tool-header' },
        h('span', { className: 'tool-name' }, name)
    ));

    if (argsStr) {
        block.appendChild(h('pre', { className: 'tool-content' }, argsStr));
    }

    if (tr) {
        const resultContent = tr.content || '';
        const isError = tr.is_error || false;
        const resultLabel = h('div', { className: 'tool-result-header' },
            'Result',
            isError ? h('span', { className: 'tool-error-badge' }, 'error') : null
        );
        block.appendChild(resultLabel);
        if (resultContent) {
            const cls = isError ? 'tool-content tool-error' : 'tool-content';
            block.appendChild(h('pre', { className: cls }, resultContent));
        }
    }

    return block;
}
