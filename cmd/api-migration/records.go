package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/cmd/api-migration/primitives"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

// Restore and Tenant records move as records. Each is re-created in the
// destination group and retired on the side that is left, never with
// cascading. A restore record controls its executor Job, which moves with it.
// A Tenant controls nothing: its provisioner renders the namespace RBAC.

func (r RecordPlan) source() primitives.TransferIdentity {
	return primitives.TransferIdentity{APIVersion: primitives.PlanSourceAPIVersion, Kind: r.Kind,
		Namespace: r.Namespace, Name: r.Name, UID: r.SourceUID}
}

func (r RecordPlan) destination() primitives.TransferIdentity {
	return primitives.TransferIdentity{APIVersion: primitives.PlanDestinationAPIVersion, Kind: r.Kind,
		Namespace: r.Namespace, Name: r.Name, UID: r.DestinationUID}
}

func (r RecordPlan) scope() custodyScope { return custodyScope{namespaceUID: r.NamespaceUID} }

func (r RecordPlan) label() string {
	return strings.ToLower(strings.TrimPrefix(r.Kind, "OpenBao")) + " " + r.Namespace + "/" + r.Name
}

// planRecords nominates every source record of kind in namespace, or in every
// namespace when it is empty.
func (p *Procedure) planRecords(ctx context.Context, kind, namespace string) ([]RecordPlan, error) {
	k := p.kube
	existing, err := k.listObjects(ctx, primitives.PlanDestinationAPIVersion, kind, namespace)
	if err != nil {
		return nil, err
	}
	if len(existing) != 0 {
		return nil, fmt.Errorf("%d destination %s record(s) already exist", len(existing), kind)
	}
	sources, err := k.listObjects(ctx, primitives.PlanSourceAPIVersion, kind, namespace)
	if err != nil {
		return nil, err
	}
	var records []RecordPlan
	for _, source := range sources {
		namespaceUID, err := k.namespaceUID(ctx, source.GetNamespace())
		if err != nil {
			return nil, err
		}
		record := RecordPlan{Kind: kind, Namespace: source.GetNamespace(), NamespaceUID: namespaceUID,
			Name: source.GetName(), SourceUID: source.GetUID(), SpecDigest: digestJSON(source.Object["spec"])}
		if kind == tenantKind {
			record.TargetNamespace, _, _ = unstructured.NestedString(source.Object, "spec", "targetNamespace")
			record.Provisioned, _, _ = unstructured.NestedBool(source.Object, "status", "provisioned")
		}
		if err := p.inspectRecord(ctx, record); err != nil {
			return nil, fmt.Errorf("%s: %w", record.label(), err)
		}
		if kind == restoreKind {
			if err := p.planRestoreJobs(ctx, &record); err != nil {
				return nil, fmt.Errorf("%s: %w", record.label(), err)
			}
		}
		records = append(records, record)
	}
	return records, nil
}

// planRestoreJobs checks the restore import exemption and inventories the
// executor Jobs the record controls.
func (p *Procedure) planRestoreJobs(ctx context.Context, record *RecordPlan) error {
	if err := p.checkMigrateVerb(ctx, "openbaorestores", record.Namespace, record.Name); err != nil {
		return err
	}
	preview := record.destination()
	preview.UID = ""
	discovery, err := primitives.DiscoverManagedResources(ctx, p.kube.metadata, record.scope().options(record.source(), preview))
	if err != nil {
		return err
	}
	if len(discovery.Blockers) != 0 {
		data, _ := json.Marshal(discovery.Blockers)
		return fmt.Errorf("resources without provable custody: %s", data)
	}
	for _, transfer := range discovery.Transfers {
		record.Jobs = append(record.Jobs, transfer.Resource)
	}
	return nil
}

// inspectRecord checks that a planned source record is unchanged, that a
// restore is finished, and that a Tenant's namespace is not terminating.
func (p *Procedure) inspectRecord(ctx context.Context, record RecordPlan) error {
	operatorNamespace, err := p.kube.namespaceUID(ctx, p.plan.Config.OperatorNamespace)
	if err != nil {
		return err
	}
	source, err := p.kube.getObject(ctx, primitives.PlanSourceAPIVersion, record.Kind, record.Namespace, record.Name)
	if err != nil {
		return err
	}
	if source == nil || source.GetUID() != record.SourceUID {
		return errors.New("source record is missing or was replaced since planning")
	}
	if digestJSON(source.Object["spec"]) != record.SpecDigest {
		return errors.New("source specification changed since planning")
	}
	if err := validateRetirementFinalizers(source); err != nil {
		return err
	}
	if _, err := primitives.InspectSource(ctx, p.kube.client, primitives.ImportRequest{
		Source: primitives.ObjectIdentity{APIVersion: primitives.PlanSourceAPIVersion, Kind: record.Kind,
			Namespace: record.Namespace, Name: record.Name, UID: record.SourceUID},
		NamespaceUID:      record.NamespaceUID,
		RelatedNamespaces: []primitives.NamespaceIdentity{{Name: p.plan.Config.OperatorNamespace, UID: operatorNamespace}},
	}); err != nil {
		return err
	}
	if record.Kind == tenantKind {
		namespace := &corev1.Namespace{}
		err := p.kube.client.Get(ctx, client.ObjectKey{Name: record.TargetNamespace}, namespace)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err == nil && !namespace.DeletionTimestamp.IsZero() {
			return fmt.Errorf("target namespace %s is terminating; wait for it to be deleted", record.TargetNamespace)
		}
	}
	return nil
}

