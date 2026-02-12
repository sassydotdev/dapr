#!/usr/bin/env bash
#
# Idempotent tooling initialization script for Dapr development
# This script installs all required tools for building and testing Dapr
#
# Usage: ./init-tooling.sh
#
# Tools installed:
#   - Go 1.24.12
#   - golangci-lint v1.64.6
#   - protoc v25.4
#   - protoc-gen-go v1.32.0
#   - protoc-gen-go-grpc v1.3.0
#   - protoc-gen-connect-go v1.9.1
#   - gotestsum (latest)
#   - gofumpt (latest)
#   - goimports (latest)

set -e

# Version configuration (keep in sync with Makefile)
GO_VERSION="1.24.12"
GOLANGCI_LINT_VERSION="1.64.6"
PROTOC_VERSION="25.4"
PROTOC_GEN_GO_VERSION="1.32.0"
PROTOC_GEN_GO_GRPC_VERSION="1.3.0"
PROTOC_GEN_CONNECT_GO_VERSION="1.9.1"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Detect architecture
detect_arch() {
    local arch=$(uname -m)
    case $arch in
        x86_64)
            echo "amd64"
            ;;
        aarch64|arm64)
            echo "arm64"
            ;;
        *)
            log_error "Unsupported architecture: $arch"
            exit 1
            ;;
    esac
}

# Detect OS
detect_os() {
    local os=$(uname -s | tr '[:upper:]' '[:lower:]')
    case $os in
        linux)
            echo "linux"
            ;;
        darwin)
            echo "darwin"
            ;;
        *)
            log_error "Unsupported OS: $os"
            exit 1
            ;;
    esac
}

ARCH=$(detect_arch)
OS=$(detect_os)

# Set up Go paths
export GOPATH="${GOPATH:-$HOME/go}"
export GOBIN="${GOBIN:-$GOPATH/bin}"
mkdir -p "$GOBIN"

# Add GOBIN to PATH if not already there
if [[ ":$PATH:" != *":$GOBIN:"* ]]; then
    export PATH="$GOBIN:$PATH"
fi

# Check if a command exists
command_exists() {
    command -v "$1" &> /dev/null
}

# Install Go
install_go() {
    local current_version=""
    if command_exists go; then
        current_version=$(go version 2>/dev/null | grep -oP 'go\K[0-9]+\.[0-9]+\.[0-9]+' || echo "")
    fi

    if [[ "$current_version" == "$GO_VERSION" ]]; then
        log_info "Go $GO_VERSION is already installed"
        return 0
    fi

    log_info "Installing Go $GO_VERSION..."

    local go_archive="go${GO_VERSION}.${OS}-${ARCH}.tar.gz"
    local go_url="https://go.dev/dl/${go_archive}"
    local install_dir="/usr/local"

    # Download Go
    curl -fsSL "$go_url" -o "/tmp/${go_archive}"

    # Remove existing Go installation
    if [[ -d "${install_dir}/go" ]]; then
        sudo rm -rf "${install_dir}/go"
    fi

    # Extract Go
    sudo tar -C "$install_dir" -xzf "/tmp/${go_archive}"
    rm -f "/tmp/${go_archive}"

    # Add to PATH for current session
    export PATH="/usr/local/go/bin:$PATH"

    log_info "Go $GO_VERSION installed successfully"
}

# Install golangci-lint
install_golangci_lint() {
    local current_version=""
    if command_exists golangci-lint; then
        current_version=$(golangci-lint --version 2>/dev/null | grep -oP 'v?\K[0-9]+\.[0-9]+\.[0-9]+' | head -1 || echo "")
    fi

    if [[ "$current_version" == "$GOLANGCI_LINT_VERSION" ]]; then
        log_info "golangci-lint v$GOLANGCI_LINT_VERSION is already installed"
        return 0
    fi

    log_info "Installing golangci-lint v$GOLANGCI_LINT_VERSION..."
    curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b "$GOBIN" "v$GOLANGCI_LINT_VERSION"
    log_info "golangci-lint v$GOLANGCI_LINT_VERSION installed successfully"
}

