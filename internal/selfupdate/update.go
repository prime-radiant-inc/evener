package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const defaultRepoURL = "https://github.com/prime-radiant-inc/serf"

var installBinaries = []string{"serf", "serf-hub", "serf-tui", "serf-doctor"}

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
	Release        string   `json:"release"`
	Channel        string   `json:"channel"`
	URL            string   `json:"url"`
	Archive        string   `json:"archive"`
	Prefix         string   `json:"prefix"`
	BinDir         string   `json:"bin_dir"`
	ShareBinDir    string   `json:"share_bin_dir"`
	Installed      []string `json:"installed"`
	RestartMessage string   `json:"restart_message"`
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
	goos := firstNonEmpty(opts.GOOS, runtime.GOOS)
	goarch := firstNonEmpty(opts.GOARCH, runtime.GOARCH)
	asset, root, err := releaseAsset(goos, goarch)
	if err != nil {
		return Result{}, err
	}
	prefix, err := installPrefix(opts.Prefix)
	if err != nil {
		return Result{}, err
	}
	binDir := firstNonEmpty(opts.BinDir, filepath.Join(prefix, "bin"))
	shareBinDir := firstNonEmpty(opts.ShareBinDir, filepath.Join(prefix, "share", "serf", "bin"))
	repoURL := strings.TrimRight(firstNonEmpty(opts.RepoURL, defaultRepoURL), "/")
	url := releaseURL(repoURL, target.Release, asset)
	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	tmpDir, err := os.MkdirTemp("", "serf-upgrade-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, asset)
	if err := download(ctx, client, url, archivePath); err != nil {
		return Result{}, err
	}
	extractDir := filepath.Join(tmpDir, root)
	if err := extractReleaseArchive(archivePath, root, extractDir); err != nil {
		return Result{}, err
	}
	if err := installExtractedBinaries(extractDir, shareBinDir, binDir); err != nil {
		return Result{}, err
	}

	installed := make([]string, 0, len(installBinaries))
	for _, bin := range installBinaries {
		installed = append(installed, filepath.Join(shareBinDir, bin))
	}
	return Result{
		Release:        target.Release,
		Channel:        target.Channel,
		URL:            url,
		Archive:        asset,
		Prefix:         prefix,
		BinDir:         binDir,
		ShareBinDir:    shareBinDir,
		Installed:      installed,
		RestartMessage: "Restart serf-tui and serf-hub to use the upgraded binaries.",
	}, nil
}

func releaseAsset(goos, goarch string) (asset, root string, err error) {
	switch goos + "-" + goarch {
	case "linux-amd64":
		return "serf_linux_amd64.tar.gz", "serf_linux_amd64", nil
	case "darwin-arm64":
		return "serf_darwin_arm64.tar.gz", "serf_darwin_arm64", nil
	default:
		return "", "", fmt.Errorf("No Serf binary release is available for %s-%s.", goos, goarch)
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

func releaseURL(repoURL, release, asset string) string {
	if release == "latest" {
		return repoURL + "/releases/latest/download/" + asset
	}
	return repoURL + "/releases/download/" + release + "/" + asset
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
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(file, resp.Body); err != nil {
		return err
	}
	return file.Close()
}

func extractReleaseArchive(archivePath, root, destRoot string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	want := map[string]string{}
	for _, bin := range installBinaries {
		want[path.Join(root, bin)] = bin
	}
	seen := map[string]bool{}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return err
	}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		bin, ok := want[header.Name]
		if !ok {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("release archive entry %s is not a regular file", header.Name)
		}
		out := filepath.Join(destRoot, bin)
		file, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(file, tr)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
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

func installExtractedBinaries(extractDir, shareBinDir, binDir string) error {
	if err := os.MkdirAll(shareBinDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	for _, bin := range installBinaries {
		src := filepath.Join(extractDir, bin)
		dst := filepath.Join(shareBinDir, bin)
		if err := copyExecutable(src, dst); err != nil {
			return err
		}
		link := filepath.Join(binDir, bin)
		_ = os.Remove(link)
		if err := os.Symlink(dst, link); err != nil {
			return err
		}
	}
	return nil
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, dst)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
