# Pinned to the version recorded in proto/alfred.pb.go, so regenerating the
# protobuf code leaves it unchanged.
{ pkgs, ... }:
pkgs.buildGoModule (finalAttrs: {
  pname = "protoc-gen-go";
  version = "1.31.0";

  src = pkgs.fetchFromGitHub {
    owner = "protocolbuffers";
    repo = "protobuf-go";
    tag = "v${finalAttrs.version}";
    hash = "sha256-wKJYy/9Bld6GXM1VFYXEs9//Y27eLrqDdw+a9P9EwfU=";
  };

  vendorHash = "sha256-yb8l4ooZwqfvenlxDRg95rqiL+hmsn0weS/dPv/oD2Y=";
  subPackages = [ "cmd/protoc-gen-go" ];

  meta.mainProgram = "protoc-gen-go";
})
