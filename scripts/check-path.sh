#!/usr/bin/env bash
set -euo pipefail
case ":$PATH:" in
  *":$HOME/.local/bin:"*) ;;
  *) printf '%s\n' 'Добавьте в shell profile: export PATH="$HOME/.local/bin:$PATH"' ;;
esac
