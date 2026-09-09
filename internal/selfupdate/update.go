package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"primeradiant.com/evener/envvars"
)

const defaultRepoURL = "https://github.com/prime-radiant-inc/evener"

// defaultUpgradeTimeout bounds the whole release-archive download when the
// caller passes no HTTP client. Archives are ~40MB; a stalled connection
// must fail instead of holding the hub's hubUpdateMu forever. A var so
// tests can shorten it.
var defaultUpgradeTimeout = 5 * time.Minute

// defaultMaxArchiveBytes caps one release-archive download. Archives are
// ~40MB; an unbounded copy lets a compromised response fill the disk before
// the checksum ever runs. A var so tests can shrink it.
var defaultMaxArchiveBytes = int64(128 << 20)

var installBinaries = []string{"evener", "evener-dev"}

var (
	copyStream = io.Copy
	closeFile  = (*os.File).Close
	renameFile = os.Rename
)

type Options struct {
	Requested      string
	CurrentChannel string
	Prefix         string
	BinDir         string
	ShareBinDir    string
	GOOS           string
	GOARCH         string
	RepoURL        string
	HTTPClient     *http.Client
	Stdout         io.Writer
}

type Target struct {
	Release string
	Channel string
}

type Result struct {
	Release     string   `json:"release"`
	Channel     string   `json:"channel"`
	URL         string   `json:"url"`
	Archive     string   `json:"archive"`
	Prefix      string   `json:"prefix"`
	BinDir      string   `json:"bin_dir"`
	ShareBinDir string   `json:"share_bin_dir"`
	Installed   []string `json:"installed"`
	// InstalledSHA256 maps each Installed path to the hex SHA-256 of the
	// bytes committed there, computed while the install lock is held. A
	// restart can re-verify the binary after the lock releases instead
	// of trusting a path a concurrent installer may have swapped.
	InstalledSHA256 map[string]string `json:"installed_sha256"`
	RestartMessage  string            `json:"restart_message"`
}

func ResolveTarget(requested, currentChannel string) (Target, error) {
	requested = strings.TrimSpace(requested)
	currentChannel = strings.TrimSpace(currentChannel)
	switch requested {
	case "", "current":
		if currentChannel == "snapshot" {
			return Target{Release: "snapshot", Channel: "snapshot"}, nil
		}
		return Target{Release: "latest", Channel: "release"}, nil
	case "snapshot":
		return Target{Release: "snapshot", Channel: "snapshot"}, nil
	case "release", "latest":
		return Target{Release: "latest", Channel: "release"}, nil
	default:
		if strings.HasPrefix(requested, "v") {
			return Target{Release: requested, Channel: "release"}, nil
		}
		return Target{}, fmt.Errorf("unknown upgrade target %q; use release, snapshot, or a v* tag", requested)
	}
}

