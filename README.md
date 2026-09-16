# technitium-operator
A Kubernetes operator that manages Technitium DNS Server zones as custom resources.

## Description
The operator reconciles `Zone` resources (API group `dns.packet.fail/v1alpha1`) against a Technitium DNS Server, keeping DNS zones declared in the cluster in sync with the server. Manage DNS state with kubectl and GitOps instead of the Technitium admin console.

## Configuration
The operator needs a Technitium DNS Server URL and credentials to reconcile `Zone` resources. Credentials are read from a Kubernetes Secret, never from a CR or ConfigMap.

| Flag | Env var | Meaning | Required |
| --- | --- | --- | --- |
| `--technitium-url` | `TECHNITIUM_URL` | Base URL of the Technitium DNS Server, e.g. `https://dns.internal:5380`. | Yes |
| `--technitium-credentials-secret` | `TECHNITIUM_CREDENTIALS_SECRET` | Name of the Secret holding credentials. | Yes |
| `--technitium-credentials-namespace` | `TECHNITIUM_CREDENTIALS_NAMESPACE` (falls back to `POD_NAMESPACE`) | Namespace of the credentials Secret. Defaults to the operator's own namespace. | No |
| `--technitium-insecure-skip-verify` | `TECHNITIUM_INSECURE_SKIP_VERIFY` | Skip TLS verification (self-signed certs). | No |

A flag overrides its env var when both are set.

The credentials Secret must contain either a `token` key (a pre-created Technitium API token) or both `username` and `password` keys.

Token form:

```sh
kubectl create secret generic technitium-creds \
  --from-literal=token=<api-token>
```

Username/password is the alternative:

```sh
kubectl create secret generic technitium-creds \
  --from-literal=username=<user> \
  --from-literal=password=<pass>
```

Helm install, setting the URL and pointing at the Secret:

```sh
helm install technitium-operator ./charts/technitium-operator \
  --set technitium.url=https://dns.internal:5380 \
  --set technitium.existingSecret=technitium-creds
```

The manager exits at startup with a descriptive error if the URL or credentials Secret is missing.

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
