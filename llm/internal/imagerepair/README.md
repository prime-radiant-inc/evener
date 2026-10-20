# Bounded provider JPEG repair

`RepairJPEG` creates a disposable repair without modifying its byte slice. It
never reads files or URLs. The caller owns provider retry decisions and must
identify `ProfileRemoved` as unmanaged color recovery.

Supported managed input:

- One baseline or progressive JPEG decoded by Go's standard library, at most
  32 MiB encoded and 16,000,000 decoded pixels. Untagged RGB and grayscale use
  the conventional sRGB interpretation. CMYK is not converted.
- ICC **v4** input/display (`scnr`/`mntr`) RGB profiles with XYZ PCS, D50 white,
  nonsingular RGB colorant columns consistent with that white, and three TRCs.
  Individual XYZ components may be negative. Profile names do not select a
  transform. Exact shared tag ranges are allowed; duplicate signatures, partial
  overlap and out-of-range offsets are not.
- `curv` identity, positive gamma up to 16, or monotonic sampled curves up to
  65,536 entries. `para` functions 0–4 require finite, monotonic, bounded curves,
  positive gamma up to 16 and positive power-branch scale, valid breakpoints,
  nonnegative linear slopes, and continuity within fixed-point rounding tolerance.
- RGB-to-D50 PCS followed by Bradford-adapted XYZ-to-sRGB, relative colorimetric,
  no black point compensation. Colorant tags are **already D50 adapted**; the
  optional `chad` is validated but never applied a second time. Destination
  linear RGB is clipped to sRGB gamut, then transfer-encoded. This does not
  preserve out-of-gamut colors exactly.
- All eight EXIF IFD0 orientations, with either TIFF byte order. Missing
  orientation means identity. Malformed/duplicate orientation or multiple EXIF
  blocks disables conversion rather than guessing. Thumbnail orientation is
  not used for the main image.

The output is an opaque, metadata-free PNG with the same visual dimensions and
a 64 MiB encoded limit. ICC assembly is limited to 1 MiB, 255 numbered chunks,
and 1,024 tags. Parsing also limits a JPEG to 65,536 markers. These budgets
limit repair, not original-input acceptance.

v2 profiles, LUT transforms (`A2B`, `B2A`, `D2B`, `B2D`), non-RGB/non-XYZ
profiles, unsupported curves and contradictory color data take the ICC-only
fallback. It copies every byte outside recognized ICC APP2 markers, preserving
compressed scans, orientation and unrelated metadata. `ProfileFree` requests
that path directly. Without ICC there is no fallback change.

Both paths refuse MPF/multipicture, repeated frames, concatenated JPEGs, trailing
data, or unsafe marker boundaries. Malformed ICC chunk sequences may still be
removed when their JPEG segment boundaries are safe. Unknown non-transform ICC
metadata does not participate in conversion.

Cancellation is checked during marker/ICC parsing, input reads (at most 4 KiB per
read), pixel conversion (at least every 1,024 pixels and each row), output writes,
and metadata copying (32 KiB chunks). Standard-library JPEG/PNG codec internals
are synchronous and cannot be preempted between their I/O calls; cancellation is
checked again on return. No background decoder or encoder outlives the call.
Only cancellation becomes an error; unsupported/malformed/over-budget repairs
return the original bytes or the safe ICC-only fallback.

Verification:

```sh
CGO_ENABLED=0 go test ./llm/internal/imagerepair -count=1
go test -race ./llm/internal/imagerepair -count=1
go test ./llm/internal/imagerepair -run '^Fuzz' -count=1 -v
```

`testdata/README.md` documents independently generated LittleCMS references,
synthetic provenance, tolerances and regeneration. No production dependency on
LittleCMS, Pillow, Python, cgo, or an external executable exists.