# Install protoc
install_protoc() {
    local current_version=""
    if command_exists protoc; then
        current_version=$(protoc --version 2>/dev/null | grep -oP '[0-9]+\.[0-9]+' || echo "")
    fi

    if [[ "$current_version" == "$PROTOC_VERSION" ]]; then
        log_info "protoc v$PROTOC_VERSION is already installed"
        return 0
    fi

    log_info "Installing protoc v$PROTOC_VERSION..."

    # Map architecture for protoc downloads
    local protoc_arch="$ARCH"
    if [[ "$ARCH" == "amd64" ]]; then
        protoc_arch="x86_64"
    elif [[ "$ARCH" == "arm64" ]]; then
        protoc_arch="aarch_64"
    fi

    local protoc_zip="protoc-${PROTOC_VERSION}-${OS}-${protoc_arch}.zip"
    local protoc_url="https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/${protoc_zip}"

    curl -fsSL "$protoc_url" -o "/tmp/${protoc_zip}"

    # Extract to /usr/local (requires sudo)
    sudo unzip -o "/tmp/${protoc_zip}" -d /usr/local bin/protoc
    sudo chmod 755 /usr/local/bin/protoc
    sudo unzip -o "/tmp/${protoc_zip}" -d /usr/local 'include/*'
    sudo chmod -R 755 /usr/local/include/google/protobuf

    rm -f "/tmp/${protoc_zip}"

    log_info "protoc v$PROTOC_VERSION installed successfully"
}

# Install protoc-gen-go
install_protoc_gen_go() {
    local current_version=""
    if command_exists protoc-gen-go; then
        current_version=$(protoc-gen-go --version 2>/dev/null | grep -oP 'v\K[0-9]+\.[0-9]+\.[0-9]+' || echo "")
    fi

    if [[ "$current_version" == "$PROTOC_GEN_GO_VERSION" ]]; then
        log_info "protoc-gen-go v$PROTOC_GEN_GO_VERSION is already installed"
        return 0
    fi

    log_info "Installing protoc-gen-go v$PROTOC_GEN_GO_VERSION..."
    go install "google.golang.org/protobuf/cmd/protoc-gen-go@v${PROTOC_GEN_GO_VERSION}"
    log_info "protoc-gen-go v$PROTOC_GEN_GO_VERSION installed successfully"
}

# Install protoc-gen-go-grpc
install_protoc_gen_go_grpc() {
    local current_version=""
    if command_exists protoc-gen-go-grpc; then
        current_version=$(protoc-gen-go-grpc --version 2>/dev/null | grep -oP '[0-9]+\.[0-9]+\.[0-9]+' || echo "")
    fi

    if [[ "$current_version" == "$PROTOC_GEN_GO_GRPC_VERSION" ]]; then
        log_info "protoc-gen-go-grpc v$PROTOC_GEN_GO_GRPC_VERSION is already installed"
        return 0
    fi

    log_info "Installing protoc-gen-go-grpc v$PROTOC_GEN_GO_GRPC_VERSION..."
    go install "google.golang.org/grpc/cmd/protoc-gen-go-grpc@v${PROTOC_GEN_GO_GRPC_VERSION}"
    log_info "protoc-gen-go-grpc v$PROTOC_GEN_GO_GRPC_VERSION installed successfully"
}

# Install protoc-gen-connect-go
install_protoc_gen_connect_go() {
    local current_version=""
    if command_exists protoc-gen-connect-go; then
        current_version=$(protoc-gen-connect-go --version 2>/dev/null | grep -oP '[0-9]+\.[0-9]+\.[0-9]+' || echo "")
    fi

    if [[ "$current_version" == "$PROTOC_GEN_CONNECT_GO_VERSION" ]]; then
        log_info "protoc-gen-connect-go v$PROTOC_GEN_CONNECT_GO_VERSION is already installed"
        return 0
    fi

    log_info "Installing protoc-gen-connect-go v$PROTOC_GEN_CONNECT_GO_VERSION..."
    go install "connectrpc.com/connect/cmd/protoc-gen-connect-go@v${PROTOC_GEN_CONNECT_GO_VERSION}"
    log_info "protoc-gen-connect-go v$PROTOC_GEN_CONNECT_GO_VERSION installed successfully"
}

# Install gotestsum
install_gotestsum() {
    if command_exists gotestsum; then
        log_info "gotestsum is already installed"
        return 0
    fi

    log_info "Installing gotestsum..."
    go install gotest.tools/gotestsum@latest
    log_info "gotestsum installed successfully"
}

# Install gofumpt
install_gofumpt() {
    if command_exists gofumpt; then
        log_info "gofumpt is already installed"
        return 0
    fi

    log_info "Installing gofumpt..."
    go install mvdan.cc/gofumpt@latest
    log_info "gofumpt installed successfully"
}

