#!/bin/sh
# Prepare a disposable HOME for a Go command without throwing away the user's
# reusable Go caches or persisted go env settings. The caller owns and removes
# the supplied root; this helper only creates state beneath it and exports the
# environment for commands run afterward.

serf_goenv_last_value() {
	serf_goenv_key=$1
	serf_goenv_file=$2
	[ -f "$serf_goenv_file" ] || return 0
	awk -v key="$serf_goenv_key" '
		index($0, key "=") == 1 {
			last = substr($0, length(key) + 2)
			found = 1
		}
		END { if (found) print last }
	' "$serf_goenv_file"
}

serf_prepare_private_go_home() {
	serf_private_root=$1
	serf_ambient_home=${HOME:-}
	serf_ambient_xdg_config=${XDG_CONFIG_HOME:-}
	serf_ambient_xdg_cache=${XDG_CACHE_HOME:-}
	serf_ambient_goenv=${GOENV:-}

	if [ -z "$serf_ambient_goenv" ]; then
		case "$(uname -s)" in
			Darwin)
				[ -z "$serf_ambient_home" ] || serf_ambient_goenv="$serf_ambient_home/Library/Application Support/go/env"
				;;
			*)
				if [ -n "$serf_ambient_xdg_config" ]; then
					serf_ambient_goenv="$serf_ambient_xdg_config/go/env"
				elif [ -n "$serf_ambient_home" ]; then
					serf_ambient_goenv="$serf_ambient_home/.config/go/env"
				fi
				;;
		esac
	fi

	serf_goenv_gopath=""
	serf_goenv_gocache=""
	if [ -n "$serf_ambient_goenv" ] && [ "$serf_ambient_goenv" != "off" ]; then
		serf_goenv_gopath=$(serf_goenv_last_value GOPATH "$serf_ambient_goenv")
		serf_goenv_gocache=$(serf_goenv_last_value GOCACHE "$serf_ambient_goenv")
	fi

	serf_default_gopath=""
	serf_default_gocache=""
	if [ -z "${GOPATH:-}" ] && [ -z "$serf_goenv_gopath" ] && [ -n "$serf_ambient_home" ]; then
		serf_default_gopath="$serf_ambient_home/go"
	fi
	if [ -z "${GOCACHE:-}" ] && [ -z "$serf_goenv_gocache" ]; then
		case "$(uname -s)" in
			Darwin)
				[ -z "$serf_ambient_home" ] || serf_default_gocache="$serf_ambient_home/Library/Caches/go-build"
				;;
			*)
				if [ -n "$serf_ambient_xdg_cache" ]; then
					serf_default_gocache="$serf_ambient_xdg_cache/go-build"
				elif [ -n "$serf_ambient_home" ]; then
					serf_default_gocache="$serf_ambient_home/.cache/go-build"
				fi
				;;
		esac
	fi

	mkdir -p "$serf_private_root/home" "$serf_private_root/xdg-config" "$serf_private_root/xdg-cache" "$serf_private_root/xdg-state" || return 1
	if [ "$serf_ambient_goenv" = "off" ]; then
		serf_private_goenv=off
	else
		serf_private_goenv="$serf_private_root/go-env"
		if [ -n "$serf_ambient_goenv" ] && [ -f "$serf_ambient_goenv" ]; then
			cp "$serf_ambient_goenv" "$serf_private_goenv" || return 1
		else
			: >"$serf_private_goenv" || return 1
		fi
	fi
	if [ -n "$serf_default_gopath" ]; then
		GOPATH="$serf_default_gopath"
		export GOPATH
	fi
	if [ -n "$serf_default_gocache" ]; then
		GOCACHE="$serf_default_gocache"
		export GOCACHE
	fi
	GOENV="$serf_private_goenv"
	HOME="$serf_private_root/home"
	XDG_CONFIG_HOME="$serf_private_root/xdg-config"
	XDG_CACHE_HOME="$serf_private_root/xdg-cache"
	XDG_STATE_HOME="$serf_private_root/xdg-state"
	export GOENV HOME XDG_CONFIG_HOME XDG_CACHE_HOME XDG_STATE_HOME

	unset serf_private_root serf_ambient_home serf_ambient_xdg_config serf_ambient_xdg_cache serf_ambient_goenv
	unset serf_goenv_gopath serf_goenv_gocache serf_default_gopath serf_default_gocache serf_private_goenv
	unset serf_goenv_key serf_goenv_file
}
