# Alfred

Is your favorite butler, who's able to run and monitor jobs to be executed on virtual machines in the cloud.

## Setup dev environment

### Required tools

- Standard tools (`make`, `docker`)
- [Go 1.21+](https://go.dev/doc/install)
- [Reflex](https://github.com/cespare/reflex)
  - `go install github.com/cespare/reflex@latest`
- https://grpc.io/docs/languages/go/quickstart
  - Download the [protoc compiler](https://github.com/protocolbuffers/protobuf/releases/tag/latest)
  - Unpack the zip
  - Move the `bin/protoc` file to `/usr/local/bin/protoc`
  - Move the `include/google` folder to `/usr/local/include/google`
  - Install the golang plugins :
    - `go install google.golang.org/protobuf/cmd/protoc-gen-go@latest`
    - `go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`
  - Add `$(go env GOPATH)/bin` to your `$PATH`

### OpenStack credentials

To run alfred server with the OpenStack provisioner, you need credentials to access the OpenStack API.
Ask [@BastienClement](https://github.com/BastienClement).

### Remote configuration

By default, the `alfred` client will connect to the production server. 

For development, you can override the default remote by setting the `ALFRED_REMOTE` environment variable to point to your local server.
