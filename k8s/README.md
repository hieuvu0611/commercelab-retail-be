# CommerceLab Retail — Local Kubernetes Lab (`be/k8s/`)

Production-shaped Kubernetes manifests for all 10 CommerceLab Retail microservices, aligned with `docs/02-architecture-and-career-roadmap.md` (§7 runtime isolation and §10 telemetry & Kubernetes lab).

## Prerequisites

- Local Kubernetes cluster (`kind`, `minikube`, or `k3d`) with a CNI that enforces `NetworkPolicy` (for example Calico or Cilium) and `metrics-server` enabled for HPA.
- Container images built locally and loaded into the cluster (`commercelab/<service>:local`).

## Deploy

1. Create the namespace and supply real runtime secrets (never commit real secrets to Git):

```bash
kubectl apply -f k8s/namespace.yaml
cp k8s/secrets.example.yaml /tmp/commercelab-secrets.yaml
# Edit /tmp/commercelab-secrets.yaml with strong 32+ byte secrets and DB URLs
kubectl apply -f /tmp/commercelab-secrets.yaml
rm -f /tmp/commercelab-secrets.yaml
```

2. Apply the full Kustomize stack:

```bash
kubectl apply -k k8s/
```

## Verify

Check rollout, probes, quotas, network policies, and autoscalers:

```bash
kubectl -n commercelab-retail get pods,svc,hpa,networkpolicy,resourcequota,limitrange
kubectl -n commercelab-retail rollout status deployment/api-gateway
```

Verify Prometheus `/metrics` scraping and W3C `traceparent` propagation:

```bash
kubectl -n commercelab-retail port-forward svc/api-gateway 8080:8080
curl -s http://localhost:8080/metrics | head -n 30
curl -i -H "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" http://localhost:8080/health
```

## Teardown

Remove all lab resources cleanly:

```bash
kubectl delete -k k8s/
```
