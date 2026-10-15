##@ Development

.PHONY: bootstrap
bootstrap: controller-gen kustomize crd-ref-docs setup-envtest golangci-lint actionlint ginkgo govulncheck govulncheck-ignore go-licenses gotestsum semgrep ast-grep ## Install tools required by the core contributor workflow.
	@echo "Bootstrap complete."
	@echo "Devenv configures Git hooks automatically."
	@echo "Run 'make doctor' to validate external prerequisites."

.PHONY: doctor
doctor: ## Validate local prerequisites for the main contributor workflow.
	@bash hack/dev/doctor.sh

.PHONY: verify-devenv
verify-devenv: ## Verify the pinned devenv toolchain contract without external service checks.
	@bash hack/dev/verify-devenv.sh

.PHONY: clean-artifacts
clean-artifacts: ## Remove known local build, test, documentation, and report artifacts.
	@$(RM) cover.out coverage.out report.xml
	@$(RM) -r website/public site artifacts
	@$(RM) -r dist/architecture dist/bench dist/fuzz dist/perf dist/test dist/mutation dist/licenses dist/semgrep
	@$(RM) dist/install.yaml dist/crds.yaml dist/checksums.txt dist/sbom-*.spdx.json
	@echo "Removed known local artifacts."

.PHONY: tilt-up
tilt-up: ## Launch the local Kubernetes dev loop in Tilt. Use TILT_ARGS for extra flags.
	@command -v "$(TILT)" >/dev/null 2>&1 || { \
		echo "tilt is required. Install it from https://docs.tilt.dev/install.html"; \
		exit 1; \
	}
	"$(TILT)" up $(TILT_ARGS)

.PHONY: tilt-down
tilt-down: ## Tear down Tilt-managed resources in the current kube context.
	@command -v "$(TILT)" >/dev/null 2>&1 || { \
		echo "tilt is required. Install it from https://docs.tilt.dev/install.html"; \
		exit 1; \
	}
	"$(TILT)" down $(TILT_ARGS)

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	# Generate CRDs and webhooks (shared across both controllers)
	"$(CONTROLLER_GEN)" crd webhook paths="./api/..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: verify-fmt
verify-fmt: ## Verify all Go code is gofmt'd (does not modify files).
	@unformatted="$$(find . \( -path ./.git -o -path ./.devenv -o -path ./vendor \) -prune -o -name '*.go' -print0 | xargs -0 gofmt -l)"; \
	if [ -n "$$unformatted" ]; then \
		echo "The following files are not gofmt'd:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

E2E_SUITE_PACKAGE := $(shell GOFLAGS="$(GOFLAGS_VENDOR)" go list -m)/test/e2e

.PHONY: vet
vet: ## Run go vet against code.
	GOFLAGS="$(GOFLAGS_VENDOR)" go vet ./...

.PHONY: vet-tagged
vet-tagged: ## Run go vet against integration and E2E tagged code without a cluster.
	GOFLAGS="$(GOFLAGS_VENDOR)" go vet -tags=integration ./...
	GOFLAGS="$(GOFLAGS_VENDOR)" go vet -tags=e2e ./test/e2e/...

.PHONY: test
test: manifests generate fmt vet ## Run unit tests (fast, no envtest).
	GOFLAGS="$(GOFLAGS_VENDOR)" go test $$(GOFLAGS="$(GOFLAGS_VENDOR)" go list ./... | grep -vx '$(E2E_SUITE_PACKAGE)') -coverprofile cover.out

.PHONY: test-sum
test-sum: manifests generate fmt vet gotestsum ## Run unit tests with gotestsum output and JUnit XML.
	@mkdir -p "$(TEST_ARTIFACT_DIR)"
	GOFLAGS="$(GOFLAGS_VENDOR)" "$(GOTESTSUM)" --format="$(GOTESTSUM_FORMAT)" --junitfile "$(TEST_ARTIFACT_DIR)/unit.xml" -- -coverprofile "$(TEST_ARTIFACT_DIR)/unit.cover.out" $$(GOFLAGS="$(GOFLAGS_VENDOR)" go list ./... | grep -vx '$(E2E_SUITE_PACKAGE)')

COVERAGE_PROFILE ?= cover.out
COVERAGE_MIN_INTERNAL ?= 68.0

.PHONY: verify-coverage
verify-coverage: ## Verify production code under internal/ meets the coverage regression floor.
	GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/coverage_check \
		--profile "$(COVERAGE_PROFILE)" \
		--minimum "$(COVERAGE_MIN_INTERNAL)"

.PHONY: test-ci
test-ci: manifests generate vet kustomize setup-envtest gotestsum test-e2e-support ## Run unit, integration, and E2E support tests and enforce the coverage floor.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" \
		GOFLAGS="$(GOFLAGS_VENDOR)" \
		"$(GOTESTSUM)" --format="$(GOTESTSUM_FORMAT)" -- \
		-tags=integration \
		-count=1 \
		-coverprofile "$(COVERAGE_PROFILE)" \
		$$(GOFLAGS="$(GOFLAGS_VENDOR)" go list -tags=integration ./... | grep -vx '$(E2E_SUITE_PACKAGE)')
	$(MAKE) verify-coverage COVERAGE_PROFILE="$(COVERAGE_PROFILE)" COVERAGE_MIN_INTERNAL="$(COVERAGE_MIN_INTERNAL)"

.PHONY: test-integration
test-integration: manifests generate vet kustomize setup-envtest ## Run envtest-based integration tests (envtest; requires -tags=integration).
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" GOFLAGS="$(GOFLAGS_VENDOR)" go test $$(GOFLAGS="$(GOFLAGS_VENDOR)" go list -tags=integration ./... | grep -vx '$(E2E_SUITE_PACKAGE)') -tags=integration -count=1 -v

.PHONY: test-integration-sum
test-integration-sum: manifests generate vet kustomize setup-envtest gotestsum ## Run envtest-based integration tests with gotestsum output and JUnit XML.
	@mkdir -p "$(TEST_ARTIFACT_DIR)"
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" GOFLAGS="$(GOFLAGS_VENDOR)" "$(GOTESTSUM)" --format="$(GOTESTSUM_FORMAT)" --junitfile "$(TEST_ARTIFACT_DIR)/integration.xml" -- -tags=integration -count=1 -coverprofile "$(TEST_ARTIFACT_DIR)/integration.cover.out" $$(GOFLAGS="$(GOFLAGS_VENDOR)" go list -tags=integration ./... | grep -vx '$(E2E_SUITE_PACKAGE)')

.PHONY: fuzz
fuzz: ## Run the curated fuzz smoke sweep across repo fuzz targets.
	@FUZZTIME="$(FUZZTIME)" FUZZ_GOMAXPROCS="$(FUZZ_GOMAXPROCS)" FUZZ_TARGET_FILTER="$(FUZZ_TARGET_FILTER)" GOFLAGS="$(GOFLAGS_VENDOR)" bash hack/ci/fuzz.sh

.PHONY: fuzz-long
fuzz-long: FUZZTIME=20s
fuzz-long: ## Run the longer fuzz sweep used by nightly CI.
	@FUZZTIME="$(FUZZTIME)" FUZZ_GOMAXPROCS="$(FUZZ_GOMAXPROCS)" FUZZ_TARGET_FILTER="$(FUZZ_TARGET_FILTER)" GOFLAGS="$(GOFLAGS_VENDOR)" bash hack/ci/fuzz.sh

.PHONY: verify-tidy
verify-tidy: ## Verify go.mod/go.sum are tidy (does not modify tracked files).
	@GOFLAGS="-mod=mod" go mod tidy
	@{ \
		git diff --exit-code -- go.mod go.sum; \
	} || { \
		echo "go.mod/go.sum are not tidy. Run 'go mod tidy' and commit the result."; \
		git --no-pager diff -- go.mod go.sum; \
		exit 1; \
	}

