package imagerepair

import (
	"context"
	"encoding/binary"
	"image/color"
	"math"
	"sort"
)

// This is deliberately not a general CMM. ICC.1:2022 sections 8.3.3/8.4.3
// define the v4 input/display RGB matrix/TRC subset. Colorant columns are already
// in D50 PCS; chad must NOT be applied to them again (Annex F.3). Relative
// colorimetric conversion uses no black point compensation or perceptual LUT.
type iccTransform struct {
	matrix [3][3]float64
	curves [3][256]float64
}
type iccTag struct {
	start, end int
}

func parseICC(ctx context.Context, p []byte) (*iccTransform, bool) {
	if len(p) < 132 || len(p) > maxICC || uint64(binary.BigEndian.Uint32(p)) != uint64(len(p)) || p[8] != 4 || p[9] > 0x40 || p[10] != 0 || p[11] != 0 {
		return nil, false
	}
	if string(p[36:40]) != "acsp" || string(p[16:20]) != "RGB " || string(p[20:24]) != "XYZ " {
		return nil, false
	}
	if class := string(p[12:16]); class != "mntr" && class != "scnr" {
		return nil, false
	}
	if binary.BigEndian.Uint32(p[64:]) > 3 {
		return nil, false
	}
	d50 := [3]float64{0.9642, 1, 0.8249} // ICC's rounded PCS illuminant, not D65.
	for i, w := range d50 {
		if math.Abs(fixed(p[68+4*i:])-w) > 0.001 {
			return nil, false
		}
	}
	rawCount := uint64(binary.BigEndian.Uint32(p[128:]))
	if rawCount > 1024 || rawCount > uint64((len(p)-132)/12) {
		return nil, false
	}
	count := int(rawCount)
	tableEnd := 132 + 12*count
	tags := make(map[string][]byte, count)
	ranges := make([]iccTag, 0, count)
	for i := range count {
		if ctx.Err() != nil {
			return nil, false
		}
		e := p[132+12*i:]
		name := string(e[:4])
		off, size := uint64(binary.BigEndian.Uint32(e[4:])), uint64(binary.BigEndian.Uint32(e[8:]))
		if _, duplicate := tags[name]; duplicate {
			return nil, false
		}
		if off < uint64(tableEnd) || off%4 != 0 || size < 8 || off+size > uint64(len(p)) {
			return nil, false
		}
		// LUTs have precedence over matrix/TRC data. Do not silently ignore them.
		switch name[:3] {
		case "A2B", "B2A", "D2B", "B2D":
			return nil, false
		}
		tags[name] = p[off : off+size]
		ranges = append(ranges, iccTag{int(off), int(off + size)})
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].start == ranges[j].start {
			return ranges[i].end < ranges[j].end
		}
		return ranges[i].start < ranges[j].start
	})
	for i := 1; i < len(ranges); i++ {
		a, b := ranges[i-1], ranges[i]
		if b.start < a.end && (a.start != b.start || a.end != b.end) {
			return nil, false
		}
	}
	white, ok := xyzTag(tags["wtpt"])
	if !ok {
		return nil, false
	}
	for i, w := range d50 {
		if math.Abs(white[i]-w) > 0.001 {
			return nil, false
		}
	}
	if chad, exists := tags["chad"]; exists {
		if !typed(chad, "sf32") || len(chad) != 44 {
			return nil, false
		}
		var m [3][3]float64
		for r := range 3 {
			for c := range 3 {
				m[r][c] = fixed(chad[8+4*(3*r+c):])
			}
		}
		if math.Abs(determinant(m)) < 1e-6 {
			return nil, false
		}
	}
	t := new(iccTransform)
	var matrix [3][3]float64
	for c, name := range []string{"rXYZ", "gXYZ", "bXYZ"} {
		xyz, ok := xyzTag(tags[name])
		if !ok {
			return nil, false
		}
		for r, v := range xyz {
			if math.Abs(v) > 2 {
				return nil, false
			}
			matrix[r][c] = v
		}
	}
	if determinant(matrix) <= 1e-6 {
		return nil, false
	}
	// Matrix white must agree with the v4 D50 PCS. Negative individual colorants
	// are valid, but contradictory columns must not invent a different white.
	for r, w := range d50 {
		if math.Abs(matrix[r][0]+matrix[r][1]+matrix[r][2]-w) > 0.01 {
			return nil, false
		}
	}
	for c, name := range []string{"rTRC", "gTRC", "bTRC"} {
		curve, ok := parseCurve(ctx, tags[name])
		if !ok {
			return nil, false
		}
		t.curves[c] = curve
	}
	// Inverse of the Bradford-adapted IEC sRGB RGB-to-XYZ(D50) matrix.
	// This includes D50 -> D65 adaptation exactly once. See CSS Color 4's
	// color-conversion-code and ICC Annex E for the equivalent two-matrix route.
	xyzToSRGB := [3][3]float64{{3.1338561, -1.6168667, -0.4906146}, {-0.9787684, 1.9161415, 0.0334540}, {0.0719453, -0.2289914, 1.4052427}}
	for r := range 3 {
		for c := range 3 {
			for k := range 3 {
				t.matrix[r][c] += xyzToSRGB[r][k] * matrix[k][c]
			}
		}
	}
	return t, ctx.Err() == nil
}

