// Package fileident derives a stable file-incarnation identity from an
// os.FileInfo: the device/inode pair on Unix, the volume/file-index pair on
// Windows. Two packages need the same derivation (agent/transcript's resume
// sidecar and internal/apptranscript's turn index) and neither can import
// the other (apptranscript already imports transcript), so the one copy
// lives here at the root internal level both can reach.
package fileident

import (
	"fmt"
	"os"
	"reflect"
)

// FileIdentity returns a string that names the file's incarnation, or ""
// when the platform or the FileInfo carries nothing usable. It reflects
// over os.FileInfo.Sys() (Stat_t on Unix, Win32FileAttributeData on
// Windows) so it stays portable without per-platform files for what is,
// on each platform, a handful of field reads.
func FileIdentity(info os.FileInfo) string {
	if info == nil || info.Sys() == nil {
		return ""
	}
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return ""
	}
	field := func(name string) (uint64, bool) {
		got := value.FieldByName(name)
		if !got.IsValid() {
			return 0, false
		}
		switch got.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return uint64(got.Int()), true
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return got.Uint(), true
		default:
			return 0, false
		}
	}
	if device, ok := field("Dev"); ok {
		if inode, ok := field("Ino"); ok {
			return fmt.Sprintf("dev:%d:ino:%d", device, inode)
		}
	}
	volume, volumeOK := field("VolumeSerialNumber")
	high, highOK := field("FileIndexHigh")
	low, lowOK := field("FileIndexLow")
	if !volumeOK {
		volume, volumeOK = field("vol")
	}
	if !highOK {
		high, highOK = field("idxhi")
	}
	if !lowOK {
		low, lowOK = field("idxlo")
	}
	if volumeOK && highOK && lowOK {
		return fmt.Sprintf("volume:%d:index:%d", volume, high<<32|low)
	}
	return ""
}
