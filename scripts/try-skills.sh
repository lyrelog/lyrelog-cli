#!/bin/bash
# Try `lyrelog skills install` from this checkout without touching your real agents' folders or
# settings: a throwaway home with the same agent folders as yours (empty), and its own config.
# Run it with no arguments, or pass any install flags (--choose, --project, --agent cursor...).
set -euo pipefail
cli="$(cd "$(dirname "$0")/.." && pwd)/bin/lyrelog-dev"
sandbox="$(mktemp -d -t lyrelog-skills-try)"
for d in .claude .codex .agents .cursor .gemini .copilot .config/opencode .codeium/windsurf .config/amp; do
  [ -d "$HOME/$d" ] && mkdir -p "$sandbox/home/$d"
done
mkdir -p "$sandbox/config"
echo "Sandbox: $sandbox (your real folders aren't touched)"
echo
HOME="$sandbox/home" LYRELOG_CONFIG_DIR="$sandbox/config" "$cli" skills install "$@"
echo
echo "What got written:"
(cd "$sandbox/home" && find . -name SKILL.md | sort)
echo
echo "Saved choice:"
cat "$sandbox/config/config.json" 2>/dev/null || echo "(none)"
echo
echo "Run it again to see it remember: HOME=$sandbox/home LYRELOG_CONFIG_DIR=$sandbox/config $cli skills install"
