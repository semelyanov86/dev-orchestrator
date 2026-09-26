#!/usr/bin/env bash
set -euo pipefail
files=$(gofmt -l cmd internal prompts)
if [[ -n "$files" ]]; then
  printf 'Go files require formatting:\n%s\n' "$files"
  exit 1
fi
