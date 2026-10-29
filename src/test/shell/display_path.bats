#!/usr/bin/env bats
# SPDX-License-Identifier: Apache-2.0

load 'helpers/stub'

setup() {
  ROOT="$(project_root)"
  HELPER="$ROOT/scripts/_lib/display.sh"
  HOME_ROOT="$BATS_TEST_TMPDIR/home"
  PROJECT_ROOT="$HOME_ROOT/snowops"
  mkdir -p "$PROJECT_ROOT" "$HOME_ROOT/elsewhere"
}

@test "display_path formats home and clone paths without changing execution paths" {
  run env HOME="$HOME_ROOT" PROJECT_ROOT="$PROJECT_ROOT" HELPER="$HELPER" bash -c '
    cd "$PROJECT_ROOT" || exit 1
    . "$HELPER"
    display_path "$PROJECT_ROOT/scenarios/demo/restore.sh"
    display_path "$HOME/.snowops/state/lab/backups/latest.json"
    display_path "/opt/snowops/keep-me"
  '
  [ "$status" -eq 0 ]
  [ "$output" = $'scenarios/demo/restore.sh\n~/.snowops/state/lab/backups/latest.json\n/opt/snowops/keep-me' ]
}

@test "display_path uses a shortened clone path outside the clone" {
  run env HOME="$HOME_ROOT" PROJECT_ROOT="$PROJECT_ROOT" HELPER="$HELPER" bash -c '
    cd "$HOME/elsewhere" || exit 1
    . "$HELPER"
    display_path "$PROJECT_ROOT/scenarios/demo/restore.sh"
  '
  [ "$status" -eq 0 ]
  [ "$output" = "~/snowops/scenarios/demo/restore.sh" ]
}

@test "display_path falls back to the script's own clone when PROJECT_ROOT is unset" {
  run env -u PROJECT_ROOT HOME="$HOME_ROOT" LAB_ROOT="$PROJECT_ROOT" HELPER="$HELPER" bash -c '
    cd "$LAB_ROOT" || exit 1
    . "$HELPER"
    display_path "$LAB_ROOT/scenarios/demo/restore.sh"
  '
  [ "$status" -eq 0 ]
  [ "$output" = "scenarios/demo/restore.sh" ]
}
