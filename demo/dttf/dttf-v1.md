# DTTF v1 Specification

**DTTF** (Direct To The Font) is a tool that takes bitmap images of glyphs as input and produces a valid TrueType font file as output. It is the first product built by the Kilroy software factory.

---

## 1. Input

### 1.1 Format

A directory of PNG images, one per glyph. Each PNG contains a single glyph rendered on a white background in black.

### 1.2 Naming Schema

Files are named by Unicode codepoint, with an optional human-readable prefix:

```
Times_New_Roman-A-U+0041.png    → A  (with font name and glyph label)
Roboto-a-U+0061.png             → a
U+0030.png                      → 0  (minimal, no prefix)
U+0021.png                      → !
U+0020.png                      → (space)
```

Format: `[FontName-GlyphLabel-]U+XXXX.png`

- **Required part:** `U+XXXX.png` — zero-padded 4-digit hex codepoint
- **Optional prefix:** `FontName-GlyphLabel-` — font name and glyph character, separated by hyphens. Purely for human readability; DTTF ignores everything before `U+`.
- Case-insensitive. DTTF parses the codepoint by finding the `U+XXXX` pattern in the filename.

### 1.3 Character Set

Derived entirely from the input. If a PNG exists for a codepoint, that glyph appears in the output font. There is no fixed character set requirement. The tool handles whatever it receives.

A `.notdef` glyph (the missing glyph placeholder) is always generated automatically as a simple rectangle.

### 1.4 Image Requirements

- **Format:** PNG, lossless
- **Color:** Grayscale or RGB (converted to grayscale internally)
- **Content:** Black glyph on white background
- **Resolution:** Minimum 200px tall. Recommended 400px. The glyph should fill the image height, with the baseline position encoded in metadata or inferred.
- **Threshold:** Grayscale input is thresholded to binary (1-bit) before tracing. Threshold value is configurable (default: 128).

### 1.5 Metadata Sidecar (Optional)

An optional `font.json` file in the input directory provides font-level metadata:

```json
{
  "family_name": "MyFont",
  "style": "Regular",
  "units_per_em": 2048,
  "ascender": 1536,
  "descender": -512,
  "baseline_ratio": 0.75
}
```

If absent, DTTF uses sensible defaults:
- `family_name`: derived from directory name
- `style`: "Regular"
- `units_per_em`: 2048 (TrueType convention)
- `ascender`: 75% of UPEm (1536)
- `descender`: 25% of UPEm (-512)
- `baseline_ratio`: 0.75 (baseline is 75% down from top of image)

---

## 2. Output

### 2.1 Format

A single `.ttf` file containing a valid TrueType font.

### 2.2 Required Tables

The output font contains these 10 tables, which constitute the minimum for a loadable TrueType font:

| Table | Purpose |
|-------|---------|
| `head` | Font header: UPEm, creation date, flags, bounding box |
| `maxp` | Maximum profile: glyph count, max points/contours |
| `hhea` | Horizontal header: ascender, descender, line gap, max advance |
| `hmtx` | Horizontal metrics: advance width and LSB per glyph |
| `OS/2` | Platform metrics: weight class, width class, Panose, Unicode ranges |
| `name` | Naming: family name, style, version, copyright |
| `post` | PostScript: format 3.0 (no glyph names, 32 bytes) |
| `cmap` | Character map: format 4 (BMP Unicode → glyph index) |
| `glyf` | Glyph outlines: quadratic Bezier contours |
| `loca` | Glyph location index: offsets into `glyf` table |

### 2.3 Recommended Table

| Table | Purpose |
|-------|---------|
| `gasp` | Grid-fitting and scan-conversion: 12 bytes, enables smoothing at all sizes. Dramatically improves Windows rendering for unhinted fonts. |

### 2.4 Not in v1

- Kerning (`kern` or `GPOS`)
- Ligatures (`GSUB`)
- Hinting (TrueType instructions)
- Variable font axes (`gvar`, `fvar`)
- CFF outlines

### 2.5 Vertical Metrics

All three metric systems set to consistent values:

```
hhea.ascender  = OS/2.sTypoAscender  = ascender
hhea.descender = OS/2.sTypoDescender = descender
hhea.lineGap   = OS/2.sTypoLineGap  = 0
OS/2.usWinAscent  = ascender
OS/2.usWinDescent = abs(descender)
OS/2.fsSelection bit 7 (USE_TYPO_METRICS) = set
```

