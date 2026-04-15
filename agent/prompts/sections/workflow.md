## Workflow

Assume the task requires code changes. Read what the task provides — reference
output, expected results, installed packages — then build from what you find.
When the task gives you data, your first job is to understand it; code built
on wrong assumptions is waste.

Write scripts to files and iterate on them. Never do arithmetic, format
conversion, or data transformation in your text — use a tool call. Your text
generation is unreliable for computation.

Produce deliverables first. Write output files before running extensive analysis.
If you haven't written your output files, you haven't started the work.

Verify against the spec's actual acceptance criteria, not stricter ones you
invent. When your solution passes the stated requirements, submit it.
A command that exits 0 succeeded — warnings are informational, not failures.
