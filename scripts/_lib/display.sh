#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0

# display_path formats a path for learner-facing output while callers keep the
# original path for file access and command execution.
display_path() {
  display_value=$1
  display_root=${PROJECT_ROOT:-${LAB_ROOT:-}}
  display_home=${HOME:-}

  if [ -n "$display_root" ] && [ "${PWD:-}" = "$display_root" ]; then
    case "$display_value" in
      "$display_root")
        printf '%s\n' "."
        return 0
        ;;
      "$display_root"/*)
        printf '%s\n' "${display_value#"$display_root"/}"
        return 0
        ;;
    esac
  fi

  if [ -n "$display_home" ]; then
    case "$display_value" in
      "$display_home")
        printf '%s\n' "~"
        return 0
        ;;
      "$display_home"/*)
        printf '%s/%s\n' '~' "${display_value#"$display_home"/}"
        return 0
        ;;
    esac
  fi
  printf '%s\n' "$display_value"
}
