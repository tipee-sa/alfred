{ pkgs, perSystem }:
pkgs.mkShell {
  packages = with pkgs; [
    go
    gopls
    just
    just-lsp
    reflex
    zstd
    protobuf
    perSystem.self.protoc-gen-go
    perSystem.self.protoc-gen-go-grpc
  ];
}
