# Pinned to the version recorded in proto/alfred_grpc.pb.go, so regenerating
# the gRPC stubs leaves them unchanged.
{ pkgs, ... }:
pkgs.buildGoModule (finalAttrs: {
  pname = "protoc-gen-go-grpc";
  version = "1.3.0";

  src = pkgs.fetchFromGitHub {
    owner = "grpc";
    repo = "grpc-go";
    tag = "cmd/protoc-gen-go-grpc/v${finalAttrs.version}";
    hash = "sha256-Zy0k5X/KFzCao9xAGt5DNb0MMGEyqmEsDj+uvXI4xH4=";
  };

  modRoot = "cmd/protoc-gen-go-grpc";
  vendorHash = "sha256-y+/hjYUTFZuq55YAZ5M4T1cwIR+XFQBmWVE+Cg1Y7PI=";

  meta.mainProgram = "protoc-gen-go-grpc";
})
