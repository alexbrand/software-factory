#!/bin/bash
set -euo pipefail

ERRORS=()

# --- Go tests (only if Go files exist) ---
if find . -name '*.go' -print -quit | grep -q .; then
  if ! go test ./... 2>&1; then
    ERRORS+=("go test failed")
  fi
fi

# --- Spec completeness checks ---
# Ensure all spec files referenced in README.md exist
for f in spec/*.md; do
  basename=$(basename "$f")
  if [ "$basename" = "README.md" ]; then
    continue
  fi
  if ! grep -q "$basename" spec/README.md; then
    echo "WARNING: $basename not listed in spec/README.md"
    ERRORS+=("$basename missing from spec/README.md")
  fi
done

# Verify CRD kind names are consistent between spec 02 and spec 04
if [ -f spec/02-concepts-and-terminology.md ] && [ -f spec/04-control-plane.md ]; then
  for kind in Pool Sandbox AgentConfig Workflow Task Session; do
    if grep -q "$kind" spec/02-concepts-and-terminology.md && ! grep -q "$kind" spec/04-control-plane.md; then
      echo "WARNING: $kind defined in spec 02 but missing from spec 04"
      ERRORS+=("CRD $kind inconsistency between spec 02 and spec 04")
    fi
  done
fi

if [ ${#ERRORS[@]} -ne 0 ]; then
  echo ""
  echo "Pre-push checks failed:"
  printf '  - %s\n' "${ERRORS[@]}"
  exit 1
fi

echo "Pre-push checks passed."
