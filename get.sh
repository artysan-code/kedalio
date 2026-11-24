#!/bin/sh
# get.sh — install Kedalio from a release, in one line:
#
#   curl -fsSL https://kedalio.com/get.sh | sh
#
# To read it before it runs (recommended): download it, read it, then run it.
#
#   curl -fsSL https://kedalio.com/get.sh -o get.sh
#   less get.sh
#   sh get.sh
#
# What it does, by mode:
#   this computer (default)  downloads kedalio-<os>-<arch>.tar.gz and SHA256SUMS from the release, checks the archive
#                            against the sum, and puts kedalio-core and kedalio-restore in ~/.local/bin (no sudo, ever).
#                            Then, if Claude Code is installed, offers its plugin.
#   --server URL             meant for the server itself (needs docker compose): in ~/kedalio it downloads the
#                            release's compose.yaml, turns on the public URL and writes .env with a new random token.
#                            It starts nothing unless --start is given, and it never prints the token.
#   --uninstall              removes the two binaries; the data folder is kept.
#
# It never touches an existing data folder, never overwrites .env or compose.yaml, and never reads first-run.txt.
#
# Options: --server URL, --start, --no-plugin, --version V, --uninstall, --help.
# Environment: KEDALIO_VERSION (same as --version), KEDALIO_BASE_URL (where the release files are; default the latest
# GitHub release), KEDALIO_BIN_DIR (default ~/.local/bin), KEDALIO_DIR (server folder, default ~/kedalio),
# KEDALIO_LANGUAGE (English or Italian) and KEDALIO_PLUGIN (yes or no) answer the plugin questions when there is no
# terminal to ask on (the defaults are English and no).
# For tests only: KEDALIO_TEST_UNAME_S and KEDALIO_TEST_UNAME_M replace uname -s and uname -m.
main() {
  set -eu

  REPO=artysan-code/kedalio

  say() { printf '%s\n' "$*"; }
  die() {
    printf 'get.sh: %s\n' "$*" >&2
    exit 1
  }

  usage() {
    cat <<'EOF'
Install Kedalio from a release.

Usage: sh get.sh [options]

  (no option)       install kedalio-core and kedalio-restore on this computer
  --server URL      set up a server in ~/kedalio (run it on the server; needs docker compose);
                    URL is the public https address, for example https://kedalio.example.com
  --start           with --server: also run `docker compose up -d`
  --no-plugin       do not offer the Claude Code plugin
  --version V       install release V (same as KEDALIO_VERSION), for example v1.0.0
  --uninstall       remove the two binaries (the data folder is kept)
  --help            this text

Environment: KEDALIO_VERSION, KEDALIO_BASE_URL, KEDALIO_BIN_DIR, KEDALIO_DIR,
KEDALIO_LANGUAGE (English or Italian) and KEDALIO_PLUGIN (yes or no) for runs with no terminal.
EOF
  }

  server=""
  start=0
  plugin=1
  uninstall=0
  version="${KEDALIO_VERSION:-}"

  while [ $# -gt 0 ]; do
    case "$1" in
      --help | -h)
        usage
        exit 0
        ;;
      --server)
        [ $# -ge 2 ] || die "--server needs the public URL, for example https://kedalio.example.com"
        server="$2"
        shift
        ;;
      --start) start=1 ;;
      --no-plugin) plugin=0 ;;
      --version)
        [ $# -ge 2 ] || die "--version needs a value, for example v1.0.0"
        version="$2"
        shift
        ;;
      --uninstall) uninstall=1 ;;
      *) die "unknown option: $1 (see --help)" ;;
    esac
    shift
  done

  HOME_DIR="${HOME:-}"
  [ -n "$HOME_DIR" ] || die "HOME is not set"
  bin_dir="${KEDALIO_BIN_DIR:-$HOME_DIR/.local/bin}"

  if [ -n "$version" ]; then
    base="${KEDALIO_BASE_URL:-https://github.com/$REPO/releases/download/$version}"
  else
    base="${KEDALIO_BASE_URL:-https://github.com/$REPO/releases/latest/download}"
  fi

  os_name="${KEDALIO_TEST_UNAME_S:-$(uname -s)}"
  case "$os_name" in
    Darwin) data_hint="$HOME_DIR/Library/Application Support/Kedalio" ;;
    *) data_hint="$HOME_DIR/.local/share/kedalio" ;;
  esac

  if [ "$uninstall" = 1 ]; then
    for f in kedalio-core kedalio-restore; do
      if [ -e "$bin_dir/$f" ]; then
        rm -f "$bin_dir/$f"
        say "removed $bin_dir/$f"
      else
        say "$bin_dir/$f is not there"
      fi
    done
    say "The data folder is kept, with your memory and the recovery file: $data_hint"
    exit 0
  fi

  command -v curl >/dev/null 2>&1 || die "curl is needed"

  tmp=$(mktemp -d "${TMPDIR:-/tmp}/kedalio-get.XXXXXX") || die "cannot make a temporary folder"
  trap 'rm -rf "$tmp"' EXIT
  trap 'exit 1' HUP INT TERM

  # https only, redirects included; the tests serve the release over http on loopback and set KEDALIO_BASE_URL, so
  # plain http is allowed for a loopback host only
  proto="--proto =https --proto-redir =https"
  case "${KEDALIO_BASE_URL:-}" in
    http://localhost | http://localhost[:/]* | http://127.0.0.1 | http://127.0.0.1[:/]* | http://\[::1\] | http://\[::1\][:/]*) proto="" ;;
  esac

  # fetch NAME: download $base/NAME into $tmp/NAME
  fetch() {
    code=$(curl -sSL $proto -o "$tmp/$1" -w '%{http_code}' "$base/$1") || die "could not download $base/$1"
    case "$code" in
      404)
        die "no Kedalio release at $base/$1 (404). Build it from a clone instead: see the README."
        ;;
      4* | 5*) die "download of $base/$1 failed with HTTP $code" ;;
    esac
  }

  # verify FILE: $tmp/FILE must match its line in $tmp/SHA256SUMS; no line, no match, no sha tool: stop
  verify() {
    want=$(awk -v n="$1" '$2 == n || $2 == "*" n { print $1; exit }' "$tmp/SHA256SUMS")
    [ -n "$want" ] || die "SHA256SUMS has no line for $1; nothing was installed"
    if command -v sha256sum >/dev/null 2>&1; then
      have=$(sha256sum "$tmp/$1" | awk '{ print $1 }')
    elif command -v shasum >/dev/null 2>&1; then
      have=$(shasum -a 256 "$tmp/$1" | awk '{ print $1 }')
    else
      die "sha256sum or shasum is needed to check the download"
    fi
    [ "$have" = "$want" ] || die "the checksum of $1 does not match SHA256SUMS; nothing was installed"
  }

  # ---- server mode ----------------------------------------------------------------------------------------------------
  if [ -n "$server" ]; then
    case "$server" in
      https://?*) ;;
      *) die "--server needs an https address, for example https://kedalio.example.com" ;;
    esac
    server="${server%/}"
    dir="${KEDALIO_DIR:-$HOME_DIR/kedalio}"
    # -L: a dangling symlink is not "absent", writing through it would put the file somewhere else
    if [ -e "$dir/.env" ] || [ -L "$dir/.env" ] || [ -e "$dir/compose.yaml" ] || [ -L "$dir/compose.yaml" ]; then
      die "$dir already has a .env or a compose.yaml, so nothing was changed. To upgrade it, follow docs/upgrade.md: https://github.com/$REPO/blob/HEAD/docs/upgrade.md"
    fi
    if [ "$start" = 1 ]; then
      if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
        die "--start needs docker with the compose plugin"
      fi
    fi

    fetch compose.yaml
    fetch SHA256SUMS
    verify compose.yaml
    grep -q '# - KEDALIO_CORE_PUBLIC_URL=' "$tmp/compose.yaml" || die "the release's compose.yaml has no KEDALIO_CORE_PUBLIC_URL line to enable"

    # the token never goes to the screen, a variable that is exported or an argument
    if command -v openssl >/dev/null 2>&1; then
      token=$(openssl rand -hex 32)
    else
      token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
    fi
    [ "${#token}" = 64 ] || die "could not make a random token"

    mkdir -p "$dir"
    umask 077
    sed 's|# - KEDALIO_CORE_PUBLIC_URL=|- KEDALIO_CORE_PUBLIC_URL=|' "$tmp/compose.yaml" >"$dir/compose.yaml"
    printf 'KEDALIO_CORE_TOKEN=%s\nKEDALIO_CORE_PUBLIC_URL=%s\n' "$token" "$server" >"$dir/.env"
    token=""
    chmod 600 "$dir/.env"

    say "Wrote $dir/compose.yaml (public URL on) and $dir/.env (new token, readable only by you)."
    if [ "$start" = 1 ]; then
      (cd "$dir" && docker compose up -d </dev/null) || die "docker compose up failed; the files in $dir are kept"
      say "Started. Check it: curl -fsS $server/health"
    else
      say "Start it with:"
      say "  cd \"$dir\" && docker compose up -d"
      say "Then check it: curl -fsS $server/health"
    fi
    say "The port stays on 127.0.0.1: put your proxy or tunnel with TLS in front of it."
    say "The first start writes first-run.txt (the recovery code) inside the container. Open it"
    say "yourself, in your own terminal, copy the code to a password manager, then delete the file:"
    say "  cd \"$dir\" && docker compose exec core cat /data/core/first-run.txt"
    exit 0
  fi

  # ---- this computer --------------------------------------------------------------------------------------------------
  arch_name="${KEDALIO_TEST_UNAME_M:-$(uname -m)}"
  case "$os_name" in
    Linux) os=linux ;; # WSL reports Linux: the data then lives inside WSL
    Darwin) os=darwin ;;
    *) die "no released binary for $os_name. Build from source: see the README (go build ./core/cmd/kedalio-core)." ;;
  esac
  case "$arch_name" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) die "no released binary for $arch_name. Build from source: see the README (go build ./core/cmd/kedalio-core)." ;;
  esac
  # an Intel-reported shell on Apple silicon (Rosetta) would fetch the wrong binary; sysctl says what the machine is
  if [ "$os" = darwin ] && [ -z "${KEDALIO_TEST_UNAME_M:-}" ] && [ "$arch" = amd64 ] &&
    [ "$(sysctl -n hw.optional.arm64 2>/dev/null || true)" = 1 ]; then
    arch=arm64
  fi

  archive="kedalio-$os-$arch.tar.gz"
  say "Downloading $archive"
  fetch "$archive"
  fetch SHA256SUMS

  verify "$archive"
  say "Checksum ok"

  mkdir "$tmp/x"
  tar -xzf "$tmp/$archive" -C "$tmp/x" || die "could not unpack $archive"
  mkdir -p "$bin_dir"
  for f in kedalio-core kedalio-restore; do
    src=$(find "$tmp/x" -type f -name "$f" | head -n 1)
    [ -n "$src" ] || die "$archive has no $f; nothing more was installed"
    cp "$src" "$bin_dir/$f.new"
    chmod 755 "$bin_dir/$f.new"
    mv -f "$bin_dir/$f.new" "$bin_dir/$f"
  done
  say "Installed kedalio-core and kedalio-restore in $bin_dir"
  "$bin_dir/kedalio-core" -version || say "(could not run kedalio-core -version on this system)"

  case ":${PATH:-}:" in
    *":$bin_dir:"*) ;;
    *) say "Note: $bin_dir is not on your PATH. Add it to your shell profile to run kedalio-core by name." ;;
  esac

  if [ "$plugin" = 0 ]; then
    say "Your data stays in $data_hint (created on the first start, never touched here)."
    exit 0
  fi

  # ---- the Claude Code plugin -----------------------------------------------------------------------------------------
  core="$bin_dir/kedalio-core"
  case "$core" in
    /*) ;;
    *) core="$(pwd)/$core" ;;
  esac

  if ! command -v claude >/dev/null 2>&1; then
    say ""
    say "Claude Code is not installed here. When it is, connect Kedalio with:"
    say "  claude plugin marketplace add $REPO"
    say "  claude plugin install kedalio-local@kedalio --config core_path=\"$core\" --config language=English"
    exit 0
  fi

  # the terminal, when there is one, even if the script itself comes from a pipe
  tty=0
  if (: </dev/tty) 2>/dev/null; then tty=1; fi

  # ask QUESTION: prints the answer read from the terminal
  ask() {
    printf '%s ' "$1" >/dev/tty
    ans=""
    read -r ans </dev/tty || ans=""
    printf '%s' "$ans"
  }

  lang="${KEDALIO_LANGUAGE:-}"
  if [ -z "$lang" ] && [ "$tty" = 1 ]; then
    lang=$(ask "Language, English or Italian? [English]")
  fi
  case "$lang" in
    [Ii]talian | [Ii]t | [Ii]taliano | 2) lang=Italian ;;
    *) lang=English ;;
  esac

  want_plugin="${KEDALIO_PLUGIN:-}"
  if [ -z "$want_plugin" ] && [ "$tty" = 1 ]; then
    want_plugin=$(ask "Install the Claude Code plugin (connection and skills)? [y/N]")
  fi
  case "$want_plugin" in
    [Yy] | [Yy][Ee][Ss]) ;;
    *)
      say ""
      say "Claude Code plugin not installed. To add it later:"
      say "  claude plugin marketplace add $REPO"
      say "  claude plugin install kedalio-local@kedalio --config core_path=\"$core\" --config language=$lang"
      exit 0
      ;;
  esac

  claude plugin marketplace add "$REPO" </dev/null || die "claude plugin marketplace add failed (is the repository reachable?)"
  claude plugin install kedalio-local@kedalio --config "core_path=$core" --config "language=$lang" </dev/null ||
    die "claude plugin install failed"
  say "Plugin installed. Start Claude Code: the first start creates the database in $data_hint, and the recovery code"
  say "goes to first-run.txt there. Open that file yourself, copy the code to a password manager, then delete it."
}

main "$@"
