##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

ifndef wait
  wait = true
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" apply --server-side -f -; else echo "No CRDs to install; skipping."; fi

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) --wait=$(wait) -f -; else echo "No CRDs to delete; skipping."; fi

.PHONY: deploy
deploy: manifests kustomize ## Deploy both provisioner and controller to the K8s cluster specified in ~/.kube/config.
	@tmp="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmp"' EXIT; \
	cp -R config "$$tmp/config"; \
	for f in "$$tmp/config/manager/controller.yaml" "$$tmp/config/manager/provisioner.yaml"; do \
		python3 -c 'import pathlib,re,sys; p=pathlib.Path(sys.argv[1]); v=sys.argv[2]; q=chr(34); s=p.read_text(encoding="utf-8"); s=re.sub(r"(\n\s*-\s*name:\s*OPERATOR_VERSION\s*\n\s*value:\s*)(\"[^\"]*\"|[^\n#]+)", lambda m: m.group(1)+q+v+q, s, count=1); p.write_text(s, encoding="utf-8")' "$$f" "$(OPERATOR_VERSION)"; \
	done; \
	( cd "$$tmp/config/manager" && "$(KUSTOMIZE)" edit set image controller=${IMG} ); \
	"$(KUSTOMIZE)" build "$$tmp/config/default" | "$(KUBECTL)" apply --server-side -f -

.PHONY: deploy-dev
deploy-dev: ## Build, push, and deploy the manager image (avoids stale local tags).
	@dev_img="$${IMG:-k3d-registry.localhost:5000/openbao-operator:dev-$$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"; \
	if [ "$(OPERATOR_VERSION)" = "0.0.0" ]; then \
		dev_operator_version=edge; \
	else \
		dev_operator_version="$(OPERATOR_VERSION)"; \
	fi; \
	echo "Deploying dev image: $$dev_img"; \
	echo "Using OPERATOR_VERSION=$$dev_operator_version for dev helper images"; \
	$(MAKE) docker-build docker-push IMG="$$dev_img"; \
	$(MAKE) deploy IMG="$$dev_img" OPERATOR_VERSION="$$dev_operator_version"

.PHONY: undeploy
undeploy: kustomize ## Undeploy both provisioner and controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion. Call with wait=false to avoid waiting for finalizers.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) --wait=$(wait) -f -
