# Image URL used by the image build/push/deploy recipes
img := env_var_or_default("IMG", "controller:latest")
# Year substituted for the YEAR placeholder in the boilerplate header
year := env_var_or_default("YEAR", `date +%Y`)
# Container tool used to build/push images
container_tool := env_var_or_default("CONTAINER_TOOL", "docker")
# Target platforms for the cross-platform image build
platforms := env_var_or_default("PLATFORMS", "linux/arm64,linux/amd64,linux/s390x,linux/ppc64le")
# Name of the Kind cluster used for e2e tests
kind_cluster := env_var_or_default("KIND_CLUSTER", "technitium-operator-test-e2e")

kubectl := env_var_or_default("KUBECTL", "kubectl")
kind := env_var_or_default("KIND", "kind")

# Where local tool binaries are installed
localbin := justfile_directory() / "bin"

kustomize_version := "v5.8.1"
controller_tools_version := "v0.22.0"
golangci_lint_version := "v2.12.2"

kustomize := localbin / "kustomize"
controller_gen := localbin / "controller-gen"
envtest := localbin / "setup-envtest"
golangci_lint := localbin / "golangci-lint"

set shell := ["bash", "-euo", "pipefail", "-c"]

# List available recipes
[group('General')]
default:
    @just --list

##
## Development
##

# Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects
[group('Development')]
manifests: controller-gen
    "{{ controller_gen }}" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

# Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations
[group('Development')]
generate: controller-gen
    "{{ controller_gen }}" object:headerFile="hack/boilerplate.go.txt",year={{ year }} paths="./..."

# Run go fmt against code
[group('Development')]
fmt:
    go fmt ./...

# Run go vet against code
[group('Development')]
vet:
    go vet ./...

# Run unit tests
[group('Development')]
test: manifests generate fmt vet setup-envtest
    #!/usr/bin/env bash
    set -euo pipefail
    eval "$(just _envtest-versions)"
    export KUBEBUILDER_ASSETS="$("{{ envtest }}" use "${ENVTEST_K8S_VERSION}" --bin-dir "{{ localbin }}" -p path)"
    go test $(go list ./... | grep -v /e2e) -coverprofile cover.out

# Set up a Kind cluster for e2e tests if it does not exist
[group('Development')]
setup-test-e2e:
    #!/usr/bin/env bash
    set -euo pipefail
    if ! command -v "{{ kind }}" >/dev/null 2>&1; then
        echo "Kind is not installed. Please install Kind manually."
        exit 1
    fi
    if "{{ kind }}" get clusters | grep -qx "{{ kind_cluster }}"; then
        echo "Kind cluster '{{ kind_cluster }}' already exists. Skipping creation."
    else
        echo "Creating Kind cluster '{{ kind_cluster }}'..."
        "{{ kind }}" create cluster --name "{{ kind_cluster }}"
    fi

# Run the e2e tests. Expects an isolated environment using Kind
[group('Development')]
test-e2e: setup-test-e2e manifests generate fmt vet
    KIND="{{ kind }}" KIND_CLUSTER="{{ kind_cluster }}" go test -tags=e2e ./test/e2e/ -v -ginkgo.v
    @just cleanup-test-e2e

# Tear down the Kind cluster used for e2e tests
[group('Development')]
cleanup-test-e2e:
    @"{{ kind }}" delete cluster --name "{{ kind_cluster }}"

# Run golangci-lint
[group('Development')]
lint: golangci-lint
    "{{ golangci_lint }}" run

# Run golangci-lint and apply fixes
[group('Development')]
lint-fix: golangci-lint
    "{{ golangci_lint }}" run --fix

# Verify golangci-lint configuration
[group('Development')]
lint-config: golangci-lint
    "{{ golangci_lint }}" config verify

##
## Build
##

# Build manager binary
[group('Build')]
build: manifests generate fmt vet
    go build -o bin/manager cmd/main.go

# Run a controller from your host
[group('Build')]
run: manifests generate fmt vet
    go run ./cmd/main.go

# Build docker image with the manager
[group('Build')]
docker-build tag=img:
    "{{ container_tool }}" build -t "{{ tag }}" .

# Push docker image with the manager
[group('Build')]
docker-push tag=img:
    "{{ container_tool }}" push "{{ tag }}"

# Build and push docker image for the manager for cross-platform support
[group('Build')]
docker-buildx tag=img:
    #!/usr/bin/env bash
    set -euo pipefail
    sed -e '1 s/\(^FROM\)/FROM --platform=${BUILDPLATFORM}/; t' -e ' 1,// s//FROM --platform=${BUILDPLATFORM}/' Dockerfile > Dockerfile.cross
    "{{ container_tool }}" buildx create --name technitium-operator-builder || true
    "{{ container_tool }}" buildx use technitium-operator-builder
    "{{ container_tool }}" buildx build --push --platform="{{ platforms }}" --tag "{{ tag }}" -f Dockerfile.cross . || true
    "{{ container_tool }}" buildx rm technitium-operator-builder || true
    rm Dockerfile.cross

# Generate a consolidated YAML with CRDs and deployment
[group('Build')]
build-installer tag=img: manifests generate kustomize
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p dist
    (cd config/manager && "{{ kustomize }}" edit set image controller="{{ tag }}")
    "{{ kustomize }}" build config/default > dist/install.yaml

##
## Deployment
##

