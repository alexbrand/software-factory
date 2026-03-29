#!/bin/bash
set -euo pipefail

ERRORS=()
OUTPUT=""

# --- Go tests (only if Go files exist) ---
if find . -name '*.go' -print -quit | grep -q .; then
  TEST_OUTPUT=$(go test ./... 2>&1) || { ERRORS+=("go test failed"); OUTPUT+="$TEST_OUTPUT"$'\n'; }
fi

# --- Spec completeness checks ---
for f in spec/*.md; do
  basename=$(basename "$f")
  if [ "$basename" = "README.md" ]; then
    continue
  fi
  if ! grep -q "$basename" spec/README.md; then
    ERRORS+=("$basename missing from spec/README.md")
    OUTPUT+="$basename is not listed in spec/README.md"$'\n'
  fi
done

# Verify CRD kind names are consistent between spec 02 and spec 04
if [ -f spec/02-concepts-and-terminology.md ] && [ -f spec/04-control-plane.md ]; then
  for kind in Pool Sandbox AgentConfig Workflow Task Session; do
    if grep -q "$kind" spec/02-concepts-and-terminology.md && ! grep -q "$kind" spec/04-control-plane.md; then
      ERRORS+=("CRD $kind inconsistency between spec 02 and spec 04")
      OUTPUT+="$kind defined in spec 02 but missing from spec 04"$'\n'
    fi
  done
fi

if [ ${#ERRORS[@]} -ne 0 ]; then
  SUMMARY=$(printf '  - %s\n' "${ERRORS[@]}")
  jq -n --arg reason "Pre-push checks failed. Fix these issues before pushing:
$SUMMARY
$OUTPUT" '{"continue": false, "stopReason": $reason}'
  exit 1
fi

echo '{"continue": true}'
