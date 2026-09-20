#!/bin/sh
#
# Install nsctl, the local NeuronSphere CLI.
#
#   curl -fsSL https://raw.githubusercontent.com/neuronsphere/hmd-cli-neuronsphere/main/install.sh | sh
#
# Environment:
#   NSCTL_VERSION   version to install, e.g. 1.0.203 (default: the latest release).
#                   Release tags carry the BACON build number and no "v" prefix.
#   PREFIX          install directory (default: ~/.local/bin, as `make install`)
#
# POSIX sh on purpose: this runs before anything is installed, on whatever
# /bin/sh the machine has.
set -eu

REPO="neuronsphere/hmd-cli-neuronsphere"
BINARY="nsctl"

die() {
    echo "install: $*" >&2
    exit 1
}

# detect_platform prints "<os>/<arch>" for the four targets that are actually
# released, and refuses everything else by name rather than by 404.
detect_platform() {
    os=$(uname -s)
    arch=$(uname -m)

    case "$os" in
        Darwin) os=darwin ;;
        Linux)  os=linux ;;
        MINGW*|MSYS*|CYGWIN*|Windows_NT)
            die "Windows is not a supported target. nsctl drives Docker through
     /var/run/docker.sock and composes host paths that sibling containers must
     resolve identically, neither of which has a Windows equivalent. Install it
     inside WSL2, where it is an ordinary linux/amd64 binary."
            ;;
        *) die "unsupported operating system: $os (nsctl releases darwin and linux)" ;;
    esac

    case "$arch" in
        x86_64|amd64)  arch=amd64 ;;
        arm64|aarch64) arch=arm64 ;;
        *) die "unsupported architecture: $arch (nsctl releases amd64 and arm64)" ;;
    esac

    echo "$os/$arch"
}

# latest_version asks GitHub which release is current.
latest_version() {
    curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
        sed -n 's/.*"tag_name" *: *"v\{0,1\}\([^"]*\)".*/\1/p' |
        head -n 1
}

# verify_checksum refuses an archive whose digest is not the one the release
# published. A partial download that unpacks is worse than one that fails.
verify_checksum() {
    archive=$1
    sums=$2

    expected=$(awk -v name="$(basename "$archive")" '$2 == name || $2 == "*" name { print $1 }' "$sums")
    [ -n "$expected" ] || die "$(basename "$archive") is not listed in checksums.txt"

    if command -v sha256sum >/dev/null 2>&1; then
        actual=$(sha256sum "$archive" | cut -d' ' -f1)
    elif command -v shasum >/dev/null 2>&1; then
        actual=$(shasum -a 256 "$archive" | cut -d' ' -f1)
    else
        echo "install: neither sha256sum nor shasum found; skipping verification" >&2
        return 0
    fi

    [ "$actual" = "$expected" ] ||
        die "checksum mismatch for $(basename "$archive"): got $actual, expected $expected"
}

main() {
    command -v curl >/dev/null 2>&1 || die "curl is required"
    command -v tar >/dev/null 2>&1 || die "tar is required"

    platform=$(detect_platform)
    os=${platform%/*}
    arch=${platform#*/}

    version=${NSCTL_VERSION:-}
    if [ -z "$version" ]; then
        version=$(latest_version) || true
        [ -n "$version" ] || die "could not determine the latest release; set NSCTL_VERSION"
    fi
    version=${version#v}

    prefix=${PREFIX:-$HOME/.local/bin}
    archive="${BINARY}_${version}_${os}_${arch}.tar.gz"
    base="https://github.com/$REPO/releases/download/$version"

    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT INT TERM

    echo "Downloading $BINARY $version for $os/$arch..."
    curl -fsSL -o "$tmp/$archive" "$base/$archive" ||
        die "no release asset $archive at $base"
    curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" ||
        die "could not download checksums.txt from $base"

    verify_checksum "$tmp/$archive" "$tmp/checksums.txt"

    tar -xzf "$tmp/$archive" -C "$tmp"
    [ -f "$tmp/$BINARY" ] || die "$archive does not contain $BINARY"

    mkdir -p "$prefix"
    install -m 0755 "$tmp/$BINARY" "$prefix/$BINARY" 2>/dev/null ||
        die "could not write to $prefix. Set PREFIX to somewhere writable, e.g. PREFIX=\$HOME/bin"

    echo "Installed $prefix/$BINARY"
    case ":$PATH:" in
        *":$prefix:"*) ;;
        *) echo "Add it to your PATH:  export PATH=\"$prefix:\$PATH\"" ;;
    esac
    "$prefix/$BINARY" version
}

# Guarded so the platform detection above can be sourced and exercised without
# downloading anything.
if [ -z "${NSCTL_INSTALL_SOURCED:-}" ]; then
    main "$@"
fi
