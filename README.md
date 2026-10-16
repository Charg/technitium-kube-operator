# technitium-operator
A Kubernetes operator that manages Technitium DNS Server zones as custom resources.

## Description
The operator provisions Technitium DNS Server instances (`TechnitiumCluster`, API group `dns.packet.fail/v1alpha1`) and reconciles `Zone` resources against them, keeping DNS zones declared in the cluster in sync with the server. Manage DNS state with kubectl and GitOps instead of the Technitium admin console.

## TechnitiumCluster resource
A `TechnitiumCluster` provisions a Technitium DNS Server instance: a StatefulSet, a client Service, and (unless `spec.adminSecretRef` is set) a generated `<name>-admin` Secret holding its credentials. It is cluster-scoped. Once the instance is reachable, its status reports `endpoint`, the client Service that load-balances across every replica, and `primaryEndpoint`, the Primary node (`<name>-0`) addressed through the headless Service. Every resource that writes to the instance (Zone, Record, Blocklist and the rest) sends its API calls to `primaryEndpoint`, so a write never lands on a Secondary. A supplied `spec.adminSecretRef` must name a Secret in the operator's namespace (the pods mount it from there) containing `username` and `password` keys; the operator writes a `token` key back into it. Its `namespace` field must be empty or the operator's namespace, otherwise the cluster is marked Degraded with reason `InvalidAdminSecretRef`.

```yaml
apiVersion: dns.packet.fail/v1alpha1
kind: TechnitiumCluster
metadata:
  name: dns
spec:
  storage:
    size: 1Gi
```

### Service options

`spec.service` shapes the client Service (`<name>`), which load-balances DNS across every replica:

- `type` and `annotations`: the Service type and annotations, for example for a load balancer or external-dns.
- `externalTrafficPolicy` (`Cluster` or `Local`): `Local` preserves client source IPs in query logs. Only valid for type `LoadBalancer` or `NodePort`.
- `exposeAPI` (default `true`): when `false` the client Service publishes DNS (53 UDP and TCP) only, keeping the plain-HTTP admin API (5380) off the Service address. The operator is unaffected because it reaches the API through the headless Service, which always keeps 5380. `status.endpoint` is then the DNS address `<name>.<namespace>.svc:53` rather than an HTTP URL.
- `perReplica`: creates one Service per StatefulSet ordinal, named `<name>-<ordinal>`, selecting only that pod and exposing DNS only (never 5380). Its `type` defaults to `LoadBalancer` and its `externalTrafficPolicy` to `Local` (for `LoadBalancer` and `NodePort`). `annotations` apply to every per-replica Service, and `ordinals[].annotations` are merged over them for one ordinal, which is how each replica is pinned to its own address. Scaling down deletes the Services of the removed ordinals, and removing `perReplica` deletes them all. Only Services owned by the cluster are ever deleted.
- `primary`: with `enabled: true`, creates a Service named `<name>-primary` that selects only the Primary pod and exposes the web/API port (5380) alone. Point an Ingress for the web console here rather than at the client Service: Technitium keeps login sessions per replica, so with several replicas a console behind the load-balanced client Service logs you out as soon as a request lands on a replica that did not issue your session. Its `type` defaults to `ClusterIP`; `externalTrafficPolicy` and `annotations` work as on the client Service, and annotations other controllers add are left alone. `status.primaryService` names the Service while it is enabled. Disabling or removing `primary` deletes it if the cluster owns it.

A MetalLB homelab with one stable LAN IP per replica, source IPs preserved, and the admin API kept off the LAN:

```yaml
apiVersion: dns.packet.fail/v1alpha1
kind: TechnitiumCluster
metadata:
  name: dns
spec:
  replicas: 3
  storage:
    size: 1Gi
  service:
    type: ClusterIP
    exposeAPI: false
    perReplica:
      type: LoadBalancer
      externalTrafficPolicy: Local
      ordinals:
        - ordinal: 0
          annotations:
            metallb.io/loadBalancerIPs: 192.168.1.50
        - ordinal: 1
          annotations:
            metallb.io/loadBalancerIPs: 192.168.1.51
        - ordinal: 2
          annotations:
            metallb.io/loadBalancerIPs: 192.168.1.52
```

