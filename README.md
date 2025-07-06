# Node Out-of-Service Operator


[![Docker Image Size](https://img.shields.io/docker/image-size/infrabuilder/node-oos-operator/latest)](https://hub.docker.com/r/infrabuilder/node-oos-operator)
[![Docker Pulls](https://img.shields.io/docker/pulls/infrabuilder/node-oos-operator)](https://hub.docker.com/r/infrabuilder/node-oos-operator)
[![Docker Image Vulnerabilities](https://img.shields.io/badge/security-scanned-brightgreen)](https://hub.docker.com/r/infrabuilder/node-oos-operator)
[![GitHub Repo](https://img.shields.io/badge/github-repo-blue?logo=github)](https://github.com/infrabuilder/node-oos-operator)
[![CI](https://github.com/infrabuilder/node-oos-operator/actions/workflows/dockerhub-build-push.yml/badge.svg)](https://github.com/infrabuilder/node-oos-operator/actions)
[![Docker Weekly Build](https://img.shields.io/badge/autobuild-weekly-brightgreen)](https://hub.docker.com/r/infrabuilder/node-oos-operator)


A Kubernetes operator that automatically manages node out-of-service taints based on node readiness status.

## Features

- Monitors all nodes in the cluster for readiness status
- Applies `node.kubernetes.io/out-of-service=shutdown:NoExecute` taint when nodes are not ready for a configurable threshold
- Removes the taint when nodes become ready again for a configurable recovery period
- Exposes Prometheus metrics including `nodehealth_node_not_ready_seconds_total`
- Structured JSON logging to stdout
- Runs with minimal RBAC permissions
- Compatible with Pod Security Standards (PSS) restricted namespaces

## Configuration

The operator is configured via environment variables:

- `NOT_READY_THRESHOLD`: Duration a node must be not ready before applying the taint (default: "300s")
- `RECOVERY_THRESHOLD`: Duration a node must be ready before removing the taint (default: "60s")

## Metrics

The operator exposes the following Prometheus metrics on `:8080/metrics`:

- `nodehealth_node_not_ready_seconds_total`: Total number of seconds a node has been not ready
- `nodehealth_node_currently_not_ready`: Current status of node readiness (1 = not ready, 0 = ready)
- `nodehealth_node_tainted`: Current taint status of node (1 = tainted, 0 = not tainted)

## Deployment

### Using kubectl

```bash
kubectl apply -f deploy/operator.yaml
```

### Using Helm

```bash
helm install node-oos-operator chart/node-oos-operator/
```

#### Helm Configuration

You can customize the deployment by modifying values in `values.yaml` or using `--set` flags:

```bash
helm install node-oos-operator chart/node-oos-operator/ \
  --set config.notReadyThreshold=600s \
  --set config.recoveryThreshold=120s
```

## External Usage

The operator can also run outside the cluster with a kubeconfig:

```bash
./node-oos-operator --kubeconfig=/path/to/kubeconfig
```

## RBAC Permissions

The operator requires minimal cluster-level permissions:

- `get`, `list`, `watch`, `update`, `patch` on `nodes`

## Security

- Runs as non-root user (UID 1001)
- Uses read-only root filesystem
- Drops all capabilities
- Compatible with Pod Security Standards restricted profile
- Runs with minimal RBAC permissions

## Building

```bash
# Build the binary
go build -o node-oos-operator .

# Build the Docker image
docker build -t node-oos-operator:latest .
```

## License

This project is licensed under the MIT License.