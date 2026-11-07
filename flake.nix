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
        pkgsAndroid = import nixpkgs {
          inherit system;
          config = {
            allowUnfree = true;
            android_sdk.accept_license = true;
          };
        };
        androidPackages = pkgsAndroid.androidenv.composeAndroidPackages {
          cmdLineToolsVersion = "latest";
          platformToolsVersion = "latest";
          buildToolsVersions = [ "latest" ];
          platformVersions = [ "36" ];
          includeEmulator = false;
          includeSystemImages = false;
          includeSources = false;
          includeCmake = false;
          includeNDK = true;
          ndkVersion = "latest";
        };
        androidSdk = androidPackages.androidsdk;
      in
      {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go_1_27
            gcc
            pkg-config
            sqlite

            # Flatpak packaging
            flatpak
            flatpak-builder
            ostree
            appstream
            desktop-file-utils
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

            # gio
            vulkan-loader
            vulkan-headers
            libxcb
          ];

          CGO_ENABLED = "1";
        };

        devShells.windows = pkgs.mkShell {
          packages = with pkgs; [
            go_1_27
            librsvg
            pkgs.pkgsCross.mingwW64.stdenv.cc
          ];

          CGO_ENABLED = "1";
        };

        devShells.android = pkgsAndroid.mkShell {
          packages = with pkgsAndroid; [
            go_1_27
            jdk17
            librsvg
            androidPackages.androidsdk
          ];

          ANDROID_HOME = "${androidSdk}/libexec/android-sdk";
          ANDROID_NDK_ROOT = "${androidSdk}/libexec/android-sdk/ndk-bundle";
          JAVA_HOME = "${pkgsAndroid.jdk17}";
        };
      }
    );
}
