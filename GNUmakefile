# Dev workflow for terraform-provider-ferentin.
#
# `make install` builds and drops the binary into ~/.terraform.d/plugins/
# under a dev namespace, then your `~/.terraformrc` `dev_overrides` block
# (see DEVELOPMENT.md) makes `terraform` find it without registry round-trips.

BINARY      := terraform-provider-ferentin
HOSTNAME    := registry.terraform.io
NAMESPACE   := ferentin-net
NAME        := ferentin
VERSION     := dev
OS_ARCH     := $(shell go env GOOS)_$(shell go env GOARCH)
INSTALL_DIR := $(HOME)/.terraform.d/plugins/$(HOSTNAME)/$(NAMESPACE)/$(NAME)/$(VERSION)/$(OS_ARCH)

.PHONY: build install fmt lint vet test testacc tidy clean

build:
	go build -o $(BINARY) .

install: build
	mkdir -p $(INSTALL_DIR)
	mv $(BINARY) $(INSTALL_DIR)/

fmt:
	gofmt -s -w .

vet:
	go vet ./...

test:
	go test ./... -count=1 -timeout 60s

# TF_ACC must be set to opt in to network-bound acceptance tests; otherwise
# they skip. Set TF_LOG=DEBUG to see provider HTTP traffic.
testacc:
	TF_ACC=1 go test ./internal/provider -v -count=1 -timeout 30m

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)
