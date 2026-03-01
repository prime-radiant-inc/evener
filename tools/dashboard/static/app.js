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
    if ((m = hash.match(/^#\/runs\/([^/]+)\/tasks\/([^/]+)$/))) {
        renderTaskDetail(app, decodeURIComponent(m[1]), decodeURIComponent(m[2]));
    } else if ((m = hash.match(/^#\/runs\/([^/]+)$/))) {
        renderRunDetail(app, decodeURIComponent(m[1]));
    } else {
        renderDashboard(app);
    }
}

window.addEventListener('hashchange', route);
window.addEventListener('DOMContentLoaded', route);

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

        const header = h('div', { className: 'page-header' },
            h('h1', null, 'Eval Runs'),
            h('div', { className: 'subtitle' }, `${runs.length} run${runs.length !== 1 ? 's' : ''}`)
        );

        const thead = h('thead', null,
            h('tr', null,
                h('th', null, 'Run'),
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

            const row = h('tr', null,
                h('td', null,
                    h('a', { className: 'table-link', href: `#/runs/${encodeURIComponent(run.job_name)}` },
                        run.job_name)
                ),
                h('td', null, String(run.total_tasks)),
                h('td', null,
                    h('div', { className: 'pass-info' },
                        h('span', { className: 'pass-fraction' }, `${run.passed}/${run.total_tasks}`),
                        h('span', { className: 'pass-pct' }, `${passRate}%`)
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
        const failCount = tasks.filter(t => !t.passed).length;
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
        const filterBar = h('div', { className: 'filter-bar' });

        // Sorting state
        let sortCol = 'name';
        let sortAsc = true;

        function matchesFilter(task) {
            switch (activeFilter) {
                case 'pass': return task.passed;
                case 'fail': return !task.passed;
                case 'timeout': return task.failure_category === 'timeout';
                case 'wrong_answer': return task.failure_category === 'wrong_answer';
                case 'no_submit': return task.failure_category === 'no_submit';
                default: return true;
            }
        }

        function sortTasks(list) {
            return list.slice().sort((a, b) => {
                let cmp;
                if (sortCol === 'name') {
                    cmp = a.task_name.localeCompare(b.task_name);
                } else {
                    cmp = (b.passed ? 1 : 0) - (a.passed ? 1 : 0);
                    if (cmp === 0) {
                        cmp = (a.failure_category || '').localeCompare(b.failure_category || '');
                    }
                }
                return sortAsc ? cmp : -cmp;
            });
        }

        // Table container
        const tableCard = h('div', { className: 'card' });
        const tableWrap = h('div', { className: 'card-body table-wrap' });
        tableCard.appendChild(tableWrap);

        function renderTable() {
            const filtered = tasks.filter(matchesFilter);
            const sorted = sortTasks(filtered);

            const nameThClass = 'sortable' + (sortCol === 'name' ? ' sorted' : '');
            const resultThClass = 'sortable' + (sortCol === 'result' ? ' sorted' : '');
            const nameArrow = sortCol === 'name' ? (sortAsc ? '\u2191' : '\u2193') : '\u2195';
            const resultArrow = sortCol === 'result' ? (sortAsc ? '\u2191' : '\u2193') : '\u2195';

            const thead = h('thead', null,
                h('tr', null,
                    h('th', { className: nameThClass, onClick: () => { toggleSort('name'); } },
                        'Task', h('span', { className: 'sort-arrow' }, nameArrow)),
                    h('th', { className: resultThClass, onClick: () => { toggleSort('result'); } },
                        'Result', h('span', { className: 'sort-arrow' }, resultArrow)),
                    h('th', null, 'Category'),
                    h('th', null, 'Transcripts')
                )
            );

            const tbody = h('tbody');
            for (const task of sorted) {
                const dotClass = task.passed ? 'pass' : failureDotClass(task.failure_category);
                const statusLabel = task.passed ? 'Pass' : 'Fail';

                const row = h('tr', null,
                    h('td', null,
                        h('a', {
                            className: 'table-link',
                            href: `#/runs/${encodeURIComponent(jobName)}/tasks/${encodeURIComponent(task.task_name)}`
                        }, task.task_name)
                    ),
                    h('td', null,
                        h('span', { className: 'status-text' },
                            h('span', { className: `status-dot ${dotClass}` }),
                            statusLabel
                        )
                    ),
                    h('td', null, failureCategoryLabel(task.failure_category)),
                    h('td', null, String(task.session_count))
                );
                tbody.appendChild(row);
            }

            const table = h('table', null, thead, tbody);
            tableWrap.innerHTML = '';
            tableWrap.appendChild(table);
        }

        function toggleSort(col) {
            if (sortCol === col) {
                sortAsc = !sortAsc;
            } else {
                sortCol = col;
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
        container.appendChild(stats);
        container.appendChild(passBar);
        if (breakdown) container.appendChild(breakdown);
        container.appendChild(filterBar);
        container.appendChild(tableCard);
    } catch (err) {
        container.innerHTML = `<div class="error-msg">Failed to load run: ${escapeHtml(err.message)}</div>`;
    }
}

// ---------------------------------------------------------------------------
// Task detail page
// ---------------------------------------------------------------------------

async function renderTaskDetail(container, jobName, taskName) {
    setBreadcrumb([
        { label: 'Dashboard', href: '#/' },
        { label: jobName, href: `#/runs/${encodeURIComponent(jobName)}` },
        { label: taskName }
    ]);
    container.innerHTML = '<div class="loading">Loading task...</div>';

    try {
        const task = await fetchJSON(
            `/api/runs/${encodeURIComponent(jobName)}/tasks/${encodeURIComponent(taskName)}`
        );

        // Header with result badge
        const resultBadge = h('span', {
            className: task.passed ? 'result-badge pass' : 'result-badge fail'
        }, task.passed ? 'PASS' : 'FAIL');

        const header = h('div', { className: 'page-header' },
            h('h1', null, taskName, resultBadge),
            h('div', { className: 'subtitle' },
                jobName,
                task.model ? ` \u00b7 ${task.model}` : '',
                task.failure_category ? ` \u00b7 ${failureCategoryLabel(task.failure_category)}` : ''
            )
        );

        // ---------------------------------------------------------------
        // Verifier output — full width, prominent, FIRST
        // ---------------------------------------------------------------
        let verifierSection = null;
        if (task.test_output) {
            verifierSection = h('div', { className: 'card verifier-card' },
                h('div', { className: 'card-header' },
                    h('span', { className: 'card-title' }, 'Verifier Output'),
                    h('button', {
                        className: 'toggle-btn',
                        onClick: (e) => {
                            const pre = e.target.closest('.card').querySelector('.verifier-output');
                            pre.classList.toggle('collapsed');
                            e.target.textContent = pre.classList.contains('collapsed') ? 'Expand' : 'Collapse';
                        }
                    }, 'Collapse')
                ),
                h('pre', { className: 'verifier-output' }, task.test_output)
            );
        }

        // ---------------------------------------------------------------
        // Agent stdout (if available)
        // ---------------------------------------------------------------
        let stdoutSection = null;
        if (task.agent_stdout && task.agent_stdout.trim()) {
            stdoutSection = h('div', { className: 'card' },
                h('div', { className: 'card-header' },
                    h('span', { className: 'card-title' }, 'Agent Stdout'),
                    h('button', {
                        className: 'toggle-btn',
                        onClick: (e) => {
                            const pre = e.target.closest('.card').querySelector('.stdout-output');
                            pre.classList.toggle('collapsed');
                            e.target.textContent = pre.classList.contains('collapsed') ? 'Expand' : 'Collapse';
                        }
                    }, 'Expand')
                ),
                h('pre', { className: 'stdout-output collapsed' }, task.agent_stdout)
            );
        }

        // ---------------------------------------------------------------
        // Trajectory — full width
        // ---------------------------------------------------------------
        const trajectorySection = h('div', { className: 'card' },
            h('div', { className: 'card-header' },
                h('span', { className: 'card-title' }, 'Trajectory'),
                h('div', { className: 'trajectory-controls' },
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

        // Assemble page
        container.innerHTML = '';
        container.appendChild(header);
        if (verifierSection) container.appendChild(verifierSection);
        if (stdoutSection) container.appendChild(stdoutSection);
        container.appendChild(trajectorySection);
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
        try {
            argsStr = prettyPrintJSON(JSON.parse(argsRaw));
        } catch {
            argsStr = argsRaw;
        }
    } else if (argsRaw && typeof argsRaw === 'object') {
        argsStr = prettyPrintJSON(argsRaw);
    }

    block.appendChild(h('div', { className: 'tool-header' },
        h('span', { className: 'tool-name' }, name)
    ));

    if (argsStr) {
        block.appendChild(makeExpandable(argsStr, 'tool-content', 200));
    }

    // Tool result
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
            block.appendChild(makeExpandable(resultContent, cls, 200));
        }
    }

    return block;
}

function makeExpandable(text, className, threshold) {
    const isLong = text.length > threshold;
    const pre = h('pre', { className: className + (isLong ? ' collapsed' : '') }, text);

    if (isLong) {
        const wrapper = h('div', { className: 'expandable-wrapper' });
        wrapper.appendChild(pre);
        const btn = h('button', { className: 'expand-btn' }, 'Show more');
        btn.addEventListener('click', (e) => {
            e.stopPropagation();
            const wasCollapsed = pre.classList.contains('collapsed');
            pre.classList.toggle('collapsed');
            btn.textContent = wasCollapsed ? 'Show less' : 'Show more';
        });
        wrapper.appendChild(btn);
        return wrapper;
    }

    return pre;
}
