#!/bin/sh
# Prepare a disposable HOME for a Go command without throwing away the user's
# reusable Go caches or persisted go env settings. The caller owns and removes
# the supplied root; this helper only creates state beneath it and exports the
# environment for commands run afterward.

evener_goenv_last_value() {
	evener_goenv_key=$1
	evener_goenv_file=$2
	[ -f "$evener_goenv_file" ] || return 0
	awk -v key="$evener_goenv_key" '
		index($0, key "=") == 1 {
			last = substr($0, length(key) + 2)
			found = 1
		}
		END { if (found) print last }
	' "$evener_goenv_file"
}

evener_prepare_private_go_home() {
	evener_private_root=$1
	evener_ambient_home=${HOME:-}
	evener_ambient_xdg_config=${XDG_CONFIG_HOME:-}
	evener_ambient_xdg_cache=${XDG_CACHE_HOME:-}
	evener_ambient_goenv=${GOENV:-}

	if [ -z "$evener_ambient_goenv" ]; then
		case "$(uname -s)" in
			Darwin)
				[ -z "$evener_ambient_home" ] || evener_ambient_goenv="$evener_ambient_home/Library/Application Support/go/env"
				;;
			*)
				if [ -n "$evener_ambient_xdg_config" ]; then
					evener_ambient_goenv="$evener_ambient_xdg_config/go/env"
				elif [ -n "$evener_ambient_home" ]; then
					evener_ambient_goenv="$evener_ambient_home/.config/go/env"
				fi
				;;
		esac
	fi

	evener_goenv_gopath=""
	evener_goenv_gocache=""
	if [ -n "$evener_ambient_goenv" ] && [ "$evener_ambient_goenv" != "off" ]; then
		evener_goenv_gopath=$(evener_goenv_last_value GOPATH "$evener_ambient_goenv")
		evener_goenv_gocache=$(evener_goenv_last_value GOCACHE "$evener_ambient_goenv")
	fi

	evener_default_gopath=""
	evener_default_gocache=""
	if [ -z "${GOPATH:-}" ] && [ -z "$evener_goenv_gopath" ] && [ -n "$evener_ambient_home" ]; then
		evener_default_gopath="$evener_ambient_home/go"
	fi
	if [ -z "${GOCACHE:-}" ] && [ -z "$evener_goenv_gocache" ]; then
		case "$(uname -s)" in
			Darwin)
				[ -z "$evener_ambient_home" ] || evener_default_gocache="$evener_ambient_home/Library/Caches/go-build"
				;;
			*)
				if [ -n "$evener_ambient_xdg_cache" ]; then
					evener_default_gocache="$evener_ambient_xdg_cache/go-build"
				elif [ -n "$evener_ambient_home" ]; then
					evener_default_gocache="$evener_ambient_home/.cache/go-build"
				fi
				;;
		esac
	fi

	mkdir -p "$evener_private_root/home" "$evener_private_root/xdg-config" "$evener_private_root/xdg-cache" "$evener_private_root/xdg-state" || return 1
	if [ "$evener_ambient_goenv" = "off" ]; then
		evener_private_goenv=off
	else
		evener_private_goenv="$evener_private_root/go-env"
		if [ -n "$evener_ambient_goenv" ] && [ -f "$evener_ambient_goenv" ]; then
			cp "$evener_ambient_goenv" "$evener_private_goenv" || return 1
		else
			: >"$evener_private_goenv" || return 1
		fi
	fi
	if [ -n "$evener_default_gopath" ]; then
		GOPATH="$evener_default_gopath"
		export GOPATH
	fi
	if [ -n "$evener_default_gocache" ]; then
		GOCACHE="$evener_default_gocache"
		export GOCACHE
	fi
	GOENV="$evener_private_goenv"
	HOME="$evener_private_root/home"
	XDG_CONFIG_HOME="$evener_private_root/xdg-config"
	XDG_CACHE_HOME="$evener_private_root/xdg-cache"
	XDG_STATE_HOME="$evener_private_root/xdg-state"
	export GOENV HOME XDG_CONFIG_HOME XDG_CACHE_HOME XDG_STATE_HOME

	unset evener_private_root evener_ambient_home evener_ambient_xdg_config evener_ambient_xdg_cache evener_ambient_goenv
	unset evener_goenv_gopath evener_goenv_gocache evener_default_gopath evener_default_gocache evener_private_goenv
	unset evener_goenv_key evener_goenv_file
}
