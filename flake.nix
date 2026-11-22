{
  description = "Kubernetes operator for Technitium DNS Server";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShellNoCC {
          packages = [
            # Task runner and git hooks.
            pkgs.just
            pkgs.prek
            # Lint (hack/lint.sh).
            pkgs.actionlint
            pkgs.nixfmt
            pkgs.shellcheck
            pkgs.yamllint
            pkgs.zizmor
            # go flavour (hack/lint.d/go.sh). The Go golangci-lint is built with, which can be newer than
            # pkgs.go: golangci-lint refuses a module whose go line is newer than its own Go.
            pkgs.golangci-lint.go
            pkgs.golangci-lint
            # container-images flavour: builds through the host's docker daemon with buildx, so
            # multi-arch builds work the same everywhere; lint in hack/lint.d/container-images.sh.
            pkgs.docker-client
            pkgs.docker-buildx
            pkgs.hadolint
            # helm-charts flavour (hack/lint.d/helm-charts.sh). helm-unittest is a helm plugin;
            # wrapHelm puts it on HELM_PLUGINS so `helm unittest` works without an install step.
            (pkgs.wrapHelm pkgs.kubernetes-helm {
              plugins = [ pkgs.kubernetes-helmPlugins.helm-unittest ];
            })
            pkgs.chart-testing
            pkgs.yamale
            pkgs.kubeconform
            # Used by the scripts directly.
            pkgs.git
            # Project: kubebuilder scaffolding, manifests, envtest and the kind e2e cluster.
            # controller-gen, setup-envtest and the plugin build of golangci-lint are pinned in the
            # justfile and installed to bin/.
            pkgs.gotools
            pkgs.jq
            pkgs.kubebuilder
            pkgs.kustomize
            pkgs.kubectl
            pkgs.kind
          ];
          # go flavour: the shell has no C compiler (mkShellNoCC), so Go builds without cgo, as static
          # binaries for containers usually are. A project that needs cgo adds pkgs.gcc and drops this.
          CGO_ENABLED = "0";
        };
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
