#!/bin/bash
set -euo pipefail

# ============================================================================
# Overnight Ralph Loop Runner
#
# Runs each task file sequentially. Each task gets up to MAX_ITER attempts.
# Progress is tracked via git commits. If a task gets stuck, the script stops.
#
# Usage:
#   ./run-overnight.sh                    # Run all tasks
#   ./run-overnight.sh --from 03          # Resume from task 03
#   ./run-overnight.sh --only 05          # Run only task 05
#
# Prerequisites:
#   - Claude Code CLI installed (`claude` in PATH)
#   - Authenticated (run `claude` interactively first to log in)
#   - Go toolchain installed
#   - git configured
# ============================================================================

MAX_ITER=${MAX_ITER:-10}
TASKS_DIR="$(cd "$(dirname "$0")" && pwd)/tasks"
LOG_DIR="$(cd "$(dirname "$0")" && pwd)/logs"
FROM_TASK=""
ONLY_TASK=""

mkdir -p "$LOG_DIR"

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --from) FROM_TASK="$2"; shift 2 ;;
        --only) ONLY_TASK="$2"; shift 2 ;;
        --max-iter) MAX_ITER="$2"; shift 2 ;;
        *) echo "Unknown argument: $1"; exit 1 ;;
    esac
done

run_task() {
    local task_file="$1"
    local task_name
    task_name="$(basename "$task_file" .md)"
    local iter=0
    local log_file="$LOG_DIR/${task_name}.log"

    echo ""
    echo "================================================================"
    echo "  TASK: $task_name"
    echo "  Max iterations: $MAX_ITER"
    echo "  Log: $log_file"
    echo "  Started: $(date)"
    echo "================================================================"

    rm -f .milestone-complete

    while [ $iter -lt $MAX_ITER ] && [ ! -f .milestone-complete ]; do
        iter=$((iter + 1))
        echo ""
        echo "--- $task_name | Iteration $iter/$MAX_ITER | $(date) ---"

        # Run claude in headless mode, append output to log
        claude -p "$(cat "$task_file")" \
            --allowedTools "Bash,Read,Write,Edit,Glob,Grep" \
            2>&1 | tee -a "$log_file" || true

        # Brief pause between iterations
        sleep 2
    done

    if [ -f .milestone-complete ]; then
        echo ""
        echo "✓ COMPLETED: $task_name after $iter iteration(s)"
        rm .milestone-complete
        return 0
    else
        echo ""
        echo "✗ STUCK: $task_name did not complete after $MAX_ITER iterations"
        echo "  Check log: $log_file"
        return 1
    fi
}

# Collect task files in order
TASK_FILES=()
for f in "$TASKS_DIR"/*.md; do
    [ -f "$f" ] || continue
    TASK_FILES+=("$f")
done

if [ ${#TASK_FILES[@]} -eq 0 ]; then
    echo "No task files found in $TASKS_DIR/"
    exit 1
fi

echo "============================================================"
echo "  OVERNIGHT RUNNER"
echo "  Tasks: ${#TASK_FILES[@]}"
echo "  Max iterations per task: $MAX_ITER"
echo "  Started: $(date)"
echo "============================================================"

COMPLETED=0
FAILED=0
SKIPPED=0
STARTED=false

for task_file in "${TASK_FILES[@]}"; do
    task_num="$(basename "$task_file" .md | cut -d- -f1)"

    # Handle --only
    if [ -n "$ONLY_TASK" ] && [ "$task_num" != "$ONLY_TASK" ]; then
        continue
    fi

    # Handle --from
    if [ -n "$FROM_TASK" ] && [ "$STARTED" = false ]; then
        if [ "$task_num" != "$FROM_TASK" ]; then
            echo "Skipping $(basename "$task_file") (before --from $FROM_TASK)"
            SKIPPED=$((SKIPPED + 1))
            continue
        fi
        STARTED=true
    fi

    if run_task "$task_file"; then
        COMPLETED=$((COMPLETED + 1))
    else
        FAILED=$((FAILED + 1))
        echo ""
        echo "Stopping: task failed. Fix the issue and resume with:"
        echo "  ./run-overnight.sh --from $task_num"
        break
    fi
done

echo ""
echo "============================================================"
echo "  SUMMARY"
echo "  Completed: $COMPLETED"
echo "  Failed:    $FAILED"
echo "  Skipped:   $SKIPPED"
echo "  Finished:  $(date)"
echo "============================================================"

[ $FAILED -eq 0 ] || exit 1
