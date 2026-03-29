#!/bin/bash
set -euo pipefail

# Environment setup for Claude Code sessions.
# Installs Go toolchain and development dependencies.

GOLANGCI_LINT_VERSION="v1.64.5"
CONTROLLER_GEN_VERSION="v0.17.2"
KUSTOMIZE_VERSION="v5.6.0"

install_go() {
  if command -v go &> /dev/null; then
    echo "Go already installed: $(go version)"
    return
  fi
  echo "Installing Go..."
  curl -fsSL https://go.dev/dl/go1.24.1.linux-amd64.tar.gz -o /tmp/go.tar.gz
  sudo tar -C /usr/local -xzf /tmp/go.tar.gz
  rm /tmp/go.tar.gz
  export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"
  if [ -n "${CLAUDE_ENV_FILE:-}" ]; then
    echo 'export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"' >> "$CLAUDE_ENV_FILE"
  fi
  echo "Installed: $(go version)"
}

install_golangci_lint() {
  if command -v golangci-lint &> /dev/null; then
    echo "golangci-lint already installed: $(golangci-lint --version 2>&1 | head -1)"
    return
  fi
  echo "Installing golangci-lint ${GOLANGCI_LINT_VERSION}..."
  curl -fsSL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b "$(go env GOPATH)/bin" "$GOLANGCI_LINT_VERSION"
  echo "Installed: $(golangci-lint --version 2>&1 | head -1)"
}

install_controller_gen() {
  if command -v controller-gen &> /dev/null; then
    echo "controller-gen already installed"
    return
  fi
  echo "Installing controller-gen ${CONTROLLER_GEN_VERSION}..."
  go install sigs.k8s.io/controller-tools/cmd/controller-gen@"$CONTROLLER_GEN_VERSION"
  echo "Installed controller-gen"
}

install_kustomize() {
  if command -v kustomize &> /dev/null; then
    echo "kustomize already installed: $(kustomize version 2>&1 | head -1)"
    return
  fi
  echo "Installing kustomize ${KUSTOMIZE_VERSION}..."
  go install sigs.k8s.io/kustomize/kustomize/v5@"$KUSTOMIZE_VERSION"
  echo "Installed kustomize"
}

install_go
install_golangci_lint
install_controller_gen
install_kustomize

# Download module dependencies if go.mod exists
if [ -f go.mod ]; then
  echo "Downloading Go module dependencies..."
  go mod download
fi

echo "Environment setup complete."
