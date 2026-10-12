package perf

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	restoresvc "github.com/kubebao/openbao-operator/internal/service/restore"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

const (
	nativeRustFSName      = "rustfs"
	nativeRustFSBucket    = "openbao-perf-backups"
	nativeRustFSAccessKey = "rustfsadmin"
	nativeRustFSSecretKey = "rustfsadmin"

	metricBackupTotalSeconds        = "backup_total_seconds"
	metricBackupRequestToJobSeconds = "backup_request_to_job_seconds"
	metricBackupJobDurationSeconds  = "backup_job_duration_seconds"
	metricRestoreTotalSeconds       = "restore_total_seconds"
	metricRestoreValidationSeconds  = "restore_validation_seconds"
	metricRestoreJobDurationSeconds = "restore_job_duration_seconds"
)

type drFixture struct {
	cluster *openbaov1alpha1.OpenBaoCluster
	target  openbaov1alpha1.BackupTarget
}

func (n *nativeScenarioContext) runBackup(ctx context.Context) (Result, error) {
	tracker := newResourceWriteTracker()
	phases := []Phase{}
	phaseTimes := map[string]time.Time{}

	fixture, err := n.prepareDRFixture(ctx, tracker, &phases, phaseTimes)
	if err != nil {
		return n.result(phases, backupMeasurements(phaseTimes, nil, tracker.count)), err
	}

	_, job, err := n.runManualBackup(ctx, fixture.cluster.Name, &phases, phaseTimes, tracker, "backup")
	measurements := backupMeasurements(phaseTimes, job, tracker.count)
	return n.result(phases, measurements), err
}

func (n *nativeScenarioContext) runRestore(ctx context.Context) (Result, error) {
	tracker := newResourceWriteTracker()
	phases := []Phase{}
	phaseTimes := map[string]time.Time{}

	fixture, err := n.prepareDRFixture(ctx, tracker, &phases, phaseTimes)
	if err != nil {
		return n.result(phases, restoreMeasurements(phaseTimes, tracker.count)), err
	}

	backupKey, _, err := n.runManualBackup(
		ctx,
		fixture.cluster.Name,
		&phases,
		phaseTimes,
		tracker,
		"restore_fixture_backup",
	)
	if err != nil {
		return n.result(phases, restoreMeasurements(phaseTimes, tracker.count)), err
	}
	recordPhaseOnce(&phases, phaseTimes, "restore_fixture_ready", time.Now().UTC(), "harness")

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      n.resourceName("perf-restore"),
			Namespace: n.namespace,
			Labels:    n.resourceLabels(),
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: fixture.cluster.Name,
			Source: openbaov1alpha1.RestoreSource{
				Target: fixture.target,
				Key:    backupKey,
			},
			Image: n.opts.BackupExecutorImage,
			Force: true,
		},
	}
	requestedAt := time.Now().UTC()
	if err := n.client.Create(ctx, restore); err != nil {
		return n.result(phases, restoreMeasurements(phaseTimes, tracker.count)), fmt.Errorf("create OpenBaoRestore: %w", err)
	}
	recordPhaseOnce(&phases, phaseTimes, "restore_requested", requestedAt, "harness")

	err = pollUntil(ctx, func() (bool, error) {
		current := &openbaov1alpha1.OpenBaoRestore{}
		if err := n.client.Get(ctx, client.ObjectKeyFromObject(restore), current); err != nil {
			return false, fmt.Errorf("get OpenBaoRestore: %w", err)
		}
		tracker.track("OpenBaoRestore", current)
		if err := tracker.observe(ctx, n.client, n.namespace, fixture.cluster.Name); err != nil {
			return false, err
		}

		configuration := meta.FindStatusCondition(
			current.Status.Conditions,
			restoresvc.RestoreConfigurationConditionType,
		)
		if configuration != nil && configuration.Status == metav1.ConditionTrue {
			recordPhaseOnce(
				&phases,
				phaseTimes,
				"restore_validation_ready",
				conditionTransitionTime(configuration),
				"openbaorestore_status",
			)
		}

		if job, err := n.latestDRJob(ctx, fixture.cluster.Name, constants.ComponentRestore, requestedAt); err != nil {
			return false, err
		} else if job != nil {
			recordPhaseOnce(
				&phases,
				phaseTimes,
				"restore_job_created",
				job.CreationTimestamp.Time,
				"job",
			)
			if jobSucceeded(job) {
				recordPhaseOnce(&phases, phaseTimes, "restore_job_completed", jobCompletionTime(job), "job")
			}
			if jobFailed(job) {
				return false, fmt.Errorf("restore job failed: %s", jobFailureMessage(job))
			}
		}

		if current.Status.StartTime != nil {
			recordPhaseOnce(&phases, phaseTimes, "restore_started", current.Status.StartTime.Time, "openbaorestore_status")
		}
		switch current.Status.Phase {
		case openbaov1alpha1.RestorePhaseCompleted:
			at := time.Now().UTC()
			if current.Status.CompletionTime != nil {
				at = current.Status.CompletionTime.Time
			}
			recordPhaseOnce(&phases, phaseTimes, "restore_recovery_released", at, "openbaorestore_status")
			return true, nil
		case openbaov1alpha1.RestorePhaseFailed:
			return false, fmt.Errorf("OpenBaoRestore failed: %s", strings.TrimSpace(current.Status.Message))
		case openbaov1alpha1.RestorePhaseUnknown:
			return n.resumeFixtureRestore(ctx, current, fixture.cluster, &phases, phaseTimes)
		default:
			return false, nil
		}
	})

	measurements := restoreMeasurements(phaseTimes, tracker.count)
	return n.result(phases, measurements), err
}

