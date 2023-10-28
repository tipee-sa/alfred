PROVISIONER ?= local

.PHONY: play
play:
	go build -o bin/playground ./playground/playground.go
	PROVISIONER=$(PROVISIONER) ./bin/playground
