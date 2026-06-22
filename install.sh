#!/bin/sh
set -eu

repo="https://github.com/prime-radiant-inc/serf"
bins="serf serf-hub serf-tui serf-doctor"

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
share_bindir=${SERF_SHARE_BINDIR:-$prefix/share/serf/bin}
version=${SERF_INSTALL_VERSION:-latest}

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
		echo "No Serf binary release is available for $os-$arch." >&2
		exit 1
		;;
esac

archive_name="serf_${os}_${arch}.tar.gz"
archive_root="serf_${os}_${arch}"
if [ "$version" = "latest" ]; then
	url="$repo/releases/latest/download/$archive_name"
else
	url="$repo/releases/download/$version/$archive_name"
fi

tmpdir=$(mktemp -d)
cleanup() {
	rm -rf "$tmpdir"
}
trap cleanup EXIT HUP INT TERM

archive="$tmpdir/$archive_name"
echo "Downloading $url"
curl -fsSL "$url" -o "$archive"
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

echo "Installed Serf binaries to $share_bindir"
echo "Symlinked commands into $bindir"
