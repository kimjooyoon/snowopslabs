#!/usr/bin/env bats
# SPDX-License-Identifier: Apache-2.0
#
# install.sh runs from a clone: it installs the labctl release named by the
# clone's LAB_VERSION, with no sudo, and records the clone so labctl finds it
# from any directory. Releases are served from a local directory through
# file:// URLs.

setup() {
  ROOT="$(cd "$(dirname "$BATS_TEST_FILENAME")/../../.." && pwd)"
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/snowops-install.XXXXXX")"
  export HOME="$WORK/home"
  mkdir -p "$HOME"
  export SNOWOPS_RELEASE_URL="file://$WORK/releases"
  unset SNOWOPS_VERSION
  OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
  ARCH="$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
  ARCHIVE="labctl_9.9.9_${OS}_${ARCH}.tar.gz"

  CLONE="$WORK/my lab"
  mkdir -p "$CLONE/scenarios" "$CLONE/runtimes" "$CLONE/config"
  CLONE="$(cd "$CLONE" && pwd)"
  cp "$ROOT/install.sh" "$CLONE/install.sh"
  echo "9.9.9" >"$CLONE/LAB_VERSION"
  echo "PROFILE=k3d" >"$CLONE/config/.env.example"
}

teardown() {
  rm -rf "$WORK"
}

# release <marker> publishes labctl 9.9.9, a script that prints marker.
release() {
  local stage="$WORK/stage" dir="$WORK/releases/download/v9.9.9"
  rm -rf "$stage" && mkdir -p "$stage" "$dir"
  printf '#!/bin/sh\necho labctl %s\n' "$1" >"$stage/labctl"
  chmod +x "$stage/labctl"
  tar -czf "$dir/$ARCHIVE" -C "$stage" .
  (cd "$dir" && { command -v sha256sum >/dev/null && sha256sum "$ARCHIVE" || shasum -a 256 "$ARCHIVE"; } >checksums.txt)
}

@test "installs the labctl of LAB_VERSION and records the clone" {
  release v1
  run sh "$CLONE/install.sh"
  [ "$status" -eq 0 ]
  [ "$("$HOME/.local/bin/labctl")" = "labctl v1" ]
  [ "$(cat "$HOME/.snowops/lab-dir")" = "$CLONE" ]
  [ -f "$CLONE/.env" ]
  [[ "$output" == *"labctl init"* ]] || false
}

@test "works when run from another directory" {
  release v1
  cd "$HOME"
  run sh "$CLONE/install.sh"
  [ "$status" -eq 0 ]
  [ "$(cat "$HOME/.snowops/lab-dir")" = "$CLONE" ]
}

@test "re-running keeps the user's .env" {
  release v1
  sh "$CLONE/install.sh" >/dev/null
  echo "CLUSTER_NAME=mine" >"$CLONE/.env"
  release v2
  run sh "$CLONE/install.sh"
  [ "$status" -eq 0 ]
  [ "$("$HOME/.local/bin/labctl")" = "labctl v2" ]
  [ "$(cat "$CLONE/.env")" = "CLUSTER_NAME=mine" ]
}

@test "development content is refused with the way forward" {
  echo "9.9.10-dev" >"$CLONE/LAB_VERSION"
  run sh "$CLONE/install.sh"
  [ "$status" -ne 0 ]
  [[ "$output" == *"development content"*"git switch stable"* ]] || false
}

@test "snowops_version overrides lab_version" {
  echo "9.9.10-dev" >"$CLONE/LAB_VERSION"
  release v1
  run env SNOWOPS_VERSION=v9.9.9 sh "$CLONE/install.sh"
  [ "$status" -eq 0 ]
  [ "$("$HOME/.local/bin/labctl")" = "labctl v1" ]
}

@test "outside a clone it says where to run it" {
  mkdir -p "$WORK/elsewhere" && cp "$ROOT/install.sh" "$WORK/elsewhere/"
  run sh "$WORK/elsewhere/install.sh"
  [ "$status" -ne 0 ]
  [[ "$output" == *"run install.sh from your snowopslabs clone"* ]] || false
}

