package transcriptindex

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// sidecarSuffix starts the name of every sidecar directory beside a transcript.
const sidecarSuffix = ".index"

// DirFor is the sidecar directory the daemon and the hub share for a
// transcript: the location every Open of that transcript's index must agree
// on, so a rebuild by one is visible to the other. The name carries this
// binary's projection, so a daemon still running an older build keeps its own
// sidecar instead of trading rebuilds with the hub over a shared one.
func DirFor(transcriptPath string) string {
	sum := sha256.Sum256([]byte(currentProjection()))
	return transcriptPath + sidecarSuffix + "-" + hex.EncodeToString(sum[:4])
}

// SidecarDirs lists every sidecar directory beside a transcript, whichever
// projection built it, for deleting a transcript's derived data. A transcript
// with none lists none.
func SidecarDirs(transcriptPath string) ([]string, error) {
	parent, prefix := filepath.Dir(transcriptPath), filepath.Base(transcriptPath)+sidecarSuffix
	entries, err := os.ReadDir(parent)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, entry := range entries {
		if name := entry.Name(); name == prefix || strings.HasPrefix(name, prefix+"-") {
			dirs = append(dirs, filepath.Join(parent, entry.Name()))
		}
	}
	return dirs, nil
}