// forEachRecord runs one convergent action per record and reports a summary.
func (p *Procedure) forEachRecord(records []RecordPlan, run func(*RecordPlan) (string, error)) (string, error) {
	var details []string
	for i := range records {
		record := &records[i]
		detail, err := run(record)
		if err != nil {
			return "", fmt.Errorf("%s: %w", record.label(), err)
		}
		details = append(details, record.label()+": "+detail)
	}
	return strings.Join(details, "; "), p.state.Save(p.plan)
}

// recordDestination resolves the imported record, including one created by a
// run that stopped before recording its UID.
func (p *Procedure) recordDestination(ctx context.Context, record *RecordPlan) (*unstructured.Unstructured, error) {
	destination, err := p.kube.getObject(ctx, primitives.PlanDestinationAPIVersion, record.Kind, record.Namespace, record.Name)
	if err != nil || destination == nil {
		return nil, err
	}
	if destination.GetAnnotations()[primitives.GateSourceUIDAnnotation] != string(record.SourceUID) ||
		(record.DestinationUID != "" && destination.GetUID() != record.DestinationUID) {
		return nil, errors.New("destination record was not created by this migration")
	}
	record.DestinationUID = destination.GetUID()
	return destination, nil
}

// importRecord re-creates a record. A restore keeps its exact status: its
// destination controller stays held until commit, and verification requires
// that status first. A Tenant's status is recomputed by its provisioner.
func (p *Procedure) importRecord(ctx context.Context, record *RecordPlan) (string, error) {
	destination, err := p.recordDestination(ctx, record)
	if err != nil {
		return "", err
	}
	if destination != nil && !primitives.ImportStatusPending(destination) {
		if status, _, _ := unstructured.NestedMap(destination.Object, "status"); len(status) > 0 || record.Kind == tenantKind {
			return "already imported", nil
		}
	}
	source, err := p.kube.getObject(ctx, primitives.PlanSourceAPIVersion, record.Kind, record.Namespace, record.Name)
	if err != nil {
		return "", err
	}
	if source == nil || source.GetUID() != record.SourceUID || digestJSON(source.Object["spec"]) != record.SpecDigest {
		return "", errors.New("source record is missing or changed since planning")
	}
	operatorNamespace, err := p.kube.namespaceUID(ctx, p.plan.Config.OperatorNamespace)
	if err != nil {
		return "", err
	}
	labels, annotations := importKeys(source)
	request := primitives.ImportRequest{
		Source: primitives.ObjectIdentity{APIVersion: primitives.PlanSourceAPIVersion, Kind: record.Kind,
			Namespace: record.Namespace, Name: record.Name, UID: record.SourceUID, ResourceVersion: source.GetResourceVersion()},
		NamespaceUID: record.NamespaceUID, MigrationID: p.plan.MigrationID,
		LabelKeys: labels, AnnotationKeys: annotations, ExpectedDestinationUID: record.DestinationUID,
		RelatedNamespaces: []primitives.NamespaceIdentity{{Name: p.plan.Config.OperatorNamespace, UID: operatorNamespace}},
	}
	payload, err := primitives.BuildImport(ctx, p.kube.client, request)
	if err != nil {
		return "", err
	}
	request.ExpectedImportDigest = payload.Digest
	result, err := primitives.Import(ctx, p.kube.client, request)
	if result.Destination.UID != "" {
		record.DestinationUID = result.Destination.UID
		if saveErr := p.state.Save(p.plan); saveErr != nil {
			return "", errors.Join(err, saveErr)
		}
	}
	if err != nil {
		return "", err
	}
	if !result.StatusImported {
		return "", errors.New("status import was not confirmed")
	}
	return "imported as " + string(record.DestinationUID), nil
}