func (n *nativeScenarioContext) prepareDRFixture(
	ctx context.Context,
	tracker *resourceWriteTracker,
	phases *[]Phase,
	phaseTimes map[string]time.Time,
) (drFixture, error) {
	storageNamespace := n.resourceName("perf-rustfs")
	if err := n.ensureUnrestrictedNamespace(ctx, storageNamespace); err != nil {
		return drFixture{}, err
	}

	rustfs := e2ehelpers.DefaultRustFSConfig()
	rustfs.Namespace = storageNamespace
	rustfs.Name = nativeRustFSName
	rustfs.AccessKey = nativeRustFSAccessKey
	rustfs.SecretKey = nativeRustFSSecretKey
	rustfs.Buckets = []string{nativeRustFSBucket}
	if err := e2ehelpers.EnsureRustFS(ctx, n.client, n.cfg, rustfs); err != nil {
		return drFixture{}, fmt.Errorf("ensure RustFS fixture: %w", err)
	}
	recordPhaseOnce(phases, phaseTimes, "storage_ready", time.Now().UTC(), "harness")

	secretName := n.resourceName("rustfs-secret")
	credentialsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: n.namespace,
			Labels:    n.resourceLabels(),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"accessKeyId":     []byte(nativeRustFSAccessKey),
			"secretAccessKey": []byte(nativeRustFSSecretKey),
		},
	}
	if err := n.client.Create(ctx, credentialsSecret); err != nil && !apierrors.IsAlreadyExists(err) {
		return drFixture{}, fmt.Errorf("create RustFS credentials secret: %w", err)
	}

	cluster := n.buildDRCluster(
		n.resourceName("perf-dr"),
		fmt.Sprintf("http://%s-svc.%s.svc:9000", nativeRustFSName, storageNamespace),
		secretName,
	)
	if err := n.client.Create(ctx, cluster); err != nil {
		return drFixture{}, fmt.Errorf("create DR OpenBaoCluster: %w", err)
	}
	recordPhaseOnce(phases, phaseTimes, "cluster_created", cluster.CreationTimestamp.Time, "harness")

	networkPolicy := newNativeDRNetworkPolicy(
		n.namespace,
		cluster.Name,
		storageNamespace,
		9000,
		constants.ComponentBackup,
		constants.ComponentRestore,
	)
	for key, value := range n.resourceLabels() {
		networkPolicy.Labels[key] = value
	}
	if err := n.client.Create(ctx, networkPolicy); err != nil && !apierrors.IsAlreadyExists(err) {
		return drFixture{}, fmt.Errorf("create DR network policy: %w", err)
	}

	if err := n.waitForAvailable(ctx, cluster.Name, int(cluster.Spec.Replicas), tracker); err != nil {
		return drFixture{}, err
	}
	recordPhaseOnce(phases, phaseTimes, "cluster_available", time.Now().UTC(), "openbaocluster_status")

	if err := n.waitForBackupConfigurationReady(ctx, cluster.Name, tracker, phases, phaseTimes); err != nil {
		return drFixture{}, err
	}

	return drFixture{cluster: cluster, target: cluster.Spec.Backup.Target}, nil
}

