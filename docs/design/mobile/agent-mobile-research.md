# Codex / ChatGPT and Claude Code mobile

Research added 5 September 2026 at Jesse’s request. This extends the initial lookbook with directly comparable agent products and community experience. [Open the visual comparisons](agent-mobile-lookbook.html).

## How to read the evidence

This is a purposive sample of relevant public discussions, not a representative survey or a ranking. We selected concrete mobile workflows, both praise and criticism, and reports with useful context. Votes are not a prevalence measure. Self-promotional alternative-app posts were excluded from the main findings. Product documentation describes supported behavior; screenshots show a particular moment; user reports identify experiences to investigate. None alone proves current reliability across devices.

We searched X as well as Reddit. Direct X pages were unavailable to the browsing tool. An indexed X screenshot is included as limited visual evidence, with its original link; we did not inspect its replies or infer wider sentiment from it. Reddit discussions and a GitHub report provide the inspectable feedback below. Historical limitations are not silently carried forward as current facts.

## Codex / ChatGPT: controls for substantial work

OpenAI’s [23 June walkthrough](https://developers.openai.com/blog/mastering-codex-remote-for-engineering) is the most useful visual reference: host/worktree selection, inline skills, selected-text actions and inline diff comments. It also explains the distinction between queuing a prompt and steering ongoing work. These are concrete mobile interactions to study, rather than an argument for a tiny terminal.

The current [Remote documentation](https://learn.chatgpt.com/docs/remote-connections) describes host-provided context and reconnect/setup troubleshooting. Its naming differs from some launch-era screenshots and comments. For Evener, a stable, discoverable place for saved hubs and sessions is more important than adopting any competitor’s navigation terminology.

**What to borrow:** contextual controls close to the work, explicit execution destination, and a short path from reading a change to discussing it. **What to improve:** reveal advanced capability without depending on people knowing a slash command, and make connection failures actionable instead of leaving a generic waiting screen.

### What people value and dislike

| Source | Reported experience | Evener implication |
| --- | --- | --- |
| [r/OpenaiCodex: using Codex on a phone](https://www.reddit.com/r/OpenaiCodex/comments/1v54g2e/how_to_use_the_codex_in_the_mobile_phone/) · displayed as one month old | One commenter describes doing most coding from the phone and praises the ease of the Remote UI. Others value creating sessions and planning remotely. | Design for substantial initiation and direction of work, not just passive monitoring. This is anecdotal enthusiasm, not proof of universal preference. |
| [r/codex launch discussion](https://www.reddit.com/r/codex/comments/1tda58v/now_in_preview_codex_mobile_in_the_chatgpt_mobile/) · May launch-era thread | Reports include confusing pairing, host/platform availability and connection removal; another requests an iOS Live Activity for long tasks. | Pairing must explain prerequisites and failure reasons; hub editing/removal must be obvious. Evaluate glanceable progress separately from core conversation UI. These comments do not establish today’s platform support. |
| [r/codex: Android entry point](https://www.reddit.com/r/codex/comments/1w7eixt/codex_on_android_chatgpt_app/) · displayed as hours old when read | The author cannot find Codex/Remote after returning to regular chat; replies disagree about the entry label. | Keep the route back to work stable and discoverable. A recent confusion report does not establish its technical cause. |
| [Carlos Carpio on X](https://x.com/uset82/status/2059140901994115276) · indexed post | Questions why a computer is needed for a repository task; the accompanying screen asks the person to finish desktop setup. | Explain what runs on the hub and what the phone can do before starting setup. Screenshot/search-index evidence only; original post and replies were unavailable for direct inspection. |

## Claude Code: continuity, and the cost of losing it

The current [Remote Control documentation](https://code.claude.com/docs/en/remote-control) describes local execution, synchronized sessions, mobile attachments, reconnect behavior, decisions and diffs. Its distinction between remote local work and cloud execution is relevant to hub identity. Some older complaints about missing capabilities may have been addressed; they remain useful failure scenarios rather than a current feature matrix.

A [25 February Android walkthrough by DevelopersIO](https://dev.classmethod.jp/articles/claude-coderemotecontrol-enables-you-to-work-on-your-local-machine-from-your-smartphone/) records actual use on Pixel 9 Pro / Android 16 with Claude Code 2.1.53. It shows a compact composer, tool work and a write approval. After stopping the local terminal, the author found the conversation’s disconnected state difficult to recognize. Its screenshots are historical, explicitly labeled in the lookbook; they are not presented as September’s exact UI.

**What to borrow:** focused conversation plus decisions in context, and mobile photo/file input. **What to improve:** the phone must remain a trustworthy view of the session during sleep, reconnect, remote decisions and interruptions.

### What people value and dislike

| Source | Reported experience | Evener implication |
| --- | --- | --- |
| [r/ClaudeCode: a window into the future](https://www.reddit.com/r/ClaudeCode/comments/1tkv87g/remotecontrol_is_a_window_into_what_the_future_is/) · May-era discussion | Praise includes continuing work away from a desk and easy voice input/camera use. Replies disagree about reliability; one says it improved after the early weeks. | Preserve native dictation and image capture affordances. Do not confuse ordinary keyboard dictation with the deferred interactive voice/barge-in feature. |
| [r/ClaudeCode: real remote control](https://www.reddit.com/r/ClaudeCode/comments/1thyrr2/claude_code_needs_real_remote_control_from_mobile/) · 19 May | The author dislikes fragmentation across product modes; replies describe stacked permission prompts and missed Android approvals. | Organize around sessions and actions, make pending decisions persistent and avoid replaying resolved prompts. Reports are not reproduced bugs. |
| [r/ClaudeCode: stale remote state](https://www.reddit.com/r/ClaudeCode/comments/1slrdxv/remotecontrol_doesnt_really_work_now/) · 15 April, follow-up 20 April | Author reports old content and invisible desktop approval while phone messages queue; later says the stale-state problem stopped occurring. | Test state freshness and decision recovery. Preserve the follow-up: do not label this an unresolved current defect. |
| [Claude Code issue #71605](https://github.com/anthropics/claude-code/issues/71605) · 26 June | iPhone 16 Pro Max, iOS 26.5, app 1.260618.1: author reports long transcription hiding Send and draft loss after visiting settings. Issue is now closed as not planned/stale. | Long-input keyboard layout and draft restoration need explicit E2E coverage. Closure does not prove a fix or establish that the defect persists today. |
| [r/ClaudeCode: input not working](https://www.reddit.com/r/ClaudeCode/comments/1uax8oz/remote_control_not_working_right/) · 20–27 June | Initial report looks like broken mobile input despite live output; author later attributes it to antivirus blocking. | Distinguish transport reachability, received events and acknowledged actions. Do not attribute every remote failure to the mobile renderer. |

## Changes to Evener’s proposed design

1. **Connection state is part of interaction design.** A socket alone cannot justify implying that an approval or submission succeeded. Show recovering state when freshness is uncertain; preserve pending input until reconciled.
2. **Decisions outlive presentations.** An unanswered approval must remain reachable after closing a sheet, returning from another app or reconnecting. Deduplicate resolved decisions and verify destination identity before applying an answer.
3. **Drafts belong to hub + session.** Navigation, settings visits, app backgrounding and relaunch must not discard composition. Preserve uncertain submissions separately from newer drafts.
4. **The composer must survive real use.** Long dictation, pasted logs, large text and an open keyboard cannot hide the submit action. Keep secondary controls quieter than the text.
5. **Review has its own readable surface.** Investigate a changed-file summary, line wrapping and contextual comments. Do not claim Evener already has a matching protocol or renderer; establish the supported contract before implementation.
6. **Navigation must make returning effortless.** Saved hubs and current sessions remain easy to find; distinguish host setup from starting a task. Use native platform controls without making Android’s workflow second-class.
7. **Notifications should bring the person back to the exact work.** Completion/decision deep links and optional glanceable progress merit screen studies. Live Activities are a proposed exploration, not an added v1 commitment.

These additions strengthen the original quiet visual direction. They also give “fluent” observable meaning: keep the person oriented, retain their work, and let them act without fighting the phone.
