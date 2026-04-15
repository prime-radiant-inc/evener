#!/usr/bin/env python3
"""Generate implementer.md variants for batch experimentation.

Each variant is a dict with:
  name: branch suffix (exp/v33-NAME)
  target: task(s) to test on
  edits: list of (old_string, new_string) replacements in implementer.md
  coordinator_edits: optional list of (old, new) for coordinator.md
"""

VARIANTS = [
    # --- dna-assembly variants (hesitation / action initiation) ---
    {
        "name": "score-framing",
        "target": "dna-assembly",
        "edits": [(
            "6. Create the deliverable file FIRST. After steps 1-5, your next tool call\n"
            "   must create or write to the required output file. A rough first draft you\n"
            "   iterate on beats an analysis that never produces output. If you have made\n"
            "   3+ tool calls since reading the spec without writing to a deliverable,\n"
            "   you are in analysis paralysis — stop reading and start writing.",

            "6. Write the deliverable file NOW. A missing deliverable always scores 0%.\n"
            "   A wrong deliverable scores partial credit and can be fixed. After reading\n"
            "   the spec, your next action must create the required output file with your\n"
            "   best attempt. Do not analyze, compute, or verify before writing."
        )],
    },
    {
        "name": "explicit-override",
        "target": "dna-assembly",
        "edits": [(
            "6. Create the deliverable file FIRST. After steps 1-5, your next tool call\n"
            "   must create or write to the required output file. A rough first draft you\n"
            "   iterate on beats an analysis that never produces output. If you have made\n"
            "   3+ tool calls since reading the spec without writing to a deliverable,\n"
            "   you are in analysis paralysis — stop reading and start writing.",

            "6. Write the deliverable file. THIS OVERRIDES STEPS 1-5. If you find\n"
            "   yourself reading another file instead of writing the deliverable, stop\n"
            "   and write. You are allowed to write a wrong first draft. You are NOT\n"
            "   allowed to have no draft after your first few turns."
        )],
    },
    {
        "name": "write-verify-order",
        "target": "dna-assembly",
        "edits": [(
            "6. Create the deliverable file FIRST. After steps 1-5, your next tool call\n"
            "   must create or write to the required output file. A rough first draft you\n"
            "   iterate on beats an analysis that never produces output. If you have made\n"
            "   3+ tool calls since reading the spec without writing to a deliverable,\n"
            "   you are in analysis paralysis — stop reading and start writing.",

            "6. Write first, verify second. Create the deliverable file with your best\n"
            "   attempt before doing any computation, analysis, or verification. Your\n"
            "   first draft is a starting point — you will improve it. But it must exist\n"
            "   before you can improve it."
        )],
    },
    {
        "name": "reorder-steps",
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
            "   you are in analysis paralysis — stop reading and start writing.\n"
            "7. Implement the solution. Keep changes minimal and focused.\n"
            "8. Run the tests. If they fail, fix your code and run them again. Keep going.\n"
            "9. Do NOT modify test files unless explicitly told to.",

            "## How to Work\n"
            "\n"
            "1. Read the spec requirements carefully.\n"
            "2. Write the deliverable file with your best attempt. Now. Before anything\n"
            "   else. For system tasks, run setup commands. Your first draft will have\n"
            "   bugs — that is fine.\n"
            "3. Read pre-written tests. Understand what they check for.\n"
            "4. Explore the codebase for patterns, conventions, and existing code.\n"
            "5. Derive answers from your tools, not from prior context.\n"
            "6. Refine your deliverable based on what you have learned.\n"
            "7. Run the tests. If they fail, fix your code and run them again. Keep going.\n"
            "8. Do NOT modify test files unless explicitly told to."
        )],
    },
    {
        "name": "remove-read-first",
        "target": "dna-assembly,mailman",
        "edits": [(
            "You implement code. Assume the task requires code changes — go ahead and build it.\n"
            "If you encounter challenges or blockers, attempt to resolve them yourself.\n"
            "Read and understand existing code before touching it.",

            "You implement code. Assume the task requires code changes — go ahead and build it.\n"
            "If you encounter challenges or blockers, attempt to resolve them yourself."
        )],
    },

    # --- mailman variants (read-instead-of-act for config/sysadmin) ---
    {
        "name": "anti-source",
        "target": "mailman",
        "edits": [(
            "6. Create the deliverable file FIRST. After steps 1-5, your next tool call\n"
            "   must create or write to the required output file. A rough first draft you\n"
            "   iterate on beats an analysis that never produces output. If you have made\n"
            "   3+ tool calls since reading the spec without writing to a deliverable,\n"
            "   you are in analysis paralysis — stop reading and start writing.",

            "6. Produce output FIRST. For coding tasks, write the deliverable file. For\n"
            "   system configuration tasks, run setup commands (service start, config edit,\n"
            "   resource creation). Source code is reference material for debugging, not a\n"
            "   prerequisite for action. A rough first attempt you iterate on beats an\n"
            "   analysis that never produces output."
        )],
    },
    {
        "name": "debug-from-errors",
        "target": "mailman",
        "edits": [(
            "- **Same fix failing repeatedly?** After 3 attempts with the same strategy, change\n"
            "  approach fundamentally: different tool, different library, different architecture.\n"
            "  Do NOT attempt fix #4 with the same strategy.",

            "- **System configuration task?** Run the obvious setup commands first (service\n"
            "  start, config edit, create resources). If something breaks, debug from the\n"
            "  error message — do not read the software's source code to plan your approach.\n"
            "- **Same fix failing repeatedly?** After 3 attempts with the same strategy, change\n"
            "  approach fundamentally: different tool, different library, different architecture.\n"
            "  Do NOT attempt fix #4 with the same strategy."
        )],
    },
    {
        "name": "command-priority",
        "target": "mailman",
        "edits": [(
            "6. Create the deliverable file FIRST. After steps 1-5, your next tool call\n"
            "   must create or write to the required output file. A rough first draft you\n"
            "   iterate on beats an analysis that never produces output. If you have made\n"
            "   3+ tool calls since reading the spec without writing to a deliverable,\n"
            "   you are in analysis paralysis — stop reading and start writing.",

            "6. Act FIRST. For coding tasks, write the deliverable file. For configuration\n"
            "   tasks, your first actions must change system state — run setup commands,\n"
            "   edit configs, create resources, start services. Do not read source code\n"
            "   or documentation until you have a running baseline to debug against."
        )],
    },
    {
        "name": "sysadmin-identity",
        "target": "mailman",
        "edits": [(
            "You implement code. Assume the task requires code changes — go ahead and build it.\n"
            "If you encounter challenges or blockers, attempt to resolve them yourself.\n"
            "Read and understand existing code before touching it.",

            "You implement solutions. For code tasks, write code. For system configuration\n"
            "tasks, run commands — you are a sysadmin deploying software, not a developer\n"
            "studying it. If you encounter challenges or blockers, attempt to resolve them\n"
            "yourself."
        )],
    },
    {
        "name": "coord-delegation",
        "target": "mailman,dna-assembly",
        "coordinator_edits": [(
            "- Tell the implementer to test from an outsider's perspective:\n"
            "  \"Does your API work the way the task description says it should?\"",

            "- Tell the implementer to test from an outsider's perspective:\n"
            "  \"Does your API work the way the task description says it should?\"\n"
            "- Tell the implementer: \"Produce the deliverable file (or run setup commands)\n"
            "  within your first few turns. Write your best attempt first, then iterate.\""
        )],
    },
]

if __name__ == "__main__":
    import json
    for v in VARIANTS:
        print(f"{v['name']:25s} -> {v['target']}")
    print(f"\n{len(VARIANTS)} variants total")