.PHONY: verify-go-toolchain-sync
verify-go-toolchain-sync: ## Verify go.mod and Dockerfile builders use the same Go version.
	@bash hack/ci/verify-go-toolchain-sync.sh

.PHONY: verify-release-automation
verify-release-automation: ## Test release request validation and Artifact Hub metadata generation.
	@bash hack/ci/test-release-automation.sh
	@bash hack/ci/test-remove-unpublished-release.sh

.PHONY: verify-spdx-normalizer
verify-spdx-normalizer: ## Validate deterministic SPDX normalization against the SPDX 2.2 and 2.3 schemas.
	@bash hack/ci/test-normalize-spdx-json.sh

.PHONY: verify-workflows
verify-workflows: actionlint verify-release-automation test-ci-tools ## Validate workflows, routing, and release automation.
	@"$(ACTIONLINT)" .github/workflows/*.yml

.PHONY: test-ci-tools
test-ci-tools: ## Test CI job expectations, diff routing, timing reports, E2E plans, and PR labels.
	@GOFLAGS="$(GOFLAGS_VENDOR)" go test -count=1 \
		./hack/tools/ci_checks ./hack/tools/ci_report \
		./hack/tools/e2e_plan ./hack/tools/e2e_report ./hack/tools/e2e_catalog ./hack/tools/e2e_manifest \
		./.github/scripts
	@PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s hack/ci -p 'test_e2e_*.py'

.PHONY: verify-vendor
verify-vendor: ## Verify vendor/ is synchronized with go.mod/go.sum.
	@GOFLAGS="-mod=mod" go mod vendor
	@{ \
		git diff --exit-code -- vendor; \
	} || { \
		echo "vendor/ is out of date. Run 'go mod vendor' and commit the result."; \
		git --no-pager diff -- vendor; \
		exit 1; \
	}

.PHONY: verify-generated
verify-generated: manifests generate api-reference ## Verify generated artifacts are up-to-date (does not modify tracked files).
	@{ \
		git diff --exit-code -- api/v1alpha1 config/crd/bases website/generated/api-reference.md; \
	} || { \
		echo "Generated artifacts are out of date. Run 'make manifests generate api-reference' and commit the result."; \
		git --no-pager diff -- api/v1alpha1 config/crd/bases website/generated/api-reference.md; \
		exit 1; \
	}

.PHONY: verify-api-stability-inventory
verify-api-stability-inventory: ## Verify every served CRD field has a proposed or approved stability classification.
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/api_inventory

CRD_COMPAT_TARGET ?= next

.PHONY: verify-crd-compatibility
verify-crd-compatibility: ## Reject breaking or review-required CRD schema changes against the supported release baseline.
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/crd_compatibility --mode enforce --target "$(CRD_COMPAT_TARGET)"

.PHONY: verify-api-contract
verify-api-contract: verify-api-stability-inventory verify-crd-compatibility ## Enforce the served API inventory and release compatibility contract.

.PHONY: report-crd-compatibility
report-crd-compatibility: ## Report CRD schema compatibility against the supported release baseline.
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/crd_compatibility --mode report --target "$(CRD_COMPAT_TARGET)"

.PHONY: update-api-stability-inventory
update-api-stability-inventory: ## Update the resolved CRD stability snapshot after an intentional API or inventory change.
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/api_inventory --update

.PHONY: api-reference
api-reference: crd-ref-docs ## Generate CRD API reference docs from api/v1alpha1.
	@CRD_REF_DOCS_BIN="$(CRD_REF_DOCS)" bash hack/docs/generate-api-reference.sh

.PHONY: verify-openbao-config-compat
verify-openbao-config-compat: ## Validate versioned HCL contracts, decoded values, and expected rejections.
	@bash hack/ci/openbao-config-compat.sh

.PHONY: report-openbao-config-schema-drift
report-openbao-config-schema-drift: ## Validate config compatibility and report upstream schema drift across the matrix.
	@REPORT_SCHEMA_DRIFT=true bash hack/ci/openbao-config-compat.sh

.PHONY: report-openbao-operator-schema-drift
report-openbao-operator-schema-drift: ## Validate config compatibility and report operator-vs-upstream schema drift.
	@REPORT_SCHEMA_DRIFT=true bash hack/ci/openbao-config-compat.sh

.PHONY: report-ast
report-ast: generate-ast-rules ast-grep ## Run ast-grep rules in report mode (non-failing; warnings only).
	@"$(AST_GREP)" scan -c .ast-grep/sgconfig.yml --report-style=medium .

.PHONY: lint-ast
lint-ast: generate-ast-rules ast-grep ## Run ast-grep rules in strict mode (treat all findings as errors).
	@"$(AST_GREP)" scan -c .ast-grep/sgconfig.yml --report-style=medium --error .

.PHONY: lint-testonly-exports
lint-testonly-exports: ## Verify production exports are not referenced only from tests.
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/testonly_exports

.PHONY: test-ast
test-ast: generate-ast-rules ast-grep ## Run ast-grep rule tests.
	@"$(AST_GREP)" test -c .ast-grep/sgconfig.yml

.PHONY: generate-ast-rules
generate-ast-rules: ## Generate ast-grep architecture boundary rules from policy.
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/ast_rulegen \
		-policy .ast-grep/policy/architecture-boundaries.yml \
		-out-dir .ast-grep/rules/generated/architecture-boundary

.PHONY: verify-ast-rules-generated
verify-ast-rules-generated: generate-ast-rules ## Verify generated ast-grep rules are up-to-date.
	@status="$$(git status --porcelain -- .ast-grep/rules/generated/architecture-boundary)"; \
	if [ -n "$$status" ]; then \
		echo "Generated ast-grep rules are out of date. Run 'make generate-ast-rules' and commit the result."; \
		echo "$$status"; \
		exit 1; \
	fi

.PHONY: verify-arch-policy
verify-arch-policy: verify-ast-rules-generated ## Verify architecture policy generation integrity.

.PHONY: report-internal-deps
report-internal-deps: ## Generate internal runtime dependency graph/report locally (report-only; non-failing).
	@bash hack/architecture/report-internal-deps.sh

.PHONY: report-internal-deps-snapshot
report-internal-deps-snapshot: report-internal-deps ## Save current internal dependency report as local baseline snapshot.
	@cp dist/architecture/internal-dependency-report.md dist/architecture/internal-dependency-report.baseline.md
	@echo "Saved baseline snapshot:"
	@echo "  dist/architecture/internal-dependency-report.baseline.md"

.PHONY: report-internal-deps-diff
report-internal-deps-diff: ## Compare internal dependency report against local baseline snapshot (report-only; non-failing).
	@bash hack/architecture/diff-internal-deps.sh \
		"$${BASELINE_REPORT:-dist/architecture/internal-dependency-report.baseline.md}" \
		"$${CURRENT_REPORT:-dist/architecture/internal-dependency-report.md}"

.PHONY: helm-sync
helm-sync: manifests ## Sync Helm chart from config/ (CRDs, admission policies).
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/helmchart

.PHONY: verify-helm-values
verify-helm-values: ## Verify Helm values.yaml and values.schema.json stay in sync with templates.
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/helmvalues

.PHONY: verify-helm
verify-helm: helm-sync verify-helm-values ## Verify Helm chart is up-to-date (does not modify tracked files).
	@{ \
		git diff --exit-code -- charts/openbao-operator/crds charts/openbao-operator/templates/admission charts/openbao-operator/templates/rbac; \
	} || { \
		echo "Helm chart is out of date. Run 'make helm-sync' and commit the result."; \
		git --no-pager diff -- charts/openbao-operator/crds charts/openbao-operator/templates/admission charts/openbao-operator/templates/rbac; \
		exit 1; \
	}

.PHONY: helm-lint
helm-lint: ## Lint the Helm chart.
	@helm lint charts/openbao-operator

.PHONY: verify-edge-chart
verify-edge-chart: ## Test edge chart contents, reproducibility, and publication guards.
	@bash hack/ci/test-edge-chart.sh

.PHONY: helm-test
helm-test: helm-sync helm-lint verify-edge-chart ## Test the Helm chart without requiring a live cluster.
	@echo "Testing Helm chart: templating with default values..."
	@helm template openbao-operator charts/openbao-operator \
		--namespace openbao-operator-system \
		--include-crds > /dev/null
	@echo "Testing Helm chart: templating with multi-tenant mode..."
	@helm template openbao-operator charts/openbao-operator \
		--namespace openbao-operator-system \
		--include-crds \
		--set tenancy.mode=multi > /dev/null
	@echo "Testing Helm chart: provisioner admission identity follows fullnameOverride..."
	@render="$$(helm template baoctl charts/openbao-operator \
		--namespace openbao-system \
		--include-crds \
		--set tenancy.mode=multi \
		--set fullnameOverride=baoctl)"; \
		grep -q "name: baoctl-provisioner" <<< "$$render"; \
		grep -q "'baoctl-provisioner'" <<< "$$render"; \
		if grep -q "'openbao-operator-provisioner'" <<< "$$render"; then \
			echo "Helm admission policies rendered a stale provisioner ServiceAccount name"; \
			exit 1; \
		fi
	@if grep -R "'openbao-operator-provisioner'" charts/openbao-operator/templates/admission; then \
		echo "Helm admission templates must derive the provisioner ServiceAccount name from helpers"; \
		exit 1; \
	fi
	@echo "Testing Helm chart: templating with single-tenant mode..."
	@helm template openbao-operator charts/openbao-operator \
		--namespace openbao-operator-system \
		--include-crds \
		--set tenancy.mode=single \
		--set tenancy.targetNamespace=openbao-system > /dev/null
	@echo "Testing Helm chart: client-side install render with default values..."
	@helm template openbao-operator charts/openbao-operator \
		--namespace openbao-operator-system \
		--include-crds > /dev/null
	@echo "Helm chart tests passed successfully!"

.PHONY: helm-e2e-smoke
helm-e2e-smoke: ## Helm chart smoke test against a Kind cluster (installs chart and waits for deployments).
	@bash hack/ci/helm-e2e-smoke.sh

.PHONY: verify-operator-upgrade-e2e
verify-operator-upgrade-e2e: ## Verify the previous-stable Development and Hardened operator upgrade harness.
	@bash -n hack/ci/operator-upgrade-e2e.sh
	@OPERATOR_UPGRADE_E2E_VERIFY_ONLY=true bash hack/ci/operator-upgrade-e2e.sh
	@bash hack/ci/test-operator-upgrade-e2e.sh

.PHONY: test-e2e-operator-upgrade
test-e2e-operator-upgrade: ## Upgrade previous-stable Development and Hardened resources to the local candidate in Kind.
	@bash hack/ci/operator-upgrade-e2e.sh

.PHONY: helm-template
helm-template: helm-sync ## Template the Helm chart with default values (useful for debugging).
	@helm template openbao-operator charts/openbao-operator \
		--namespace openbao-operator-system \
		--include-crds

.PHONY: helm-package
helm-package: helm-sync ## Package the Helm chart to verify it's valid.
	@mkdir -p dist
	@helm package charts/openbao-operator -d dist
	@echo "Helm chart packaged successfully in dist/"

.PHONY: helm-install
helm-install: helm-sync ## Install the Helm chart from local charts directory. Use IMG=image:tag or IMG=image@digest to override the operator image.
	@if [ -z "$(IMG)" ]; then \
		echo "Warning: IMG not set. Using default image from values.yaml"; \
		helm install openbao-operator charts/openbao-operator \
			--namespace openbao-operator-system \
			--create-namespace; \
	else \
		if echo "$(IMG)" | grep -q '@'; then \
			image_repo=$$(echo "$(IMG)" | cut -d@ -f1); \
			image_digest=$$(echo "$(IMG)" | cut -d@ -f2); \
			echo "Installing with image: $(IMG) (using digest)"; \
			helm install openbao-operator charts/openbao-operator \
				--namespace openbao-operator-system \
				--create-namespace \
				--set image.repository=$$image_repo \
				--set image.digest=$$image_digest; \
		else \
			image_repo=$$(echo "$(IMG)" | sed 's/:[^:]*$$//'); \
			image_tag=$$(echo "$(IMG)" | sed 's/.*://'); \
			echo "Installing with image: $(IMG) (using tag)"; \
			helm install openbao-operator charts/openbao-operator \
				--namespace openbao-operator-system \
				--create-namespace \
				--set image.repository=$$image_repo \
				--set image.tag=$$image_tag; \
		fi; \
	fi

.PHONY: helm-upgrade
helm-upgrade: helm-sync ## Upgrade the Helm chart from local charts directory. Use IMG=image:tag or IMG=image@digest to override the operator image.
	@if [ -z "$(IMG)" ]; then \
		echo "Warning: IMG not set. Using default image from values.yaml"; \
		helm upgrade openbao-operator charts/openbao-operator \
			--namespace openbao-operator-system; \
	else \
		if echo "$(IMG)" | grep -q '@'; then \
			image_repo=$$(echo "$(IMG)" | cut -d@ -f1); \
			image_digest=$$(echo "$(IMG)" | cut -d@ -f2); \
			echo "Upgrading with image: $(IMG) (using digest)"; \
			helm upgrade openbao-operator charts/openbao-operator \
				--namespace openbao-operator-system \
				--set image.repository=$$image_repo \
				--set image.digest=$$image_digest; \
		else \
			image_repo=$$(echo "$(IMG)" | sed 's/:[^:]*$$//'); \
			image_tag=$$(echo "$(IMG)" | sed 's/.*://'); \
			echo "Upgrading with image: $(IMG) (using tag)"; \
			helm upgrade openbao-operator charts/openbao-operator \
				--namespace openbao-operator-system \
				--set image.repository=$$image_repo \
				--set image.tag=$$image_tag; \
		fi; \
	fi

.PHONY: helm-uninstall
helm-uninstall: ## Uninstall the Helm chart from the cluster.
	@helm uninstall openbao-operator --namespace openbao-operator-system || true

.PHONY: test-update-golden
test-update-golden: ## Update golden files for HCL generation tests. Run this when modifying internal/adapter/config/builder.go or related config generation logic.
	UPDATE_GOLDEN=true go test ./internal/adapter/config/... -v

.PHONY: verify-trusted-root
verify-trusted-root: ## Verify that trusted_root.json exists and is valid JSON.
	@if [ ! -f internal/adapter/security/trusted_root.json ]; then \
		echo "Error: trusted_root.json not found. Run 'make update-trusted-root' to download it."; \
		exit 1; \
	fi
	@python3 -m json.tool internal/adapter/security/trusted_root.json > /dev/null 2>&1 || { \
		echo "Error: trusted_root.json is not valid JSON. Run 'make update-trusted-root' to fix it."; \
		exit 1; \
	}
	@echo "trusted_root.json is valid"

DOCS_DIR ?= website
DOCS_DESTINATION ?= $(DOCS_DIR)/public
HUGO ?= hugo
TEST_ARTIFACT_DIR ?= dist/test
GOTESTSUM_FORMAT ?= pkgname
FUZZTIME ?= 3s
FUZZ_GOMAXPROCS ?= 4
FUZZ_TARGET_FILTER ?=

.PHONY: docs-build
docs-build: ## Build and validate the canonical Hugo documentation site. Writes ./website/public/.
	@"$(DOCS_DIR)/scripts/sync-api-reference.sh" --all --check
	@$(HUGO) --source "$(DOCS_DIR)" --gc --minify --panicOnWarning --cleanDestinationDir
	@"$(DOCS_DIR)/scripts/apply-legacy-redirects.sh" --destination "$(DOCS_DESTINATION)"
	@"$(DOCS_DIR)/scripts/apply-legacy-redirects.sh" --destination "$(DOCS_DESTINATION)" --check
	@python3 "$(DOCS_DIR)/scripts/check-rendered-site.py" "$(DOCS_DESTINATION)"

.PHONY: docs-serve
docs-serve: ## Serve the Hugo documentation locally. http://localhost:1313/
	@"$(DOCS_DIR)/scripts/sync-api-reference.sh" --all --check
	@$(HUGO) server --source "$(DOCS_DIR)" --baseURL http://127.0.0.1:1313/

.PHONY: docs-preview
docs-preview: docs-serve ## Alias for the local Hugo server.

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The default setup assumes Kind is pre-installed and builds/loads the Manager Docker image locally.
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
KIND_CLUSTER ?= openbao-operator-test-e2e
KIND_NODE_IMAGE ?=
E2E_PARALLEL_NODES ?= 1
E2E_TIMEOUT ?= 1h
E2E_JUNIT_REPORT ?=
E2E_JSON_REPORT ?=
E2E_GOJSON_REPORT ?=
E2E_POLL_PROGRESS_AFTER ?=
E2E_FAIL_ON_EMPTY ?= false
E2E_KEEP_GOING ?= false
E2E_TRACE ?= false
E2E_NO_COLOR ?= false
E2E_FOCUS ?=
E2E_LABEL_FILTER ?=
E2E_SKIP_CLEANUP ?= false
E2E_BINARY_DIR ?=
E2E_SOURCE_SHA ?= $(shell git rev-parse HEAD)
E2E_DRY_RUN ?= false

export E2E_PARALLEL_NODES E2E_TIMEOUT E2E_JUNIT_REPORT E2E_JSON_REPORT E2E_GOJSON_REPORT
export E2E_POLL_PROGRESS_AFTER E2E_FAIL_ON_EMPTY E2E_KEEP_GOING E2E_TRACE E2E_NO_COLOR
export E2E_FOCUS E2E_LABEL_FILTER E2E_BINARY_DIR E2E_SOURCE_SHA E2E_DRY_RUN

PERF_NODE_IMAGE ?= kindest/node:v1.34.3
PERF_SAMPLES ?= 3
PERF_WARMUPS ?= 1
PERF_SCENARIO_TIMEOUT ?= 90m
PERF_SMOKE_SCENARIO_TIMEOUT ?= 45m
PERF_SMOKE_SCENARIOS ?= lifecycle-convergence
PERF_SCENARIOS ?= all
PERF_SCENARIOS_FILE ?= hack/perf/v2/scenarios.yaml
PERF_BASELINE_DIR ?= hack/perf/v2/baselines
PERF_POLICY_FILE ?= hack/perf/v2/policies/weekly.yaml
PERF_ARTIFACT_DIR ?= dist/perf
PERF_ENVIRONMENT ?= kind-v1.34.3
PERF_PREVIOUS_SUMMARY ?=
PERF_REPORT_FAIL_ON_FAILURES ?= false
PERF_CONTINUE_ON_SAMPLE_ERROR ?= false
PERF_MIN_SUCCESSFUL_SAMPLES ?= 0
PERF_OPERATOR_IMAGE ?= example.com/openbao-operator:0.0.1
PERF_CONFIG_INIT_IMAGE ?= openbao-init:dev
PERF_BACKUP_EXECUTOR_IMAGE ?= openbao-backup:dev
PERF_UPGRADE_EXECUTOR_IMAGE ?= openbao-upgrade:dev
PERF_OPENBAO_VERSION ?= 2.6.4
PERF_OPENBAO_IMAGE ?= openbao/openbao:2.6.4
PERF_UPGRADE_FROM_VERSION ?= 2.6.1
PERF_UPGRADE_FROM_IMAGE ?= openbao/openbao:2.6.1
PERF_UPGRADE_TO_VERSION ?= 2.6.4
PERF_UPGRADE_TO_IMAGE ?= openbao/openbao:2.6.4
PERF_API_SERVER_CIDR ?= 10.96.0.0/12
PERF_STORAGE_CLASS ?=
PERF_TENANT_CHURN_COUNT ?= 10

MUTATION_TARGET_PATH ?= ./internal/service/opslifecycle
MUTATION_PATHS ?= $(shell find ./internal -mindepth 1 -maxdepth 1 -type d | LC_ALL=C sort | paste -sd, -)
MUTATION_WORKERS ?= 1
MUTATION_TIMEOUT ?= 30
MUTATION_INCREMENTAL ?= false
MUTATION_TOP_SURVIVORS ?= 20
MUTATION_GOFLAGS ?= -p=1
MUTATION_GOMEMLIMIT ?= 8GiB

.PHONY: test-e2e-support
test-e2e-support: ## Run cluster-independent tests for E2E framework and helper packages.
	GOFLAGS="$(GOFLAGS_VENDOR)" go test -tags=e2e -count=1 ./test/e2e/framework ./test/e2e/helpers

.PHONY: setup-test-e2e
setup-test-e2e: ## Set up a Kind cluster for e2e tests if it does not exist
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@cluster_name="$(KIND_CLUSTER)"; \
	if $(KIND) get clusters | grep -qx "$$cluster_name"; then \
		echo "Kind cluster '$$cluster_name' already exists. Skipping creation."; \
	else \
		echo "Creating Kind cluster '$$cluster_name'..."; \
		if [ -n "$(KIND_NODE_IMAGE)" ]; then \
			$(KIND) create cluster --name "$$cluster_name" --image "$(KIND_NODE_IMAGE)" ; \
		else \
			$(KIND) create cluster --name "$$cluster_name" ; \
		fi; \
	fi

.PHONY: test-e2e
test-e2e: setup-test-e2e manifests generate fmt vet ginkgo ## Run the e2e tests. Expected an isolated environment using Kind. Use E2E_PARALLEL_NODES=N to run tests in parallel (default: 1). Use E2E_FOCUS="Backup" to run only specific tests. See Makefile for additional E2E_* variables.
	@for report in "$(E2E_JUNIT_REPORT)" "$(E2E_JSON_REPORT)" "$(E2E_GOJSON_REPORT)"; do \
		if [ -n "$$report" ]; then \
			mkdir -p "$$(dirname "$$report")"; \
		fi; \
	done; \
	GINKGO_FLAGS="-tags=e2e -v --timeout=$(E2E_TIMEOUT)"; \
	if [ -n "$(E2E_FOCUS)" ]; then \
		GINKGO_FLAGS="$$GINKGO_FLAGS --focus=\"$(E2E_FOCUS)\""; \
	fi; \
	if [ -n "$(E2E_LABEL_FILTER)" ]; then \
		GINKGO_FLAGS="$$GINKGO_FLAGS --label-filter=\"$(E2E_LABEL_FILTER)\""; \
	fi; \
	if [ "$(E2E_TRACE)" = "true" ]; then \
		GINKGO_FLAGS="$$GINKGO_FLAGS --trace"; \
	fi; \
	if [ "$(E2E_NO_COLOR)" = "true" ]; then \
		GINKGO_FLAGS="$$GINKGO_FLAGS --no-color"; \
	fi; \
	if [ -n "$(E2E_JUNIT_REPORT)" ]; then \
		GINKGO_FLAGS="$$GINKGO_FLAGS --junit-report=$(E2E_JUNIT_REPORT)"; \
	fi; \
	if [ -n "$(E2E_JSON_REPORT)" ]; then \
		GINKGO_FLAGS="$$GINKGO_FLAGS --json-report=$(E2E_JSON_REPORT)"; \
	fi; \
	if [ -n "$(E2E_GOJSON_REPORT)" ]; then \
		GINKGO_FLAGS="$$GINKGO_FLAGS --gojson-report=$(E2E_GOJSON_REPORT)"; \
	fi; \
	if [ -n "$(E2E_POLL_PROGRESS_AFTER)" ]; then \
		GINKGO_FLAGS="$$GINKGO_FLAGS --poll-progress-after=$(E2E_POLL_PROGRESS_AFTER)"; \
	fi; \
	if [ "$(E2E_FAIL_ON_EMPTY)" = "true" ]; then \
		GINKGO_FLAGS="$$GINKGO_FLAGS --fail-on-empty"; \
	fi; \
	if [ "$(E2E_KEEP_GOING)" = "true" ]; then \
		GINKGO_FLAGS="$$GINKGO_FLAGS --keep-going"; \
	fi; \
	eval KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) "$(GINKGO)" $$GINKGO_FLAGS --procs=$(E2E_PARALLEL_NODES) ./test/e2e/
	@if [ "$(E2E_SKIP_CLEANUP)" != "true" ]; then \
		$(MAKE) cleanup-test-e2e; \
	else \
		echo "E2E_SKIP_CLEANUP=true: Keeping Kind cluster $(KIND_CLUSTER) for debugging"; \
	fi

.PHONY: test-e2e-existing
test-e2e-existing: manifests generate fmt vet ginkgo ## Run the e2e tests against an existing cluster (e.g. OpenShift Local/CRC). Requires KUBECONFIG set and E2E_OPERATOR_IMAGE pointing to a pullable image. Use E2E_LABEL_FILTER to run a subset (e.g. 'openshift').
	@for report in "$(E2E_JUNIT_REPORT)" "$(E2E_JSON_REPORT)" "$(E2E_GOJSON_REPORT)"; do \
		if [ -n "$$report" ]; then \
			mkdir -p "$$(dirname "$$report")"; \
		fi; \
	done; \
	GO_TEST_FLAGS="-tags=e2e -timeout=$(E2E_TIMEOUT) -v -ginkgo.v -ginkgo.timeout=$(E2E_TIMEOUT)"; \
	if [ -n "$(E2E_FOCUS)" ]; then \
		GO_TEST_FLAGS="$$GO_TEST_FLAGS -ginkgo.focus=\"$(E2E_FOCUS)\""; \
	fi; \
	if [ -n "$(E2E_LABEL_FILTER)" ]; then \
		GO_TEST_FLAGS="$$GO_TEST_FLAGS -ginkgo.label-filter=\"$(E2E_LABEL_FILTER)\""; \
	fi; \
	if [ "$(E2E_TRACE)" = "true" ]; then \
		GO_TEST_FLAGS="$$GO_TEST_FLAGS -ginkgo.trace"; \
	fi; \
	if [ -n "$(E2E_JUNIT_REPORT)" ]; then \
		GO_TEST_FLAGS="$$GO_TEST_FLAGS -ginkgo.junit-report=$(E2E_JUNIT_REPORT)"; \
	fi; \
	if [ -n "$(E2E_JSON_REPORT)" ]; then \
		GO_TEST_FLAGS="$$GO_TEST_FLAGS -ginkgo.json-report=$(E2E_JSON_REPORT)"; \
	fi; \
	if [ -n "$(E2E_GOJSON_REPORT)" ]; then \
		GO_TEST_FLAGS="$$GO_TEST_FLAGS -ginkgo.gojson-report=$(E2E_GOJSON_REPORT)"; \
	fi; \
	if [ -n "$(E2E_POLL_PROGRESS_AFTER)" ]; then \
		GO_TEST_FLAGS="$$GO_TEST_FLAGS -ginkgo.poll-progress-after=$(E2E_POLL_PROGRESS_AFTER)"; \
	fi; \
	if [ "$(E2E_FAIL_ON_EMPTY)" = "true" ]; then \
		GO_TEST_FLAGS="$$GO_TEST_FLAGS -ginkgo.fail-on-empty"; \
	fi; \
	eval E2E_USE_EXISTING_CLUSTER=true go test ./test/e2e/ $$GO_TEST_FLAGS

.PHONY: build-e2e-binaries
build-e2e-binaries: ## Build the E2E suite, Ginkgo runner, and reporter once for this source and platform.
	@bash hack/ci/build-e2e-binaries.sh "$(or $(E2E_BINARY_DIR),artifacts/e2e-binaries)" "$(E2E_SOURCE_SHA)"

.PHONY: test-e2e-ci
test-e2e-ci: $(if $(E2E_BINARY_DIR),,ginkgo) ## Run E2E with source or verified precompiled binaries.
	@KIND="$(KIND)" KIND_CLUSTER="$(KIND_CLUSTER)" GINKGO="$(GINKGO)" bash hack/ci/run-e2e.sh
	@if [ "$(E2E_SKIP_CLEANUP)" != "true" ] && [ "$(E2E_DRY_RUN)" != "true" ]; then \
		$(MAKE) cleanup-test-e2e; \
	else \
		echo "Skipping cleanup: Keeping Kind cluster $(KIND_CLUSTER) for debugging"; \
	fi

.PHONY: e2e-catalog
e2e-catalog: ginkgo ## Generate a catalog of E2E suites, specs, labels, and By-steps under test/e2e/catalog/.
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/e2e_catalog \
		--ginkgo "$(GINKGO)" \
		--input-dir test/e2e \
		--output-dir test/e2e/catalog

.PHONY: e2e-manifest-validate
e2e-manifest-validate: ## Validate test/e2e/suites.yaml against the generated E2E catalog.
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/e2e_manifest \
		--manifest test/e2e/suites.yaml \
		--catalog test/e2e/catalog/cases.json \
		--require-case-ids

.PHONY: e2e-ci-matrix
e2e-ci-matrix: ## Generate the GitHub Actions E2E matrix from test/e2e/suites.yaml.
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/e2e_plan \
		--manifest test/e2e/suites.yaml \
		--format github-matrix

.PHONY: e2e-nightly-matrix
e2e-nightly-matrix: ## Generate the GitHub Actions nightly E2E matrix. Set E2E_NIGHTLY_PROFILE, E2E_NIGHTLY_LANE, E2E_NIGHTLY_KUBERNETES, or E2E_NIGHTLY_COMPATIBILITY_ROTATION_INDEX.
	@args=""; \
	if [ -n "$(E2E_NIGHTLY_LANE)" ] && [ "$(E2E_NIGHTLY_LANE)" != "all" ]; then args="$${args} --lane $(E2E_NIGHTLY_LANE)"; fi; \
	if [ -n "$(E2E_NIGHTLY_KUBERNETES)" ] && [ "$(E2E_NIGHTLY_KUBERNETES)" != "all" ]; then args="$${args} --kubernetes $(E2E_NIGHTLY_KUBERNETES)"; fi; \
	GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/e2e_plan \
		--manifest test/e2e/suites.yaml \
		--format github-nightly-matrix \
		--profile "$(or $(E2E_NIGHTLY_PROFILE),daily)" \
		--compatibility-rotation-index "$(or $(E2E_NIGHTLY_COMPATIBILITY_ROTATION_INDEX),0)" \
		$${args}

.PHONY: e2e-release-matrix
e2e-release-matrix: ## Generate the GitHub Actions release-gate E2E matrix. Set E2E_RELEASE_LANE or E2E_RELEASE_KUBERNETES.
	@args=""; \
	if [ -n "$(E2E_RELEASE_LANE)" ] && [ "$(E2E_RELEASE_LANE)" != "all" ]; then args="$${args} --lane $(E2E_RELEASE_LANE)"; fi; \
	if [ -n "$(E2E_RELEASE_KUBERNETES)" ] && [ "$(E2E_RELEASE_KUBERNETES)" != "all" ]; then args="$${args} --kubernetes $(E2E_RELEASE_KUBERNETES)"; fi; \
	GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/e2e_plan \
		--manifest test/e2e/suites.yaml \
		--format github-nightly-matrix \
		--profile release-gate \
		$${args}

.PHONY: e2e-ci-matrix-validate
e2e-ci-matrix-validate: ## Validate that the GitHub Actions E2E matrix can be generated.
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/e2e_plan \
		--manifest test/e2e/suites.yaml \
		--format github-matrix >/dev/null

.PHONY: e2e-nightly-matrix-validate
e2e-nightly-matrix-validate: ## Validate that the nightly E2E matrices can be generated.
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/e2e_plan \
		--manifest test/e2e/suites.yaml \
		--format github-nightly-matrix \
		--profile daily >/dev/null
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/e2e_plan \
		--manifest test/e2e/suites.yaml \
		--format github-nightly-matrix \
		--profile weekly-full \
		--compatibility-rotation-index 0 >/dev/null
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/e2e_plan \
		--manifest test/e2e/suites.yaml \
		--format github-nightly-matrix \
		--profile weekly-full \
		--compatibility-rotation-index 1 >/dev/null

.PHONY: e2e-release-matrix-validate
e2e-release-matrix-validate: ## Validate that the release-gate E2E matrix can be generated.
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/e2e_plan \
		--manifest test/e2e/suites.yaml \
		--format github-nightly-matrix \
		--profile release-gate >/dev/null
	@GOFLAGS="$(GOFLAGS_VENDOR)" go run ./hack/tools/e2e_plan \
		--manifest test/e2e/suites.yaml \
		--format github-nightly-matrix \
		--profile release-gate \
		--lane core \
		--kubernetes 1.36.1 >/dev/null

.PHONY: verify-e2e-manifest
verify-e2e-manifest: e2e-catalog e2e-manifest-validate e2e-ci-matrix-validate e2e-nightly-matrix-validate e2e-release-matrix-validate ## Verify the E2E catalog and suite manifest are up-to-date.
	@{ \
		git diff --exit-code -- test/e2e/catalog test/e2e/suites.yaml; \
	} || { \
		echo "E2E catalog or suite manifest is out of date. Run 'make e2e-catalog e2e-manifest-validate' and commit the result."; \
		git --no-pager diff -- test/e2e/catalog test/e2e/suites.yaml; \
		exit 1; \
	}

.PHONY: perf-baseline
perf-baseline: perf-v2-capture ## Capture performance baseline samples and write v2 distribution baselines.

.PHONY: perf-v2-capture
perf-v2-capture: ## Capture v2 performance samples and update per-scenario distribution baselines.
	go run ./hack/perfcheck capture \
		--samples="$(PERF_SAMPLES)" \
		--warmups="$(PERF_WARMUPS)" \
		--scenarios="$(PERF_SCENARIOS)" \
		--scenario-manifest="$(PERF_SCENARIOS_FILE)" \
		--kind="$(KIND)" \
		--node-image="$(PERF_NODE_IMAGE)" \
		--baseline-dir="$(PERF_BASELINE_DIR)" \
		--artifact-dir="$(PERF_ARTIFACT_DIR)" \
		--environment="$(PERF_ENVIRONMENT)" \
		--scenario-timeout="$(PERF_SCENARIO_TIMEOUT)" \
		--continue-on-sample-error="$(PERF_CONTINUE_ON_SAMPLE_ERROR)" \
		--minimum-successful-samples="$(PERF_MIN_SUCCESSFUL_SAMPLES)" \
		--operator-image="$(PERF_OPERATOR_IMAGE)" \
		--config-init-image="$(PERF_CONFIG_INIT_IMAGE)" \
		--backup-executor-image="$(PERF_BACKUP_EXECUTOR_IMAGE)" \
		--upgrade-executor-image="$(PERF_UPGRADE_EXECUTOR_IMAGE)" \
		--openbao-version="$(PERF_OPENBAO_VERSION)" \
		--openbao-image="$(PERF_OPENBAO_IMAGE)" \
		--upgrade-from-version="$(PERF_UPGRADE_FROM_VERSION)" \
		--upgrade-from-image="$(PERF_UPGRADE_FROM_IMAGE)" \
		--upgrade-to-version="$(PERF_UPGRADE_TO_VERSION)" \
		--upgrade-to-image="$(PERF_UPGRADE_TO_IMAGE)" \
		--api-server-cidr="$(PERF_API_SERVER_CIDR)" \
		--storage-class="$(PERF_STORAGE_CLASS)" \
		--tenant-churn-count="$(PERF_TENANT_CHURN_COUNT)"

.PHONY: verify-perf
verify-perf: perf-v2-verify ## Run v2 performance verification against committed distribution baselines.

.PHONY: perf-v2-verify
perf-v2-verify: ## Run v2 performance verification against committed distribution baselines.
	go run ./hack/perfcheck verify \
		--samples="$(PERF_SAMPLES)" \
		--warmups="$(PERF_WARMUPS)" \
		--scenarios="$(PERF_SCENARIOS)" \
		--scenario-manifest="$(PERF_SCENARIOS_FILE)" \
		--kind="$(KIND)" \
		--node-image="$(PERF_NODE_IMAGE)" \
		--baseline-dir="$(PERF_BASELINE_DIR)" \
		--policy="$(PERF_POLICY_FILE)" \
		--artifact-dir="$(PERF_ARTIFACT_DIR)" \
		--environment="$(PERF_ENVIRONMENT)" \
		--scenario-timeout="$(PERF_SCENARIO_TIMEOUT)" \
		--continue-on-sample-error="$(PERF_CONTINUE_ON_SAMPLE_ERROR)" \
		--operator-image="$(PERF_OPERATOR_IMAGE)" \
		--config-init-image="$(PERF_CONFIG_INIT_IMAGE)" \
		--backup-executor-image="$(PERF_BACKUP_EXECUTOR_IMAGE)" \
		--upgrade-executor-image="$(PERF_UPGRADE_EXECUTOR_IMAGE)" \
		--openbao-version="$(PERF_OPENBAO_VERSION)" \
		--openbao-image="$(PERF_OPENBAO_IMAGE)" \
		--upgrade-from-version="$(PERF_UPGRADE_FROM_VERSION)" \
		--upgrade-from-image="$(PERF_UPGRADE_FROM_IMAGE)" \
		--upgrade-to-version="$(PERF_UPGRADE_TO_VERSION)" \
		--upgrade-to-image="$(PERF_UPGRADE_TO_IMAGE)" \
		--api-server-cidr="$(PERF_API_SERVER_CIDR)" \
		--storage-class="$(PERF_STORAGE_CLASS)" \
		--tenant-churn-count="$(PERF_TENANT_CHURN_COUNT)"

.PHONY: verify-perf-smoke
verify-perf-smoke: perf-v2-smoke ## Run a lightweight v2 performance smoke gate (PR-focused).

.PHONY: perf-v2-smoke
perf-v2-smoke: ## Run a lightweight v2 performance smoke gate (PR-focused).
	go run ./hack/perfcheck verify \
		--samples=1 \
		--warmups=0 \
		--scenarios="$(PERF_SMOKE_SCENARIOS)" \
		--scenario-manifest="$(PERF_SCENARIOS_FILE)" \
		--kind="$(KIND)" \
		--node-image="$(PERF_NODE_IMAGE)" \
		--baseline-dir="$(PERF_BASELINE_DIR)" \
		--policy="$(PERF_POLICY_FILE)" \
		--artifact-dir="$(PERF_ARTIFACT_DIR)" \
		--environment="$(PERF_ENVIRONMENT)" \
		--scenario-timeout="$(PERF_SMOKE_SCENARIO_TIMEOUT)" \
		--continue-on-sample-error="$(PERF_CONTINUE_ON_SAMPLE_ERROR)" \
		--operator-image="$(PERF_OPERATOR_IMAGE)" \
		--config-init-image="$(PERF_CONFIG_INIT_IMAGE)" \
		--backup-executor-image="$(PERF_BACKUP_EXECUTOR_IMAGE)" \
		--upgrade-executor-image="$(PERF_UPGRADE_EXECUTOR_IMAGE)" \
		--openbao-version="$(PERF_OPENBAO_VERSION)" \
		--openbao-image="$(PERF_OPENBAO_IMAGE)" \
		--upgrade-from-version="$(PERF_UPGRADE_FROM_VERSION)" \
		--upgrade-from-image="$(PERF_UPGRADE_FROM_IMAGE)" \
		--upgrade-to-version="$(PERF_UPGRADE_TO_VERSION)" \
		--upgrade-to-image="$(PERF_UPGRADE_TO_IMAGE)" \
		--api-server-cidr="$(PERF_API_SERVER_CIDR)" \
		--storage-class="$(PERF_STORAGE_CLASS)" \
		--tenant-churn-count="$(PERF_TENANT_CHURN_COUNT)"

.PHONY: perf-v2-report
perf-v2-report: ## Render a v2 performance report from existing sample artifacts.
	go run ./hack/perfcheck report \
		--scenario-manifest="$(PERF_SCENARIOS_FILE)" \
		--scenarios="$(PERF_SCENARIOS)" \
		--baseline-dir="$(PERF_BASELINE_DIR)" \
		--policy="$(PERF_POLICY_FILE)" \
		--artifact-dir="$(PERF_ARTIFACT_DIR)" \
		--environment="$(PERF_ENVIRONMENT)" \
		--previous-summary="$(PERF_PREVIOUS_SUMMARY)" \
		--fail-on-failures="$(PERF_REPORT_FAIL_ON_FAILURES)"

.PHONY: mutation-smoke
mutation-smoke: gomu ## Run a fast mutation smoke check (operation lifecycle package).
	@out="dist/mutation/smoke-$$(date -u +%Y%m%dT%H%M%SZ)"; \
	GOMU_BIN="$(GOMU)" bash hack/ci/run-gomu.sh \
		--path "./internal/service/opslifecycle" \
		--workers "1" \
		--timeout "20" \
		--incremental "false" \
		--ci-mode "false" \
		--fail-on-gate "false" \
		--output-dir "$$out"; \
	echo "Mutation smoke artifacts: $$out"

.PHONY: mutation-target
mutation-target: gomu ## Run mutation testing for one target path (set MUTATION_TARGET_PATH=./internal/<pkg>).
	@out="dist/mutation/target-$$(date -u +%Y%m%dT%H%M%SZ)"; \
	GOMU_BIN="$(GOMU)" bash hack/ci/run-gomu.sh \
		--path "$(MUTATION_TARGET_PATH)" \
		--workers "$(MUTATION_WORKERS)" \
		--timeout "$(MUTATION_TIMEOUT)" \
		--incremental "$(MUTATION_INCREMENTAL)" \
		--go-flags "$(MUTATION_GOFLAGS)" \
		--go-mem-limit "$(MUTATION_GOMEMLIMIT)" \
		--ci-mode "false" \
		--fail-on-gate "false" \
		--top-survivors "$(MUTATION_TOP_SURVIVORS)" \
		--output-dir "$$out"; \
	echo "Mutation target artifacts: $$out"

.PHONY: mutation-local
mutation-local: gomu ## Run broad local mutation testing (report-only mode).
	@out="dist/mutation/local-$$(date -u +%Y%m%dT%H%M%SZ)"; \
	GOMU_BIN="$(GOMU)" bash hack/ci/run-gomu.sh \
		--path "$(MUTATION_PATHS)" \
		--workers "$(MUTATION_WORKERS)" \
		--timeout "$(MUTATION_TIMEOUT)" \
		--incremental "$(MUTATION_INCREMENTAL)" \
		--go-flags "$(MUTATION_GOFLAGS)" \
		--go-mem-limit "$(MUTATION_GOMEMLIMIT)" \
		--ci-mode "false" \
		--fail-on-gate "false" \
		--top-survivors "$(MUTATION_TOP_SURVIVORS)" \
		--output-dir "$$out"; \
	echo "Mutation local artifacts: $$out"

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Tear down the Kind cluster used for e2e tests
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}; \
	echo "Deleting Kind cluster '$(KIND_CLUSTER)'"; \
	$(KIND) delete cluster --name "$(KIND_CLUSTER)" || true

BENCH_ARTIFACT_DIR ?= dist/bench
BENCH_PKG ?= ./...
BENCH_FILTER ?= .
BENCH_COUNT ?= 10
GO_LICENSES_ALLOWED ?= Apache-2.0 BSD-2-Clause BSD-3-Clause ISC MIT MPL-2.0 Unicode-DFS-2016
GO_LICENSES_IGNORE ?= github.com/kubebao/openbao-operator
GO_LICENSES_PACKAGE_TARGETS ?= ./cmd/controller ./cmd/bao-backup ./cmd/bao-upgrade ./cmd/provisioner
LICENSE_REPORT_DIR ?= dist/licenses
go_licenses_empty :=
go_licenses_space := $(go_licenses_empty) $(go_licenses_empty)
go_licenses_comma := ,
GO_LICENSES_ALLOWED_CSV := $(subst $(go_licenses_space),$(go_licenses_comma),$(strip $(GO_LICENSES_ALLOWED)))
SEMGREP_ARTIFACT_DIR ?= dist/semgrep
SEMGREP_CONFIG_FLAGS ?= --config p/default --config .semgrep/rules
SEMGREP_TARGETS ?= ./cmd ./internal ./api ./hack ./config ./.github ./website/assets/js
SEMGREP_OUTPUT_JSON ?= $(SEMGREP_ARTIFACT_DIR)/semgrep.json

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

.PHONY: lint-ci
lint-ci: lint-config lint vet-tagged verify-arch-policy lint-testonly-exports test-ast lint-ast ## Run CI lint gates (golangci-lint + tagged vet + test-only export audit + ast-grep tests/scans).

.PHONY: vulncheck
vulncheck: govulncheck govulncheck-ignore ## Run govulncheck to scan for known vulnerabilities (production code only). Findings listed in .govulnignore are ignored. Set VULNCHECK_SHOW_IGNORED=true to print traces even if all findings are ignored.
	@"$(GOVULNCHECK_IGNORE)" -govulncheck "$(GOVULNCHECK)" -ignore .govulnignore -show-ignored="$${VULNCHECK_SHOW_IGNORED:-false}" ./...

.PHONY: semgrep-rules-test
semgrep-rules-test: semgrep ## Validate repo-local Semgrep custom rules against test fixtures.
	"$(SEMGREP)" scan --test --config .semgrep/rules .semgrep/tests

.PHONY: semgrep-scan
semgrep-scan: semgrep ## Run Semgrep against security-relevant code, config, and CI surfaces (report-only).
	"$(SEMGREP)" scan --metrics=off $(SEMGREP_CONFIG_FLAGS) $(SEMGREP_TARGETS)

.PHONY: semgrep-ci
semgrep-ci: semgrep semgrep-rules-test ## Run the blocking CI-equivalent Semgrep scan and write JSON output.
	@mkdir -p "$(SEMGREP_ARTIFACT_DIR)"
	"$(SEMGREP)" scan --metrics=off --error --json --output "$(SEMGREP_OUTPUT_JSON)" $(SEMGREP_CONFIG_FLAGS) $(SEMGREP_TARGETS)

.PHONY: license-check
license-check: verify-vendor go-licenses ## Verify shipped Go dependencies use approved licenses.
	@GOFLAGS="$(GOFLAGS_VENDOR)" "$(GO_LICENSES)" check \
		--allowed_licenses="$(GO_LICENSES_ALLOWED_CSV)" \
		--ignore "$(GO_LICENSES_IGNORE)" \
		$(GO_LICENSES_PACKAGE_TARGETS)

.PHONY: license-report
license-report: verify-vendor go-licenses ## Write a CSV inventory for shipped Go dependency licenses to dist/licenses/.
	@mkdir -p "$(LICENSE_REPORT_DIR)"
	@GOFLAGS="$(GOFLAGS_VENDOR)" "$(GO_LICENSES)" report \
		--ignore "$(GO_LICENSES_IGNORE)" \
		$(GO_LICENSES_PACKAGE_TARGETS) \
		> "$(LICENSE_REPORT_DIR)/go-licenses-report.csv" \
		2> "$(LICENSE_REPORT_DIR)/go-licenses-report.stderr.log"
	@echo "License report written to $(LICENSE_REPORT_DIR)/go-licenses-report.csv"

.PHONY: security-ci
security-ci: vulncheck license-check semgrep-ci security-scan-fs ## Run CI-equivalent security checks.

.PHONY: security-scan
security-scan: security-scan-fs security-scan-image ## Run Trivy security scans (filesystem and container image).

.PHONY: security-scan-fs
security-scan-fs: ## Run the Trivy filesystem scan used by CI.
	# Keep local scans aligned with CI (see .github/workflows/ci.yml):
	# - Use "misconfig" (not deprecated "config")
	# - Explicitly load ignore rules from .trivyignore
	# - Render Helm charts against a modern Kubernetes version
	trivy fs \
		--scanners vuln,misconfig \
		--severity HIGH,CRITICAL \
		--ignore-unfixed \
		--exit-code 1 \
		--ignorefile .trivyignore \
		--skip-version-check \
		--helm-kube-version 1.34.0 \
		--skip-files config/rbac/provisioner_minimal_role.yaml \
		--skip-files charts/openbao-operator/templates/rbac/provisioner-clusterroles.yaml \
		--skip-files config/rbac/single_tenant_clusterrole.yaml \
		--skip-files config/overlays/single-tenant/single_tenant_clusterrole.yaml \
		--skip-files config/overlays/single-tenant-custom-identity/single_tenant_clusterrole.yaml \
		--skip-files dist/install.yaml \
		--skip-dirs test/manifests \
		--skip-dirs vendor \
		--skip-dirs bin \
		--skip-dirs .devenv \
		.

.PHONY: security-scan-image
security-scan-image: ## Run a Trivy image scan against IMG.
	trivy image \
		--severity HIGH,CRITICAL \
		--ignore-unfixed \
		--exit-code 1 \
		--skip-version-check \
		${IMG}

.PHONY: debug-controller
debug-controller: dlv manifests generate fmt vet ## Debug the cluster controller locally with Delve.
	"$(DLV)" debug ./cmd/main.go -- controller

.PHONY: debug-provisioner
debug-provisioner: dlv manifests generate fmt vet ## Debug the provisioner locally with Delve.
	"$(DLV)" debug ./cmd/main.go -- provisioner

.PHONY: debug-test
debug-test: dlv ## Debug a Go test package with Delve. Set PKG=./path and optionally TEST=Regex.
	@if [ -z "$(PKG)" ]; then \
		echo "Error: PKG is required. Example: make debug-test PKG=./internal/service/upgrade TEST=TestManager"; \
		exit 1; \
	fi
	@args='test $(PKG)'; \
	if [ -n "$(TEST)" ]; then \
		exec "$(DLV)" $$args -- -test.run='$(TEST)'; \
	else \
		exec "$(DLV)" $$args; \
	fi

.PHONY: air-controller
air-controller: air ## Run the cluster controller with live reload via Air.
	"$(AIR)" -c hack/dev/air.controller.toml

.PHONY: air-provisioner
air-provisioner: air ## Run the provisioner with live reload via Air.
	"$(AIR)" -c hack/dev/air.provisioner.toml

.PHONY: bench
bench: ## Run Go benchmarks. Override BENCH_PKG, BENCH_FILTER, and BENCH_COUNT as needed.
	GOFLAGS="$(GOFLAGS_VENDOR)" go test -run='^$$' -bench="$(BENCH_FILTER)" -benchmem -count="$(BENCH_COUNT)" $(BENCH_PKG)

.PHONY: bench-save
bench-save: ## Run Go benchmarks and save the output under dist/bench/.
	@mkdir -p "$(BENCH_ARTIFACT_DIR)"
	@out="$${BENCH_OUT:-$(BENCH_ARTIFACT_DIR)/bench-$$(date -u +%Y%m%dT%H%M%SZ).txt}"; \
	echo "Writing benchmark output to $$out"; \
	GOFLAGS="$(GOFLAGS_VENDOR)" go test -run='^$$' -bench="$(BENCH_FILTER)" -benchmem -count="$(BENCH_COUNT)" $(BENCH_PKG) | tee "$$out"

.PHONY: bench-compare
bench-compare: benchstat ## Compare benchmark result files with benchstat. Set OLD=/path/to/old.txt NEW=/path/to/new.txt.
	@if [ -z "$(OLD)" ] || [ -z "$(NEW)" ]; then \
		echo "Error: OLD and NEW are required. Example: make bench-compare OLD=dist/bench/old.txt NEW=dist/bench/new.txt"; \
		exit 1; \
	fi
	"$(BENCHSTAT)" "$(OLD)" "$(NEW)"

.PHONY: test-policy-reconciliation-openbao
test-policy-reconciliation-openbao: ## Test policy authorization and repair against a disposable OpenBao container (requires Docker).
	@GOFLAGS="$(GOFLAGS_VENDOR)" go test -tags=e2e ./test/e2e/policyreconciliation -count=1 -v

.PHONY: test-tls-key-exchange-openbao
test-tls-key-exchange-openbao: ## Test hybrid PQ TLS with operator-issued certificates against OpenBao 2.7.1 (requires Docker).
	@GOFLAGS="$(GOFLAGS_VENDOR)" go test -tags=e2e ./test/e2e/tlskeyexchange -count=1 -v

.PHONY: test-controller-jwt-openbao
OPENBAO_JWT_TEST_IMAGE ?= openbao/openbao:2.6.4
test-controller-jwt-openbao: setup-envtest ## Test target JWT issuance and cross-target replay against disposable OpenBao containers.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" \
		OPENBAO_JWT_TEST_IMAGE="$(OPENBAO_JWT_TEST_IMAGE)" GOFLAGS="$(GOFLAGS_VENDOR)" \
		go test -tags=integration ./test/integration -run '^TestControllerJWT' -count=1 -v
