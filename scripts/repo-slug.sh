#!/usr/bin/env bash
set -euo pipefail

remote="$(git remote get-url origin)"
slug="$(printf '%s\n' "$remote" | sed -E 's#^.*github\.com[:/]##; s#\.git$##')"

if [[ -z "$slug" ]]; then
  echo "failed to determine GitHub repo slug from origin remote" >&2
  exit 1
fi

printf '%s\n' "$slug"
