package provisioner

import (
	"context"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	serviceprovisioner "github.com/kubebao/openbao-operator/internal/service/provisioner"
)

const (
	TenantSecretsReaderRoleName        = serviceprovisioner.TenantSecretsReaderRoleName
	TenantSecretsReaderRoleBindingName = serviceprovisioner.TenantSecretsReaderRoleBindingName
	TenantSecretsWriterRoleName        = serviceprovisioner.TenantSecretsWriterRoleName
	TenantSecretsWriterRoleBindingName = serviceprovisioner.TenantSecretsWriterRoleBindingName
)

// Provisioner coordinates tenant RBAC provisioning behind the app facade.
type Provisioner interface {
	EnsureTenantRBAC(ctx context.Context, tenant *openbaov1alpha1.OpenBaoTenant) error
	EnsureTenantSecretRBAC(ctx context.Context, namespace string) error
	IsTenantNamespaceProvisioned(ctx context.Context, namespace string) (bool, error)
	CleanupTenantResources(ctx context.Context, namespace string) error
	FinalizeNamespace(ctx context.Context, namespace string, uid types.UID) (bool, error)
}

// ProvisionerDependencies groups the runtime collaborators required to build the tenant provisioner.
type ProvisionerDependencies struct {
	Client    client.Client
	APIReader client.Reader
	Logger    logr.Logger
}

// NewProvisioner constructs the tenant provisioner behind the app facade so controllers do not depend on the service package directly.
func NewProvisioner(deps ProvisionerDependencies) (Provisioner, error) {
	return serviceprovisioner.NewManagerWithReader(deps.Client, deps.APIReader, deps.Logger)
}
