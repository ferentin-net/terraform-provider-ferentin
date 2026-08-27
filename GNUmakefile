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

.PHONY: build install fmt lint vet test testacc testacc-local tidy clean docs docs-check
.PHONY: print-golangci-lint-version

# The ONE place the golangci-lint version is written. The CI lint job reads it
# back out of here rather than keeping a second copy in its `with: version:`.
#
# That second copy is not hypothetical — it is the bug this pin fixes. CI
# hardcoded v2.12.2 while `lint` below invoked a bare `golangci-lint` off
# PATH, so a developer with a newer one on PATH got a clean run against a
# different linter than the one gating the merge. v2.12.2 vendors
# honnef.co/go/tools v0.7.0, whose IR builder panics with
# "unexpected expr: *ast.KeyValueExpr" on Go 1.27's internal/poll and takes
# buildir down, which cascades into typedness / nilness / fact_purity /
# SA5012 and exits 3 before a single one of our own files is judged.
#
# It is also GOOS-dependent, which is why nobody saw it locally: internal/poll
# is per-platform source, the offending construct is in a Linux-only file, and
# a scan on darwin never parses it. Reproduce with
# `GOOS=linux GOARCH=amd64 golangci-lint run ./...`.
GOLANGCI_LINT_VERSION := v2.13.1

# Built from source at that version, not downloaded prebuilt, matching the
# workflow's `install-mode: goinstall`. A released binary type-checks with the
# Go it was built with, so the moment go.mod's language version outruns the
# linter release a prebuilt binary rejects our source before running anything.
#
# The path carries the version AND the toolchain, so bumping either rebuilds
# rather than silently reusing a stale binary — a cache that reintroduces the
# exact drift this variable exists to prevent. `:=` because a recursive
# assignment re-forks `go env` on every expansion, including while make merely
# PARSES this file.
GOLANGCI_LINT_GOVERSION := $(shell go env GOVERSION)
GOLANGCI_LINT_BIN := $(CURDIR)/.tools/golangci-lint-$(GOLANGCI_LINT_VERSION)-$(GOLANGCI_LINT_GOVERSION)

build:
	go build -o $(BINARY) .

install: build
	mkdir -p $(INSTALL_DIR)
	mv $(BINARY) $(INSTALL_DIR)/

fmt:
	gofmt -s -w .

$(GOLANGCI_LINT_BIN):
	@GOOS= GOARCH= GOBIN=$(CURDIR)/.tools go install \
		github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@mv $(CURDIR)/.tools/golangci-lint $@

lint: $(GOLANGCI_LINT_BIN)
	$(GOLANGCI_LINT_BIN) run ./...

# Consumed by the CI lint job so the workflow does not carry its own copy of
# the version. `-s` on the make invocation there keeps the output to the bare
# string.
print-golangci-lint-version:
	@echo $(GOLANGCI_LINT_VERSION)

vet:
	go vet ./...

test:
	go test ./... -count=1 -timeout 60s

# TF_ACC must be set to opt in to network-bound acceptance tests; otherwise
# they skip. Set TF_LOG=DEBUG to see provider HTTP traffic.
testacc:
	TF_ACC=1 go test ./internal/provider -v -count=1 -timeout 30m $(if $(RUN),-run '$(RUN)',)

# Acceptance run against LOCAL DEV (the `nginx` profile on this machine).
#
# This is the acceptance gate today: the platform has only local dev and
# production, a GitHub runner cannot reach local dev, and the tests must never
# touch production (they upsert tenant-default endpoint posture and cannot clean
# up after themselves — see .github/workflows/acceptance.yml).
#
# Requires admin-api-server running (`feri a`) and credentials for a principal
# holding policy:rw plus devices:groups:rw or devices:rw. Export FERENTIN_TOKEN,
# or FERENTIN_CLIENT_ID + FERENTIN_CLIENT_SECRET, and FERENTIN_TENANT_ID first.
# INSECURE_SKIP_VERIFY is set because local dev serves a self-signed cert.
#
# Pass RUN= to run a subset, which is usually what you want when validating one
# resource:
#
#   make testacc-local RUN='TestAccDeviceGroup|TestAccEndpointDestinationRule'
#
# Prefer it over a full run with FERENTIN_CLIENT_ID auth. Each test and each
# step builds a fresh provider, so each mints a token, and the auth-server's
# client_credentials burst limit defaults to 10 per 10s — a full suite trips it
# and the remaining tests fail as `rate_limit_exceeded` (before the platform
# fix, misreported as `invalid_client`, which looks exactly like bad
# credentials). The SDK now backs off on 429, but a subset is still faster and
# does not spend a shared budget. FERENTIN_TOKEN avoids minting entirely.
testacc-local:
	@test -n "$$FERENTIN_TENANT_ID" || { echo "FERENTIN_TENANT_ID must be set"; exit 1; }
	@test -n "$$FERENTIN_TOKEN" -o -n "$$FERENTIN_CLIENT_ID" || \
		{ echo "set FERENTIN_TOKEN, or FERENTIN_CLIENT_ID + FERENTIN_CLIENT_SECRET"; exit 1; }
	TF_ACC=1 \
	FERENTIN_ENDPOINT=$${FERENTIN_ENDPOINT:-https://api.local.ferentin.test} \
	FERENTIN_INSECURE_SKIP_VERIFY=1 \
	go test ./internal/provider -v -count=1 -timeout 30m $(if $(RUN),-run '$(RUN)',)

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
	rm -rf $(CURDIR)/.tools