### 2.6 Glyph Metrics

Per glyph, derived from the traced outline:

```
advanceWidth = glyph bounding box width + left sidebearing + right sidebearing
leftSideBearing = xMin of glyph outline
```

Default sidebearing strategy: proportional to UPEm (configurable).

---

## 3. Tracer

DTTF includes a custom, font-specialized bitmap-to-vector tracer written in Go. This is not a wrapper around an external tool.

### 3.1 Rationale

General-purpose tracers (Potrace, VTracer) produce cubic Bezier output, require format conversion (SVG intermediates), and cannot enforce font-specific constraints during tracing. A custom tracer:

- Outputs **quadratic Beziers directly** (TrueType native), eliminating the lossy cubic-to-quadratic conversion step
- Enforces font constraints **during tracing**, not after: correct winding direction, points at extrema, no self-intersections
- Operates in **font units** from the start, with no intermediate SVG representation
- Tunes corner detection for **letterforms** (serifs, stroke junctions, ink traps) rather than general shapes
- Optimizes point count against the **quality function** — minimizes control points while maintaining SSIM above threshold

### 3.2 Algorithm

Based on Potrace's published 4-phase approach, adapted for font-specific output:

**Phase 1 — Path Decomposition:**
Decompose the binary image into a set of closed boundary paths (outer contours and inner contours/counters). Uses the standard contour-following algorithm on the 1-bit raster.

**Phase 2 — Optimal Polygon:**
For each path, compute the optimal polygon approximation. This converts the pixel staircase into a minimal set of straight-line segments that preserve the shape.

**Phase 3 — Quadratic Bezier Fitting:**
Fit quadratic Bezier curves (not cubic) directly to the polygon segments. This is the key divergence from Potrace, which fits cubics. Quadratic fitting:
- Produces TrueType-native splines with no conversion needed
- Uses fewer parameters per curve (3 control points vs 4)
- May require more segments for the same accuracy, but eliminates conversion error entirely

**Phase 4 — Font-Aware Optimization:**
Optimize the fitted curves with font-specific constraints:
- Insert points at extrema (required by OpenType spec for correct rendering)
- Ensure correct winding direction (clockwise outer contours, counter-clockwise inner)
- Remove self-intersections
- Eliminate short segments (< 2 font units)
- Enforce hard cap of 1000 control points per glyph
- Minimize total point count while maintaining SSIM above threshold

### 3.3 Coordinate Mapping

The tracer maps pixel coordinates to font units:

```
font_x = (pixel_x / image_width) * advance_width
font_y = ((image_height - pixel_y) / image_height) * (ascender - descender) + descender
```

All output coordinates are integers in font units (UPEm = 2048).

### 3.4 Configuration

| Parameter | Default | Description |
|-----------|---------|-------------|
| `threshold` | 128 | Grayscale-to-binary threshold (0-255) |
| `corner_threshold` | 1.0 | Angle threshold for corner vs smooth (radians) |
| `min_segment` | 2 | Minimum segment length in font units |
| `optimization_tolerance` | 0.1 | Curve fitting error tolerance |
| `turd_size` | 0 | Minimum contour area (suppress noise) |

---

## 4. Architecture

### 4.1 Language

Go is the primary language. External tools (if any) are invoked as subprocesses.

### 4.2 API Surface

**Library:** A Go package (`dttf`) exposing the core pipeline:

```go
// Core pipeline
func Build(inputDir string, outputPath string, opts Options) error

// Individual stages (for testing and composition)
func LoadGlyphs(inputDir string) ([]GlyphBitmap, *FontMetadata, error)
func TraceGlyph(bitmap GlyphBitmap, opts TraceOptions) ([]Contour, error)
func AssembleFont(glyphs []TracedGlyph, meta *FontMetadata) (*Font, error)
func WriteFont(font *Font, outputPath string) error
```

**CLI:** A command-line tool wrapping the library:

```
dttf build ./input/ -o myfont.ttf
dttf build ./input/ -o myfont.ttf --threshold 100 --units-per-em 1000
dttf validate myfont.ttf
dttf test --reference ./fonts/Roboto-Regular.ttf
```

### 4.3 Pipeline

