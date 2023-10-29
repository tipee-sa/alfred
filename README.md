# Alfred

Is a your favorite butler, who's able to run and monitor jobs to be executed on virtual machines in the cloud.

## Setup dev environment

### Required tools

- Go 1.21+
- Standard tools (`make`, `docker`)
- `go install github.com/cespare/reflex@latest`
- https://grpc.io/docs/languages/go/quickstart

### OpenStack credentials

To run alfred server with the OpenStack provisioner, you need credentials to access the OpenStack API.
Ask @BastienClement.

### Remote configuration

By default, `alfred` will connect to the production server. 

For development, you can override the default remote by setting the `ALFRED_REMOTE` environment variable to point to your local server.
