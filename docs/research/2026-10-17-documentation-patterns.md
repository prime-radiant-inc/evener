# Documentation patterns for Evener

Research date: 2026-10-17. This study concerns documentation structure and writing, not verified Claude Code or Codex runtime behavior.

## Findings

A strong coding-agent guide gets readers to a checked result before introducing the full product. It separates tasks from exhaustive references, explains which configuration actually takes effect, and treats completion, failure and recovery as contracts rather than reassuring prose.

Evener should adopt these patterns:

1. **Start with a complete task.** Installation, provider setup, first input and independent result verification belong in one continuous path. Distinguish shell commands from prompts sent to the agent.
2. **Give readers choices in their terms.** Explain when to use a skill, hook, plugin, MCP tool or delegate before presenting their formats.
3. **Assign each rule one owner.** Task guides link to configuration and protocol references instead of repeating their details.
4. **Explain configuration resolution.** State source, scope, precedence, merge rules, trust, reload timing and inspection for each subsystem. A single universal ladder can be misleading.
5. **Document the caller's contract.** Protocol guides need transport, authentication, initialization, identifiers, event order, partial output, terminal outcomes, cancellation, retry, reconnect and compatibility before a method catalog.
6. **Verify procedures.** Show what success looks like, what a failed check means, and how to recover without destroying work. A process exit, accepted request or generated answer may not establish the intended outcome.
7. **Make the static source useful.** Markdown exports must include complete explanations and reference data. Interactive components must not hide tables behind unavailable JavaScript variables.
8. **Put boundaries beside actions.** Explain external effects, credential exposure and restrictions beside the relevant command, rather than only in a distant security chapter.

These are editorial choices. They do not establish that Evener supports a competitor's settings, approval model, schema exporter, extension budgets or distribution channels.

## Strong source examples

### Claude Code

- [Quickstart](https://code.claude.com/docs/en/quickstart): a working path before feature taxonomy.
- [Features overview](https://code.claude.com/docs/en/features-overview): comparisons that help readers choose an extension mechanism.
- [Debug your configuration](https://code.claude.com/docs/en/debug-your-config): inspect active configuration and isolate the source of a problem.
- [Create plugins](https://code.claude.com/docs/en/plugins/create): create, validate, load and invoke a concrete artifact.
- [Agent loop](https://code.claude.com/docs/en/agent-sdk/agent-loop): distinguish a message's completion from the whole task's result.
- [LLM gateway protocol](https://code.claude.com/docs/en/llm-gateway-protocol): semantics, framing and failure contracts beyond a list of endpoints.
- [Channels reference](https://code.claude.com/docs/en/channels-reference): correlation, delivery and trust boundaries.
- [Settings reference](https://code.claude.com/docs/en/settings-reference) and [environment variables](https://code.claude.com/docs/en/env-vars): per-key exceptions to general precedence and merge rules.

The six largest references reinforce the need to distinguish partial success, uncertain persistence, safe retries, asynchronous lifetime and effective configuration. They also show the cost of oversized pages and presentation code leaking into Markdown exports.

### Codex

The [official documentation index](https://developers.openai.com/codex/llms.txt) now points into the combined documentation at `learn.chatgpt.com`. The following are the canonical Markdown sources read during this study:

- [Prompting](https://learn.chatgpt.com/docs/prompting.md): when to use a recipe, steps, context and verification.
- [Configuration basics](https://learn.chatgpt.com/docs/config-file/config-basic.md): a scoped precedence ladder and separate constraints.
- [App server](https://learn.chatgpt.com/docs/app-server.md): primitives and lifecycle before methods, with request/response examples and failure behavior.
- [Security cookbook](https://learn.chatgpt.com/docs/enterprise/security-cookbook.md): intended result, prerequisites, configuration, positive and negative checks, rollback and boundaries.
- [Troubleshooting](https://learn.chatgpt.com/docs/reference/troubleshooting.md): symptoms, causes, probes and recovery.

Codex's surface-specific sections offer useful paths for different clients. Its merged-product index also illustrates a findability risk. Evener should keep one focused documentation tree, avoid duplicate rules, and ensure its exported references contain the actual data rather than unresolved renderer variables.

## Coverage and limitations

Research agents used OpenAI Codex and Lunaroute. Coverage was recorded per URL, not inferred from successful downloads.

The [coverage ledger](2026-10-17-documentation-coverage.json) retains each source URL, content hash, byte length and review method without storing the downloaded documentation in this repository.

| Corpus | Reviewed | Explicit exceptions |
| --- | --- | --- |
| Claude Code | 201 current nonsymbol pages: 186 whole-content tool-assisted reviews, two direct reads, seven mixed reader/direct reads, six oversized references read in consecutive chunks | Two generated SDK symbol catalogs and 26 historical pages inventoried rather than read fully |
| Codex | 130 current pages read fully | One plugin changelog sampled; the separate HTML-only site changelog sampled |

The Claude inventory contains 229 unique URLs. The six oversized references total 1,672,146 bytes and were read in 78 gap-free chunks. Whole-content tool-assisted review means a reader tool analyzed the whole page; it is not a claim that the coordinating researcher read every original line verbatim.

The corrected Codex ledger contains 131 unique Markdown URLs, all fetched successfully. Parent verification summed the retained file lengths to **1,988,822 bytes**; this replaces the researcher's incorrect aggregate of 1,995,284 bytes. Its initial report also overstated full-read coverage. The researcher returned to finish partial and missed pages, and the final per-URL ledger supersedes those initial counts.

The parent independently verified source lengths and hashes, URL uniqueness, coverage-state counts, and contiguous ranges for the six oversized references. It also fetched the official Codex index and configuration-basics page to check the claimed canonical host and precedence presentation.

No competitor CLI examples were executed. The study did not test their runtime guarantees, browser accessibility, every external integration link, translated copies, or historical release material. The findings do not claim those checks.

## Application to Evener

The [design](../superpowers/specs/2026-10-17-documentation-overhaul-design.md) assigns content owners and editorial failure criteria. The [implementation plan](../superpowers/plans/2026-10-17-documentation-overhaul.md) assigns isolated writing scopes and independent checks.

Jesse's product scope is explicit: omit the retiring TUI, retain the CLI, and publish documentation for main automatically to GitHub Pages. The first-run path therefore uses a snapshot build and explains the release alternative. Public navigation and search exclude this research record, internal implementation plans and historical mockups.
