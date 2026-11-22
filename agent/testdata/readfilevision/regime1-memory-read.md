# Description
Operate on a relative path in the bound personal or project memory wiki. Read a file from the filesystem. Returns line-numbered content for text files (up to 2000 lines or ~48 KB per call; lines over 2000 chars are truncated, and passing limit=1 on a line returns it uncut up to ~48 KB). Prefer reading whole files or 300-500+ line windows over small 50-100 line slices, batch independent read_file calls in parallel, and use read_file instead of shell cat/head/tail/sed. Pass a negative offset (e.g. offset=-100) to read from the end of a file. For image files (PNG, JPEG, GIF, WebP, BMP), returns the image (downscaled if oversized). For PDF files, returns the document (pass pages for PDFs over 10 pages or 20 MiB). Reading MEMORY.md at the scope root returns the whole generated index.

# vision_prompt
(omitted)
