# Security Guardrails

Source: `test/e2e/Security_Guardrails_test.go`

Note: recorded checkpoints are best-effort extracts from literal `By(...)` calls visible to `ginkgo outline`.

## Cases

| Case ID | Spec | State | Covers | Labels |
| --- | --- | --- | --- | --- |
| `admission-runtime-binding-loss` | pauses managed-resource reconciliation when a required admission binding disappears, then recovers when restored | active | `admission-runtime-recheck`, `managed-resource-pause-on-policy-loss` | `security`, `critical`, `admission`, `pentest` |
| `security-guardrails-accepts-structured-configuration-protected-stanzas-cannot-2a18b9bd` | accepts structured configuration (protected stanzas cannot be overridden) | active | _none_ | `security`, `critical`, `admission` |
| `security-guardrails-blocks-cross-namespace-tenant-targeting-self-6a21b1fd` | blocks cross-namespace tenant targeting (self-service mode) | active | _none_ | `security`, `critical`, `admission` |
| `security-guardrails-enforces-hardened-profile-invariants-446324b0` | enforces Hardened profile invariants | active | _none_ | `security`, `critical`, `admission` |
| `security-guardrails-enforces-digest-pinned-images-for-managed-a108b1a3` | enforces digest-pinned images for managed workloads when digest enforcement is required | active | _none_ | `security`, `critical`, `admission` |
| `security-guardrails-requires-backup-and-restore-credential-secret-aa243663` | requires backup and restore credential Secret read authorization | active | _none_ | `security`, `critical`, `admission` |
| `security-guardrails-requires-transit-credential-secret-read-authorization-abe9d539` | requires transit credential Secret read authorization | active | _none_ | `security`, `critical`, `admission` |
| `security-guardrails-reports-degraded-when-gateway-api-crds-d5cb95ff` | reports Degraded when Gateway API CRDs are missing | active | _none_ | `security`, `critical`, `config` |
| `security-guardrails-applies-admission-guardrails-to-controller-rbac-7093e068` | applies admission guardrails to controller RBAC writes | active | _none_ | `security`, `critical`, `pentest`, `tokens`, `rbac` |
| `security-guardrails-applies-admission-guardrails-to-provisioner-namespace-451d674f` | applies admission guardrails to provisioner Namespace mutations | active | _none_ | `security`, `critical`, `pentest`, `tokens`, `rbac` |
| `security-guardrails-applies-admission-guardrails-to-provisioner-identity-911cb8f6` | applies admission guardrails to provisioner identity | active | _none_ | `security`, `critical`, `pentest`, `tokens`, `rbac` |
| `security-guardrails-disables-default-serviceaccount-token-automount-67dd5069` | disables default ServiceAccount token automount | active | _none_ | `security`, `critical`, `pentest`, `tokens` |
| `security-guardrails-prevents-operator-identities-from-cluster-scoped-14024c52` | prevents operator identities from cluster-scoped RBAC writes | active | _none_ | `security`, `critical`, `pentest`, `tokens`, `rbac` |
| `security-guardrails-uses-projected-kubernetes-api-token-with-51dbf1f2` | uses projected Kubernetes API token with explicit audience and TTL | active | _none_ | `security`, `critical`, `pentest`, `tokens` |
| `security-guardrails-has-required-validatingadmissionpolicy-dependencies-installed-and-87ec7cda` | has required ValidatingAdmissionPolicy dependencies installed and correctly bound | active | _none_ | `security`, `critical`, `rbac` |
| `security-guardrails-restricts-openbao-pod-serviceaccount-pod-patching-abbf5716` | restricts OpenBao pod ServiceAccount pod patching to cluster pods | active | _none_ | `security`, `critical`, `rbac`, `pentest` |
| `security-guardrails-allows-managed-pod-deletion-during-maintenance-80b5ee0d` | allows managed Pod deletion during maintenance with cluster maintenance permission | active | _none_ | `security`, `critical`, `tamper` |
| `security-guardrails-prevents-managed-pod-deletion-during-maintenance-238e18f8` | prevents managed Pod deletion during maintenance without cluster maintenance permission | active | _none_ | `security`, `critical`, `tamper` |
| `security-guardrails-prevents-sidecar-injection-via-statefulset-updates-145c71ee` | prevents sidecar injection via StatefulSet updates | active | _none_ | `security`, `critical`, `tamper` |
| `security-guardrails-prevents-unauthorized-deletion-of-the-tls-9e011135` | prevents unauthorized deletion of the TLS CA secret | active | _none_ | `security`, `critical`, `tamper` |
| `security-guardrails-prevents-unauthorized-deletion-of-the-unseal-2ea235de` | prevents unauthorized deletion of the unseal Secret | active | _none_ | `security`, `critical`, `tamper` |
| `restore-target-jwt-ignores-static-token-secret` | uses Target JWT authentication without mounting a supplied static token Secret | active | `restore-target-jwt-precedence` | `security`, `critical`, `tokens`, `pentest` |