func Upgrade(ctx context.Context, opts Options) (Result, error) {
	target, err := ResolveTarget(opts.Requested, opts.CurrentChannel)
	if err != nil {
		return Result{}, err
	}
	goos := envvars.FirstNonEmpty(opts.GOOS, runtime.GOOS)
	goarch := envvars.FirstNonEmpty(opts.GOARCH, runtime.GOARCH)
	asset, root, err := releaseAsset(goos, goarch)
	if err != nil {
		return Result{}, err
	}
	prefix, err := installPrefix(opts.Prefix)
	if err != nil {
		return Result{}, err
	}
	binDir := envvars.FirstNonEmpty(opts.BinDir, filepath.Join(prefix, "bin"))
	shareBinDir := envvars.FirstNonEmpty(opts.ShareBinDir, filepath.Join(prefix, "share", "evener", "bin"))
	repoURL := strings.TrimRight(envvars.FirstNonEmpty(opts.RepoURL, defaultRepoURL), "/")
	url := releaseURL(repoURL, target.Release, asset)
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultUpgradeTimeout}
	}

	tmpDir, err := os.MkdirTemp("", "evener-upgrade-*")
	if err != nil {
		return Result{}, err
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	archivePath := filepath.Join(tmpDir, asset)
	if err := download(ctx, client, url, archivePath); err != nil {
		return Result{}, err
	}
	if err := verifyArchiveChecksum(ctx, client, repoURL, target.Release, asset, archivePath); err != nil {
		return Result{}, err
	}
	extractDir := filepath.Join(tmpDir, root)
	if err := extractReleaseArchive(ctx, archivePath, root, extractDir); err != nil {
		return Result{}, err
	}
	// The digests come back from inside the install lock: they pin
	// exactly the bytes this install committed, so a concurrent
	// installer swapping paths afterwards cannot poison the restart.
	digests, err := installExtractedBinaries(ctx, extractDir, shareBinDir, binDir)
	if err != nil {
		return Result{}, err
	}

	installed := make([]string, 0, len(installBinaries))
	for _, bin := range installBinaries {
		installed = append(installed, filepath.Join(shareBinDir, bin))
	}
	return Result{
		Release:         target.Release,
		Channel:         target.Channel,
		URL:             url,
		Archive:         asset,
		Prefix:          prefix,
		BinDir:          binDir,
		ShareBinDir:     shareBinDir,
		Installed:       installed,
		InstalledSHA256: digests,
		RestartMessage:  "Restart evener to use the upgraded binary.",
	}, nil
}

func releaseAsset(goos, goarch string) (asset, root string, err error) {
	switch goos + "-" + goarch {
	case "linux-amd64":
		return "evener_linux_amd64.tar.gz", "evener_linux_amd64", nil
	case "darwin-arm64":
		return "evener_darwin_arm64.tar.gz", "evener_darwin_arm64", nil
	default:
		return "", "", fmt.Errorf("unsupported platform %s-%s: no Evener binary release is available", goos, goarch)
	}
}

func installPrefix(prefix string) (string, error) {
	if strings.TrimSpace(prefix) != "" {
		return prefix, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("set HOME or PREFIX before upgrading")
	}
	return filepath.Join(home, ".local"), nil
}

