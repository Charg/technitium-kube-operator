# technitium-operator

A Helm chart for the Technitium DNS operator (manages `Zone` resources via the
`dns.packet.fail/v1alpha1` API).

## Installing

```console
helm install technitium-operator ./charts/technitium-operator \
  --set image.repository=<your-registry>/technitium-operator \
  --set image.tag=<tag>
```

By default the chart installs the `zones.dns.packet.fail` CRD. It is annotated
with `helm.sh/resource-policy: keep` (see `crds.keep`) so `helm uninstall`
does not delete it, and with it any `Zone` resources, along with the release.

## Uninstalling

```console
helm uninstall technitium-operator
```

To also remove the CRD (and all `Zone` resources) afterwards:

```console
kubectl delete crd zones.dns.packet.fail
```

## Values

| Key | Default | Description |
| --- | --- | --- |
| `replicaCount` | `1` | Number of controller manager replicas |
| `image.repository` | `ghcr.io/charg/technitium-operator` | Manager image repository |
| `image.tag` | `""` (uses `Chart.AppVersion`) | Manager image tag |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `crds.install` | `true` | Install the `zones.dns.packet.fail` CRD |
| `crds.keep` | `true` | Keep the CRD on `helm uninstall` |
| `serviceAccount.create` | `true` | Create a ServiceAccount for the manager |
| `rbac.create` | `true` | Create the ClusterRole/RoleBindings the manager needs |
| `rbac.zoneAggregateRoles` | `true` | Create `zone-admin`/`zone-editor`/`zone-viewer` ClusterRoles for delegating access to `Zone` resources (not used by the operator itself) |
| `leaderElection.enabled` | `true` | Enable leader election (`--leader-elect`) |
| `webhook.enabled` | `false` | Enable the validating/defaulting admission webhook for `Zone` (requires cert-manager) |
| `webhook.failurePolicy` | `Fail` | Admission behavior when the webhook is unreachable (`Fail` or `Ignore`) |
| `webhook.certManager.enabled` | `true` | Create a self-signed cert-manager Issuer and Certificate for the webhook |
| `metrics.enabled` | `true` | Expose `/metrics` over HTTPS and create the metrics Service |
| `metrics.serviceMonitor.enabled` | `false` | Create a Prometheus Operator `ServiceMonitor` (requires the CRD to be installed) |
| `networkPolicy.enabled` | `false` | Restrict ingress to `/metrics` to namespaces labeled `metrics: enabled` |
| `resources` | `limits: {cpu: 500m, memory: 128Mi}`, `requests: {cpu: 10m, memory: 64Mi}` | Manager container resources |
| `extraArgs` | `[]` | Additional args appended to the manager command |
| `extraEnv` | `[]` | Additional env vars for the manager container |

See `values.yaml` for the full set of configurable values.
