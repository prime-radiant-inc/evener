# Delegate Report Capture and Protocol Tables Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan task by task. No additional agents in this assigned isolated lane.

**Goal:** Bound owned report-copy work across a captured delegate page and expose nested session activity field tables in generated protocol documentation.

**Architecture:** Retain each existing bounded owned report copy. Stop candidate capture through its existing matched-limit break path when cumulative captured report bytes reach the public page byte allowance, permitting at most one bounded candidate overshoot. Register the actual nested session activity structs through the documentation generator's existing explicit registry.

**Tech Stack:** Go agent activity reader; AppWire reflection-based documentation generator.

**Spec:** The report/run-identity contract in `docs/product/session-activity.md` and the accepted PR #3570 documentation and capture-work Low follow-ups.

## Constraints

- Preserve own copies; never retain raw subslices backed by arbitrary durable packet sizes.
- Captured report bytes stay at most `sessionActivityPageBytes + activityMaxReportPreviewBytes`; this does not bound all candidate metadata or total controller-lock work.
- Preserve opaque cursor progression, existing response admission, excluded candidate retry and remaining-work accounting.
- No new fetch, retry owner, compatibility shim or general reflection traversal.
- No push, runtime/provider actions or edits to the browser lane.

## Tasks

- [ ] Add a RED structured generator test for Context, Ancestor, Counts, Issue, Page, Delegate and Watch tables with public JSON field names.
- [ ] Register those structs explicitly; run generator tests and `make generate`.
- [ ] Add a RED real delegate-producer fixture with large reported packets at limit 200. Assert cumulative captured report bytes, owned copies, bounded preview and complete public cursor walk without lost or duplicate identities. Include response-byte admission excluding a later captured row.
- [ ] Apply the cumulative captured-report allowance using the existing candidate limit break/start decrement path.
- [ ] Verify report, activity paging and generator tests plus race and generated freshness checks. Update the owning guide with the narrow resource bound.
- [ ] Commit with normal hooks and provide exact RED/GREEN evidence to root. Integrate the actual G13 main merge when root supplies it.