// verifyRecords checks each record on the active side. While a source restore
// record exists, its destination copy must carry its exact status; once the
// source is retired, the destination controller owns that status. Every
// executor Job that remains is controlled by the active record.
func (p *Procedure) verifyRecords(ctx context.Context, records []RecordPlan, apiVersion string) (string, error) {
	return p.forEachRecord(records, func(record *RecordPlan) (string, error) {
		source, err := p.kube.getObject(ctx, primitives.PlanSourceAPIVersion, record.Kind, record.Namespace, record.Name)
		if err != nil {
			return "", err
		}
		if source != nil && source.GetUID() != record.SourceUID {
			return "", errors.New("source record was replaced since planning")
		}
		destination, err := p.recordDestination(ctx, record)
		if err != nil {
			return "", err
		}
		active, other, activeObject := record.source(), record.destination(), source
		if apiVersion == primitives.PlanDestinationAPIVersion {
			active, other, activeObject = other, active, destination
		}
		if activeObject == nil {
			return "", fmt.Errorf("%s record is missing", apiVersion)
		}
		if record.Kind != restoreKind {
			return "present", nil
		}
		if destination != nil && apiVersion == primitives.PlanDestinationAPIVersion {
			status, _, _ := unstructured.NestedMap(destination.Object, "status")
			if len(status) == 0 || primitives.ImportStatusPending(destination) {
				return "", errors.New("destination record has no imported status")
			}
			if source != nil && digestJSON(status) != digestJSON(source.Object["status"]) {
				return "", errors.New("destination record status differs from the source record")
			}
		}
		if (other.APIVersion == primitives.PlanSourceAPIVersion && source == nil) ||
			(other.APIVersion == primitives.PlanDestinationAPIVersion && destination == nil) {
			other.UID = ""
			preview, err := primitives.DiscoverManagedResources(ctx, p.kube.metadata, record.scope().options(active, other))
			if err != nil {
				return "", err
			}
			if len(preview.Blockers) != 0 {
				data, _ := json.Marshal(preview.Blockers)
				return "", fmt.Errorf("resources without provable custody: %s", data)
			}
			return fmt.Sprintf("present; controls %d Job(s)", len(preview.Transfers)), nil
		}
		state, err := discoverCustody(ctx, p.kube, record.scope(), other, active)
		if err != nil {
			return "", err
		}
		if err := state.blockerError(); err != nil {
			return "", err
		}
		if len(state.pending) != 0 {
			return "", fmt.Errorf("%d Job(s) still reference %s", len(state.pending), other.APIVersion)
		}
		return fmt.Sprintf("present; controls %d Job(s)", len(state.settled)), nil
	})
}

// retireRecord deletes one side's record without cascading. A restore record
// is deleted only once it controls nothing: its Job belongs to the other side.
func (p *Procedure) retireRecord(ctx context.Context, record *RecordPlan, retire, keep primitives.TransferIdentity) (string, error) {
	object, err := p.kube.getObject(ctx, retire.APIVersion, record.Kind, record.Namespace, record.Name)
	if err != nil {
		return "", err
	}
	if object == nil {
		return alreadyRetired, nil
	}
	if object.GetUID() != retire.UID {
		return "", fmt.Errorf("%s record was replaced; refusing to delete it", retire.APIVersion)
	}
	if deleting, err := orphaningDeletion(object); err != nil || deleting {
		if err != nil {
			return "", err
		}
		return retire.APIVersion + " record deletion finished",
			p.kube.waitDeleted(ctx, retire.APIVersion, record.Kind, record.Namespace, record.Name)
	}
	if record.Kind == restoreKind {
		state, err := discoverCustody(ctx, p.kube, record.scope(), retire, keep)
		if err != nil {
			return "", err
		}
		if err := state.blockerError(); err != nil {
			return "", err
		}
		if len(state.pending) != 0 {
			return "", fmt.Errorf("%d Job(s) still reference %s", len(state.pending), retire.APIVersion)
		}
	}
	return retire.APIVersion + " record deleted without cascading",
		p.kube.removeFinalizerAndDelete(ctx, retire.APIVersion, record.Kind, record.Namespace, record.Name, retire.UID)
}

// waitServing waits until the operator of apiVersion can serve every Cluster:
// in a multi-tenant installation its provisioner has re-rendered each tenant
// namespace's RBAC for its group, and then every Cluster is available.
func (p *Procedure) waitServing(ctx context.Context, apiVersion string) error {
	if apiVersion == primitives.PlanDestinationAPIVersion {
		if err := p.checkDestinationBackupHelper(ctx); err != nil {
			return err
		}
	}
	if p.plan.Operator.Mode == tenancyMulti {
		group := strings.Split(apiVersion, "/")[0]
		if err := p.kube.wait(ctx, "tenant RBAC to grant "+group, func(ctx context.Context) (bool, error) {
			for _, tenant := range p.plan.Tenants {
				if !tenant.Provisioned {
					continue
				}
				role := &rbacv1.Role{}
				err := p.kube.client.Get(ctx, client.ObjectKey{Namespace: tenant.TargetNamespace, Name: constants.TenantRoleName}, role)
				if apierrors.IsNotFound(err) || (err == nil && !roleGrantsClusters(role, group)) {
					return false, nil
				}
				if err != nil {
					return false, err
				}
			}
			return true, nil
		}); err != nil {
			return err
		}
	}
	return p.waitClusters(ctx, apiVersion)
}

func roleGrantsClusters(role *rbacv1.Role, group string) bool {
	return slices.ContainsFunc(role.Rules, func(rule rbacv1.PolicyRule) bool {
		return slices.Contains(rule.APIGroups, group) && slices.Contains(rule.Resources, "openbaoclusters")
	})
}
