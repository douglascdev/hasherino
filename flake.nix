{
  description = "hasherino-go";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
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
            go_1_27
            gcc
            pkg-config
          ];

          buildInputs = with pkgs; [
            libGL
            libGLU
            libX11
            libXcursor
            libXrandr
            libXinerama
            libXi
            libXxf86vm
            libxkbcommon
            wayland
          ];

          CGO_ENABLED = "1";
        };
      }
    );
}
