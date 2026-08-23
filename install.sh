#!/bin/sh
set -eu

repo="https://github.com/prime-radiant-inc/evener"
bins="evener evener-dev"

if [ -n "${PREFIX:-}" ]; then
	prefix=$PREFIX
else
	if [ -z "${HOME:-}" ]; then
		echo "Set HOME or PREFIX before running install.sh." >&2
		exit 1
	fi
	prefix=$HOME/.local
fi

bindir=${BINDIR:-$prefix/bin}
share_bindir=${EVENER_SHARE_BINDIR:-$prefix/share/evener/bin}
version=${EVENER_INSTALL_VERSION:-latest}

case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*)
		echo "Unsupported OS: $(uname -s)" >&2
		exit 1
		;;
esac

case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*)
		echo "Unsupported architecture: $(uname -m)" >&2
		exit 1
		;;
esac

case "$os-$arch" in
	linux-amd64 | darwin-arm64) ;;
	*)
		echo "No Evener binary release is available for $os-$arch." >&2
		exit 1
		;;
esac

archive_name="evener_${os}_${arch}.tar.gz"
archive_root="evener_${os}_${arch}"
if [ "$version" = "latest" ]; then
	base_url="$repo/releases/latest/download"
else
	base_url="$repo/releases/download/$version"
fi
url="$base_url/$archive_name"

tmpdir=$(mktemp -d)
if [ -z "$tmpdir" ] || [ ! -d "$tmpdir" ]; then
	echo "mktemp did not produce a usable temporary directory." >&2
	exit 1
fi
cleanup() {
	# Never hand rm an empty root: an unset or emptied tmpdir must make this
	# a no-op, not a recursive delete of the current directory.
	if [ -n "${tmpdir:-}" ]; then
		rm -rf "$tmpdir"
	fi
}
trap cleanup EXIT HUP INT TERM

archive="$tmpdir/$archive_name"
echo "Downloading $url"
curl -fsSL "$url" -o "$archive"
curl -fsSL "$base_url/checksums.txt" -o "$tmpdir/checksums.txt"

# The release publishes checksums.txt; installing an unverified archive is
# not an option. Fail closed when no sha256 tool exists.
if command -v sha256sum >/dev/null 2>&1; then
	sha_check="sha256sum -c -"
elif command -v shasum >/dev/null 2>&1; then
	sha_check="shasum -a 256 -c -"
else
	echo "Neither sha256sum nor shasum is available; refusing to install an unverified archive." >&2
	exit 1
fi

# Find this archive's line in checksums.txt BEFORE handing anything to the
# checksum tool, and treat "not exactly one line" as a hard failure. Piping
# grep straight into the tool fails open twice over: a pipeline's status is
# its last command's, and macOS's /sbin/sha256sum exits 0 on empty input — so
# an unmatched grep read as "verified" and installed the archive unchecked.
#
# The name may carry a path prefix: goreleaser writes a bare name, the older
# hand-rolled publisher wrote "dist/<name>". Accept either, anchored, with the
# archive name's regex metacharacters escaped.
archive_name_re=$(printf '%s' "$archive_name" | sed 's/[^A-Za-z0-9_-]/\\&/g')
checksum_line=$(grep -E "^[0-9a-fA-F]{64}[[:space:]]+(dist/)?${archive_name_re}\$" "$tmpdir/checksums.txt" || true)
if [ -z "$checksum_line" ]; then
	echo "checksums.txt has no entry for $archive_name; refusing to install an unverified archive." >&2
	exit 1
fi
if [ "$(printf '%s\n' "$checksum_line" | wc -l | tr -d ' ')" -ne 1 ]; then
	echo "checksums.txt has more than one entry for $archive_name; refusing to install an ambiguous archive." >&2
	exit 1
fi

# Re-emit the line under the bare name so the tool finds the file next to
# checksums.txt whatever prefix the release wrote.
expected_sha=${checksum_line%% *}
if ! (cd "$tmpdir" && printf '%s  %s\n' "$expected_sha" "$archive_name" | $sha_check); then
	echo "Checksum verification failed for $archive_name." >&2
	exit 1
fi

tar -xzf "$archive" -C "$tmpdir"

extract_dir="$tmpdir/$archive_root"
if [ ! -d "$extract_dir" ]; then
	echo "Release archive did not contain $archive_root." >&2
	exit 1
fi

install -d "$share_bindir" "$bindir"
for bin in $bins; do
	src="$extract_dir/$bin"
	if [ ! -f "$src" ]; then
		echo "Release archive did not contain $bin." >&2
		exit 1
	fi
	install -m 0755 "$src" "$share_bindir/$bin"
	ln -sfn "$share_bindir/$bin" "$bindir/$bin"
done

echo "Installed Evener binaries to $share_bindir"
echo "Symlinked commands into $bindir"

if [ -n "${HOME:-}" ] && { [ -e "$HOME/.serf" ] || [ -e "$HOME/.evener" ]; }; then
	echo ""
	echo "Found an existing ~/.serf or ~/.evener: run 'evener migrate' once before"
	echo "your first Evener launch to move it into place (pass --dry-run to preview)."
fi
