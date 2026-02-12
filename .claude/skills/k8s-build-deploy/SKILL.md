---
name: k8s-build-deploy
description: Build container images with BuildKit on Kubernetes and deploy to the cluster. Use for building Docker images, pushing to the local registry, and deploying services.
---

# Kubernetes Build Infrastructure

Build container images using BuildKit on Kubernetes and push to the local registry.

**Related Skills:**
- `/dapr-dev` - Full Dapr development workflow (build, deploy, validate)
- `/init-tooling` - Install Go and development tools

## Container Registry

**URL:** `registry:5000`

- **ALWAYS** use `registry:5000` (not `localhost:5000`)
- HTTP only (no HTTPS)
- Requires `registry.insecure=true` in BuildKit output

```bash
# Verify registry
curl -s http://registry:5000/v2/_catalog
```

## CRITICAL: Workspace Access

The `/workspace` directory is available as a PVC mount in Kubernetes:

- **DO NOT** use `kubectl cp` or ConfigMaps for source code
- **ALWAYS** mount `director-workspace` PVC
- Use `readOnly: true` for build jobs

## BuildKit Job Template

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: buildkit-$IMAGE_NAME
  namespace: default
spec:
  ttlSecondsAfterFinished: 300
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: buildkit
        image: moby/buildkit:latest
        securityContext:
          privileged: true
        command: ["buildctl-daemonless.sh"]
        args:
        - build
        - --frontend=dockerfile.v0
        - --local
        - context=/workspace
        - --local
        - dockerfile=/workspace
        - --opt
        - filename=$DOCKERFILE_PATH
        - --output
        - type=image,name=registry:5000/$IMAGE_NAME:$TAG,push=true,registry.insecure=true
        volumeMounts:
        - name: workspace
          mountPath: /workspace
          readOnly: true
      volumes:
      - name: workspace
        persistentVolumeClaim:
          claimName: director-workspace
```

## Dapr Components

For Dapr binaries, the context and dockerfile paths are different:

```yaml
args:
- build
- --frontend=dockerfile.v0
- --local
- context=/workspace/dist/linux_arm64/release   # Pre-built binaries
- --local
- dockerfile=/workspace/docker                   # Dockerfile location
- --opt
- build-arg:PKG_FILES=daprd                      # Which binary to package
- --output
- type=image,name=registry:5000/daprd:$TAG,push=true,registry.insecure=true
```

Components: `daprd`, `placement`, `operator`, `injector`, `sentry`, `scheduler`

See `/dapr-dev` for the full Dapr build-deploy workflow.

## Monitor Build

```bash
# Watch job
kubectl get jobs -w

# View logs
kubectl logs job/buildkit-$IMAGE_NAME -f

# Check registry
curl -s http://registry:5000/v2/$IMAGE_NAME/tags/list | jq
```

## Registry Commands

```bash
# List all images
curl -s http://registry:5000/v2/_catalog | jq

# List tags
curl -s http://registry:5000/v2/$IMAGE_NAME/tags/list | jq

# Delete job
kubectl delete job buildkit-$IMAGE_NAME
```

## Troubleshooting

### Permission Denied
Ensure `privileged: true` in securityContext.

### Push Failed
- Check registry: `curl http://registry:5000/v2/`
- Ensure `registry.insecure=true` in output options

### Pod Can't Pull Image
- Use full path: `registry:5000/image:tag`
- Set `imagePullPolicy: Always`

## Arguments

- `$1`: Image name (e.g., `daprd`, `my-service`)
- `$2`: Tag (e.g., `dev`, `v1.0.0`)

Example: `/k8s-build-deploy daprd 1.16.8-sassy.1.rc1`