func (n *nativeScenarioContext) buildDRCluster(
	name string,
	storageEndpoint string,
	credentialsSecret string,
) *openbaov1alpha1.OpenBaoCluster {
	cluster := n.buildCluster(name, n.opts.OpenBaoVersion, n.opts.OpenBaoImage, 1)
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Schedule: "0 0 1 1 *",
		Image:    n.opts.BackupExecutorImage,
		Target: openbaov1alpha1.BackupTarget{
			Provider:     constants.StorageProviderS3,
			Endpoint:     storageEndpoint,
			Bucket:       nativeRustFSBucket,
			PathPrefix:   fmt.Sprintf("perf/%s/%s", n.scenario.Name, n.runID),
			UsePathStyle: true,
			CredentialsSecretRef: &corev1.LocalObjectReference{
				Name: credentialsSecret,
			},
		},
		Retention: &openbaov1alpha1.BackupRetention{
			MaxCount: 3,
			MaxAge:   "24h",
		},
	}
	cluster.Spec.Restore = &openbaov1alpha1.RestoreConfig{}
	return cluster
}

func (n *nativeScenarioContext) runManualBackup(
	ctx context.Context,
	clusterName string,
	phases *[]Phase,
	phaseTimes map[string]time.Time,
	tracker *resourceWriteTracker,
	phasePrefix string,
) (string, *batchv1.Job, error) {
	requestedAt := time.Now().UTC()
	if err := n.triggerManualBackup(ctx, clusterName, requestedAt); err != nil {
		return "", nil, err
	}
	recordPhaseOnce(phases, phaseTimes, phasePrefix+"_requested", requestedAt, "harness")

	var (
		backupKey string
		latestJob *batchv1.Job
	)
	err := pollUntil(ctx, func() (bool, error) {
		if err := tracker.observe(ctx, n.client, n.namespace, clusterName); err != nil {
			return false, err
		}
		if job, err := n.latestDRJob(ctx, clusterName, constants.ComponentBackup, requestedAt); err != nil {
			return false, err
		} else if job != nil {
			latestJob = job.DeepCopy()
			recordPhaseOnce(phases, phaseTimes, phasePrefix+"_job_created", job.CreationTimestamp.Time, "job")
			if jobSucceeded(job) {
				recordPhaseOnce(phases, phaseTimes, phasePrefix+"_job_succeeded", jobCompletionTime(job), "job")
			}
			if jobFailed(job) {
				return false, fmt.Errorf("backup job failed: %s", jobFailureMessage(job))
			}
		}

		cluster := &openbaov1alpha1.OpenBaoCluster{}
		if err := n.client.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: n.namespace}, cluster); err != nil {
			return false, fmt.Errorf("get OpenBaoCluster backup status: %w", err)
		}
		if cluster.Status.Backup == nil {
			return false, nil
		}
		if cluster.Status.Backup.LastFailureReason != "" && failureAfter(cluster.Status.Backup.LastFailureTime, requestedAt) {
			return false, fmt.Errorf(
				"backup failed: %s: %s",
				cluster.Status.Backup.LastFailureReason,
				cluster.Status.Backup.LastFailureMessage,
			)
		}
		if cluster.Status.Backup.LastBackupName == "" || cluster.Status.Backup.LastBackupTime == nil {
			return false, nil
		}
		if cluster.Status.Backup.LastBackupTime.Time.Before(requestedAt.Add(-30 * time.Second)) {
			return false, nil
		}
		backupKey = cluster.Status.Backup.LastBackupName
		recordPhaseOnce(
			phases,
			phaseTimes,
			phasePrefix+"_status_recorded",
			cluster.Status.Backup.LastBackupTime.Time,
			"openbaocluster_status",
		)
		return true, nil
	})
	return backupKey, latestJob, err
}

