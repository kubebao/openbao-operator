package main

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The handoff writes run as a dedicated migrator principal, which the chart's
// lock-policy exception admits while the operators are stopped. Unless the
// config names an existing one, the tool manages it: a ServiceAccount in the
// operator namespace, and a Role with exactly the handoff writes in each
// namespace it writes to. It exists only from migrate until commit or rollback
// completes.

const (
	labelManagedBy      = "app.kubernetes.io/managed-by"
	migratorManagedBy   = "kubebao-api-migration"
	labelMigrationID    = "migration.kubebao.org/id"
	migratorAccessScope = "the handoff writes in this namespace"
)

func (p *Procedure) managedMigrator() bool { return p.plan.Config.Migrator == "" }

// migratorNamespaces are the namespaces the migrator writes to: every Cluster
// namespace and every namespace with a restore Job to move.
func (p *Procedure) migratorNamespaces() []string {
	namespaces := p.restoreNamespaces()
	for _, cluster := range p.plan.Clusters {
		namespaces = append(namespaces, cluster.Namespace)
	}
	return slices.Compact(slices.Sorted(slices.Values(namespaces)))
}

// migratorRules grants exactly the writes migratorAccess checks.
func migratorRules() []rbacv1.PolicyRule {
	access := migratorAccess()
	rules := make([]rbacv1.PolicyRule, 0, len(access))
	for _, access := range access {
		resource := access.Resource
		if access.Subresource != "" {
			resource += "/" + access.Subresource
		}
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{access.Group}, Resources: []string{resource},
			Verbs: []string{access.Verb}})
	}
	return rules
}

func (p *Procedure) migratorObjects(namespaces []string) []client.Object {
	name := managedMigratorName(p.plan.Config.Release)
	labels := map[string]string{labelManagedBy: migratorManagedBy, labelMigrationID: p.plan.MigrationID}
	meta := func(namespace string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels,
			Annotations: map[string]string{"migration.kubebao.org/grants": migratorAccessScope}}
	}
	objects := make([]client.Object, 0, 1+2*len(namespaces))
	objects = append(objects, &corev1.ServiceAccount{ObjectMeta: meta(p.plan.Config.OperatorNamespace)})
	for _, namespace := range namespaces {
		objects = append(objects,
			&rbacv1.Role{ObjectMeta: meta(namespace), Rules: migratorRules()},
			&rbacv1.RoleBinding{ObjectMeta: meta(namespace),
				RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
				Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name,
					Namespace: p.plan.Config.OperatorNamespace}}})
	}
	return objects
}

// planMigrator proves the handoff principal can exist before anything changes:
// a configured one by its grants, a managed one by a server-side dry run,
// which also applies the RBAC escalation checks to the administrator.
func (p *Procedure) planMigrator(ctx context.Context, namespaces []string) error {
	namespaces = slices.Compact(slices.Sorted(slices.Values(namespaces)))
	if !p.managedMigrator() {
		if err := p.checkMigrator(ctx); err != nil {
			return err
		}
		return p.checkMigratorAccess(ctx, namespaces)
	}
	for _, object := range p.migratorObjects(namespaces) {
		existing := object.DeepCopyObject().(client.Object)
		err := p.kube.client.Get(ctx, client.ObjectKeyFromObject(object), existing)
		if err == nil {
			return fmt.Errorf("%T %s/%s already exists; set migrator to use an existing principal",
				object, object.GetNamespace(), object.GetName())
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
		if err := p.kube.client.Create(ctx, object, client.DryRunAll); err != nil {
			return fmt.Errorf("the administrator cannot create the migrator %T %s/%s: %w",
				object, object.GetNamespace(), object.GetName(), err)
		}
	}
	return nil
}

// ensureMigrator creates the managed principal, or adopts its own objects from
// an interrupted run, and then checks that impersonation and its grants work.
func (p *Procedure) ensureMigrator(ctx context.Context) (string, error) {
	if !p.managedMigrator() {
		return "using the configured migrator " + p.plan.Config.Migrator, p.checkMigrator(ctx)
	}
	for _, object := range p.migratorObjects(p.migratorNamespaces()) {
		existing := object.DeepCopyObject().(client.Object)
		err := p.kube.client.Get(ctx, client.ObjectKeyFromObject(object), existing)
		switch {
		case apierrors.IsNotFound(err):
			if err := p.kube.client.Create(ctx, object); err != nil && !apierrors.IsAlreadyExists(err) {
				return "", fmt.Errorf("create migrator %T %s/%s: %w", object, object.GetNamespace(), object.GetName(), err)
			}
		case err != nil:
			return "", err
		case existing.GetLabels()[labelMigrationID] != p.plan.MigrationID:
			return "", fmt.Errorf("%T %s/%s was not created by this migration", object, object.GetNamespace(), object.GetName())
		}
	}
	if err := p.kube.wait(ctx, "migrator grants", func(ctx context.Context) (bool, error) {
		return p.checkMigrator(ctx) == nil, nil
	}); err != nil {
		return "", p.checkMigrator(ctx)
	}
	return fmt.Sprintf("managed migrator %s can write in %d namespace(s)", p.plan.Config.migratorPrincipal(),
		len(p.migratorNamespaces())), nil
}

// removeMigrator deletes the managed principal's objects; others are kept.
func (p *Procedure) removeMigrator(ctx context.Context) (string, error) {
	if !p.managedMigrator() {
		return "the configured migrator is kept", nil
	}
	removed := 0
	for _, object := range slices.Backward(p.migratorObjects(p.migratorNamespaces())) {
		existing := object.DeepCopyObject().(client.Object)
		err := p.kube.client.Get(ctx, client.ObjectKeyFromObject(object), existing)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if existing.GetLabels()[labelMigrationID] != p.plan.MigrationID {
			return "", fmt.Errorf("%T %s/%s was not created by this migration", object, object.GetNamespace(), object.GetName())
		}
		uid := existing.GetUID()
		if err := p.kube.client.Delete(ctx, existing, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
			return "", err
		}
		removed++
	}
	return fmt.Sprintf("%d migrator object(s) removed", removed), nil
}
