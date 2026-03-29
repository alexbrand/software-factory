#!/bin/bash
set -euo pipefail

ERRORS=()
OUTPUT=""

# --- Go checks (only if Go files exist) ---
if find . -name '*.go' -print -quit | grep -q .; then
  VET_OUTPUT=$(go vet ./... 2>&1) || { ERRORS+=("go vet failed"); OUTPUT+="$VET_OUTPUT"$'\n'; }

  if command -v golangci-lint &> /dev/null; then
    LINT_OUTPUT=$(golangci-lint run --timeout 2m 2>&1) || { ERRORS+=("golangci-lint failed"); OUTPUT+="$LINT_OUTPUT"$'\n'; }
  fi

  BUILD_OUTPUT=$(go build ./... 2>&1) || { ERRORS+=("go build failed"); OUTPUT+="$BUILD_OUTPUT"$'\n'; }
fi

# --- Spec consistency checks ---
if git diff --cached --name-only | grep -q "^spec/"; then
  for f in spec/*.md; do
    grep -oP '\[.*?\]\(\K[^)]+' "$f" 2>/dev/null | grep -v '^http' | while read -r link; do
      target="spec/$link"
      if [ ! -f "$target" ]; then
        ERRORS+=("broken link: $f -> $link")
        OUTPUT+="Broken link in $f -> $link"$'\n'
      fi
    done
  done
fi

if [ ${#ERRORS[@]} -ne 0 ]; then
  SUMMARY=$(printf '  - %s\n' "${ERRORS[@]}")
  jq -n --arg reason "Pre-commit checks failed. Fix these issues before committing:
$SUMMARY
$OUTPUT" '{"continue": false, "stopReason": $reason}'
  exit 1
fi

echo '{"continue": true}'
