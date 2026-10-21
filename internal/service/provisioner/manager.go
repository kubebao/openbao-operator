package provisioner

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/internal/adapter/kube"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
)

const (
	NamespacePodSecurityLabelsModeEnforce  = "enforce"
	NamespacePodSecurityLabelsModeExternal = "external"
)

// Manager handles the provisioning of RBAC resources for tenant namespaces.
type Manager struct {
	client                         client.Client
	reader                         client.Reader
	operatorSA                     OperatorServiceAccount
	logger                         logr.Logger
	namespacePodSecurityLabelsMode string
}

// NewManager creates a new provisioner Manager.
func NewManager(c client.Client, logger logr.Logger) (*Manager, error) {
	// Get operator namespace from environment or use default
	saNamespace := os.Getenv("POD_NAMESPACE")
	if saNamespace == "" {
		saNamespace = os.Getenv("OPERATOR_NAMESPACE")
	}
	if saNamespace == "" {
		saNamespace = "openbao-operator-system"
	}

	// Discover the controller ServiceAccount name dynamically
	// The base name is "controller", which becomes "openbao-operator-controller" after kustomize prefix
	controllerSAName := os.Getenv("OPERATOR_SERVICE_ACCOUNT_NAME")
	if controllerSAName == "" {
		controllerSAName = "openbao-operator-controller"
	}
	controllerSANamespace := saNamespace

	namespacePodSecurityLabelsMode, err := resolveNamespacePodSecurityLabelsMode()
	if err != nil {
		return nil, err
	}

	return &Manager{
		client: c,
		reader: c,
		operatorSA: OperatorServiceAccount{
			Name:      controllerSAName,
			Namespace: controllerSANamespace,
		},
		logger:                         logger,
		namespacePodSecurityLabelsMode: namespacePodSecurityLabelsMode,
	}, nil
}

// NewManagerWithReader uses the supplied reader to authorize namespace finalization.
func NewManagerWithReader(c client.Client, reader client.Reader, logger logr.Logger) (*Manager, error) {
	manager, err := NewManager(c, logger)
	if err != nil {
		return nil, err
	}
	if reader != nil {
		manager.reader = reader
	}
	return manager, nil
}

func resolveNamespacePodSecurityLabelsMode() (string, error) {
	mode := strings.TrimSpace(os.Getenv(constants.EnvTenantNamespacePodSecurityLabelsMode))
	if mode == "" {
		return NamespacePodSecurityLabelsModeEnforce, nil
	}
	mode = strings.ToLower(mode)
	switch mode {
	case NamespacePodSecurityLabelsModeEnforce, NamespacePodSecurityLabelsModeExternal:
		return mode, nil
	default:
		return "", fmt.Errorf("%s must be %q or %q, got %q",
			constants.EnvTenantNamespacePodSecurityLabelsMode,
			NamespacePodSecurityLabelsModeEnforce,
			NamespacePodSecurityLabelsModeExternal,
			mode,
		)
	}
}

// applyResource uses Server-Side Apply.
// Unlike infra.applyResource, this does NOT set owner references since
// tenant RBAC resources should not be garbage-collected with any single cluster.
func (m *Manager) applyResource(ctx context.Context, obj client.Object) error {
	applyConfig, err := kube.ToApplyConfiguration(obj, m.client)
	if err != nil {
		return fmt.Errorf("failed to convert object to ApplyConfiguration: %w", err)
	}

	applyOpts := []client.ApplyOption{
		client.ForceOwnership,
		client.FieldOwner("openbao-provisioner"),
	}

	if err := m.client.Apply(ctx, applyConfig, applyOpts...); err != nil {
		if operatorerrors.IsTransientKubernetesAPI(err) || apierrors.IsConflict(err) {
			return operatorerrors.WrapTransientKubernetesAPI(
				fmt.Errorf("failed to apply resource %s/%s: %w", obj.GetNamespace(), obj.GetName(), err))
		}
		return fmt.Errorf("failed to apply resource %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	return nil
}
