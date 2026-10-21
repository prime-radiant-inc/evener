# Synthetic color references

These fixtures contain no photographs or user attachments. `generate.py` creates
18 neutral, saturated, and mixed RGB patches, their baseline/progressive JPEGs,
and a synthetic CMYK JPEG. It creates Display P3 and sRGB ICC v4 profiles through
LittleCMS and records relative-colorimetric conversions to sRGB, with no black
point compensation and optimization disabled. `samples.json` records the exact
RGB bytes given to LittleCMS, independently of any JPEG decoder.

Generated with Pillow 10.2.0 and LittleCMS 2.14. Regenerate from the repository root:

```sh
python3 llm/internal/imagerepair/testdata/generate.py
CGO_ENABLED=0 go test ./llm/internal/imagerepair -count=1
```

Python/Pillow and the installed LittleCMS shared library are development-only;
Go production code and tests read the checked-in references without them. Profile
creation dates are fixed to 2025-01-01 for reproducibility. The script does not
import or invoke Go conversion code, use credentials, or contact a network.

`p3.icc` uses D65 white, P3 primaries (0.680,0.320), (0.265,0.690),
(0.150,0.060), and the sRGB transfer curve. `srgb.icc` comes from LittleCMS's
built-in sRGB profile. The remaining profiles keep P3's matrix and use synthetic
TRCs encoded directly according to ICC curveType or parametricCurveType:
identity, u8Fixed8 gamma, 17-entry sampled gamma, and parametric functions 0–4.
The parametric 2 and 4 fixtures include nonzero offsets. LittleCMS interprets
these bytes to generate every expected output, not a duplicate Go algorithm.

Tests compare byte samples within one 8-bit channel value, allowing fixed-point
matrix and transfer-function rounding between implementations. End-to-end JPEG
references allow three values because Go and libjpeg also round JPEG decoding
differently. Geometry, orientation, opacity and metadata removal are exact.

Normative sources used for the implementation and fixture shapes:

- ICC.1:2022, https://www.color.org/specification/ICC.1-2022-05.pdf,
  sections 8.3.3/8.4.3 (matrix/TRC), 8.10.2 (transform precedence), 10.6
  (curveType), 10.18 table 68 (parametricCurveType), 10.31 (XYZType), Annex F.3
  (D50-adapted colorants).
- https://www.w3.org/TR/css-color-4/#color-conversion-code for the equivalent
  D50-to-D65 Bradford adaptation and linear-sRGB conversion.

Generated files in this directory may be regenerated together by the recipe.
