package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubebao/openbao-operator/cmd/api-migration/primitives"
)

// Phases are informational. Every step decides from observed state, so the
// phase only selects which commands are allowed.
const (
	phasePlanned     = "Planned"
	phaseMigrating   = "Migrating"
	phaseVerified    = "Verified"
	phaseCommitting  = "Committing"
	phaseComplete    = "Complete"
	phaseRollingBack = "RollingBack"
	phaseRolledBack  = "RolledBack"
)

const (
	planFile          = "plan.json"
	afterCommitFile   = "after-commit.md"
	afterRollbackFile = "after-rollback.md"
	planVersion       = 3
)

// Plan is everything the procedure pins before it changes the installation.
type Plan struct {
	Version       int          `json:"version"`
	CreatedAt     time.Time    `json:"createdAt"`
	Phase         string       `json:"phase"`
	Config        Config       `json:"config"`
	KubeSystemUID types.UID    `json:"kubeSystemUID"`
	MigrationID   string       `json:"migrationID"`
	Operator      OperatorPlan `json:"operator"`
	// These optional fields describe recorded evidence, not current health.
	DestinationVersion string              `json:"destinationVersion,omitempty"`
	LastVerification   *VerificationRecord `json:"lastVerification,omitempty"`
	// GitOps are the GitOps objects that manage the installation; each must
	// stay suspended until commit completes and Git is updated.
	GitOps []GitOpsManager `json:"gitops,omitempty"`
	// Progress records the steps the current command completed.
	Progress Progress `json:"progress"`
	Helm     HelmPlan `json:"helm"`
	// Deployments are the operator Deployments with their pre-migration replica counts.
	Deployments []DeploymentPlan `json:"deployments"`
	Clusters    []ClusterPlan    `json:"clusters"`
	// Restores are the finished restore records the operator manages. Each is
	// re-created as history; a remaining executor Job moves with its record.
	Restores []RecordPlan `json:"restores,omitempty"`
	// Tenants are every Tenant record. Each is re-created; the destination
	// provisioner re-renders its namespace RBAC for the new group.
	Tenants []RecordPlan `json:"tenants,omitempty"`
}

type VerificationRecord struct {
	CheckedAt  time.Time `json:"checkedAt"`
	APIVersion string    `json:"apiVersion"`
	Step       string    `json:"step"`
}

type HelmPlan struct {
	SourceRevision int    `json:"sourceRevision"`
	SourceChart    string `json:"sourceChart"`
	// SourceValues are the release's user-supplied values; the destination chart reuses them.
	SourceValues map[string]any `json:"sourceValues"`
	// DestinationChart is the copy under the state directory.
	DestinationChart   string         `json:"destinationChart"`
	DestinationOverlay map[string]any `json:"destinationOverlay,omitempty"`
	// RestoredRevision is the revision a rollback created from SourceRevision.
	// The chart versions can match, so the procedure tracks revisions instead.
	RestoredRevision int `json:"restoredRevision,omitempty"`
}

// Progress lets a re-run continue after the last completed step.
type Progress struct {
	Command   string   `json:"command,omitempty"`
	Completed []string `json:"completed,omitempty"`
}

// OperatorPlan is how the source operator is installed and configured, as its
// controller Deployment shows it.
type OperatorPlan struct {
	// Install is "helm" for a Helm release, or "manifests"; Flavour then says
	// whether they were rendered from the chart or built with Kustomize.
	Install string `json:"install"`
	Flavour string `json:"flavour"`
	Version string `json:"version"`
	// BackupImage records the source helper selection, including a valueFrom reference.
	BackupImage *corev1.EnvVar `json:"backupImage,omitempty"`
	// Mode is the tenancy mode; TargetNamespace is set in single mode.
	Mode            string `json:"mode"`
	TargetNamespace string `json:"targetNamespace,omitempty"`
	// Audience is the installation-wide controller audience used in Shared mode.
	Audience string `json:"audience"`
	// Checkpoint is the operator state the procedure last waited for.
	Checkpoint string `json:"checkpoint,omitempty"`
}

type DeploymentPlan struct {
	Name           string    `json:"name"`
	UID            types.UID `json:"uid"`
	Replicas       int32     `json:"replicas"`
	ServiceAccount string    `json:"serviceAccount"`
	Controller     bool      `json:"controller"`
}