```
Input PNGs
    │
    ├─ Load & parse filenames → codepoint mapping
    │
    ├─ Convert to grayscale → threshold to 1-bit
    │
    ├─ Trace (per glyph, parallelizable):
    │     Path decomposition → polygon → quadratic Bezier → optimize
    │
    ├─ Compute metrics:
    │     Bounding boxes, advance widths, sidebearings
    │
    ├─ Assemble font:
    │     Build all required tables, compute checksums
    │
    └─ Write .ttf
```

Each glyph traces independently. Tracing is parallelized across available cores.

---

## 5. Quality Function

DTTF includes a computable, single-number objective function for measuring output quality. All components are deterministic with zero human judgment.

### 5.1 Layer 1: Font Validity (Binary)

| Check | Method |
|-------|--------|
| Loadable by standard parsers | Parse with Go sfnt package |
| Contours closed | Verify all paths return to origin |
| Correct winding direction | Signed area: negative = CW (outer), positive = CCW (inner) |
| No self-intersections | Segment-segment intersection test |
| Points at extrema | Check curve derivatives for roots in [0,1] |

Validity is a gate: if any check fails, score is 0.

### 5.2 Layer 2: Pixel Fidelity at Input Resolution (SSIM)

Render source font → bitmap → trace → assemble → render output font → compare.

At the input resolution (400px/em), SSIM should approach 0.99+. This measures how well the tracer reproduces the exact input pixels.

**Metric:** SSIM (Structural Similarity Index), range 0.0–1.0.

| Threshold | Quality |
|-----------|---------|
| > 0.99 | Excellent |
| > 0.95 | Good |
| > 0.90 | Acceptable |
| < 0.85 | Poor |

### 5.3 Layer 3: Multi-Scale Fidelity (SSIM at Unseen Sizes)

Render the output font at sizes the tracer never saw: 12, 16, 24, 48, 96px per em.

Good Bezier curves generalize to other sizes. Bad ones only look right at the training size. This layer detects overfitting.

### 5.4 Layer 4: Curve Quality

| Metric | Target |
|--------|--------|
| Control points per glyph | Hard cap: ≤ 1000. Minimize below that. |
| Points at extrema violations | 0 |
| Winding direction violations | 0 |
| Self-intersections | 0 |
| Short segments (< 2 units) | 0 |

**Optimization hierarchy:**

1. **Hard cap:** ≤ 1000 control points per glyph (configurable). If exceeded, simplify unconditionally. Professional fonts top out around 150 for Latin glyphs; 1000 accommodates decorative and complex script glyphs.
2. **Constraint:** SSIM ≥ threshold at input resolution.
3. **Objective:** Minimize control points while respecting the SSIM constraint.

The tracer never trades fidelity for fewer points. It finds the fewest points that keep fidelity above the floor.

### 5.5 Layer 5: Metric Accuracy

| Metric | Tolerance |
|--------|-----------|
| Advance widths | < 2% of UPEm |
| Sidebearings | < 2% of UPEm |
| Bounding boxes | < 2% of UPEm |

### 5.6 Layer 6: Text-Level Coherence

Render a paragraph with both fonts and compare:
- Full-page SSIM
- Total string width difference < 1%
- Line break positions at fixed width: identical

### 5.7 Composite Score

```
score = w1 * validity          (0 or 1, gate)
      + w2 * ssim_input        (0.0–1.0)
      + w3 * ssim_multiscale   (0.0–1.0)
      + w4 * curve_quality     (0.0–1.0)
      + w5 * metric_accuracy   (0.0–1.0)
      + w6 * text_coherence    (0.0–1.0)
```

Weights are configurable. Default: equal weighting after the validity gate.

---

## 6. Test Harness

DTTF includes a self-contained round-trip test harness.

### 6.1 Round-Trip Test

For a given reference font:

1. **Render:** Rasterize each glyph from the reference font to a PNG at 400px/em
2. **Trace:** Run the DTTF pipeline on the rendered PNGs
3. **Compare:** Render each glyph from the output font, compute SSIM against step 1
4. **Report:** Per-glyph scores, aggregate score, failing glyphs

### 6.2 Reference Fonts

Starter set for automated testing:

| Category | Font | Why |
|----------|------|-----|
| Sans-serif | Roboto | Most-downloaded Google Font |
| Sans-serif | Open Sans | Humanist, high usage |
| Serif | Noto Serif | Comprehensive, clean |
| Monospace | Source Code Pro | Well-hinted, Adobe |
| Display | Playfair Display | High contrast, stress-tests thin strokes |
| Slab | Roboto Slab | Tests slab serifs |

