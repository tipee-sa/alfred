# The alfred client, built from source; consumers take it as a flake input.
{ pkgs, flake, ... }:
let
  buildInfo = import ../build-info.nix { inherit flake; };
in
pkgs.buildGoModule {
  pname = "alfred";
  inherit (buildInfo) version;

  src = flake;

  vendorHash = "sha256-etgxw9bRWIMYe270DonBqf4kVa66NteQPqJn3AWN+io=";
  # The default name embeds the version, which changes with every commit and
  # would refetch every module.
  overrideModAttrs = {
    name = "alfred-go-modules";
  };

  subPackages = [ "client" ];

  env.CGO_ENABLED = "0";

  # `go test ./...` runs in CI; consumers build this from source and need not.
  doCheck = false;

  ldflags = [
    "-s"
    "-w"
    "-X main.version=${buildInfo.version}"
    "-X main.commit=${buildInfo.revision}"
  ];

  postInstall = ''
    mv $out/bin/client $out/bin/alfred
  '';

  meta = {
    description = "Client for Alfred, the tipee CI job runner";
    mainProgram = "alfred";
  };
}
