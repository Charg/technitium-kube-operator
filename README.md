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

### Writes with multiple replicas

With `spec.replicas` greater than 1, `<name>-0` initializes a Technitium cluster as the Primary and every other pod joins as a Secondary. Technitium does not reliably reject writes sent to a Secondary: zone, settings and DHCP changes are applied locally and never reach the Primary, and the Primary's next config sync overwrites local settings. The operator therefore sends every write, and bootstraps the admin token, through `primaryEndpoint`.

As a result:

- While `<name>-0` is down, writes fail and are retried. Technitium does not promote a Secondary automatically. Secondaries keep serving DNS from what they last synced.
- A zone reaches the Secondaries only if it is a member of the cluster's catalog zone. A `Primary`, `Secondary`, `Stub` or `Forwarder` `Zone` joins `cluster-catalog.<clusterDomain>` automatically. Set `spec.catalog: ""` to keep a zone on the Primary only (this removes it from any catalog, and queries that land on a Secondary fail for it), or name another catalog to override. Scaling back to one replica leaves existing membership in place.
- Global settings (`ServerSettings`, block list URLs) and allowed/blocked entries sync to the Secondaries.
- DHCP scopes are per node and do not sync. A `DHCPScope` is configured on the Primary only.

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
