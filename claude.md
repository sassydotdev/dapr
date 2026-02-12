# Dapr Development Guide

## Overview

Dapr (Distributed Application Runtime) is a CNCF graduated project providing APIs for building secure and reliable microservices. This is the main runtime repository written in Go.

## Quick Start

```bash
# Install all required tooling
./.claude/skills/init-tooling/init-tooling.sh

# Build all binaries
make build

# Run unit tests
make test

# Run linter
make lint
```

## Required Tooling

Run `./.claude/skills/init-tooling/init-tooling.sh` or `/init-tooling` to install all tools. The script is idempotent and can be run multiple times safely.

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.24.12 | Main language |
| golangci-lint | v1.64.6 | Linting (specific version required) |
| protoc | v25.4 | Protocol buffer compiler |
| protoc-gen-go | v1.32.0 | Go protobuf code generator |
| protoc-gen-go-grpc | v1.3.0 | gRPC code generator |
| protoc-gen-connect-go | v1.9.1 | Connect-RPC code generator |
| gotestsum | latest | Test runner with better output |
| gofumpt | latest | Code formatting |
| goimports | latest | Import management |

## Project Structure

```
dapr/
├── cmd/                    # Main binaries (daprd, placement, operator, injector, sentry, scheduler)
├── pkg/                    # Core packages (api, components, proto, runtime, etc.)
├── dapr/proto/            # Protobuf definitions
├── tests/                 # Test suites (integration, e2e, perf, apps)
├── charts/                # Helm charts
└── docker/                # Docker configs
```

## Key Make Targets

```bash
make build              # Build all binaries for current OS/arch
make build-linux        # Build Linux binaries (for Docker)
make test               # Run unit tests
make lint               # Run golangci-lint
make format             # Format code (gofumpt + goimports)
make gen-proto          # Regenerate protobuf code
make modtidy-all        # Run go mod tidy for all go.mod files
```

## Build Tags

- `allcomponents` - Include all components (default)
- `unit` - Unit test files
- `integration` - Integration test files
- `e2e` - End-to-end test files

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make changes with tests
4. Run `make lint` and `make test`
5. Sign commits with DCO (`git commit -s`)
6. Submit PR

## Useful Links

- [Dapr Docs](https://docs.dapr.io/)
- [Contributing Guide](https://docs.dapr.io/contributing/)
- [API Reference](https://docs.dapr.io/reference/api/)