func (n *nativeScenarioContext) triggerManualBackup(
	ctx context.Context,
	clusterName string,
	requestedAt time.Time,
) error {
	cluster := &openbaov1alpha1.OpenBaoCluster{}
	if err := n.client.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: n.namespace}, cluster); err != nil {
		return fmt.Errorf("get OpenBaoCluster for backup trigger: %w", err)
	}
	original := cluster.DeepCopy()
	if cluster.Annotations == nil {
		cluster.Annotations = map[string]string{}
	}
	cluster.Annotations[constants.AnnotationTriggerBackup] = requestedAt.Format(time.RFC3339Nano)
	if err := n.client.Patch(ctx, cluster, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("patch manual backup trigger: %w", err)
	}
	return nil
}

func (n *nativeScenarioContext) waitForBackupConfigurationReady(
	ctx context.Context,
	clusterName string,
	tracker *resourceWriteTracker,
	phases *[]Phase,
	phaseTimes map[string]time.Time,
) error {
	return pollUntil(ctx, func() (bool, error) {
		if err := tracker.observe(ctx, n.client, n.namespace, clusterName); err != nil {
			return false, err
		}
		cluster := &openbaov1alpha1.OpenBaoCluster{}
		if err := n.client.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: n.namespace}, cluster); err != nil {
			return false, fmt.Errorf("get OpenBaoCluster backup condition: %w", err)
		}
		condition := meta.FindStatusCondition(
			cluster.Status.Conditions,
			string(openbaov1alpha1.ConditionBackupConfigurationReady),
		)
		if condition == nil || condition.Status != metav1.ConditionTrue {
			return false, nil
		}
		recordPhaseOnce(
			phases,
			phaseTimes,
			"backup_configuration_ready",
			conditionTransitionTime(condition),
			"openbaocluster_status",
		)
		return true, nil
	})
}

func (n *nativeScenarioContext) latestDRJob(
	ctx context.Context,
	clusterName string,
	component string,
	requestedAt time.Time,
) (*batchv1.Job, error) {
	jobs := &batchv1.JobList{}
	if err := n.client.List(ctx, jobs,
		client.InNamespace(n.namespace),
		client.MatchingLabels{
			constants.LabelAppManagedBy:     constants.LabelValueAppManagedByOpenBaoOperator,
			constants.LabelOpenBaoCluster:   clusterName,
			constants.LabelOpenBaoComponent: component,
		},
	); err != nil {
		return nil, fmt.Errorf("list %s jobs: %w", component, err)
	}
	sort.Slice(jobs.Items, func(i, j int) bool {
		return jobs.Items[i].CreationTimestamp.After(jobs.Items[j].CreationTimestamp.Time)
	})
	cutoff := requestedAt.Add(-30 * time.Second)
	for i := range jobs.Items {
		if jobs.Items[i].CreationTimestamp.Time.Before(cutoff) {
			continue
		}
		return jobs.Items[i].DeepCopy(), nil
	}
	return nil, nil
}

func (n *nativeScenarioContext) ensureUnrestrictedNamespace(ctx context.Context, name string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: n.resourceLabels(),
		},
	}
	err := n.client.Create(ctx, ns)
	if err == nil {
		n.createdNamespaces = appendUniqueString(n.createdNamespaces, name)
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %q: %w", name, err)
	}
	current := &corev1.Namespace{}
	if err := n.client.Get(ctx, types.NamespacedName{Name: name}, current); err != nil {
		return fmt.Errorf("get namespace %q: %w", name, err)
	}
	original := current.DeepCopy()
	if current.Labels == nil {
		current.Labels = map[string]string{}
	}
	for key, value := range n.resourceLabels() {
		current.Labels[key] = value
	}
	if err := n.client.Patch(ctx, current, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("label namespace %q: %w", name, err)
	}
	return nil
}

func backupMeasurements(
	phaseTimes map[string]time.Time,
	job *batchv1.Job,
	writes int,
) map[string]float64 {
	measurements := phaseMeasurements(phaseTimes, "backup_requested", map[string]string{
		metricBackupRequestToJobSeconds: "backup_job_created",
		metricBackupTotalSeconds:        "backup_status_recorded",
	})
	if duration, ok := jobDurationSeconds(job); ok {
		measurements[metricBackupJobDurationSeconds] = duration
		measurements["backup_last_duration_seconds"] = duration
	}
	measurements[metricObservedKubernetesWrites] = float64(writes)
	measurements[metricKubernetesWrites] = float64(writes)
	return measurements
}

