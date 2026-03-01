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
        } else {
            el.appendChild(child);
        }
    }
    return el;
}

function pct(n, d) {
    if (d === 0) return '0%';
    return Math.round((n / d) * 100) + '%';
}

function formatTokens(usage) {
    if (!usage) return '';
    const inp = usage.input_tokens || 0;
    const out = usage.output_tokens || 0;
    if (inp === 0 && out === 0) return '';
    return `${(inp / 1000).toFixed(1)}k in / ${(out / 1000).toFixed(1)}k out`;
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
                    h('div', { className: 'pass-bar', style: `width:120px` },
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
        let sortCol = 'name'; // 'name' or 'result'
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
                    // Sort by result: pass first, then by category
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
                    h('th', null, 'Sessions')
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

        const header = h('div', { className: 'page-header' },
            h('h1', null, taskName),
            h('div', { className: 'subtitle' }, jobName)
        );

        // Left: trajectory
        const trajectoryCol = h('div', { className: 'trajectory' });
        if (task.trajectory && task.trajectory.length > 0) {
            for (const session of task.trajectory) {
                trajectoryCol.appendChild(renderSession(session, 0));
            }
        } else {
            trajectoryCol.appendChild(
                h('div', { className: 'empty-state' }, 'No trajectory data.')
            );
        }

        // Right: context panel
        const contextPanel = h('div', { className: 'context-panel' });

        // Result card
        const resultCard = h('div', { className: 'card' });
        const resultSection = h('div', { className: 'context-section' },
            h('div', { className: 'context-label' }, 'Result'),
            h('div', {
                className: task.passed ? 'context-value result-pass' : 'context-value result-fail'
            }, task.passed ? 'PASS' : 'FAIL')
        );
        resultCard.appendChild(resultSection);

        if (task.failure_category) {
            resultCard.appendChild(h('div', { className: 'context-section' },
                h('div', { className: 'context-label' }, 'Failure Category'),
                h('div', { className: 'context-value' }, failureCategoryLabel(task.failure_category))
            ));
        }

        if (task.model) {
            resultCard.appendChild(h('div', { className: 'context-section' },
                h('div', { className: 'context-label' }, 'Model'),
                h('div', { className: 'context-value' }, task.model)
            ));
        }

        if (task.reward != null) {
            resultCard.appendChild(h('div', { className: 'context-section' },
                h('div', { className: 'context-label' }, 'Reward'),
                h('div', { className: 'context-value' }, String(task.reward))
            ));
        }

        if (task.session_count != null) {
            resultCard.appendChild(h('div', { className: 'context-section' },
                h('div', { className: 'context-label' }, 'Sessions'),
                h('div', { className: 'context-value' }, String(task.session_count))
            ));
        }

        contextPanel.appendChild(resultCard);

        // Test output card
        if (task.test_output) {
            const testCard = h('div', { className: 'card' },
                h('div', { className: 'context-section' },
                    h('div', { className: 'context-label' }, 'Verifier Output'),
                    h('pre', { className: 'test-output' }, task.test_output)
                )
            );
            contextPanel.appendChild(testCard);
        }

        // Layout
        const layout = h('div', { className: 'task-layout' },
            trajectoryCol,
            contextPanel
        );

        container.innerHTML = '';
        container.appendChild(header);
        container.appendChild(layout);
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
        block.appendChild(h('div', { className: 'session-label' },
            `Session: ${session.session_id || 'child'} (depth ${session.depth || depth})`
        ));
    }

    const timeline = h('div', { className: 'timeline' });
    const rounds = session.trajectory || [];

    for (const round of rounds) {
        timeline.appendChild(renderRound(round));
    }

    if (depth > 0) {
        const wrapper = h('div', { className: 'child-session' }, timeline);
        block.appendChild(wrapper);
    } else {
        block.appendChild(timeline);
    }

    // Render children inline after their parent's spawn round
    if (session.children && session.children.length > 0) {
        for (const child of session.children) {
            block.appendChild(renderSession(child, (session.depth || 0) + 1));
        }
    }

    return block;
}

function renderRound(round) {
    const action = round.action || 'PLAN';
    const roundNum = round.round || 0;
    const summary = round.summary || '';
    const tokens = formatTokens(round.usage);

    const el = h('div', { className: 'timeline-round' });

    // Dot
    el.appendChild(h('div', { className: `timeline-dot ${action}` }));

    // Header row
    const headerItems = [
        h('span', { className: 'round-num' }, `#${roundNum}`),
        h('span', { className: `round-action ${action}` }, action),
        h('span', { className: 'round-summary', title: summary }, summary),
    ];
    if (tokens) {
        headerItems.push(h('span', { className: 'round-tokens' }, tokens));
    }
    el.appendChild(h('div', { className: 'round-header' }, ...headerItems));

    // Detail (expanded on click)
    const detail = h('div', { className: 'round-detail' });

    // Assistant text
    if (round.text && round.text.trim()) {
        detail.appendChild(h('div', { className: 'round-text' }, round.text));
    }

    // Tool calls
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

    // Click to expand/collapse
    el.addEventListener('click', (e) => {
        // Don't toggle if clicking a show-more button
        if (e.target.classList.contains('show-more')) return;
        el.classList.toggle('expanded');
    });

    return el;
}

function renderToolCall(tc, tr) {
    const block = h('div', { className: 'tool-block' });

    // Tool call header + args
    const name = tc.name || 'unknown';
    const argsRaw = tc.arguments;
    let argsStr = '';
    if (typeof argsRaw === 'string') {
        try {
            argsStr = JSON.stringify(JSON.parse(argsRaw), null, 2);
        } catch {
            argsStr = argsRaw;
        }
    } else if (argsRaw && typeof argsRaw === 'object') {
        argsStr = JSON.stringify(argsRaw, null, 2);
    }

    block.appendChild(h('div', { className: 'tool-header' },
        'Call: ', h('span', { className: 'tool-name' }, name)
    ));

    if (argsStr) {
        block.appendChild(makeCollapsible(argsStr, 300));
    }

    // Tool result
    if (tr) {
        const resultContent = tr.content || '';
        const isError = tr.is_error || false;
        block.appendChild(h('div', { className: 'tool-header', style: 'margin-top:6px' },
            'Result:',
            isError ? h('span', { style: 'color:#DC2626;margin-left:6px;font-weight:400' }, '(error)') : null
        ));
        if (resultContent) {
            const resultEl = makeCollapsible(resultContent, 300);
            if (isError) resultEl.querySelector('.tool-content').classList.add('tool-error');
            block.appendChild(resultEl);
        }
    }

    return block;
}

function makeCollapsible(text, threshold) {
    const wrapper = h('div');
    const isLong = text.length > threshold;
    const content = h('pre', { className: `tool-content${isLong ? ' collapsed' : ''}` }, text);
    wrapper.appendChild(content);

    if (isLong) {
        const btn = h('button', { className: 'show-more' }, 'Show more');
        btn.addEventListener('click', (e) => {
            e.stopPropagation();
            const isCollapsed = content.classList.contains('collapsed');
            content.classList.toggle('collapsed');
            btn.textContent = isCollapsed ? 'Show less' : 'Show more';
        });
        wrapper.appendChild(btn);
    }

    return wrapper;
}
