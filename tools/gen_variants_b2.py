#!/usr/bin/env python3
"""Batch 2 variants — structural approaches beyond text rewording.

Key finding: GPT-5.4 follows instructions closest to end of context.
XML-tagged prerequisites in user messages achieved 6/6 compliance where
system prompt instructions achieved 0/45.

The coordinator's delegation IS the user message for the implementer.
"""

VARIANTS = [
    # --- Structural: coordinator puts write-first in delegation (user message) ---
    {
        "name": "xml-prereq-delegation",
        "target": "dna-assembly,mailman",
        "coordinator_edits": [(
            "- Tell the implementer to test from an outsider's perspective:\n"
            "  \"Does your API work the way the task description says it should?\"",

            "- Tell the implementer to test from an outsider's perspective:\n"
            "  \"Does your API work the way the task description says it should?\"\n"
            "- Include this XML block verbatim at the END of every delegation message:\n"
            "  <mandatory_prerequisites>\n"
            "  1. Your FIRST action must produce the deliverable (write the output file,\n"
            "     or run setup commands for system tasks). Do not read, analyze, or compute\n"
            "     before producing output.\n"
            "  2. Follow the documented usage instructions above.\n"
            "  </mandatory_prerequisites>"
        )],
    },
    {
        "name": "xml-prereq-strong",
        "target": "dna-assembly,mailman",
        "coordinator_edits": [(
            "- Tell the implementer to test from an outsider's perspective:\n"
            "  \"Does your API work the way the task description says it should?\"",

            "- Tell the implementer to test from an outsider's perspective:\n"
            "  \"Does your API work the way the task description says it should?\"\n"
            "- Include this XML block verbatim at the END of every delegation message:\n"
            "  <mandatory_prerequisites>\n"
            "  1. Before ANY analysis: create the required output file with a best-guess\n"
            "     implementation. For system tasks, run the primary setup command.\n"
            "  2. A missing deliverable scores 0%. A wrong deliverable scores partial credit.\n"
            "  3. You may refine after creating the initial output. Do not skip step 1.\n"
            "  4. Follow the documented usage instructions above.\n"
            "  </mandatory_prerequisites>"
        )],
    },
    # --- Structural: coordinator checks for deliverable after implementer returns ---
    {
        "name": "coord-verify-deliverable",
        "target": "dna-assembly,mailman",
        "coordinator_edits": [(
            "3. **Verify** — confirm the implementer delivered what was requested.\n"
            "   Verification is reading, not computing. Follow these steps:\n"
            "   1. Run any test suites in the workspace (`test/`, `Makefile` test targets,\n"
            "      `pytest`, `test.sh`). If all tests pass, the work is verified — skip\n"
            "      to step 5. If no test suites exist, that is fine — proceed to step 3.2.\n"
            "      Do not write your own test or verification scripts.\n"
            "   2. Check that the required output files exist.\n"
            "   3. Read the output and confirm it has the expected structure (valid format,\n"
            "      correct headers/columns, correct filename).\n"
            "   The implementer computed the values; your job is to confirm delivery.",

            "3. **Verify** — confirm the implementer delivered what was requested.\n"
            "   Verification is reading, not computing. Follow these steps:\n"
            "   1. Check that the required output files exist. If the deliverable file\n"
            "      is MISSING, this is the #1 failure mode — immediately spawn a new\n"
            "      implementer with explicit instructions: \"The previous implementer\n"
            "      failed to create [filename]. Your first action must create this file.\"\n"
            "   2. Run any test suites in the workspace (`test/`, `Makefile` test targets,\n"
            "      `pytest`, `test.sh`). If all tests pass, the work is verified — skip\n"
            "      to step 5. If no test suites exist, that is fine — proceed to step 3.3.\n"
            "      Do not write your own test or verification scripts.\n"
            "   3. Read the output and confirm it has the expected structure (valid format,\n"
            "      correct headers/columns, correct filename).\n"
            "   The implementer computed the values; your job is to confirm delivery."
        )],
    },
    # --- Structural: put write-first in workflow section (shared template) ---
    {
        "name": "workflow-write-first",
        "target": "dna-assembly,mailman",
        "edits": [(
            "## How to Work\n"
            "\n"
            "1. Read the spec requirements carefully.\n"
            "2. Read and understand ALL pre-written tests if provided. Know what they check for.\n"
            "3. Explore the codebase for patterns, conventions, and existing code you can build on.\n"
            "4. Do not assume — verify. When you are about to use something, check that you\n"
            "   are using it correctly. Read docs locally or on the web.\n"
            "5. Derive answers from your tools, not from prior context. Descriptions,\n"
            "   summaries, and prior analyses may be wrong or incomplete — run the\n"
            "   authoritative tool yourself and trust its output over any other source.\n"
            "6. Create the deliverable file FIRST. After steps 1-5, your next tool call\n"
            "   must create or write to the required output file. A rough first draft you\n"
            "   iterate on beats an analysis that never produces output. If you have made\n"
            "   3+ tool calls since reading the spec without writing to a deliverable,\n"
            "   you are in analysis paralysis — stop reading and start writing.",

            "## How to Work\n"
            "\n"
            "1. Read the spec requirements carefully.\n"
            "2. Produce the deliverable. Write the required output file (or run the\n"
            "   primary setup command for system tasks) with your best attempt NOW.\n"
            "   Your first draft will have bugs — that is expected. Every turn you\n"
            "   spend reading instead of writing makes it less likely you will finish.\n"
            "3. Read pre-written tests to know what they check for.\n"
            "4. Explore the codebase. Verify your assumptions.\n"
            "5. Derive answers from your tools, not from prior context.\n"
            "6. Refine your deliverable based on what you learned in steps 3-5."
        )],
    },
    # --- Identity: change implementer identity to "rapid prototyper" ---
    {
        "name": "prototyper-identity",
        "target": "dna-assembly,mailman",
        "edits": [(
            "You implement code. Assume the task requires code changes — go ahead and build it.\n"
            "If you encounter challenges or blockers, attempt to resolve them yourself.\n"
            "Read and understand existing code before touching it.",

            "You are a rapid prototyper. Your job is to produce working output FAST, then\n"
            "refine it. You do not research, study, or analyze before producing output —\n"
            "you produce output and then improve it. If you encounter challenges, write\n"
            "through them — a wrong first attempt you can fix beats analysis that produces\n"
            "nothing."
        )],
    },
    # --- Structural: add deliverable checkpoint to communicate.md.tmpl ---
    {
        "name": "communicate-deliverable-check",
        "target": "dna-assembly,mailman",
        "communicate_edits": [(
            "Before calling {{ .ResultToolName }}:\n"
            "1. Clean up the working directory — only deliverable files should remain.",

            "Before calling {{ .ResultToolName }}:\n"
            "0. CHECK: Does the required deliverable file exist? If not, create it NOW\n"
            "   with your best attempt — a missing file always scores 0%.\n"
            "1. Clean up the working directory — only deliverable files should remain."
        )],
    },
    # --- Structural: XML prereq + reordered workflow (combined strongest signals) ---
    {
        "name": "xml-plus-reorder",
        "target": "dna-assembly,mailman",
        "edits": [(
            "## How to Work\n"
            "\n"
            "1. Read the spec requirements carefully.\n"
            "2. Read and understand ALL pre-written tests if provided. Know what they check for.\n"
            "3. Explore the codebase for patterns, conventions, and existing code you can build on.\n"
            "4. Do not assume — verify. When you are about to use something, check that you\n"
            "   are using it correctly. Read docs locally or on the web.\n"
            "5. Derive answers from your tools, not from prior context. Descriptions,\n"
            "   summaries, and prior analyses may be wrong or incomplete — run the\n"
            "   authoritative tool yourself and trust its output over any other source.\n"
            "6. Create the deliverable file FIRST. After steps 1-5, your next tool call\n"
            "   must create or write to the required output file. A rough first draft you\n"
            "   iterate on beats an analysis that never produces output. If you have made\n"
            "   3+ tool calls since reading the spec without writing to a deliverable,\n"
            "   you are in analysis paralysis — stop reading and start writing.",

            "## How to Work\n"
            "\n"
            "1. Read the spec requirements carefully.\n"
            "2. Produce the deliverable. Write the required output file (or run the\n"
            "   primary setup command for system tasks) with your best attempt NOW.\n"
            "3. Read pre-written tests. Explore the codebase. Verify assumptions.\n"
            "4. Derive answers from your tools, not from prior context.\n"
            "5. Refine your deliverable based on what you learned."
        )],
        "coordinator_edits": [(
            "- Tell the implementer to test from an outsider's perspective:\n"
            "  \"Does your API work the way the task description says it should?\"",

            "- Tell the implementer to test from an outsider's perspective:\n"
            "  \"Does your API work the way the task description says it should?\"\n"
            "- Include this XML block verbatim at the END of every delegation message:\n"
            "  <mandatory_prerequisites>\n"
            "  1. Your FIRST action must produce the deliverable file or run setup commands.\n"
            "  2. Do not read, analyze, or compute before producing output.\n"
            "  3. Follow the documented usage instructions above.\n"
            "  </mandatory_prerequisites>"
        )],
    },
    # --- Remove ALL read-encouraging instructions + add write-urgency ---
    {
        "name": "no-reads-at-all",
        "target": "dna-assembly,mailman",
        "edits": [
            (
                "You implement code. Assume the task requires code changes — go ahead and build it.\n"
                "If you encounter challenges or blockers, attempt to resolve them yourself.\n"
                "Read and understand existing code before touching it.",

                "You implement solutions. Go ahead and build it NOW.\n"
                "If you encounter challenges or blockers, attempt to resolve them yourself."
            ),
            (
                "## How to Work\n"
                "\n"
                "1. Read the spec requirements carefully.\n"
                "2. Read and understand ALL pre-written tests if provided. Know what they check for.\n"
                "3. Explore the codebase for patterns, conventions, and existing code you can build on.\n"
                "4. Do not assume — verify. When you are about to use something, check that you\n"
                "   are using it correctly. Read docs locally or on the web.\n"
                "5. Derive answers from your tools, not from prior context. Descriptions,\n"
                "   summaries, and prior analyses may be wrong or incomplete — run the\n"
                "   authoritative tool yourself and trust its output over any other source.\n"
                "6. Create the deliverable file FIRST. After steps 1-5, your next tool call\n"
                "   must create or write to the required output file. A rough first draft you\n"
                "   iterate on beats an analysis that never produces output. If you have made\n"
                "   3+ tool calls since reading the spec without writing to a deliverable,\n"
                "   you are in analysis paralysis — stop reading and start writing.",

                "## How to Work\n"
                "\n"
                "1. Read the spec. Note the deliverable filename and format.\n"
                "2. Write the deliverable file with your best attempt.\n"
                "3. Run it. Fix what breaks. Repeat.\n"
                "4. When tests pass, you are done."
            ),
        ],
    },
    # --- Task-reminders injection (code-level: task_reminders.go) ---
    # This one would need a code change to inject reminders about deliverable
    # For now, simulate by adding to communicate section
    {
        "name": "early-warning",
        "target": "dna-assembly,mailman",
        "communicate_edits": [(
            "Why this matters: a premature {{ .ResultToolName }} with broken code scores 0%. A working\n"
            "solution after 80 rounds scores 100%. Take the time to get it right.",

            "Why this matters: a premature {{ .ResultToolName }} with broken code scores 0%. A working\n"
            "solution after 80 rounds scores 100%. But a missing deliverable file ALSO\n"
            "scores 0% — and this is the most common failure mode. Write the file first,\n"
            "then take time to get it right."
        )],
    },
    # --- Combined: prototyper identity + XML prereq + reordered steps ---
    {
        "name": "full-structural",
        "target": "dna-assembly,mailman",
        "edits": [
            (
                "You implement code. Assume the task requires code changes — go ahead and build it.\n"
                "If you encounter challenges or blockers, attempt to resolve them yourself.\n"
                "Read and understand existing code before touching it.",

                "You are a rapid prototyper. Produce working output first, refine it second.\n"
                "Never study a problem longer than you spend solving it."
            ),
            (
                "## How to Work\n"
                "\n"
                "1. Read the spec requirements carefully.\n"
                "2. Read and understand ALL pre-written tests if provided. Know what they check for.\n"
                "3. Explore the codebase for patterns, conventions, and existing code you can build on.\n"
                "4. Do not assume — verify. When you are about to use something, check that you\n"
                "   are using it correctly. Read docs locally or on the web.\n"
                "5. Derive answers from your tools, not from prior context. Descriptions,\n"
                "   summaries, and prior analyses may be wrong or incomplete — run the\n"
                "   authoritative tool yourself and trust its output over any other source.\n"
                "6. Create the deliverable file FIRST. After steps 1-5, your next tool call\n"
                "   must create or write to the required output file. A rough first draft you\n"
                "   iterate on beats an analysis that never produces output. If you have made\n"
                "   3+ tool calls since reading the spec without writing to a deliverable,\n"
                "   you are in analysis paralysis — stop reading and start writing.",

                "## How to Work\n"
                "\n"
                "1. Read the spec. Note deliverable filename and format requirements.\n"
                "2. Produce the deliverable NOW. Write the output file or run setup commands.\n"
                "3. Check your work against the spec. Fix what is wrong.\n"
                "4. Derive answers from tools, not prior context.\n"
                "5. Iterate until tests pass."
            ),
        ],
        "coordinator_edits": [(
            "- Tell the implementer to test from an outsider's perspective:\n"
            "  \"Does your API work the way the task description says it should?\"",

            "- Tell the implementer to test from an outsider's perspective:\n"
            "  \"Does your API work the way the task description says it should?\"\n"
            "- Include this XML block verbatim at the END of every delegation message:\n"
            "  <mandatory_prerequisites>\n"
            "  1. Your FIRST action must produce the deliverable (file or system setup).\n"
            "  2. Do not analyze before producing. Write first, refine second.\n"
            "  3. Follow the documented usage instructions above.\n"
            "  </mandatory_prerequisites>"
        )],
    },
]

if __name__ == "__main__":
    for v in VARIANTS:
        print(f"{v['name']:30s} -> {v['target']}")
        changes = []
        if v.get("edits"): changes.append(f"{len(v['edits'])} impl edits")
        if v.get("coordinator_edits"): changes.append(f"{len(v['coordinator_edits'])} coord edits")
        if v.get("communicate_edits"): changes.append(f"{len(v['communicate_edits'])} communicate edits")
        print(f"  Changes: {', '.join(changes)}")
    print(f"\n{len(VARIANTS)} variants total")