// InstallDirsFromExecutable derives the install layout (prefix, binDir,
// shareBinDir) from the path of the running binary, so a hub installed
// under /usr/local upgrades /usr/local instead of the ~/.local default.
//
// The managed binary lives at <prefix>/share/evener/bin/<name>; the
// install.sh entrypoint is <prefix>/bin/<name> (a symlink there), and
// install.sh, building.mk, and `evener upgrade` all accept BINDIR /
// EVENER_SHARE_BINDIR overrides that decouple those dirs from the prefix.
// os.Executable reports the symlink path unresolved, so the executable is
// first resolved through symlinks (a name that resolves against nothing
// falls back to itself) to find the managed binary and the prefix -- but
// the UNRESOLVED entrypoint directory is kept as the binDir, because that
// is where this installation's symlinks live, custom or standard. The
// share layout resolves the shareBinDir; a bare bin-layout path (hardlink
// or copy with no resolvable share target) maps back to the prefix with
// the standard share dir. Anything else (a worktree build, an ad-hoc
// path) returns all "" and Upgrade's own defaults apply.
func InstallDirsFromExecutable(exe string) (prefix, binDir, shareBinDir string) {
	entryDir := filepath.Dir(exe)
	candidate := exe
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = resolved
	}
	dir := filepath.Dir(candidate)
	const shareSuffix = string(filepath.Separator) + "share" + string(filepath.Separator) + "evener" + string(filepath.Separator) + "bin"
	if rest, ok := strings.CutSuffix(dir, shareSuffix); ok {
		// The filesystem root is a valid prefix: cutting the suffix
		// off a root-joined dir leaves "" which means separator.
		// Only a bare relative dir or "." means no install layout.
		if prefix = rest; prefix == "" {
			prefix = string(filepath.Separator)
		}
		if prefix != "." {
			// The entrypoint dir is this installation's symlink dir --
			// custom BINDIR included -- but only when it holds a live
			// entrypoint distinct from the managed binary: a sibling
			// there with the launched name that resolves to the managed
			// binary. On Linux os.Executable pre-resolves /proc/self/exe
			// to the share target itself, so entry==dir and the sibling
			// check would trivially pass -- and returning binDir==dir
			// makes the installer Remove the just-copied binary and
			// symlink it to itself. A synthetic exe path (or a different
			// binary's dir) must not redirect symlinks elsewhere either:
			// fall back to the standard <prefix>/bin in all these cases.
			entry := entryDir
			if abs, err := filepath.Abs(entry); err == nil {
				entry = abs
			}
			sibling := filepath.Join(entry, filepath.Base(exe))
			if entry != dir {
				if resolved, err := filepath.EvalSymlinks(sibling); err == nil && resolved == candidate {
					return prefix, entry, dir
				}
			}
			return prefix, filepath.Join(prefix, "bin"), dir
		}
		return "", "", ""
	}
	// Non-standard share layout (custom EVENER_SHARE_BINDIR): when the
	// launch entrypoint itself is a symlink resolving into the managed
	// binary, derive both dirs directly from the link instead of matching
	// suffixes -- the prefix is unknowable, so return it empty and let the
	// caller use binDir/shareBinDir as given.
	if entryDir != dir {
		sibling := filepath.Join(entryDir, filepath.Base(exe))
		if abs, err := filepath.Abs(entryDir); err == nil {
			if resolved, err := filepath.EvalSymlinks(sibling); err == nil && resolved == candidate {
				return "", abs, dir
			}
		}
	}
	const binSuffix = string(filepath.Separator) + "bin"
	if rest, ok := strings.CutSuffix(dir, binSuffix); ok {
		if prefix = rest; prefix != "" {
			return prefix, dir, filepath.Join(prefix, "share", "evener", "bin")
		}
	}
	return "", "", ""
}

func releaseURL(repoURL, release, asset string) string {
	if release == "latest" {
		return repoURL + "/releases/latest/download/" + asset
	}
	return repoURL + "/releases/download/" + release + "/" + asset
}

// maxExtractedBytes caps total uncompressed output across one archive
// extraction. Binaries are ~100MB installed; a highly compressible entry
// could otherwise expand the 128MB download cap into disk exhaustion while
// ignoring the operation deadline inside a single entry copy.
const maxExtractedBytes = int64(512 << 20)

// ctxReader returns a reader that stops the copy when ctx expires, so a
// single large entry cannot outlive the overall upgrade deadline.
func ctxReader(ctx context.Context, r io.Reader) io.Reader {
	return &ctxReaderT{ctx: ctx, r: r}
}

type ctxReaderT struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReaderT) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// verifyArchiveChecksum fails closed unless the downloaded archive's
// SHA-256 matches the release's checksums.txt entry for it, mirroring
// install.sh's "refusing to install an unverified archive" guarantee. It
// runs after download and before extraction, so a tampered archive never
// reaches the install prefix, let alone an exec.
//
// Limitation, stated plainly: checksums.txt is fetched over the same
// transport as the archive (GitHub TLS), unsigned. This stops
// bit-rot, CDN corruption, and asset-swaps that leave checksums.txt
// intact; it does not survive a compromise that rewrites both files the
// way a signature would.
func verifyArchiveChecksum(ctx context.Context, client *http.Client, repoURL, release, asset, archivePath string) error {
	checksumsURL := releaseURL(repoURL, release, "checksums.txt")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumsURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download %s: HTTP %d", checksumsURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read %s: %w", checksumsURL, err)
	}
	var matches []string
	for line := range strings.SplitSeq(string(body), "\n") {
		if sum := parseChecksumEntry(line, asset); sum != "" {
			matches = append(matches, sum)
		}
	}
	switch len(matches) {
	case 0:
		return fmt.Errorf("checksums.txt has no entry for %s; refusing to install an unverified archive", asset)
	case 1:
		// The one vouched digest; compare below.
	default:
		return fmt.Errorf("checksums.txt has more than one entry for %s; refusing to install an ambiguous archive", asset)
	}
	return verifyChecksumFile(archivePath, "", asset, matches[0])
}

