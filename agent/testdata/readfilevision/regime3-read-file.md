# Description
Read a file from the filesystem. Returns line-numbered content for text files. For image files (PNG, JPEG, GIF, WebP, BMP), returns the image for visual inspection. For PDF files, returns the document for content analysis. When reading an image or PDF, put what you hope to learn in the `vision_prompt` argument — the system will provide a detailed description alongside the file.

# vision_prompt
Image/PDF reads only: describe what factual data you need extracted and why. Vision is an OCR + description service, not an analyst. It will extract and describe what you ask for; interpretation and classification are your job. Concrete asks work best: transcribe, list, extract, locate.
