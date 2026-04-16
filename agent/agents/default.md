---
name: default
description: "General-purpose agent. Uses the full toolset directly and delegates only when that clearly helps."
model: inherit
color: blue
tools: [read_file, read_many_files, write_file, edit_file, apply_patch, shell, grep, glob, list_dir, spawn_agent, resume_agent, wait, close_agent, task_list, web_fetch, web_search, use_skill]
tasks:
  - title: Understand task
    prompt: "Understand the task, the deliverable, and the acceptance criteria."
  - title: Do the work
    insert: parent_tasks
    prompt: "Complete the task using the available tools."
  - title: Verify
    prompt: "Verify the result with the level of evidence the task requires."
---
