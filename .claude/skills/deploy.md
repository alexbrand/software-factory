---
name: deploy
description: Build, load, and deploy the latest code to the kind cluster
user-invocable: true
---

Deploy the latest code to the kind cluster named "factory". Run these steps in order, stopping on any failure:

1. `make docker-build` — build all three container images
2. `kind load docker-image software-factory-controller-manager:latest software-factory-apiserver:latest software-factory-bridge:latest --name factory` — load into kind
3. `kubectl apply -k config/default/` — apply latest CRDs and manifests
4. `kubectl rollout restart deployment/controller-manager deployment/apiserver -n factory-system` — restart deployments to pick up new images
5. `kubectl rollout status deployment/controller-manager deployment/apiserver -n factory-system --timeout=120s` — wait for rollout

If the user said "fresh" or "clean", also run:
6. `kubectl delete sandbox --all -n demo` — force sandbox recreation with latest pod spec
7. Wait for new sandboxes to become Ready

Finally, show the cluster state:
- `kubectl get pods -n factory-system`
- `kubectl get pods -n demo`
- `kubectl get sandbox -n demo`
