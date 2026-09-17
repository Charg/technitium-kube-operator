# technitium-operator
A Kubernetes operator that manages Technitium DNS Server zones as custom resources.

## Description
The operator provisions Technitium DNS Server instances (`TechnitiumCluster`, API group `dns.packet.fail/v1alpha1`) and reconciles `Zone` resources against them, keeping DNS zones declared in the cluster in sync with the server. Manage DNS state with kubectl and GitOps instead of the Technitium admin console.

## TechnitiumCluster resource
A `TechnitiumCluster` provisions a Technitium DNS Server instance: a StatefulSet, a client Service, and (unless `spec.adminSecretRef` is set) a generated `<name>-admin` Secret holding its credentials. It is cluster-scoped, and its status reports `endpoint` once the instance is reachable.

```yaml
apiVersion: dns.packet.fail/v1alpha1
kind: TechnitiumCluster
metadata:
  name: dns
spec:
  storage:
    size: 1Gi
```

## Zone resource
A `Zone` is cluster-scoped (no namespace): a Technitium server has one global zone namespace, so `zoneName` must be unique across the whole cluster rather than per Kubernetes namespace. Every `Zone` names the `TechnitiumCluster` it belongs to via `spec.serverRef.name`; the reconciler resolves that instance's endpoint and admin credentials on its own, so nothing further needs configuring.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `zoneName` | string | Yes | Fully qualified DNS name of the zone, e.g. `example.com`. Immutable: the API server rejects any update that changes it. Delete and recreate the resource to rename a zone. |
| `serverRef.name` | string | Yes | Name of the `TechnitiumCluster` this zone is created on. The instance must be Ready. |
| `type` | enum | No (default `Primary`) | One of `Primary`, `Secondary`, `Stub`, `Forwarder`, `Catalog`. |
| `primaryNameServerAddresses` | []string | No | IP addresses or hostnames of the upstream primary. Used by `Secondary` and `Stub` zones, ignored by other types. |
| `forwarder` | string | No | Address of the upstream resolver for a `Forwarder` zone. The special value `this-server` forwards to the local DNS server. Ignored by other types. |
| `forwarderProtocol` | enum | No | Transport to the forwarder: `Udp`, `Tcp`, `Tls`, `Https`, `Quic`. Applies to `Forwarder` zones only; defaults to `Udp` on the server when unset. |
| `catalog` | string | No | Name of an existing catalog zone this zone joins as a member. Applies to `Primary`, `Secondary`, `Stub`, and `Forwarder` zones. |

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