# Install CRDs into the K8s cluster specified in ~/.kube/config
[group('Deployment')]
install: manifests kustomize
    #!/usr/bin/env bash
    set -euo pipefail
    out="$("{{ kustomize }}" build config/crd 2>/dev/null || true)"
    if [ -n "$out" ]; then echo "$out" | "{{ kubectl }}" apply -f -; else echo "No CRDs to install; skipping."; fi

# Uninstall CRDs from the K8s cluster specified in ~/.kube/config
[group('Deployment')]
uninstall ignore-not-found="false": manifests kustomize
    #!/usr/bin/env bash
    set -euo pipefail
    out="$("{{ kustomize }}" build config/crd 2>/dev/null || true)"
    if [ -n "$out" ]; then echo "$out" | "{{ kubectl }}" delete --ignore-not-found="{{ ignore-not-found }}" -f -; else echo "No CRDs to delete; skipping."; fi

# Deploy the controller to the K8s cluster specified in ~/.kube/config
[group('Deployment')]
deploy tag=img: manifests kustomize
    #!/usr/bin/env bash
    set -euo pipefail
    (cd config/manager && "{{ kustomize }}" edit set image controller="{{ tag }}")
    "{{ kustomize }}" build config/default | "{{ kubectl }}" apply -f -

# Undeploy the controller from the K8s cluster specified in ~/.kube/config
[group('Deployment')]
undeploy ignore-not-found="false": kustomize
    "{{ kustomize }}" build config/default | "{{ kubectl }}" delete --ignore-not-found="{{ ignore-not-found }}" -f -

##
## Scaffolding
##

# Scaffold a new API/controller, e.g. `just create-api dns v1alpha1 Zone`
[group('Scaffolding')]
create-api group version kind:
    kubebuilder create api --group {{ group }} --version {{ version }} --kind {{ kind }}

##
## Local cluster
##

# Create a local kind cluster for manual testing
[group('Local cluster')]
kind-up name="technitium-operator":
    "{{ kind }}" create cluster --name {{ name }}

# Delete the local kind cluster
[group('Local cluster')]
kind-down name="technitium-operator":
    "{{ kind }}" delete cluster --name {{ name }}

##
## Misc
##

# Remove build artifacts and local tool cache
[group('Misc')]
clean:
    rm -rf bin dist

##
## Dependencies (not shown in --list)
##

# Download kustomize locally if necessary
[group('Dependencies')]
kustomize:
    @just _install-tool kustomize {{ kustomize_version }} sigs.k8s.io/kustomize/kustomize/v5

# Download controller-gen locally if necessary
[group('Dependencies')]
controller-gen:
    @just _install-tool controller-gen {{ controller_tools_version }} sigs.k8s.io/controller-tools/cmd/controller-gen

# Download setup-envtest locally if necessary
[group('Dependencies')]
envtest:
    #!/usr/bin/env bash
    set -euo pipefail
    eval "$(just _envtest-versions)"
    just _install-tool setup-envtest "${ENVTEST_VERSION}" sigs.k8s.io/controller-runtime/tools/setup-envtest

# Download the envtest binaries required for the Kubernetes version derived from go.mod
[group('Dependencies')]
setup-envtest: envtest
    #!/usr/bin/env bash
    set -euo pipefail
    eval "$(just _envtest-versions)"
    echo "Setting up envtest binaries for Kubernetes version ${ENVTEST_K8S_VERSION}..."
    "{{ envtest }}" use "${ENVTEST_K8S_VERSION}" --bin-dir "{{ localbin }}" -p path

# Download golangci-lint locally if necessary
[group('Dependencies')]
golangci-lint:
    #!/usr/bin/env bash
    set -euo pipefail
    just _install-tool golangci-lint {{ golangci_lint_version }} github.com/golangci/golangci-lint/v2/cmd/golangci-lint
    if [ -f .custom-gcl.yml ]; then
        echo "Building custom golangci-lint with plugins..."
        "{{ golangci_lint }}" custom --destination "{{ localbin }}" --name golangci-lint-custom
        mv -f "{{ localbin }}/golangci-lint-custom" "{{ golangci_lint }}"
    fi

# Print ENVTEST_VERSION and ENVTEST_K8S_VERSION derived from go.mod
_envtest-versions:
    #!/usr/bin/env bash
    set -euo pipefail
    cr_version=$(go list -m -json sigs.k8s.io/controller-runtime | jq -r 'if .Replace then .Replace.Version else .Version end')
    if [ -z "${cr_version}" ] || [ "${cr_version}" = "null" ]; then
        echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2
        exit 1
    fi
    api_version=$(go list -m -json k8s.io/api | jq -r 'if .Replace then .Replace.Version else .Version end')
    if [ -z "${api_version}" ] || [ "${api_version}" = "null" ]; then
        echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2
        exit 1
    fi
    echo "ENVTEST_VERSION=${cr_version}"
    echo "ENVTEST_K8S_VERSION=$(echo "${api_version}" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')"

# Install a Go tool binary, pinned to a specific version via a symlink
_install-tool bin version package:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p "{{ localbin }}"
    target="{{ localbin }}/{{ bin }}"
    if [ -f "${target}-{{ version }}" ] && [ "$(readlink -- "${target}" 2>/dev/null)" = "${target}-{{ version }}" ]; then
        exit 0
    fi
    echo "Downloading {{ package }}@{{ version }}"
    rm -f "${target}"
    GOBIN="{{ localbin }}" go install "{{ package }}@{{ version }}"
    mv "{{ localbin }}/{{ bin }}" "${target}-{{ version }}"
    ln -sf "$(realpath "${target}-{{ version }}")" "${target}"