## `admission-runtime-binding-loss`

Path: `Security Guardrails > Admission Dependency Runtime Recheck > pauses managed-resource reconciliation when a required admission binding disappears, then recovers when restored`

State: `active`

Generated fallback ID: `security-guardrails-pauses-managed-resource-reconciliation-when-a-ee48afbc`

Covers: `admission-runtime-recheck`, `managed-resource-pause-on-policy-loss`

Labels: `security`, `critical`, `admission`, `pentest`

Recorded checkpoints:
- removing a required admission binding after the cluster is healthy
- waiting for live dependency checks to report the missing binding
- requesting a scale-up that would normally mutate the managed StatefulSet
- proving the controller fails closed and does not mutate the StatefulSet
- restoring the admission binding and verifying recovery


## `security-guardrails-accepts-structured-configuration-protected-stanzas-cannot-2a18b9bd`

Path: `Security Guardrails > Admission Policy Enforcement > accepts structured configuration (protected stanzas cannot be overridden)`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `admission`


## `security-guardrails-blocks-cross-namespace-tenant-targeting-self-6a21b1fd`

Path: `Security Guardrails > Admission Policy Enforcement > blocks cross-namespace tenant targeting (self-service mode)`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `admission`


## `security-guardrails-enforces-hardened-profile-invariants-446324b0`

Path: `Security Guardrails > Admission Policy Enforcement > enforces Hardened profile invariants`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `admission`


## `security-guardrails-enforces-digest-pinned-images-for-managed-a108b1a3`

Path: `Security Guardrails > Admission Policy Enforcement > enforces digest-pinned images for managed workloads when digest enforcement is required`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `admission`


## `security-guardrails-requires-backup-and-restore-credential-secret-aa243663`

Path: `Security Guardrails > Admission Policy Enforcement > requires backup and restore credential Secret read authorization`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `admission`


## `security-guardrails-requires-transit-credential-secret-read-authorization-abe9d539`

Path: `Security Guardrails > Admission Policy Enforcement > requires transit credential Secret read authorization`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `admission`


## `security-guardrails-reports-degraded-when-gateway-api-crds-d5cb95ff`

Path: `Security Guardrails > Configuration Handling > reports Degraded when Gateway API CRDs are missing`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `config`


## `security-guardrails-applies-admission-guardrails-to-controller-rbac-7093e068`

Path: `Security Guardrails > Operator Pod Hardening > applies admission guardrails to controller RBAC writes`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `pentest`, `tokens`, `rbac`

Recorded checkpoints:
- denying controller creation of arbitrary Roles
- denying controller creation of RoleBindings that do not match the allowlisted pattern


## `security-guardrails-applies-admission-guardrails-to-provisioner-namespace-451d674f`

Path: `Security Guardrails > Operator Pod Hardening > applies admission guardrails to provisioner Namespace mutations`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `pentest`, `tokens`, `rbac`

Recorded checkpoints:
- denying Namespace label mutations outside the PSS enforcement keys
- allowing Pod Security Standards enforce=restricted label enforcement