func typed(b []byte, name string) bool {
	return len(b) >= 8 && string(b[:4]) == name && binary.BigEndian.Uint32(b[4:]) == 0
}
func fixed(b []byte) float64 { return float64(int32(binary.BigEndian.Uint32(b))) / 65536 }
func xyzTag(b []byte) ([3]float64, bool) {
	var v [3]float64
	if !typed(b, "XYZ ") || len(b) != 20 {
		return v, false
	}
	for i := range v {
		v[i] = fixed(b[8+4*i:])
	}
	return v, true
}
func determinant(m [3][3]float64) float64 {
	return m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) - m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) + m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
}

// ICC curveType (10.6) and parametricCurveType (10.18, table 68). Every
// arithmetic domain and discontinuity is checked before building byte LUTs.
func parseCurve(ctx context.Context, b []byte) ([256]float64, bool) {
	var out [256]float64
	if len(b) < 12 {
		return out, false
	}
	var evaluate func(float64) float64
	switch {
	case typed(b, "curv"):
		n := uint64(binary.BigEndian.Uint32(b[8:]))
		if n > 65536 || 12+2*n != uint64(len(b)) {
			return out, false
		}
		switch n {
		case 0:
			evaluate = func(x float64) float64 { return x }
		case 1:
			gamma := float64(binary.BigEndian.Uint16(b[12:])) / 256
			if gamma <= 0 || gamma > 16 {
				return out, false
			}
			evaluate = func(x float64) float64 { return math.Pow(x, gamma) }
		default:
			prev := uint16(0)
			for i := range n {
				if i%1024 == 0 && ctx.Err() != nil {
					return out, false
				}
				v := binary.BigEndian.Uint16(b[12+2*i:])
				if v < prev {
					return out, false
				}
				prev = v
			}
			evaluate = func(x float64) float64 {
				at := x * float64(n-1)
				i := uint64(at)
				a := float64(binary.BigEndian.Uint16(b[12+2*i:])) / 65535
				if i == n-1 {
					return a
				}
				next := float64(binary.BigEndian.Uint16(b[14+2*i:])) / 65535
				return a + (next-a)*(at-float64(i))
			}
		}
	case typed(b, "para"):
		kind := binary.BigEndian.Uint16(b[8:])
		if kind > 4 || binary.BigEndian.Uint16(b[10:]) != 0 {
			return out, false
		}
		counts := [5]int{1, 3, 4, 5, 7}
		if len(b) != 12+4*counts[kind] {
			return out, false
		}
		var p [7]float64
		for i := 0; i < counts[kind]; i++ {
			p[i] = fixed(b[12+4*i:])
		}
		g, a, offset, c, d, e, f := p[0], p[1], p[2], p[3], p[4], p[5], p[6]
		if g <= 0 || g > 16 {
			return out, false
		}
		if kind == 0 {
			evaluate = func(x float64) float64 { return math.Pow(x, g) }
			break
		}
		if a <= 0 {
			return out, false
		}
		if kind <= 2 {
			d = -offset / a
		}
		if d < 0 || d > 1 {
			return out, false
		}
		if a*d+offset < -1e-12 || a+offset < 0 {
			return out, false
		}
		if kind >= 3 && c < 0 {
			return out, false
		}
		high := func(x float64) float64 {
			v := math.Pow(math.Max(0, a*x+offset), g)
			if kind == 2 {
				v += c
			}
			if kind == 4 {
				v += e
			}
			return v
		}
		low := func(x float64) float64 {
			switch kind {
			case 2:
				return c
			case 3:
				return c * x
			case 4:
				return c*x + f
			default:
				return 0
			}
		}
		if math.Abs(high(d)-low(d)) > 0.0001 {
			return out, false
		}
		evaluate = func(x float64) float64 {
			if x >= d {
				return high(x)
			}
			return low(x)
		}
	default:
		return out, false
	}
	previous := -1.0
	for i := range out {
		if i%64 == 0 && ctx.Err() != nil {
			return out, false
		}
		y := evaluate(float64(i) / 255)
		if math.IsNaN(y) || math.IsInf(y, 0) || y < -0.0001 || y > 1.0001 || y < previous {
			return out, false
		}
		out[i] = y
		previous = y
	}
	return out, true
}

func (t *iccTransform) rgb(r, g, b uint8) color.RGBA {
	linear := [3]float64{t.curves[0][r], t.curves[1][g], t.curves[2][b]}
	var out [3]uint8
	for c := range out {
		v := t.matrix[c][0]*linear[0] + t.matrix[c][1]*linear[1] + t.matrix[c][2]*linear[2]
		// Relative colorimetric gamut clipping occurs in destination linear RGB.
		v = math.Max(0, math.Min(1, v))
		if v <= 0.0031308 {
			v *= 12.92
		} else {
			v = 1.055*math.Pow(v, 1/2.4) - 0.055
		}
		out[c] = uint8(math.Round(v * 255))
	}
	return color.RGBA{R: out[0], G: out[1], B: out[2], A: 255}
}
