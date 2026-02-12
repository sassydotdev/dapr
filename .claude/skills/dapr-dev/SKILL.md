---
name: dapr-dev
description: Build, test, deploy, and validate Dapr changes. Use for iterating on Dapr runtime code and deploying to the local K8s cluster.
---

# Dapr Development Workflow

Build, test, deploy, and validate Dapr changes on the local Kubernetes cluster.

**Related Skills:**
- `/init-tooling` - Install Go, protoc, and other required tools
- `/k8s-build-deploy` - Build container images with BuildKit

## Quick Reference

```bash
# Full rebuild and deploy cycle
make build-linux
/k8s-build-deploy daprd <version>
helm upgrade dapr ./charts/dapr -n dapr-system --reuse-values \
  --set dapr_sidecar_injector.image.name=registry:5000/daprd \
  --set dapr_sidecar_injector.image.tag=<version> --wait

# Restart app pods to pick up new sidecar
kubectl rollout restart deployment <app-name>
```

## Version Naming Convention

```
<base-version>-sassy.<iteration>.rc<revision>

Examples:
- 1.16.8-sassy.1.rc1   # First revision of first iteration
- 1.16.8-sassy.1.rc2   # Bug fix to first iteration
- 1.16.8-sassy.2.rc1   # New feature iteration
```

## Step 1: Build Go Binaries

```bash
# Ensure Go is available (run /init-tooling if not)
export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"

# Build all Linux binaries
make build-linux
```

Creates binaries in `./dist/linux_<arch>/release/`:
- daprd, placement, operator, injector, sentry, scheduler

## Step 2: Build Container Images

Use `/k8s-build-deploy` skill:

```bash
/k8s-build-deploy daprd <version>
```

For multiple components:

| Component | Command |
|-----------|---------|
| Sidecar | `/k8s-build-deploy daprd <version>` |
| Placement | `/k8s-build-deploy placement <version>` |
| Operator | `/k8s-build-deploy operator <version>` |
| Injector | `/k8s-build-deploy injector <version>` |
| Sentry | `/k8s-build-deploy sentry <version>` |
| Scheduler | `/k8s-build-deploy scheduler <version>` |

## Step 3: Deploy with Helm

### Sidecar Only (Most Common)

```bash
helm upgrade dapr ./charts/dapr \
  --namespace dapr-system \
  --reuse-values \
  --set dapr_sidecar_injector.image.name=registry:5000/daprd \
  --set dapr_sidecar_injector.image.tag=<version> \
  --wait
```

### Full Deployment

```bash
helm upgrade dapr ./charts/dapr \
  --namespace dapr-system \
  --set global.registry=registry:5000 \
  --set global.tag=<version> \
  --set global.imagePullPolicy=Always \
  --set dapr_sidecar_injector.image.name=daprd \
  --set dapr_sidecar_injector.injectorImage.name=injector \
  --set dapr_operator.image.name=operator \
  --set dapr_placement.image.name=placement \
  --set dapr_sentry.image.name=sentry \
  --set dapr_scheduler.image.name=scheduler \
  --wait --timeout 5m0s
```

**Important:**
- `dapr_sidecar_injector.image.name` = sidecar injected into app pods (daprd)
- `dapr_sidecar_injector.injectorImage.name` = injector service itself

## Step 4: Validate

```bash
# Check Dapr pods
kubectl get pods -n dapr-system

# Verify sidecar image
kubectl get deployment dapr-sidecar-injector -n dapr-system \
  -o jsonpath='{.spec.template.spec.containers[0].env}' | jq '.[] | select(.name | contains("IMAGE"))'

# Restart app pods to pick up new sidecar
kubectl rollout restart deployment <app-name>

# Check sidecar logs
kubectl logs <pod> -c daprd | head -20
```

## Step 5: Testing

```bash
# Unit tests
make test

# Specific package
go test -tags=unit,allcomponents ./pkg/api/grpc/...

# Integration tests
make test-integration

# Manual API test
kubectl port-forward <pod> 3500:3500
curl http://localhost:3500/v1.0/metadata
```

## Troubleshooting

### Sidecar Not Updated
```bash
kubectl rollout restart deployment <app-name>
```

### Helm Upgrade Failed
```bash
helm get values dapr -n dapr-system
helm rollback dapr -n dapr-system
```

### Check Injector Logs
```bash
kubectl logs -l app=dapr-sidecar-injector -n dapr-system
```
