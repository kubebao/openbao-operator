package networking

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ErrServiceIPFamilyChangeNotSupported identifies a request to change an existing
// Service's immutable primary IP family.
var ErrServiceIPFamilyChangeNotSupported = errors.New("primary IP family change is not supported")

func (m *Manager) validateServicePrimaryIPFamily(ctx context.Context, desired *corev1.Service) error {
	if len(desired.Spec.IPFamilies) == 0 {
		return nil
	}
	existing := &corev1.Service{}
	if err := m.reader.Get(ctx, client.ObjectKeyFromObject(desired), existing); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to read Service %s/%s IP families: %w", desired.Namespace, desired.Name, err)
	}
	if len(existing.Spec.IPFamilies) == 0 || existing.Spec.IPFamilies[0] == desired.Spec.IPFamilies[0] {
		return nil
	}
	return fmt.Errorf(
		"%w: Service %s/%s uses %s, but the configuration requests %s; keep %s first in ipFamilies or omit ipFamilies to retain the allocated family",
		ErrServiceIPFamilyChangeNotSupported, desired.Namespace, desired.Name,
		existing.Spec.IPFamilies[0], desired.Spec.IPFamilies[0], existing.Spec.IPFamilies[0],
	)
}
