---
name: init-tooling
description: Initialize Dapr development tooling. Installs Go, golangci-lint, protoc, and other required tools. Idempotent - safe to run multiple times.
---

# Initialize Dapr Development Tooling

Install all required tools for Dapr development. The script is idempotent - safe to run multiple times.

**Related Skills:**
- `/dapr-dev` - Full Dapr build-deploy workflow
- `/k8s-build-deploy` - Build container images with BuildKit

## Usage

Run the init script:

```bash
./.claude/skills/init-tooling/init-tooling.sh
```

Or invoke via skill:

```
/init-tooling
```

## Tools Installed

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.24.12 | Main language runtime |
| golangci-lint | v1.64.6 | Linting (exact version required) |
| protoc | v25.4 | Protocol buffer compiler |
| protoc-gen-go | v1.32.0 | Go protobuf code generator |
| protoc-gen-go-grpc | v1.3.0 | gRPC code generator |
| protoc-gen-connect-go | v1.9.1 | Connect-RPC code generator |
| gotestsum | latest | Test runner with better output |
| gofumpt | latest | Code formatting |
| goimports | latest | Import management |

## After Installation

Add to your shell profile (`.bashrc`, `.zshrc`, etc.):

```bash
export PATH="/usr/local/go/bin:$PATH"
export GOPATH="$HOME/go"
export GOBIN="$GOPATH/bin"
export PATH="$GOBIN:$PATH"
```

Or for the current session:

```bash
export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"
```

## Verification

Check all tools are installed:

```bash
go version
golangci-lint --version
protoc --version
make check-proto-version
```

## Version Updates

To update tool versions, edit the version variables at the top of `init-tooling.sh`:

```bash
GO_VERSION="1.24.12"
GOLANGCI_LINT_VERSION="1.64.6"
PROTOC_VERSION="25.4"
# etc.
```

Then re-run the script.

## Troubleshooting

### Permission Denied

The script requires `sudo` for installing Go and protoc to `/usr/local`. Ensure you have sudo access.

### golangci-lint Version Mismatch

The Dapr project requires exactly v1.64.6. Other versions may produce different linting results:

```bash
# Check version
golangci-lint --version

# Force reinstall
rm -f $GOBIN/golangci-lint
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $GOBIN v1.64.6
```

### Go Not Found After Install

Ensure `/usr/local/go/bin` is in your PATH:

```bash
export PATH="/usr/local/go/bin:$PATH"
which go
```