@test "a corrupt download is refused and the current labctl is untouched" {
  release v1
  sh "$CLONE/install.sh" >/dev/null
  release v2
  echo "0000000000000000000000000000000000000000000000000000000000000000  $ARCHIVE" \
    >"$WORK/releases/download/v9.9.9/checksums.txt"
  run sh "$CLONE/install.sh"
  [ "$status" -ne 0 ]
  [[ "$output" == *"checksum mismatch"* ]] || false
  [ "$("$HOME/.local/bin/labctl")" = "labctl v1" ]
}

@test "a missing release names the URL it tried" {
  echo "0.0.1" >"$CLONE/LAB_VERSION"
  run sh "$CLONE/install.sh"
  [ "$status" -ne 0 ]
  [[ "$output" == *"could not download"*"v0.0.1"* ]] || false
}

@test "says how to put labctl on PATH when it is not" {
  release v1
  run env PATH="/usr/bin:/bin:/usr/sbin:/sbin" sh "$CLONE/install.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"is not on your PATH"* ]] || false
}

@test "the PATH hint names the zsh startup file and is one paste-safe command" {
  release v1
  run env PATH="/usr/bin:/bin:/usr/sbin:/sbin" SHELL="/bin/zsh" sh "$CLONE/install.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"$HOME/.zshrc"* ]] || false
  [[ "$output" == *"echo 'export PATH="*"' >>"* ]] || false
  [[ "$output" == *"&& source"* ]] || false
  # The printed command must be paste-safe: single quotes inside, so the
  # nested double quotes of the export line survive the echo.
  [[ "$output" != *'echo "export PATH='* ]] || false
}

@test "the PATH hint names the bash startup file for the platform" {
  release v1
  if [ "$OS" = darwin ]; then
    run env PATH="/usr/bin:/bin:/usr/sbin:/sbin" SHELL="/bin/bash" sh "$CLONE/install.sh"
    [ "$status" -eq 0 ]
    [[ "$output" == *"$HOME/.bash_profile"* ]] || false
  else
    run env PATH="/usr/bin:/bin:/usr/sbin:/sbin" SHELL="/bin/bash" sh "$CLONE/install.sh"
    [ "$status" -eq 0 ]
    [[ "$output" == *"$HOME/.bashrc"* ]] || false
  fi
}

@test "the printed PATH line keeps the home placeholder so it survives a moved home directory" {
  release v1
  run env PATH="/usr/bin:/bin:/usr/sbin:/sbin" SHELL="/bin/zsh" sh "$CLONE/install.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *'export PATH="$HOME/.local/bin:$PATH"'* ]] || false
}

@test "an unknown shell still gets a PATH line to add by hand" {
  release v1
  run env PATH="/usr/bin:/bin:/usr/sbin:/sbin" SHELL="/usr/bin/fish" sh "$CLONE/install.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"is not on your PATH"* ]] || false
  [[ "$output" == *"export PATH="* ]] || false
  [[ "$output" != *"source"* ]] || false
}

@test "the printed PATH command actually works" {
  release v1
  run env PATH="/usr/bin:/bin:/usr/sbin:/sbin" SHELL="/bin/zsh" sh "$CLONE/install.sh"
  [ "$status" -eq 0 ]
  # Take the exact line the installer printed and run it: it must create the
  # startup file when it is missing and leave PATH set in that shell.
  line="$(printf '%s\n' "$output" | grep -F 'export PATH=' | head -1 | sed 's/^[[:space:]]*//')"
  [ -n "$line" ]
  # `source` is what the printed command uses, so run it under bash the way a
  # user's zsh would run it.
  run bash -c "$line"
  [ "$status" -eq 0 ]
  [ -f "$HOME/.zshrc" ]
  run bash -c "source \"$HOME/.zshrc\"; printf '%s' \"\$PATH\""
  [[ "$output" == *"$HOME/.local/bin"* ]] || false
}