type ClusterPlan struct {
	Namespace    string    `json:"namespace"`
	NamespaceUID types.UID `json:"namespaceUID"`
	Name         string    `json:"name"`
	SourceUID    types.UID `json:"sourceUID"`
	// VoterStatefulSet pins the active voter workload, including a BlueGreen
	// revision suffix. Empty selects Name for plans written before this field.
	VoterStatefulSet string `json:"voterStatefulSet,omitempty"`
	// Shared marks a Cluster whose controller uses the installation-wide audience.
	// Its JWT role does not name the Cluster UID, so the handover leaves it unchanged.
	Shared         bool                          `json:"shared,omitempty"`
	SpecDigest     string                        `json:"specDigest"`
	DestinationUID types.UID                     `json:"destinationUID,omitempty"`
	Retained       []primitives.TransferIdentity `json:"retained"`
	Inventory      []primitives.TransferIdentity `json:"inventory"`
	// Audience is the controller audience planned for both sides: an imported
	// Target Cluster keeps its source identity, so the JWT role never changes.
	Audience string `json:"audience"`
	// RaftPeers and Snapshot are recorded only with an administrator token.
	RaftPeers []string             `json:"raftPeers,omitempty"`
	Claims    []ClaimBinding       `json:"claims"`
	Pods      map[string]types.UID `json:"pods"`
	Snapshot  string               `json:"snapshot,omitempty"`
	// Paused marks a Cluster whose reconciliation was paused at plan. Its
	// operator leaves the workload alone, so it moves as it is: availability is
	// not required, and the Pods that were ready at plan must stay ready.
	Paused    bool     `json:"paused,omitempty"`
	ReadyPods []string `json:"readyPods,omitempty"`
	// LoginSkipped says why the controller login was not verified at plan.
	// Only a paused Cluster may skip it, and later verifications skip it too.
	LoginSkipped string `json:"loginSkipped,omitempty"`
}

func (c ClusterPlan) voterStatefulSet() string {
	if c.VoterStatefulSet != "" {
		return c.VoterStatefulSet
	}
	return c.Name
}

type ClaimBinding struct {
	Name     string    `json:"name"`
	UID      types.UID `json:"uid"`
	Volume   string    `json:"volume"`
	VolumeID types.UID `json:"volumeUID"`
}

// RecordPlan pins a restore or Tenant record.
type RecordPlan struct {
	Kind           string    `json:"kind"`
	Namespace      string    `json:"namespace"`
	NamespaceUID   types.UID `json:"namespaceUID"`
	Name           string    `json:"name"`
	SourceUID      types.UID `json:"sourceUID"`
	SpecDigest     string    `json:"specDigest"`
	DestinationUID types.UID `json:"destinationUID,omitempty"`
	// Jobs are the executor Jobs a restore record controlled when planned.
	Jobs []primitives.TransferIdentity `json:"jobs,omitempty"`
	// TargetNamespace and Provisioned describe a Tenant when planned.
	TargetNamespace string `json:"targetNamespace,omitempty"`
	Provisioned     bool   `json:"provisioned,omitempty"`
}

func (c ClusterPlan) source() primitives.TransferIdentity {
	return primitives.TransferIdentity{APIVersion: primitives.PlanSourceAPIVersion, Kind: clusterKind,
		Namespace: c.Namespace, Name: c.Name, UID: c.SourceUID}
}

func (c ClusterPlan) destination() primitives.TransferIdentity {
	return primitives.TransferIdentity{APIVersion: primitives.PlanDestinationAPIVersion, Kind: clusterKind,
		Namespace: c.Namespace, Name: c.Name, UID: c.DestinationUID}
}

// State is an exclusively locked state directory.
type State struct {
	dir  string
	lock *os.File
}

func openState(dir string, create bool) (*State, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve state directory: %w", err)
	}
	dir = absolute
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create state directory: %w", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			return nil, errors.New("plan requires a new, empty state directory; preserve the existing artifacts and use another --state directory")
		}
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("state directory %s is not available", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("state directory must not be accessible by other users")
	}
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, errors.New("another api-migration process holds this state directory")
	}
	return &State{dir: dir, lock: lock}, nil
}

func (s *State) Close() {
	_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	_ = s.lock.Close()
}

func (s *State) path(name string) string { return filepath.Join(s.dir, name) }

func (s *State) Load() (*Plan, error) {
	data, err := os.ReadFile(s.path(planFile))
	if err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}
	plan := &Plan{}
	if err := json.Unmarshal(data, plan); err != nil {
		return nil, fmt.Errorf("decode plan: %w", err)
	}
	return plan, nil
}

func (s *State) Save(plan *Plan) error {
	return writePrivateJSON(s.path(planFile), plan)
}

// writePrivateJSON replaces path atomically with owner-only permissions.
func writePrivateJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(data, '\n'))
}

func writePrivate(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