### Pod disruption budget

With `spec.replicas` greater than 1 the operator creates a `PodDisruptionBudget` named `<name>` in the operator's namespace, selecting the cluster's pods with `maxUnavailable: 1`, so a node drain cannot evict several resolvers at once. Tune it with `spec.podDisruptionBudget.maxUnavailable` (a number or percentage). The budget is removed again when replicas drops to 1.

If you already manage your own `PodDisruptionBudget` for these pods, remove it or set `spec.podDisruptionBudget.enabled: false`. Two budgets selecting the same pods make the eviction API refuse every eviction, so drains hang. The operator never adopts or deletes a budget it did not create: an existing, unowned `<name>` PDB marks the cluster Degraded with reason `PodDisruptionBudgetConflict`.

### Writes with multiple replicas

With `spec.replicas` greater than 1, `<name>-0` initializes a Technitium cluster as the Primary and every other pod joins as a Secondary. Technitium does not reliably reject writes sent to a Secondary: zone, settings and DHCP changes are applied locally and never reach the Primary, and the Primary's next config sync overwrites local settings. The operator therefore sends every write, and bootstraps the admin token, through `primaryEndpoint`.

As a result:

- While `<name>-0` is down, writes fail and are retried. Technitium does not promote a Secondary automatically. Secondaries keep serving DNS from what they last synced.
- A zone reaches the Secondaries only if it is a member of the cluster's catalog zone. A `Primary`, `Secondary`, `Stub` or `Forwarder` `Zone` joins `cluster-catalog.<clusterDomain>` automatically. Set `spec.catalog: ""` to keep a zone on the Primary only (this removes it from any catalog, and queries that land on a Secondary fail for it), or name another catalog to override. Scaling back to one replica leaves existing membership in place.
- Global settings (`ServerSettings`, block list URLs) and allowed/blocked entries sync to the Secondaries.
- DHCP scopes are per node and do not sync. A `DHCPScope` is configured on the Primary only.
- The web console has the same constraint: reach it through the `<name>-primary` Service (`spec.service.primary`), which also keeps console edits on the Primary. It is unavailable while `<name>-0` is down.

### Upgrading Technitium

Changing `spec.image` performs a cluster-aware rolling upgrade rather than a blind restart. The StatefulSet uses `RollingUpdate` with a partition the operator manages: Secondaries restart one at a time from the highest ordinal down, and the Primary (`<name>-0`) restarts last. The operator releases the next node only once the one just restarted is on the new pod revision and Ready, its own cluster state reports the target version (skipped when the image tag has no parseable version, such as `latest` or a digest), and, for a Secondary, the Primary lists it as `Connected` at its new pod IP and has heard from it since the pod became Ready (right after a restart the Primary still shows the old entry as `Connected`). A single replica is simply replaced.

Progress is reported by the `Upgrading` condition (`kubectl get technitiumcluster -o wide` shows it): `True` with reason `RollingOut` and a message such as `Upgrading tc-2 to 15.6.1 (1/3 nodes updated)`, then `False` with reason `UpToDate`. The `Version` column reads `mixed` while nodes disagree. If the node being upgraded does not pass its gate for 10 minutes, the condition becomes `True` with reason `Stalled` (and `Degraded` gets reason `UpgradeStalled`); the operator releases no further node and resumes by itself if the node recovers. Any other change to the pod template (resources, env, probes) goes through the same gated walk.

Downgrades are refused, because a newer server may have written configuration to the data volume that an older one cannot read. When `spec.image` names an older version than the one deployed, the operator keeps the deployed image and reports `DowngradeBlocked` on the `Upgrading` and `Degraded` conditions. To allow it, annotate the cluster:

