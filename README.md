# Alfred

Is your favorite butler, who's able to run and monitor jobs to be executed on virtual machines in the cloud.

## Dependencies

- bash
- docker
- ssh
- tar
- zstd

## Setup

1. Install the required dependencies.
2. Download Alfred's [client binary](../../releases/latest) and put it in your `$PATH`.
3. Your SSH key must be added to Alfred remote server by an administrator. Ask [@gnutix](https://github.com/gnutix).
4. Add the Alfred host key to your known_hosts: `ssh-keyscan -H alfred.tipee.dev >> ~/.ssh/known_hosts`
5. You should now be able to run `alfred version` and see the version of the remote server.

### Usage

Run `alfred --help` to see the list of available commands and options.

## Setup dev environment

### Required tools

- `make`
- [Go 1.21+](https://go.dev/doc/install)
- [Reflex](https://github.com/cespare/reflex)
  - `go install github.com/cespare/reflex@latest`
- [Protocol Buffer](https://grpc.io/docs/protoc-installation/) (needed for [gRPC](https://grpc.io/))
  - Download the [protoc compiler](https://github.com/protocolbuffers/protobuf/releases/latest) archive, unpack it
  - Move the `bin/protoc` file to `/usr/local/bin/protoc`
  - Move the `include/google` folder to `/usr/local/include/google`
  - Install Go plugins :
    - `go install google.golang.org/protobuf/cmd/protoc-gen-go@latest`
    - `go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`
  - If not done already, add `$(go env GOPATH)/bin` to your `$PATH`

### Run alfred server in local

#### Commands

Run the server:
```shell
make bin/alfred-server
bin/alfred-server [options...]
```

Run the server (with auto-reload):
```shell
make run-server
```

#### Remote configuration

By default, the `alfred` client will connect to the production server.

For development, you can override the default remote by setting the `ALFRED_REMOTE="localhost:25373"` environment variable to point to your local server.
You can also pass the `--remote localhost:25373` to `alfred`'s commands each time.

#### OpenStack credentials

To run alfred server with the OpenStack provisioner, you need credentials to access the OpenStack API.
Ask [@BastienClement](https://github.com/BastienClement).

## Project maintenance

### Releasing the client binary

Releasing a new version of the client binary is done by creating a release on GitHub. The binaries are automatically
created for different platforms by a GitHub Action.

### Deploying the server binary

Deployment is done manually using a GitHub Action. **Beware that running jobs are stopped during the deployment.**

### Updating Alfred nodes' image

The Alfred nodes' image is maintained using [Packer](https://www.packer.io/) in a separate repository.
Ask [@BastienClement](https://github.com/BastienClement), he's the only one who can do it for now...