# Install goimports
install_goimports() {
    if command_exists goimports; then
        log_info "goimports is already installed"
        return 0
    fi

    log_info "Installing goimports..."
    go install golang.org/x/tools/cmd/goimports@latest
    log_info "goimports installed successfully"
}

# Check for required system tools
check_system_tools() {
    local missing_tools=()

    if ! command_exists make; then
        missing_tools+=("make")
    fi

    if ! command_exists git; then
        missing_tools+=("git")
    fi

    if ! command_exists curl; then
        missing_tools+=("curl")
    fi

    if ! command_exists unzip; then
        missing_tools+=("unzip")
    fi

    if [[ ${#missing_tools[@]} -gt 0 ]]; then
        log_warn "The following system tools are missing: ${missing_tools[*]}"
        log_warn "Please install them using your package manager:"
        log_warn "  Ubuntu/Debian: sudo apt-get install ${missing_tools[*]}"
        log_warn "  macOS: brew install ${missing_tools[*]}"
        return 1
    fi

    log_info "All required system tools are present"
    return 0
}

# Print environment setup instructions
print_env_setup() {
    echo ""
    echo "=============================================="
    echo "Add the following to your shell profile:"
    echo "=============================================="
    echo ""
    echo "export PATH=\"/usr/local/go/bin:\$PATH\""
    echo "export GOPATH=\"\$HOME/go\""
    echo "export GOBIN=\"\$GOPATH/bin\""
    echo "export PATH=\"\$GOBIN:\$PATH\""
    echo ""
    echo "=============================================="
}

# Verify installation
verify_installation() {
    echo ""
    log_info "Verifying installation..."
    echo ""

    local all_good=true

    if command_exists go; then
        echo "  Go:                    $(go version)"
    else
        log_error "  Go: NOT FOUND"
        all_good=false
    fi

    if command_exists golangci-lint; then
        echo "  golangci-lint:         $(golangci-lint --version 2>&1 | head -1)"
    else
        log_error "  golangci-lint: NOT FOUND"
        all_good=false
    fi

    if command_exists protoc; then
        echo "  protoc:                $(protoc --version)"
    else
        log_error "  protoc: NOT FOUND"
        all_good=false
    fi

    if command_exists protoc-gen-go; then
        echo "  protoc-gen-go:         $(protoc-gen-go --version 2>&1)"
    else
        log_error "  protoc-gen-go: NOT FOUND"
        all_good=false
    fi

    if command_exists protoc-gen-go-grpc; then
        echo "  protoc-gen-go-grpc:    $(protoc-gen-go-grpc --version 2>&1)"
    else
        log_error "  protoc-gen-go-grpc: NOT FOUND"
        all_good=false
    fi

    if command_exists protoc-gen-connect-go; then
        echo "  protoc-gen-connect-go: $(protoc-gen-connect-go --version 2>&1)"
    else
        log_error "  protoc-gen-connect-go: NOT FOUND"
        all_good=false
    fi

    if command_exists gotestsum; then
        echo "  gotestsum:             installed"
    else
        log_error "  gotestsum: NOT FOUND"
        all_good=false
    fi

    if command_exists gofumpt; then
        echo "  gofumpt:               installed"
    else
        log_error "  gofumpt: NOT FOUND"
        all_good=false
    fi

    if command_exists goimports; then
        echo "  goimports:             installed"
    else
        log_error "  goimports: NOT FOUND"
        all_good=false
    fi

    echo ""

    if $all_good; then
        log_info "All tools installed successfully!"
        return 0
    else
        log_error "Some tools failed to install. Please check the errors above."
        return 1
    fi
}

# Main
main() {
    echo ""
    echo "=============================================="
    echo "Dapr Development Environment Setup"
    echo "=============================================="
    echo ""

    log_info "Detected OS: $OS, Architecture: $ARCH"
    echo ""

    # Check system tools first
    if ! check_system_tools; then
        exit 1
    fi

    echo ""

    # Install tools in order (Go first, then Go-based tools)
    install_go

    # Ensure Go is in PATH for subsequent installations
    export PATH="/usr/local/go/bin:$GOBIN:$PATH"

    install_golangci_lint
    install_protoc
    install_protoc_gen_go
    install_protoc_gen_go_grpc
    install_protoc_gen_connect_go
    install_gotestsum
    install_gofumpt
    install_goimports

    # Verify everything is installed
    verify_installation

    # Print environment setup instructions
    print_env_setup
}

main "$@"