```sh
kubectl annotate technitiumcluster <name> dns.packet.fail/allow-downgrade=true
```

Versions that cannot be parsed from the image tag are never guarded.

### Deletion and PVC retention

Deleting a `TechnitiumCluster` is guarded by a finalizer, `dns.packet.fail/cluster-cleanup`, so the operator gets a chance to leave Technitium's own cluster state consistent before the StatefulSet, Services, and generated Secret are garbage collected via their owner references.

`spec.deletionPolicy` controls that step:

- `Delete` (default): the controller removes every Secondary from the Primary's cluster membership, then deletes the Primary's own cluster configuration. Every call is best-effort: the workload is being torn down regardless, so a failure (credentials gone, a node already unreachable) is logged and does not block deletion.
- `Orphan`: the in-Technitium cluster teardown is skipped entirely, leaving whatever cluster state exists. Use this when migrating or adopting the workload rather than decommissioning it.

`spec.storage.retentionPolicy` controls the fate of the data PVCs. StatefulSet `volumeClaimTemplate` PVCs are not garbage collected along with the StatefulSet itself, so this is an explicit decision rather than something owner references handle for you:

- `Retain` (default): the PVCs are left in place, and so is the generated `<name>-admin` Secret (its owner reference is released). Technitium applies the admin password only on a volume's first boot, so a cluster recreated with the same name must reuse that Secret to log in to the retained volumes; it re-adopts the Secret automatically. A caller-supplied `spec.adminSecretRef` Secret is never touched. If the Secret is lost while the PVCs survive, the new password is rejected and the cluster reports `Degraded` with reason `AdminCredentialsRejected`. Silently deleting a volume holding zone data and DNSSEC keys is a worse failure mode than an administrator later noticing an orphaned PVC and removing it by hand.
- `Delete`: the PVCs are removed as part of the same finalizer pass, and the generated admin Secret is garbage collected with the cluster, for a full, intentional teardown with no storage left behind.

```yaml
apiVersion: dns.packet.fail/v1alpha1
kind: TechnitiumCluster
metadata:
  name: dns
spec:
  deletionPolicy: Delete
  storage:
    size: 1Gi
    retentionPolicy: Retain
```

## Zone resource
A `Zone` is cluster-scoped (no namespace): a Technitium server has one global zone namespace, so `zoneName` must be unique across the whole cluster rather than per Kubernetes namespace. Every `Zone` names the `TechnitiumCluster` it belongs to via `spec.serverRef.name`; the reconciler resolves that instance's primary endpoint and admin credentials on its own, so nothing further needs configuring.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `zoneName` | string | Yes | Fully qualified DNS name of the zone, e.g. `example.com`. Immutable: the API server rejects any update that changes it. Delete and recreate the resource to rename a zone. |
| `serverRef.name` | string | Yes | Name of the `TechnitiumCluster` this zone is created on. The instance must be Ready. |
| `type` | enum | No (default `Primary`) | One of `Primary`, `Secondary`, `Stub`, `Forwarder`, `Catalog`. |
| `primaryNameServerAddresses` | []string | No | IP addresses or hostnames of the upstream primary. Used by `Secondary` and `Stub` zones, ignored by other types. |
| `forwarder` | string | No | Address of the upstream resolver for a `Forwarder` zone. The special value `this-server` forwards to the local DNS server. Ignored by other types. |
| `forwarderProtocol` | enum | No | Transport to the forwarder: `Udp`, `Tcp`, `Tls`, `Https`, `Quic`. Applies to `Forwarder` zones only; defaults to `Udp` on the server when unset. |
| `catalog` | string | No | Name of a catalog zone this zone joins as a member. Applies to `Primary`, `Secondary`, `Stub`, and `Forwarder` zones. Defaults to `cluster-catalog.<clusterDomain>` when the cluster has more than one replica; `""` opts out. |

Minimal Primary zone, once the `TechnitiumCluster` above is Ready:

