##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
KUBECTL ?= kubectl
KIND ?= kind
TILT ?= tilt
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
CRD_REF_DOCS ?= $(LOCALBIN)/crd-ref-docs
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
ACTIONLINT ?= $(LOCALBIN)/actionlint
GINKGO ?= $(LOCALBIN)/ginkgo
GOVULNCHECK ?= $(LOCALBIN)/govulncheck
GOVULNCHECK_IGNORE ?= $(LOCALBIN)/govulncheck-ignore
GO_LICENSES ?= $(LOCALBIN)/go-licenses
GOMU ?= $(LOCALBIN)/gomu
GOTESTSUM ?= $(LOCALBIN)/gotestsum
DLV ?= $(LOCALBIN)/dlv
AIR ?= $(LOCALBIN)/air
BENCHSTAT ?= $(LOCALBIN)/benchstat
SEMGREP ?= $(LOCALBIN)/semgrep
SEMGREP_VENV ?= $(LOCALBIN)/semgrep-venv
AST_GREP_LOCAL_BIN ?= $(LOCALBIN)/ast-grep
AST_GREP ?= $(AST_GREP_LOCAL_BIN)

define go-install-tool
@set -e; \
toolchain="$$(env -u GOFLAGS go env GOVERSION)"; \
target="$(1)-$(3)-$${toolchain}"; \
[ -f "$$target" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$$target" ] || { \
package=$(2)@$(3) ;\
echo "Downloading $${package} with $${toolchain}" ;\
rm -f "$(1)" ;\
env -u GOFLAGS GOBIN="$(LOCALBIN)" GO111MODULE=on go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$$target" ;\
} ;\
ln -sf "$$(realpath "$$target")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef

