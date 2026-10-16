##@ Build

.PHONY: update-trusted-root
update-trusted-root: ## Download/update the embedded trusted_root.json file for keyless verification.
	@echo "Fetching latest trusted_root.json from Sigstore TUF repository..."
	@go run internal/adapter/security/fetch_trusted_root.go

.PHONY: build
build: verify-trusted-root manifests generate fmt vet ## Build manager binary (dispatcher for provisioner and controller).
	go build -o bin/manager cmd/main.go

.PHONY: build-linux
build-linux: verify-trusted-root manifests generate fmt vet ## Build all binaries for Linux/AMD64 (used by CI fast Dockerfiles).
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/linux_amd64/manager cmd/main.go
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/linux_amd64/bao-init-config ./cmd/bao-config-init
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/linux_amd64/bao-wrapper ./cmd/bao-wrapper
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/linux_amd64/bao-probe ./cmd/bao-probe
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/linux_amd64/bao-backup ./cmd/bao-backup
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/linux_amd64/bao-upgrade-executor ./cmd/bao-upgrade

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host (defaults to controller). Use COMMAND=provisioner to run the provisioner.
	@command="${COMMAND}"; \
	if [ -z "$$command" ]; then \
		command="controller"; \
		echo "COMMAND not set; defaulting to '$$command'. Use 'make run COMMAND=provisioner' to run the provisioner."; \
	fi; \
	go run ./cmd/main.go "$$command"

.PHONY: run-provisioner
run-provisioner: manifests generate fmt vet ## Run the provisioner controller from your host.
	go run ./cmd/main.go provisioner

.PHONY: run-controller
run-controller: manifests generate fmt vet ## Run the OpenBaoCluster controller from your host.
	go run ./cmd/main.go controller

.PHONY: docker-build
docker-build: ## Build docker image with the manager (dispatcher binary for both provisioner and controller).
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

.PHONY: docker-build-init
docker-build-init: ## Build docker image with the init helper (config rendering + wrapper).
	$(CONTAINER_TOOL) build -f Dockerfile.init -t ${IMG} .

.PHONY: docker-push-init
docker-push-init: ## Push docker image with the init helper (config rendering + wrapper).
	$(CONTAINER_TOOL) push ${IMG}

.PHONY: docker-build-backup
docker-build-backup: ## Build docker image with the backup helper.
	$(CONTAINER_TOOL) build -f Dockerfile.backup -t ${IMG} .

.PHONY: docker-push-backup
docker-push-backup: ## Push docker image with the backup helper.
	$(CONTAINER_TOOL) push ${IMG}

.PHONY: docker-build-upgrade
docker-build-upgrade: ## Build docker image with the upgrade helper.
	$(CONTAINER_TOOL) build -f Dockerfile.upgrade -t ${IMG} .

.PHONY: docker-push-upgrade
docker-push-upgrade: ## Push docker image with the upgrade helper.
	$(CONTAINER_TOOL) push ${IMG}

OPENBAO_SOFTHSM_BASE_IMAGE ?= $(shell sed -n 's/^ARG OPENBAO_BASE_IMAGE=//p' test/e2e/images/openbao-softhsm/Dockerfile)
OPENBAO_SOFTHSM_IMG ?= openbao-softhsm:dev
PYKMIP_BASE_IMAGE ?= $(shell sed -n 's/^ARG PYKMIP_BASE_IMAGE=//p' test/e2e/images/pykmip-server/Dockerfile)
PYKMIP_SERVER_IMG ?= pykmip-server:dev

.PHONY: docker-build-e2e-openbao-softhsm
docker-build-e2e-openbao-softhsm: ## Build the test-only OpenBao image with SoftHSM PKCS#11 support.
	$(CONTAINER_TOOL) build \
		-f test/e2e/images/openbao-softhsm/Dockerfile \
		--build-arg OPENBAO_BASE_IMAGE=$(OPENBAO_SOFTHSM_BASE_IMAGE) \
		-t $(OPENBAO_SOFTHSM_IMG) \
		test/e2e/images/openbao-softhsm

.PHONY: docker-build-e2e-pykmip-server
docker-build-e2e-pykmip-server: ## Build the test-only PyKMIP server image used by KMIP unseal E2E coverage.
	$(CONTAINER_TOOL) build \
		-f test/e2e/images/pykmip-server/Dockerfile \
		--build-arg PYKMIP_BASE_IMAGE=$(PYKMIP_BASE_IMAGE) \
		-t $(PYKMIP_SERVER_IMG) \
		test/e2e/images/pykmip-server

.PHONY: docker-release
docker-release: docker-release-build docker-release-push ## Build and push all images to registry with consistent VERSION.

.PHONY: docker-release-build
docker-release-build: ## Build all images with consistent VERSION (does not push).
	@echo "Building images with VERSION=$(VERSION) to REGISTRY=$(REGISTRY)..."
	$(CONTAINER_TOOL) build -t $(MANAGER_IMG) .
	$(CONTAINER_TOOL) build -f Dockerfile.init -t $(INIT_IMG_RELEASE) .
	$(CONTAINER_TOOL) build -f Dockerfile.backup -t $(BACKUP_IMG) .
	$(CONTAINER_TOOL) build -f Dockerfile.upgrade -t $(UPGRADE_IMG) .
	@echo "✅ All images built successfully"

.PHONY: docker-release-push
docker-release-push: ## Push all images to registry.
	@echo "Pushing images to $(REGISTRY)..."
	$(CONTAINER_TOOL) push $(MANAGER_IMG)
	$(CONTAINER_TOOL) push $(INIT_IMG_RELEASE)
	$(CONTAINER_TOOL) push $(BACKUP_IMG)
	$(CONTAINER_TOOL) push $(UPGRADE_IMG)
	@echo "✅ All images pushed successfully"

.PHONY: docker-build-all

PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager (dispatcher) for cross-platform support
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name openbao-operator-builder
	$(CONTAINER_TOOL) buildx use openbao-operator-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm openbao-operator-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployments (provisioner and controller).
	@tmp="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmp"' EXIT; \
	out="$(PWD)/dist/install.yaml"; \
	mkdir -p dist; \
	cp -R config "$$tmp/config"; \
	for f in "$$tmp/config/manager/controller.yaml" "$$tmp/config/manager/provisioner.yaml"; do \
		python3 -c 'import pathlib,re,sys; p=pathlib.Path(sys.argv[1]); v=sys.argv[2]; q=chr(34); s=p.read_text(encoding="utf-8"); s=re.sub(r"(\n\s*-\s*name:\s*OPERATOR_VERSION\s*\n\s*value:\s*)(\"[^\"]*\"|[^\n#]+)", lambda m: m.group(1)+q+v+q, s, count=1); p.write_text(s, encoding="utf-8")' "$$f" "$(OPERATOR_VERSION)"; \
	done; \
		( cd "$$tmp/config/manager" && "$(KUSTOMIZE)" edit set image controller=${IMG} ); \
		"$(KUSTOMIZE)" build "$$tmp/config/default" > "$$out"

.PHONY: build-crds
build-crds: manifests kustomize ## Generate a consolidated YAML containing CRDs only.
	@mkdir -p dist; \
	out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" > dist/crds.yaml; else echo "No CRDs to export; skipping."; fi

.PHONY: build-policy-approvals
build-policy-approvals: ## Generate both release policy approval files from the built-in policies.
	go run ./hack/tools/operator_policy_approval --output-dir dist