func restoreMeasurements(
	phaseTimes map[string]time.Time,
	writes int,
) map[string]float64 {
	measurements := phaseMeasurements(phaseTimes, "restore_requested", map[string]string{
		metricRestoreValidationSeconds: "restore_validation_ready",
		metricRestoreTotalSeconds:      "restore_recovery_released",
	})
	if started := phaseTimes["restore_job_created"]; !started.IsZero() {
		duration := phaseTimes["restore_job_completed"].Sub(started).Seconds()
		if duration < 0 {
			duration = 0
		}
		if !phaseTimes["restore_job_completed"].IsZero() {
			measurements[metricRestoreJobDurationSeconds] = duration
		}
	}
	measurements[metricObservedKubernetesWrites] = float64(writes)
	measurements[metricKubernetesWrites] = float64(writes)
	return measurements
}

func failureAfter(at *metav1.Time, cutoff time.Time) bool {
	return at != nil && !at.Time.Before(cutoff.Add(-30*time.Second))
}

func jobSucceeded(job *batchv1.Job) bool {
	if job == nil {
		return false
	}
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return job.Status.Succeeded > 0
}

func jobFailed(job *batchv1.Job) bool {
	if job == nil {
		return false
	}
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return job.Status.Failed > 0 && job.Status.Active == 0
}

func jobCompletionTime(job *batchv1.Job) time.Time {
	if job == nil {
		return time.Now().UTC()
	}
	if job.Status.CompletionTime != nil {
		return job.Status.CompletionTime.Time
	}
	for _, condition := range job.Status.Conditions {
		if condition.LastTransitionTime.IsZero() {
			continue
		}
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			return condition.LastTransitionTime.Time
		}
	}
	return time.Now().UTC()
}

func jobDurationSeconds(job *batchv1.Job) (float64, bool) {
	if job == nil || job.Status.StartTime == nil {
		return 0, false
	}
	end := jobCompletionTime(job)
	seconds := end.Sub(job.Status.StartTime.Time).Seconds()
	if seconds < 0 {
		seconds = 0
	}
	return seconds, true
}

func jobFailureMessage(job *batchv1.Job) string {
	if job == nil {
		return "job unavailable"
	}
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			msg := strings.TrimSpace(condition.Message)
			if msg == "" {
				msg = strings.TrimSpace(condition.Reason)
			}
			if msg != "" {
				return msg
			}
		}
	}
	return fmt.Sprintf("failed=%d active=%d succeeded=%d", job.Status.Failed, job.Status.Active, job.Status.Succeeded)
}

func newNativeDRNetworkPolicy(
	namespace string,
	clusterName string,
	storageNamespace string,
	storagePort int,
	components ...string,
) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-dr-network-policy", clusterName),
			Namespace: namespace,
			Labels:    map[string]string{},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.LabelOpenBaoCluster: clusterName,
				},
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      constants.LabelOpenBaoComponent,
						Operator: metav1.LabelSelectorOpIn,
						Values:   components,
					},
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeEgress,
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				nativeNamespaceEgressRule("kube-system", corev1.ProtocolUDP, 53),
				nativeNamespaceEgressRule(storageNamespace, corev1.ProtocolTCP, storagePort),
				nativeClusterEgressRule(clusterName, corev1.ProtocolTCP, 8200),
			},
		},
	}
}

func nativeNamespaceEgressRule(
	namespace string,
	protocol corev1.Protocol,
	port int,
) networkingv1.NetworkPolicyEgressRule {
	return networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"kubernetes.io/metadata.name": namespace,
					},
				},
			},
		},
		Ports: []networkingv1.NetworkPolicyPort{nativeNetworkPolicyPort(protocol, port)},
	}
}

func nativeClusterEgressRule(
	clusterName string,
	protocol corev1.Protocol,
	port int,
) networkingv1.NetworkPolicyEgressRule {
	return networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{
				PodSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						constants.LabelOpenBaoCluster: clusterName,
					},
				},
			},
		},
		Ports: []networkingv1.NetworkPolicyPort{nativeNetworkPolicyPort(protocol, port)},
	}
}

func nativeNetworkPolicyPort(protocol corev1.Protocol, port int) networkingv1.NetworkPolicyPort {
	return networkingv1.NetworkPolicyPort{
		Protocol: ptr.To(protocol),
		Port:     ptr.To(intstr.FromInt(port)),
	}
}