## Tool Versions
KUSTOMIZE_VERSION ?= v5.7.1
CONTROLLER_TOOLS_VERSION ?= v0.19.0
CRD_REF_DOCS_VERSION ?= v0.3.0
GINKGO_VERSION ?= $(shell v='$(call gomodver,github.com/onsi/ginkgo/v2)'; \
  [ -n "$$v" ] || { echo "Set GINKGO_VERSION manually (ginkgo not in go.mod?)" >&2; exit 1; }; \
  printf '%s\n' "$$v")
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?([0-9]+)\.([0-9]+).*/release-\1.\2/')
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')
GOLANGCI_LINT_VERSION ?= v2.14.0
ACTIONLINT_VERSION ?= v1.7.11
GOVULNCHECK_VERSION ?= v1.8.0
GOVULNCHECK_IGNORE_VERSION ?= v1.0.0
GO_LICENSES_VERSION ?= v2.0.1
GOMU_VERSION ?= v0.1.0
GOTESTSUM_VERSION ?= v1.13.0
DLV_VERSION ?= v1.26.1
AIR_VERSION ?= v1.64.5
BENCHSTAT_VERSION ?= v0.0.0-20260211190930-8161c38c6cdc
SEMGREP_VERSION ?= 1.157.0
AST_GREP_VERSION ?= 0.42.3

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: crd-ref-docs
crd-ref-docs: $(CRD_REF_DOCS) ## Download crd-ref-docs locally if necessary.
$(CRD_REF_DOCS): $(LOCALBIN)
	$(call go-install-tool,$(CRD_REF_DOCS),github.com/elastic/crd-ref-docs,$(CRD_REF_DOCS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: actionlint
actionlint: $(ACTIONLINT) ## Download actionlint locally if necessary.
$(ACTIONLINT): $(LOCALBIN)
	$(call go-install-tool,$(ACTIONLINT),github.com/rhysd/actionlint/cmd/actionlint,$(ACTIONLINT_VERSION))

.PHONY: ginkgo
ginkgo: $(GINKGO) ## Download ginkgo CLI locally if necessary.
$(GINKGO): $(LOCALBIN)
	$(call go-install-tool,$(GINKGO),github.com/onsi/ginkgo/v2/ginkgo,$(GINKGO_VERSION))

.PHONY: govulncheck
govulncheck: $(GOVULNCHECK) ## Download govulncheck locally if necessary.
$(GOVULNCHECK): $(LOCALBIN)
	$(call go-install-tool,$(GOVULNCHECK),golang.org/x/vuln/cmd/govulncheck,$(GOVULNCHECK_VERSION))

.PHONY: govulncheck-ignore
govulncheck-ignore: $(GOVULNCHECK_IGNORE) ## Download govulncheck-ignore locally if necessary.
$(GOVULNCHECK_IGNORE): $(LOCALBIN)
	$(call go-install-tool,$(GOVULNCHECK_IGNORE),github.com/dc-tec/govulncheck-ignore,$(GOVULNCHECK_IGNORE_VERSION))

.PHONY: go-licenses
go-licenses: $(GO_LICENSES) ## Download go-licenses locally if necessary.
$(GO_LICENSES): $(LOCALBIN)
	$(call go-install-tool,$(GO_LICENSES),github.com/google/go-licenses/v2,$(GO_LICENSES_VERSION))

.PHONY: gomu
gomu: $(GOMU) ## Download gomu locally if necessary.
$(GOMU): $(LOCALBIN)
	$(call go-install-tool,$(GOMU),github.com/sivchari/gomu/cmd/gomu,$(GOMU_VERSION))

.PHONY: gotestsum
gotestsum: $(GOTESTSUM) ## Download gotestsum locally if necessary.
$(GOTESTSUM): $(LOCALBIN)
	$(call go-install-tool,$(GOTESTSUM),gotest.tools/gotestsum,$(GOTESTSUM_VERSION))

.PHONY: dlv
dlv: $(DLV) ## Download Delve locally if necessary.
$(DLV): $(LOCALBIN)
	$(call go-install-tool,$(DLV),github.com/go-delve/delve/cmd/dlv,$(DLV_VERSION))

.PHONY: air
air: $(AIR) ## Download Air locally if necessary.
$(AIR): $(LOCALBIN)
	$(call go-install-tool,$(AIR),github.com/air-verse/air,$(AIR_VERSION))

.PHONY: benchstat
benchstat: $(BENCHSTAT) ## Download benchstat locally if necessary.
$(BENCHSTAT): $(LOCALBIN)
	$(call go-install-tool,$(BENCHSTAT),golang.org/x/perf/cmd/benchstat,$(BENCHSTAT_VERSION))

.PHONY: semgrep
semgrep: $(SEMGREP) ## Install Semgrep locally in the repo tool cache if necessary.
$(SEMGREP): $(LOCALBIN)
	@command -v python3 >/dev/null 2>&1 || { \
		echo "python3 is required to install Semgrep. Install Python 3.11+ and retry."; \
		exit 1; \
	}
	@current_version=""; \
	if [ -x "$(SEMGREP_VENV)/bin/python" ]; then \
		current_version="$$("$(SEMGREP_VENV)/bin/python" -c 'import importlib.metadata; print(importlib.metadata.version("semgrep"))' 2>/dev/null || true)"; \
	fi; \
	if [ "$$current_version" != "$(SEMGREP_VERSION)" ]; then \
		rm -rf "$(SEMGREP_VENV)"; \
		python3 -m venv "$(SEMGREP_VENV)"; \
		"$(SEMGREP_VENV)/bin/pip" install --upgrade pip >/dev/null; \
		"$(SEMGREP_VENV)/bin/pip" install "semgrep==$(SEMGREP_VERSION)"; \
	fi
	@ln -sf "$$(realpath "$(SEMGREP_VENV)/bin/semgrep")" "$(SEMGREP)"

.PHONY: ast-grep
ast-grep: $(LOCALBIN) ## Install the pinned ast-grep release binary locally if necessary.
	@AST_GREP_VERSION="$(AST_GREP_VERSION)" \
		AST_GREP_DESTINATION="$(AST_GREP_LOCAL_BIN)" \
		bash hack/dev/install-ast-grep.sh