// verifyChecksumFile compares a downloaded archive against one vouched hex
// digest, streaming the file through the hash so a ~40MB archive never
// sits fully in memory next to the copy already made. want may come from
// the matches slice verifyArchiveChecksum parsed, or be empty to parse
// checksumsPath for the asset instead (tests).
func verifyChecksumFile(archivePath, checksumsPath, asset, want string) error {
	if want == "" {
		body, err := os.ReadFile(checksumsPath)
		if err != nil {
			return err
		}
		for line := range strings.SplitSeq(string(body), "\n") {
			if sum := parseChecksumEntry(line, asset); sum != "" {
				if want != "" {
					return fmt.Errorf("checksums.txt has more than one entry for %s; refusing to install an ambiguous archive", asset)
				}
				want = sum
			}
		}
		if want == "" {
			return fmt.Errorf("checksums.txt has no entry for %s; refusing to install an unverified archive", asset)
		}
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
	}()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, defaultMaxArchiveBytes+1)); err != nil {
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: refusing to install a tampered archive", asset)
	}
	return nil
}

// parseChecksumEntry extracts the hex digest from one checksums.txt line
// for asset, or "" when the line is for another file or malformed. Format:
// 64 hex digits, whitespace, then the archive name, tolerating goreleaser's
// "dist/<name>" spelling the way install.sh does.
func parseChecksumEntry(line, asset string) string {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) != 2 {
		return ""
	}
	sum, name := fields[0], strings.TrimPrefix(fields[1], "dist/")
	if len(sum) != 64 || name != asset {
		return ""
	}
	for _, c := range sum {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return ""
		}
	}
	return sum
}

func download(ctx context.Context, client *http.Client, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := copyStream(file, io.LimitReader(resp.Body, defaultMaxArchiveBytes+1)); err != nil {
		if closeErr := closeFile(file); closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return err
	}
	if err := closeFile(file); err != nil {
		return err
	}
	if info, err := os.Stat(dest); err != nil {
		return err
	} else if info.Size() > defaultMaxArchiveBytes {
		_ = os.Remove(dest)
		return fmt.Errorf("download %s: archive larger than %d bytes, refusing a response that would exhaust the disk", url, defaultMaxArchiveBytes)
	}
	return nil
}

func extractReleaseArchive(ctx context.Context, archivePath, root, destRoot string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
	}()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() {
		_ = gz.Close()
	}()
	tr := tar.NewReader(gz)

	want := map[string]string{}
	for _, bin := range installBinaries {
		want[path.Join(root, bin)] = bin
	}
	seen := map[string]bool{}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return err
	}
	var extractedTotal int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		bin, ok := want[header.Name]
		if !ok {
			// Drain skipped entries through the same budget: Next()
			// discards the previous entry's body on the following
			// call, so an unbounded `continue` would decompress a
			// giant ignored member outside the cap and ctx checks.
			n, err := io.Copy(io.Discard, ctxReader(ctx, io.LimitReader(tr, maxExtractedBytes-extractedTotal+1)))
			if err != nil {
				return err
			}
			extractedTotal += n
			if extractedTotal > maxExtractedBytes {
				return fmt.Errorf("release archive expands past %d bytes, refusing a decompression bomb", maxExtractedBytes)
			}
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("release archive entry %s is not a regular file", header.Name)
		}
		out := filepath.Join(destRoot, bin)
		file, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		// Bound each entry and the running total: a compressible entry
		// must not expand past disk sense, and the copy observes ctx
		// so one giant entry cannot outlive the deadline either.
		remaining := maxExtractedBytes - extractedTotal
		n, copyErr := copyStream(file, ctxReader(ctx, io.LimitReader(tr, remaining+1)))
		closeErr := closeFile(file)
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		extractedTotal += n
		if extractedTotal > maxExtractedBytes {
			return fmt.Errorf("release archive expands past %d bytes, refusing a decompression bomb", maxExtractedBytes)
		}
		seen[bin] = true
	}
	for _, bin := range installBinaries {
		if !seen[bin] {
			return fmt.Errorf("release archive did not contain %s", bin)
		}
	}
	return nil
}

