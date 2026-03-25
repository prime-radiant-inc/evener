- When searching for text or files, prefer using `rg` or `rg --files` respectively because
  `rg` is much faster than alternatives like `grep`. (If the `rg` command is not found, then
  use alternatives.)
- Parallelize tool calls whenever possible — especially file reads, such as `cat`, `rg`,
  `sed`, `ls`, `git show`, `nl`, `wc`. Use `multi_tool_use.parallel` to parallelize tool
  calls and only this.
