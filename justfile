set shell := ["bash", "-euo", "pipefail", "-c"]

# List available recipes
default:
    @just --list

# Run go fmt, go vet, and generate manifests/deepcopy code
generate:
    make manifests generate fmt vet

# Build the manager binary
build: generate
    make build

# Run the controller locally against the current kubeconfig context
run: generate
    make run

# Run unit tests (envtest)
test:
    make test

# Run end-to-end tests against a local kind cluster
test-e2e:
    make test-e2e

# Tear down the kind cluster used for e2e tests
test-e2e-cleanup:
    make cleanup-test-e2e

# Lint the codebase
lint:
    make lint

# Lint and auto-fix
lint-fix:
    make lint-fix

# Build the manager container image
docker-build tag="controller:latest":
    make docker-build IMG={{tag}}

# Push the manager container image
docker-push tag="controller:latest":
    make docker-push IMG={{tag}}

# Install CRDs into the cluster pointed to by the current kubeconfig
install:
    make install

# Remove CRDs from the cluster pointed to by the current kubeconfig
uninstall:
    make uninstall

# Deploy the controller to the cluster pointed to by the current kubeconfig
deploy tag="controller:latest":
    make deploy IMG={{tag}}

# Remove the controller from the cluster pointed to by the current kubeconfig
undeploy:
    make undeploy

# Render a single consolidated install manifest (CRDs + controller)
build-installer tag="controller:latest":
    make build-installer IMG={{tag}}

# Scaffold a new API/controller, e.g. `just create-api group=dns version=v1alpha1 kind=Zone`
create-api group version kind:
    kubebuilder create api --group {{group}} --version {{version}} --kind {{kind}}

# Create a local kind cluster for manual testing
kind-up name="technitium-operator":
    kind create cluster --name {{name}}

# Delete the local kind cluster
kind-down name="technitium-operator":
    kind delete cluster --name {{name}}

# Remove build artifacts and local tool cache
clean:
    rm -rf bin dist