## `security-guardrails-applies-admission-guardrails-to-provisioner-identity-911cb8f6`

Path: `Security Guardrails > Operator Pod Hardening > applies admission guardrails to provisioner identity`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `pentest`, `tokens`, `rbac`

Recorded checkpoints:
- denying creation of non-allowlisted Roles
- denying updates that attempt to broaden the tenant Role
- denying updates that attempt to grant pods/exec on the tenant Role
- denying RBAC writes in system namespaces


## `security-guardrails-disables-default-serviceaccount-token-automount-67dd5069`

Path: `Security Guardrails > Operator Pod Hardening > disables default ServiceAccount token automount`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `pentest`, `tokens`


## `security-guardrails-prevents-operator-identities-from-cluster-scoped-14024c52`

Path: `Security Guardrails > Operator Pod Hardening > prevents operator identities from cluster-scoped RBAC writes`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `pentest`, `tokens`, `rbac`

Recorded checkpoints:
- denying controller clusterrole/clusterrolebinding creation via RBAC
- denying provisioner clusterrole/clusterrolebinding creation via RBAC


## `security-guardrails-uses-projected-kubernetes-api-token-with-51dbf1f2`

Path: `Security Guardrails > Operator Pod Hardening > uses projected Kubernetes API token with explicit audience and TTL`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `pentest`, `tokens`

Recorded checkpoints:
- inspecting the controller projected service account tokens
- inspecting the provisioner projected Kubernetes API token when present


## `security-guardrails-has-required-validatingadmissionpolicy-dependencies-installed-and-87ec7cda`

Path: `Security Guardrails > RBAC & Dependencies > has required ValidatingAdmissionPolicy dependencies installed and correctly bound`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `rbac`

Recorded checkpoints:
- waiting for every required policy to finish type checking without warnings


## `security-guardrails-restricts-openbao-pod-serviceaccount-pod-patching-abbf5716`

Path: `Security Guardrails > RBAC & Dependencies > restricts OpenBao pod ServiceAccount pod patching to cluster pods`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `rbac`, `pentest`

Recorded checkpoints:
- waiting for the cluster StatefulSet to exist and expose the Pod service account
- creating a non-OpenBao pod in the tenant namespace
- waiting for an OpenBao pod to exist


## `security-guardrails-allows-managed-pod-deletion-during-maintenance-80b5ee0d`

Path: `Security Guardrails > Resource Locking (anti-tamper) > allows managed Pod deletion during maintenance with cluster maintenance permission`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `tamper`


## `security-guardrails-prevents-managed-pod-deletion-during-maintenance-238e18f8`

Path: `Security Guardrails > Resource Locking (anti-tamper) > prevents managed Pod deletion during maintenance without cluster maintenance permission`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `tamper`


## `security-guardrails-prevents-sidecar-injection-via-statefulset-updates-145c71ee`

Path: `Security Guardrails > Resource Locking (anti-tamper) > prevents sidecar injection via StatefulSet updates`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `tamper`


## `security-guardrails-prevents-unauthorized-deletion-of-the-tls-9e011135`

Path: `Security Guardrails > Resource Locking (anti-tamper) > prevents unauthorized deletion of the TLS CA secret`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `tamper`


## `security-guardrails-prevents-unauthorized-deletion-of-the-unseal-2ea235de`

Path: `Security Guardrails > Resource Locking (anti-tamper) > prevents unauthorized deletion of the unseal Secret`

State: `active`

Covers: _none_

Labels: `security`, `critical`, `tamper`


## `restore-target-jwt-ignores-static-token-secret`

Path: `Security Guardrails > Restore Token Guardrails > uses Target JWT authentication without mounting a supplied static token Secret`

State: `active`

Generated fallback ID: `security-guardrails-uses-target-jwt-authentication-without-mounting-1cf38427`

Covers: `restore-target-jwt-precedence`

Labels: `security`, `critical`, `tokens`, `pentest`

Recorded checkpoints:
- waiting for restore validation to select JWT authentication
- checking the restore Job uses JWT authentication and excludes the static token
