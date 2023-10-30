PROVISIONER ?= local

.PHONY: bin/alfred-client # TODO: proper dependencies
bin/alfred-client:
	go build -o $@ ./client

.PHONY: bin/alfred-server # TODO: proper dependencies
bin/alfred-server:
	go build -o $@ ./server

.PHONY: proto
proto:
	protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/alfred.proto

.PHONY: run-server
run-server: proto
	reflex -s -t 120s -r '\.go' -- bash -c "make bin/alfred-server && bin/alfred-server"
