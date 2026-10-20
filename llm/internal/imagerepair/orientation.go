package imagerepair

import "encoding/binary"

// Only IFD0 controls the main image. Thumbnail/sub-IFD orientations never do.
// Invalid or ambiguous IFD0 orientation disables conversion, preserving EXIF in
// the metadata-only fallback instead of guessing how the image should turn.
func parseOrientation(tiff []byte) (int, bool) {
	if len(tiff) < 8 {
		return 0, false
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0, false
	}
	if order.Uint16(tiff[2:]) != 42 {
		return 0, false
	}
	off := uint64(order.Uint32(tiff[4:]))
	if off < 8 || off+2 > uint64(len(tiff)) {
		return 0, false
	}
	n := uint64(order.Uint16(tiff[off:]))
	if off+2+12*n+4 > uint64(len(tiff)) {
		return 0, false
	}
	orientation, found := 1, false
	for i := range n {
		e := tiff[off+2+12*i:]
		if order.Uint16(e) != 0x112 {
			continue
		}
		if found || order.Uint16(e[2:]) != 3 || order.Uint32(e[4:]) != 1 {
			return 0, false
		}
		orientation = int(order.Uint16(e[8:]))
		if orientation < 1 || orientation > 8 {
			return 0, false
		}
		found = true
	}
	return orientation, true
}

func orientPoint(x, y, w, h, o int) (int, int) {
	switch o {
	case 2:
		return w - 1 - x, y
	case 3:
		return w - 1 - x, h - 1 - y
	case 4:
		return x, h - 1 - y
	case 5:
		return y, x
	case 6:
		return h - 1 - y, x
	case 7:
		return h - 1 - y, w - 1 - x
	case 8:
		return y, w - 1 - x
	default:
		return x, y
	}
}
