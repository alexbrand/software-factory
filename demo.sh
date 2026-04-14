#!/usr/bin/env bash
# Software Factory Demo Script
# Demonstrates: task execution, interactive sessions, SSE streaming, error handling
set -euo pipefail

API="http://apiserver:8080"
NS="factory-system"

run() {
  local desc="$1"; shift
  echo ""
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo "  $desc"
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo ""
  "$@"
  echo ""
}

api() {
  kubectl run "demo-$(date +%s)" --rm -it --image=curlimages/curl --restart=Never -n "$NS" -- \
    curl -s --max-time "${TIMEOUT:-30}" "$@" 2>&1 | grep -v "^Unable\|^All commands\|^If you\|^warning\|^pod "
}

api_bg() {
  kubectl run "demo-bg-$(date +%s)" --rm -it --image=curlimages/curl --restart=Never -n "$NS" -- \
    curl -s --max-time "${TIMEOUT:-120}" "$@" 2>&1 | grep -v "^Unable\|^All commands\|^If you\|^warning\|^pod " &
  sleep 2
}

sandbox_exec() {
  local pod
  pod=$(kubectl get pod -n demo -l factory.example.com/pool=coding-pool -o jsonpath='{.items[0].metadata.name}')
  kubectl exec "$pod" -n demo -c sandbox-agent-sdk -- "$@" 2>&1
}

wait_task() {
  local name="$1" target="$2" timeout="${3:-60}"
  local deadline=$((SECONDS + timeout))
  while [ $SECONDS -lt $deadline ]; do
    phase=$(api -X GET "$API/v1/tasks/$name" | python3 -c "import json,sys; print(json.load(sys.stdin).get('phase',''))" 2>/dev/null || echo "")
    if [ "$phase" = "$target" ]; then
      echo "  ✓ Task '$name' reached phase: $target"
      return 0
    fi
    sleep 2
  done
  echo "  ✗ Timeout waiting for task '$name' to reach $target (currently: $phase)"
  return 1
}

wait_session() {
  local name="$1" target="$2" timeout="${3:-30}"
  local deadline=$((SECONDS + timeout))
  while [ $SECONDS -lt $deadline ]; do
    phase=$(kubectl get session "$name" -n demo -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    if [ "$phase" = "$target" ]; then
      echo "  ✓ Session '$name' reached phase: $target"
      return 0
    fi
    sleep 2
  done
  echo "  ✗ Timeout waiting for session '$name' to reach $target (currently: $phase)"
  return 1
}

echo "╔══════════════════════════════════════════════════════╗"
echo "║         SOFTWARE FACTORY — LIVE DEMO                ║"
echo "╠══════════════════════════════════════════════════════╣"
echo "║  Kubernetes-native AI agent orchestration platform  ║"
echo "╚══════════════════════════════════════════════════════╝"

# ─── Demo 1: Task Execution (Golden Path) ───────────────────

run "DEMO 1: Submit a task — agent writes a Go program" \
  api -X POST "$API/v1/tasks" \
    -H 'Content-Type: application/json' \
    -d '{"name":"demo-hello","poolRef":"coding-pool","prompt":"Create a Go program in /workspace/hello/ that prints Hello from the Software Factory and includes a unit test. Make sure the test passes."}'

echo "Waiting for task to complete..."
TIMEOUT=120 wait_task "demo-hello" "Succeeded" 120

run "Check the code the agent wrote" \
  sandbox_exec cat /workspace/hello/main.go

run "Check the test" \
  sandbox_exec cat /workspace/hello/main_test.go

# ─── Demo 2: Interactive Session ─────────────────────────────

run "DEMO 2: Interactive session — multi-turn conversation" \
  api -X POST "$API/v1/sessions" \
    -H 'Content-Type: application/json' \
    -d '{"poolRef":"coding-pool","prompt":"Create /workspace/chat/turn1.txt with content: I am ready to help"}'

SESSION=$(kubectl get session -n demo -l '!factory.example.com/task' -o jsonpath='{.items[-1].metadata.name}' 2>/dev/null)
echo "Session: $SESSION"
wait_session "$SESSION" "Active" 30

echo "Checking turn 1 output..."
sleep 5
sandbox_exec cat /workspace/chat/turn1.txt 2>/dev/null || echo "  (agent still working...)"

TIMEOUT=120 run "Sending follow-up message (turn 2)" \
  api -X POST "$API/v1/sessions/$SESSION/messages" \
    -H 'Content-Type: application/json' \
    -d '{"message":"Now create /workspace/chat/turn2.txt with content: Follow-up received"}'

echo "Checking turn 2 output..."
sandbox_exec cat /workspace/chat/turn2.txt 2>/dev/null || echo "  (agent still working...)"

run "Close the interactive session" \
  api --max-time 10 -X DELETE "$API/v1/sessions/$SESSION"

echo "Session status:"
kubectl get session "$SESSION" -n demo -o jsonpath='  Phase: {.status.phase}  CompletedAt: {.status.completedAt}' 2>/dev/null
echo ""

# ─── Demo 3: SSE Event Streaming ─────────────────────────────

run "DEMO 3: SSE streaming — watch agent work in real time" \
  echo "Creating a session and streaming events for 15 seconds..."

api -X POST "$API/v1/sessions" \
  -H 'Content-Type: application/json' \
  -d '{"poolRef":"coding-pool","prompt":"Create /workspace/stream-test.txt with the text: SSE streaming works"}' >/dev/null 2>&1

SSE_SESSION=$(kubectl get session -n demo -l '!factory.example.com/task' -o jsonpath='{.items[-1].metadata.name}' 2>/dev/null)
wait_session "$SSE_SESSION" "Active" 15

TIMEOUT=15 api -N "$API/v1/sessions/$SSE_SESSION/events" | head -30 || true

echo ""
echo "  (showing first 30 lines of SSE stream)"

# ─── Summary ──────────────────────────────────────────────────

echo ""
echo "╔══════════════════════════════════════════════════════╗"
echo "║                    DEMO COMPLETE                    ║"
echo "╠══════════════════════════════════════════════════════╣"
echo "║  ✓ Task execution: agent wrote code + tests         ║"
echo "║  ✓ Interactive session: multi-turn conversation     ║"
echo "║  ✓ SSE streaming: real-time event visibility        ║"
echo "╚══════════════════════════════════════════════════════╝"

echo ""
echo "Resources created:"
kubectl get task,session -n demo 2>/dev/null