func installExtractedBinaries(ctx context.Context, extractDir, shareBinDir, binDir string) (map[string]string, error) {
	if err := os.MkdirAll(shareBinDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return nil, err
	}
	// Cross-process mutual exclusion: hubUpdateMu covers one process's
	// threads, but `evener upgrade` or another hub may install into the
	// same prefix concurrently. The lock file lives in the managed dir
	// so custom share layouts lock the right place. The wait observes
	// ctx so a stuck holder cannot strand the RPC past the overall
	// upgrade deadline.
	release, err := acquireInstallLockCtx(ctx, shareBinDir)
	if err != nil {
		return nil, err
	}
	defer release()
	// Stage then commit: copy both new binaries to temp names first, so
	// a failure before the commit point leaves the live pair untouched.
	// Commit renames each staged file over its destination and swaps
	// both symlinks; a commit failure rolls back to the pre-install
	// binaries captured below, never leaving a mixed-release pair.
	type staged struct {
		bin      string
		tmp      string
		previous []byte // pre-install content, nil when dst is new
		hadPrev  bool
		// linkHadEntry reports a pre-install binDir entry; linkTarget and
		// linkIsLink describe it. linkFile holds the bytes of a regular
		// file entry (a copied executable is a supported layout), so
		// rollback can restore it after swapSymlink's rename-over.
		linkHadEntry bool
		linkTarget   string
		linkIsLink   bool
		linkFile     []byte
	}
	var stagedBins []staged
	rollback := func() {
		for _, s := range stagedBins {
			_ = os.Remove(s.tmp)
		}
	}
	for _, bin := range installBinaries {
		if err := ctx.Err(); err != nil {
			rollback()
			return nil, err
		}
		src := filepath.Join(extractDir, bin)
		dst := filepath.Join(shareBinDir, bin)
		tmp, err := stageExecutable(ctx, src, shareBinDir, bin)
		if err != nil {
			rollback()
			return nil, err
		}
		prev, statErr := os.ReadFile(dst)
		s := staged{bin: bin, tmp: tmp, previous: prev, hadPrev: statErr == nil}
		if fi, lerr := os.Lstat(filepath.Join(binDir, bin)); lerr == nil {
			s.linkHadEntry = true
			if fi.Mode()&os.ModeSymlink != 0 {
				s.linkIsLink = true
				if target, rerr := os.Readlink(filepath.Join(binDir, bin)); rerr == nil {
					s.linkTarget = target
				}
			} else if fi.Mode().IsRegular() {
				// Snapshot now: the commit loop's rename-over destroys
				// these bytes, and rollback must restore them.
				if data, rerr := os.ReadFile(filepath.Join(binDir, bin)); rerr == nil {
					s.linkFile = data
				}
			}
		}
		stagedBins = append(stagedBins, s)
	}
	committed := false
	defer func() {
		if !committed {
			rollback()
		}
	}()
	restore := func() {
		for _, s := range stagedBins {
			// The binDir entry is restored independently of hadPrev:
			// the commit loop swaps the link even on a first-time
			// install (no prior managed binary), so a mid-commit
			// failure must remove the swapped-in link rather than
			// leave it dangling after its target is deleted.
			link := filepath.Join(binDir, s.bin)
			switch {
			case !s.linkHadEntry:
				_ = os.Remove(link)
			case s.linkIsLink:
				_ = os.Remove(link)
				_ = os.Symlink(s.linkTarget, link)
			case s.linkFile != nil:
				// A copied executable: restore the snapshotted bytes
				// over the swapped-in link.
				_ = os.Remove(link)
				_ = os.WriteFile(link, s.linkFile, 0o755)
			default:
				// A non-regular entry (dir and friends) cannot be
				// reconstructed after rename-over destroyed it; leave
				// the new link pointing at the restored binary rather
				// than guessing.
			}
			dst := filepath.Join(shareBinDir, s.bin)
			if !s.hadPrev {
				_ = os.Remove(dst)
				continue
			}
			_ = os.WriteFile(dst, s.previous, 0o755)
		}
	}
	for _, s := range stagedBins {
		if err := ctx.Err(); err != nil {
			restore()
			return nil, err
		}
		dst := filepath.Join(shareBinDir, s.bin)
		if err := renameFile(s.tmp, dst); err != nil {
			_ = os.Remove(s.tmp)
			restore()
			return nil, err
		}
		if err := swapSymlink(dst, filepath.Join(binDir, s.bin)); err != nil {
			restore()
			return nil, err
		}
	}
	// Digest before committing the transaction: a digest failure must roll
	// back the swapped pair like any other commit error, not leave the new
	// binaries live while the install reports failure and no restart runs.
	digests, err := digestsUnderLock(shareBinDir)
	if err != nil {
		restore()
		return nil, err
	}
	committed = true
	return digests, nil
}

