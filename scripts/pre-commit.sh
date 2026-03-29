#!/bin/bash
set -euo pipefail

ERRORS=()

# --- Go checks (only if Go files exist) ---
if compgen -G "**/*.go" > /dev/null 2>&1; then
  if ! go vet ./... 2>&1; then
    ERRORS+=("go vet failed")
  fi

  if command -v golangci-lint &> /dev/null; then
    if ! golangci-lint run --timeout 2m 2>&1; then
      ERRORS+=("golangci-lint failed")
    fi
  fi

  if ! go build ./... 2>&1; then
    ERRORS+=("go build failed")
  fi
fi

# --- Spec consistency checks ---
if git diff --cached --name-only | grep -q "^spec/"; then
  # Check for stale HarnessConfig references
  STALE=$(grep -rn "HarnessConfig\|harnessRef\|harness_config" spec/ 2>/dev/null || true)
  if [ -n "$STALE" ]; then
    echo "WARNING: Found stale harness terminology in spec files:"
    echo "$STALE"
    ERRORS+=("stale harness terminology found in spec")
  fi

  # Check for broken internal links
  for f in spec/*.md; do
    # Extract markdown links to local files
    grep -oP '\[.*?\]\(\K[^)]+' "$f" 2>/dev/null | grep -v '^http' | while read -r link; do
      target="spec/$link"
      if [ ! -f "$target" ]; then
        echo "WARNING: Broken link in $f -> $link"
        ERRORS+=("broken link: $f -> $link")
      fi
    done
  done
fi

if [ ${#ERRORS[@]} -ne 0 ]; then
  echo ""
  echo "Pre-commit checks failed:"
  printf '  - %s\n' "${ERRORS[@]}"
  exit 1
fi

echo "Pre-commit checks passed."