All SIL Open Font License. Downloaded automatically by the test harness.

### 6.3 Test Commands

```
dttf test --reference ./fonts/Roboto-Regular.ttf
dttf test --reference-dir ./fonts/           # run all
dttf test --reference-dir ./fonts/ --threshold 0.95  # fail below SSIM 0.95
```

### 6.4 Rendering for Tests

Use Go's `golang.org/x/image/font/sfnt` and `golang.org/x/image/font/opentype` packages for rasterizing reference fonts to bitmaps. These are official Go team packages, stable, and sufficient for test rendering.

---

## 7. Data Structures

### 7.1 Core Types

```go
// A single glyph bitmap loaded from input
type GlyphBitmap struct {
    Codepoint rune
    Pixels    [][]uint8  // grayscale, row-major
    Width     int
    Height    int
}

// A point in font units
type Point struct {
    X       int16
    Y       int16
    OnCurve bool  // true = on-curve, false = off-curve (quadratic control point)
}

// A single closed contour
type Contour struct {
    Points []Point
}

// A traced glyph ready for font assembly
type TracedGlyph struct {
    Codepoint    rune
    Contours     []Contour
    AdvanceWidth uint16
    LSB          int16   // left sidebearing
    XMin         int16
    YMin         int16
    XMax         int16
    YMax         int16
}

// Font-level metadata
type FontMetadata struct {
    FamilyName  string
    Style       string
    UnitsPerEm  uint16
    Ascender    int16
    Descender   int16
}
```

---

## 8. File Assembly

### 8.1 TrueType File Structure

```
Offset Table (12 bytes)
    numTables, searchRange, entrySelector, rangeShift

Table Directory (16 bytes per table)
    tag, checksum, offset, length

Table Data (padded to 4-byte boundaries)
    head, maxp, hhea, hmtx, OS/2, name, post, cmap, loca, glyf, gasp
```

### 8.2 Table Ordering

Tables ordered alphabetically by tag (OpenType recommendation).

### 8.3 Checksums

Each table has a uint32 checksum (sum of 4-byte words). The `head` table has a special `checksumAdjustment` field: `0xB1B0AFBA - checksum_of_entire_file`.

### 8.4 Glyph Encoding (glyf table)

Each glyph is encoded as:
- `int16` numberOfContours (negative = composite glyph, not in v1)
- `int16` xMin, yMin, xMax, yMax
- `uint16[]` endPtsOfContours
- `uint16` instructionLength (0, no hinting)
- `uint8[]` flags (packed, with repeat optimization)
- Coordinates: delta-encoded, variable-length (uint8 or int16 per flag)

### 8.5 cmap Format 4

BMP Unicode mapping. Segments of contiguous codepoint ranges mapped to contiguous glyph indices. Requires sorted segment arrays with binary search headers.

---

## 9. Error Handling

DTTF fails loudly and early:

- Missing or unreadable PNG → skip glyph, warn to stderr
- Zero valid glyphs → fatal error
- Invalid PNG content (all white, all black) → skip glyph, warn
- Tracing produces empty contour → skip glyph, warn
- Font assembly failure → fatal error with diagnostic
- Output font fails self-validation → fatal error, output file still written for inspection

---

## 10. Dependencies

### 10.1 Go Standard Library / Official Packages

- `image`, `image/png` — PNG loading
- `golang.org/x/image/font/sfnt` — font parsing (test harness)
- `golang.org/x/image/font/opentype` — font rasterization (test harness)

### 10.2 External Go Packages

- None required for core pipeline (tracer is custom, font assembly is custom)
- SSIM computation: implement in Go or use a minimal package

### 10.3 External Tools

- None required at runtime
- Test harness downloads reference fonts from Google Fonts API

---

## 11. Rasterizer

DTTF includes a font-to-bitmap rasterizer that produces input-format PNGs from an existing font file. This serves two purposes: generating input sets for round-trip QA testing, and letting users create DTTF-compatible glyph bitmaps from any font for experimentation.

### 11.1 Function

Given a `.ttf` or `.otf` font file, render each selected glyph to an individual PNG and write a `font.json` sidecar with the source font's real metrics.

### 11.2 Output

A directory matching DTTF's input format exactly:

```
output/
  font.json
  Roboto-A-U+0041.png
  Roboto-B-U+0042.png
  Roboto-a-U+0061.png
  Roboto-zero-U+0030.png
  Roboto-space-U+0020.png
  ...
```

Each PNG is a black-on-white glyph rendered at the specified height. The naming prefix uses the font's family name and a human-readable glyph label (the character itself for printable glyphs, a descriptive name like "space" or "nonbreakingspace" for non-printable ones).

### 11.3 Character Set Selection

| Flag | Selects |
|------|---------|
| `--ascii` | Printable ASCII: U+0020–U+007E (95 glyphs). **Default.** |
| `--all` | Every glyph in the font that has an outline (skips empty/whitespace-only glyphs except space). |
| `--chars "ABCabc123!?"` | Exactly the characters in the string. Deduplicated, order ignored. |
| `--range U+0041-U+005A` | Inclusive Unicode range. May be specified multiple times. |

Flags combine: `--ascii --range U+00C0-U+00FF` renders printable ASCII plus Latin Extended-A. If a requested codepoint has no glyph in the source font, it is skipped with a warning.

### 11.4 Rendering

- **Rasterizer:** Go `golang.org/x/image/font/opentype` package.
- **Height:** Configurable. Default 400px per em. Minimum 200px.
- **Format:** Grayscale PNG. Black glyph (#000000) on white background (#FFFFFF). Anti-aliased rendering, matching what the test harness will compare against.
- **Glyph positioning:** Each glyph is rendered in its own image, sized to the glyph's advance width (horizontally) and the full em height (vertically). The baseline is positioned according to the font's ascender/descender ratio.
- **Space glyph (U+0020):** Rendered as an all-white image at the correct advance width. Required because DTTF derives character set from present PNGs.

### 11.5 Metric Extraction

The rasterizer reads real metrics from the source font and writes them to `font.json`:

```json
{
  "family_name": "Roboto",
  "style": "Regular",
  "units_per_em": 2048,
  "ascender": 1536,
  "descender": -512,
  "baseline_ratio": 0.75,
  "source_font": "Roboto-Regular.ttf"
}
```

`baseline_ratio` is computed as `ascender / (ascender + abs(descender))`. The `source_font` field records provenance for test traceability.

### 11.6 API

```go
func Rasterize(fontPath string, outputDir string, opts RasterizeOptions) error

type RasterizeOptions struct {
    Charset    CharsetSpec  // which glyphs to render
    Height     int          // pixels per em (default: 400)
    Background uint8        // background gray value (default: 255)
    Foreground uint8        // glyph gray value (default: 0)
}

type CharsetSpec struct {
    ASCII  bool       // U+0020–U+007E
    All    bool       // every glyph in the font
    Chars  string     // explicit characters
    Ranges [][2]rune  // Unicode ranges (inclusive)
}
```

---

## 12. CLI Reference

```
dttf build <input-dir> [flags]
    -o, --output <path>          Output TTF path (default: <input-dir>.ttf)
    --family <name>              Font family name (overrides font.json)
    --style <name>               Font style (default: Regular)
    --units-per-em <int>         UPEm (default: 2048)
    --threshold <int>            Binary threshold 0-255 (default: 128)
    --corner-threshold <float>   Corner detection angle (default: 1.0)
    --optimization-tolerance <float>  Curve fitting tolerance (default: 0.1)
    --verbose                    Print per-glyph tracing stats

dttf rasterize <font.ttf> [flags]
    Render glyphs from a font file to DTTF-format PNGs.
    -o, --output-dir <path>      Output directory (default: ./<font-family>/)
    --ascii                      Printable ASCII U+0020-U+007E (default)
    --all                        Every glyph in the font
    --chars <string>             Specific characters (e.g. "AaBb123")
    --range <U+XXXX-U+XXXX>     Unicode range (repeatable)
    --height <int>               Pixels per em (default: 400)
    --verbose                    Print per-glyph rendering stats

dttf validate <font.ttf>
    Run validity checks on a font file. Exit 0 = valid, 1 = invalid.

dttf test --reference <font.ttf> [flags]
    Run round-trip test against a reference font.
    --sizes <list>               Render sizes for multi-scale test (default: 12,16,24,48,96)
    --threshold <float>          Minimum SSIM to pass (default: 0.90)
    --output-dir <path>          Save comparison renders

dttf test --reference-dir <dir> [flags]
    Run round-trip tests against all .ttf/.otf files in directory.
```