```yaml
apiVersion: dns.packet.fail/v1alpha1
kind: Zone
metadata:
  name: example-com
spec:
  zoneName: example.com
  serverRef:
    name: dns
  type: Primary
```

```sh
kubectl apply -f zone.yaml
kubectl get zones
```

```
NAME          ZONE          TYPE      READY   AGE
example-com   example.com   Primary   True    12s
```

More examples, including a `Forwarder` zone, are in `config/samples/dns_v1alpha1_zone.yaml`.

## ExternalDNS webhook

The operator image doubles as an [ExternalDNS webhook provider](https://kubernetes-sigs.github.io/external-dns/latest/docs/tutorials/webhook-provider/). Run it as a sidecar next to ExternalDNS with `manager external-dns-webhook`. It never calls Technitium itself: it turns ExternalDNS endpoints into `Record` resources and lets the `Record` controller do the server-side work. ExternalDNS records therefore get the same treatment as hand-written ones:

- writes routed to the primary endpoint, with retries while it is down
- drift correction, finalizer-guarded cleanup and `deletionPolicy`
- visibility through `kubectl get records` and the Ready and Degraded conditions

The zones you pass with `--zone` must already exist on that `TechnitiumCluster`, for example as `Zone` resources: like any `Record`, the webhook's Records do not create their zone.

| Flag | Default | Description |
| --- | --- | --- |
| `--server-ref` | (required) | Name of the `TechnitiumCluster` Records are created for. |
| `--zone` | (at least one) | Zone the webhook manages. Repeat for several zones; an endpoint goes to the longest matching zone suffix and is rejected when none match. |
| `--namespace` | `POD_NAMESPACE`, then the ServiceAccount namespace | Namespace Records are created in. |
| `--owner-id` | `default` | Identifier stamped on the Records this webhook owns. Use a distinct value per webhook instance sharing a namespace. |
| `--listen` | `127.0.0.1:8888` | Address of the webhook API ExternalDNS calls. |
| `--health-listen` | `:8080` | Address serving `GET /healthz`. |

### RBAC

ExternalDNS's ServiceAccount needs access to `Record` resources in the namespace the webhook writes to. The operator chart can create that Role and RoleBinding:

```yaml
# technitium-operator chart values
externalDNS:
  rbac:
    create: true
    serviceAccount:
      name: external-dns        # the ServiceAccount ExternalDNS runs as
      namespace: external-dns   # defaults to the release namespace
    recordNamespace: dns        # where Records are written; defaults to the release namespace
```

Use the same namespace for `recordNamespace` and the webhook's `--namespace`.

### ExternalDNS Helm values

For the [kubernetes-sigs/external-dns chart](https://github.com/kubernetes-sigs/external-dns/tree/master/charts/external-dns). The chart appends `provider.webhook.args` to the image entrypoint (`/manager`), so the args start with the subcommand. Its default webhook probes hit `/healthz` on port 8080, which matches `--health-listen`.

```yaml
provider:
  name: webhook
  webhook:
    image:
      repository: ghcr.io/charg/technitium-kube-operator
      tag: 1.4.0 # x-release-please-version
    args:
      - external-dns-webhook
      - --server-ref=dns
      - --zone=home.example.com
      - --namespace=dns
sources:
  - service
  - ingress
domainFilters:
  - home.example.com
policy: sync # upsert-only never deletes Records
registry: txt
txtOwnerId: my-cluster
```

### Notes

- Supported record types are `A`, `AAAA`, `CNAME`, `TXT`, `NS`, `PTR` and `MX`. ExternalDNS only manages `A`, `AAAA` and `CNAME` by default, so add `managedRecordTypes: [A, AAAA, CNAME, MX, NS]` (the chart's `--managed-record-types`) when you need others.
- With `registry: txt`, every ownership TXT record becomes a `Record` of its own, which roughly doubles the number of resources. When the operator is the only writer to the zone, `registry: noop` avoids that: the Records already carry the owner labels.
- `policy: sync` deletes the `Record` when its source goes away, and the finalizer then removes it from the server (subject to `deletionPolicy`). `upsert-only` leaves it in place.
- Each target becomes its own `Record`, named `edns-<hash>` and labelled `app.kubernetes.io/managed-by=external-dns` and `dns.packet.fail/external-dns-owner=<owner-id>`. List them with `kubectl get records -l app.kubernetes.io/managed-by=external-dns`.
- Records have no owner references, because their sources live in other namespaces. They live as long as ExternalDNS keeps them.
- Records match their server record on name, type and data. A hand-written `Record` with the same name, type and data as an ExternalDNS one shares the server record, so deleting either one removes it until the other's drift reconcile restores it.

## Metrics

Technitium has no Prometheus endpoint of its own, so the operator polls each ready node's dashboard API (`/api/dashboard/stats/get`, last hour, that node only) in the background and republishes the values on its own metrics endpoint (`--metrics-bind-address`). Each node is read directly through its headless-Service address with the cluster's minted API token, so there is one series set per pod. Polling runs only on the elected leader.

The query counters are aggregates over Technitium's rolling last-hour window, so they are **gauges that rise and fall as the window slides, not monotonic counters**. Do not wrap them in `rate()`; use them as "queries in the last hour" (or `delta()`/`deriv()` for a trend). The window has up to one polling interval of lag.

All metrics carry the labels `cluster` (TechnitiumCluster name) and `node` (pod name):

| Metric | Extra label | Meaning |
| --- | --- | --- |
| `technitium_dns_queries_last_hour` | `result`: `no_error`, `server_failure`, `nx_domain`, `refused` | Queries by response code |
| `technitium_dns_queries_by_source_last_hour` | `source`: `authoritative`, `recursive`, `cached`, `blocked`, `dropped` | Queries by how they were answered |
| `technitium_dns_clients_last_hour` | | Distinct clients |
| `technitium_dns_cache_entries` | | Entries in the DNS cache |
| `technitium_dns_zones` | | Hosted zones |
| `technitium_dns_allowed_zones`, `technitium_dns_blocked_zones` | | Domains in the allowed / blocked zones |
| `technitium_dns_allow_list_zones`, `technitium_dns_block_list_zones` | | Domains loaded from allow / block lists |
| `technitium_dns_stats_scrape_success` | | 1 if the last read of the node succeeded, else 0 |
| `technitium_dns_stats_last_success_timestamp_seconds` | | Unix time of the last successful read |

When a node is not ready or cannot be read, its data series are removed (rather than left at their last value) and `technitium_dns_stats_scrape_success` is set to 0. Series for deleted clusters and nodes are removed. A cluster that has not been bootstrapped yet (no token) publishes nothing.

Example, the blocked share of the last hour's queries per node:

```promql
technitium_dns_queries_by_source_last_hour{source="blocked"}
  / ignoring(source) sum without (source) (technitium_dns_queries_by_source_last_hour)
```

Set `--technitium-stats-interval` (default `60s`) to change how often nodes are polled, or `0` to disable the collector.

A ready-made Grafana dashboard for these metrics, plus the operator's own reconcile metrics, is in [`grafana/technitium-dns.json`](grafana/technitium-dns.json). Import it through **Dashboards → New → Import** and pick your Prometheus data source.

## Getting Started

### Prerequisites
- go version v1.24.6+
- docker version 17.03+.
- kubectl version v1.11.3+.
- [just](https://just.systems) command runner.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
just docker-build <some-registry>/technitium-operator:tag
just docker-push <some-registry>/technitium-operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
just install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
just deploy <some-registry>/technitium-operator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
just uninstall
```

**UnDeploy the controller from the cluster:**

```sh
just undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
just build-installer <some-registry>/technitium-operator:tag
```

**NOTE:** The recipe mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/technitium-operator/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v2-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.
