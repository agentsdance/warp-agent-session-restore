#!/bin/sh
set -eu

fail() {
    printf 'Installation failed: %s\n' "$*" >&2
    exit 1
}

download() {
    curl -fsSL --proto '=https' --tlsv1.2 --connect-timeout 15 --retry 2 "$@"
}

main() {
    bin_dir=/usr/local/bin
    install_home=
    no_service=false
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --bin-dir|--home)
                [ "$#" -ge 2 ] && [ -n "$2" ] || fail "$1 needs a directory."
                case "$2" in
                    /*) ;;
                    *) fail "$1 needs an absolute directory path." ;;
                esac
                if [ "$1" = --bin-dir ]; then
                    bin_dir=$2
                else
                    install_home=$2
                fi
                shift 2
                ;;
            --no-service) no_service=true; shift ;;
            -h|--help)
                printf '%s\n' \
                    'Install Warp Agent Session Restore for macOS.' \
                    'Usage: sh install.sh [--bin-dir DIR] [--no-service] [--home DIR]' \
                    'Both warp-agent-session-restore and warp-restore are installed in /usr/local/bin by default.' \
                    'Run as your normal user; sudo is requested only if the command directory requires it.' \
                    'Set WARP_RESTORE_VERSION to install a specific release, such as v0.0.1.'
                exit 0
                ;;
            *) fail "Unknown option: $1. Use --help for usage." ;;
        esac
    done

    [ "$(uname -s)" = Darwin ] || fail 'This app supports macOS only.'
    case "$(uname -m)" in
        arm64|aarch64) architecture=arm64 ;;
        x86_64|amd64) architecture=amd64 ;;
        *) fail 'This Mac has an unsupported processor. Apple Silicon and Intel Macs are supported.' ;;
    esac
    for dependency in curl shasum awk mktemp; do
        command -v "$dependency" >/dev/null 2>&1 || fail "Required macOS command not found: $dependency."
    done

    releases_url=https://github.com/agentsdance/warp-agent-session-restore/releases
    release_tag=${WARP_RESTORE_VERSION:-}
    if [ -z "$release_tag" ]; then
        printf '%s\n' 'Finding the latest Warp Agent Session Restore release...'
        latest_url=$(download --head --output /dev/null --write-out '%{url_effective}' "$releases_url/latest") ||
            fail "Could not find a downloadable release. Check your connection and $releases_url."
        case "$latest_url" in
            "$releases_url"/tag/v*) release_tag=${latest_url##*/} ;;
            *) fail "No published version was found. Check $releases_url." ;;
        esac
    fi
    case "$release_tag" in
        v[0-9]*) ;;
        *) fail 'Release versions must start with v and a number, such as v0.0.1.' ;;
    esac
    case "$release_tag" in
        *[!a-zA-Z0-9._-]*) fail "Invalid release version: $release_tag." ;;
    esac

    umask 077
    download_dir=$(mktemp -d "${TMPDIR:-/tmp}/warp-agent-session-restore.XXXXXX") ||
        fail 'Could not create a temporary download directory.'
    trap 'rm -rf "$download_dir"' 0
    trap 'exit 1' 1 2 15

    asset=warp-agent-session-restore-darwin-$architecture
    binary=$download_dir/$asset
    download_url=$releases_url/download/$release_tag
    printf 'Downloading %s for macOS (%s)...\n' "$release_tag" "$architecture"
    download --output "$binary" "$download_url/$asset" ||
        fail "Could not download $release_tag for this Mac. Check $releases_url."
    download --output "$download_dir/SHA256SUMS" "$download_url/SHA256SUMS" ||
        fail 'Could not download the release checksums. Please try again.'

    expected=$(awk -v name="$asset" '$2 == name { print $1; count++ } END { if (count != 1) exit 1 }' "$download_dir/SHA256SUMS") ||
        fail 'The release is missing a checksum for this Mac.'
    case "$expected" in
        ''|*[!0-9a-f]*) fail 'The release checksum is invalid.' ;;
    esac
    [ "${#expected}" -eq 64 ] || fail 'The release checksum is invalid.'
    actual=$(shasum -a 256 "$binary") || fail 'Could not verify the download.'
    actual=${actual%% *}
    [ "$actual" = "$expected" ] || fail 'The download checksum did not match. Please try again.'

    command_path=$bin_dir/warp-agent-session-restore
    if [ -L "$command_path" ]; then
        fail "$command_path is a symbolic link. Choose another --bin-dir or remove the link before installing."
    fi
    if [ -e "$command_path" ] && [ ! -f "$command_path" ]; then
        fail "$command_path is not a regular file. Choose another --bin-dir."
    fi
    alias_path=$bin_dir/warp-restore
    if [ -L "$alias_path" ]; then
        alias_target=$(readlink "$alias_path") || fail "Could not read $alias_path."
        case "$alias_target" in
            warp-agent-session-restore|"$command_path") ;;
            *) fail "$alias_path already points to another command. Choose another --bin-dir or remove the link before installing." ;;
        esac
    elif [ -e "$alias_path" ]; then
        fail "$alias_path already exists. Choose another --bin-dir or move it before installing."
    fi
    printf 'Installing command at %s...\n' "$command_path"
    needs_sudo=false
    if mkdir -p -m 755 "$bin_dir" 2>/dev/null && [ -w "$bin_dir" ]; then
        /usr/bin/install -m 755 "$binary" "$command_path" || fail "Could not install the command at $command_path."
    else
        needs_sudo=true
        printf '%s\n' 'macOS may ask for your password to install the command.'
        /usr/bin/sudo /usr/bin/install -d -m 755 "$bin_dir" || fail "Could not create $bin_dir. Try --bin-dir \"\$HOME/.local/bin\"."
        /usr/bin/sudo /usr/bin/install -m 755 "$binary" "$command_path" || fail "Could not install the command at $command_path."
    fi
    if [ ! -L "$alias_path" ]; then
        if [ "$needs_sudo" = true ]; then
            /usr/bin/sudo /bin/ln -s warp-agent-session-restore "$alias_path" || fail "Could not create $alias_path."
        else
            ln -s warp-agent-session-restore "$alias_path" || fail "Could not create $alias_path."
        fi
    fi

    set -- install
    if [ "$no_service" = true ]; then
        set -- "$@" --no-service
    fi
    if [ -n "$install_home" ]; then
        set -- --home "$install_home" "$@"
    fi
    "$command_path" "$@"
    printf '\nCommands installed:\n  %s\n  %s\n' "$command_path" "$alias_path"
    case ":$PATH:" in
        *":$bin_dir:"*) printf '%s\n' 'Run warp-restore status to check saved sessions. Both command names work.' ;;
        *) printf 'Add %s to your PATH to run warp-restore or warp-agent-session-restore by name.\n' "$bin_dir" ;;
    esac
}

main "$@"
