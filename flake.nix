{
  description = "Kubernetes operator for Technitium DNS Server";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
      in
      {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gotools
            golangci-lint
            jq
            just
            kubebuilder
            kustomize
            kubectl
            kind
            docker-client
          ];

          shellHook = ''
            export GOBIN="$PWD/bin"
            export PATH="$GOBIN:$PATH"
          '';
        };
      });
}
