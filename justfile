# Build binaries
build: build-server build-client

build-server:
    go build -o bin/alfred-server ./server

build-client:
    go build -o bin/alfred ./client

# Run the server locally (local Docker provisioner)
server: build-server
    bin/alfred-server

# Run the server with auto-reload on code changes (requires reflex)
dev-server:
    reflex -s -t 120s \
        -R '^.github/' -R '^client/' -R '^hack/' -R '^workspace/' \
        -r '\.go' -- bash -c "just build-server && bin/alfred-server"

# Run a test job against a local server
run *args: build-client
    bin/alfred --remote localhost run hack/test-job.yaml {{args}}

# Run tests
test:
    go test -v ./...

# Regenerate protobuf
proto:
    protoc -I . -I /usr/local/include \
        --go_out=. --go_opt=paths=source_relative \
        --go-grpc_out=. --go-grpc_opt=paths=source_relative \
        proto/alfred.proto