// digestsUnderLock hashes each committed managed binary. Callers must
// hold the install lock: the digests pin exactly the bytes committed by
// this install, so a concurrent installer swapping paths afterwards
// cannot poison a restart pin computed from them.
func digestsUnderLock(shareBinDir string) (map[string]string, error) {
	digests := make(map[string]string, len(installBinaries))
	for _, bin := range installBinaries {
		data, err := os.ReadFile(filepath.Join(shareBinDir, bin))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		digests[filepath.Join(shareBinDir, bin)] = hex.EncodeToString(sum[:])
	}
	return digests, nil
}

// stageExecutable copies src into a temp file beside the managed dir and
// returns its path; the caller commits it with renameFile or removes it.
// Staging (not direct copy) keeps a failed install from touching the live
// pair before the commit point.
func stageExecutable(ctx context.Context, src, shareBinDir, bin string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = in.Close()
	}()
	out, err := os.CreateTemp(shareBinDir, bin+".*.stage")
	if err != nil {
		return "", err
	}
	tmp := out.Name()
	if err := out.Chmod(0o755); err != nil { // CreateTemp opens 0600
		_ = closeFile(out)
		_ = os.Remove(tmp)
		return "", err
	}
	if err := ctx.Err(); err != nil {
		_ = closeFile(out)
		_ = os.Remove(tmp)
		return "", err
	}
	_, copyErr := copyStream(out, in)
	closeErr := closeFile(out)
	if copyErr != nil {
		_ = os.Remove(tmp)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return "", closeErr
	}
	return tmp, nil
}

// swapSymlink atomically points link at target via a temp link + rename:
// concurrent readers (or a racing installer) see the old or the new
// target, never a missing path or EEXIST from Remove+Symlink racing.
func swapSymlink(target, link string) error {
	tmp, err := os.CreateTemp(filepath.Dir(link), filepath.Base(link)+".*.link")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpName) // symlink(2) needs a free path; CreateTemp reserved the name
	if err := os.Symlink(target, tmpName); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := renameFile(tmpName, link); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// AcquireInstallLockForVerify re-acquires the cross-process install lock
// for a share dir, for the restart path's verify+exec critical section.
// Upgrade's lock releases at return while the exec runs later; holding
// the lock across verify+Exec closes the swap window a digest check alone
// leaves open (verify-then-exec is itself racy). The fd is O_CLOEXEC so a
// successful Exec drops the lock exactly when the new image takes over;
// on any failure the caller releases and the old process keeps running.
func AcquireInstallLockForVerify(ctx context.Context, shareBinDir string) (release func(), err error) {
	return acquireInstallLockCtx(ctx, shareBinDir)
}
