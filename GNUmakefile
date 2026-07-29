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

.PHONY: build install fmt lint vet test testacc tidy clean docs docs-check

build:
	go build -o $(BINARY) .

install: build
	mkdir -p $(INSTALL_DIR)
	mv $(BINARY) $(INSTALL_DIR)/

fmt:
	gofmt -s -w .

lint:
	golangci-lint run ./...

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

# Regenerate docs/ from schema MarkdownDescription + examples/. Run after any
# schema change. Output is committed to the repo so the registry can render
# it without rebuilding.
docs:
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs \
		generate --provider-name ferentin --rendered-provider-name "Ferentin"

# CI guard: regenerate docs into a tmp dir and `diff` against the committed
# copy. Fails when schema changes haven't been followed by `make docs`.
# NOTE: --rendered-website-dir is resolved RELATIVE TO the provider directory,
# so it must not be an absolute path. Passing an absolute mktemp path made
# tfplugindocs write a stray ./var/folders/... tree into the repo and left the
# diff target nonexistent — this gate could never pass. Use a repo-relative
# scratch dir and always clean it up.
DOCS_CHECK_DIR := .docs-check-tmp

docs-check:
	@rm -rf $(DOCS_CHECK_DIR) && \
		go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs \
			generate --provider-name ferentin --rendered-provider-name "Ferentin" \
			--rendered-website-dir $(DOCS_CHECK_DIR)/docs; \
		status=$$?; \
		if [ $$status -ne 0 ]; then rm -rf $(DOCS_CHECK_DIR); exit $$status; fi; \
		diff -r docs $(DOCS_CHECK_DIR)/docs; \
		status=$$?; \
		rm -rf $(DOCS_CHECK_DIR); \
		exit $$status

clean:
	rm -f $(BINARY)
