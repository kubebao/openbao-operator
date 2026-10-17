package rolling

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	openbaoapi "github.com/kubebao/openbao-operator/internal/platform/testutil/openbao"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/backup"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
)

const testNamespace = "ns1"

func TestStepDownLeader_DoesNotTimeoutFromUpgradeStart(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	ns := testNamespace
	name := "c1"
	podName := name + "-0"

	startedLongAgo := metav1.NewTime(time.Now().Add(-20 * time.Minute))
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
			TLS: openbaov1alpha1.TLSConfig{
				Enabled: true,
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				ExecutionID:   "test-execution",
				TargetVersion: "2.5.0",
				FromVersion:   "2.4.4",
				StartedAt:     &startedLongAgo,
			},
		},
	}

	jobName := rollingStepDownJobName(cluster, podName)
	job := managedRollingJob(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              jobName,
			Annotations:       map[string]string{"openbao.org/upgrade-action": string(upgrade.ExecutorActionRollingStepDownLeader), "openbao.org/upgrade-run-id": upgrade.RollingStepDownRunID(cluster, podName)},
			Namespace:         ns,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-10 * time.Second)),
		},
		Status: batchv1.JobStatus{
			Active: 1,
		},
	}, cluster)

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(job).Build()
	mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		return &openbaoapi.MockClusterActions{}, nil
	}, nil)

	ok, err := mgr.stepDownLeader(context.Background(), testr.New(t), cluster, podName, upgrade.NewMetrics(ns, name))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if ok {
		t.Fatalf("expected step-down to be in progress")
	}
}

func TestStepDownLeader_TimesOutBasedOnJobAge(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	ns := testNamespace
	name := "c1"
	podName := name + "-0"

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
			TLS: openbaov1alpha1.TLSConfig{
				Enabled: true,
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				ExecutionID:   "test-execution",
				TargetVersion: "2.5.0",
				FromVersion:   "2.4.4",
			},
		},
	}

	jobName := rollingStepDownJobName(cluster, podName)
	job := managedRollingJob(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              jobName,
			Annotations:       map[string]string{"openbao.org/upgrade-action": string(upgrade.ExecutorActionRollingStepDownLeader), "openbao.org/upgrade-run-id": upgrade.RollingStepDownRunID(cluster, podName)},
			Namespace:         ns,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-(upgrade.DefaultStepDownTimeout + time.Second))),
		},
		Status: batchv1.JobStatus{
			Active: 1,
		},
	}, cluster)

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(job).Build()
	mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		return &openbaoapi.MockClusterActions{}, nil
	}, nil)

	ok, err := mgr.stepDownLeader(context.Background(), testr.New(t), cluster, podName, upgrade.NewMetrics(ns, name))
	if err == nil {
		t.Fatalf("expected error")
	}
	if ok {
		t.Fatalf("expected step-down to not be complete")
	}
	if cluster.Status.Upgrade == nil {
		t.Fatalf("expected upgrade status to be present")
	}
	if cluster.Status.Upgrade.Failure == nil || cluster.Status.Upgrade.Failure.Reason != upgrade.ReasonStepDownTimeout {
		t.Fatalf("Failure=%#v, want reason %q", cluster.Status.Upgrade.Failure, upgrade.ReasonStepDownTimeout)
	}
	if cluster.Status.Upgrade.Failure.Message != "Leader step-down timed out for pod "+podName {
		t.Fatalf("Failure.Message=%q, want %q", cluster.Status.Upgrade.Failure.Message, "Leader step-down timed out for pod "+podName)
	}
}

func TestStepDownLeader_FailsWhenSucceededJobStillLeavesTargetAsLeader(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	ns := testNamespace
	name := "c1"
	podName := name + "-0"

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
			TLS: openbaov1alpha1.TLSConfig{
				Enabled: true,
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				ExecutionID:   "test-execution",
				TargetVersion: "2.5.0",
				FromVersion:   "2.4.4",
			},
		},
	}

	jobName := rollingStepDownJobName(cluster, podName)
	job := managedRollingJob(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              jobName,
			Annotations:       map[string]string{"openbao.org/upgrade-action": string(upgrade.ExecutorActionRollingStepDownLeader), "openbao.org/upgrade-run-id": upgrade.RollingStepDownRunID(cluster, podName)},
			Namespace:         ns,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-(upgrade.DefaultStepDownTimeout + 30*time.Second))),
		},
		Status: batchv1.JobStatus{
			Succeeded: 1,
		},
	}, cluster)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
			Labels: map[string]string{
				portopenbao.LabelActive: "true",
			},
		},
	}

	caSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name + constants.SuffixTLSCA,
			Namespace: ns,
		},
		Data: map[string][]byte{
			"ca.crt": []byte("fake-ca"),
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(job, pod, caSecret).Build()
	mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		return &openbaoapi.MockClusterActions{
			IsLeaderFunc: func(ctx context.Context) (bool, error) {
				return true, nil
			},
		}, nil
	}, nil)

	ok, err := mgr.stepDownLeader(context.Background(), testr.New(t), cluster, podName, upgrade.NewMetrics(ns, name))
	if err == nil {
		t.Fatal("expected step-down timeout error")
	}
	if ok {
		t.Fatalf("expected step-down to remain incomplete after timeout")
	}
	if cluster.Status.Upgrade.Failure == nil || cluster.Status.Upgrade.Failure.Reason != upgrade.ReasonStepDownTimeout {
		t.Fatalf("Failure=%#v, want reason %q", cluster.Status.Upgrade.Failure, upgrade.ReasonStepDownTimeout)
	}
	if cluster.Status.Upgrade.Failure.Message != "Leader step-down timed out for pod "+podName {
		t.Fatalf("Failure.Message=%q, want %q", cluster.Status.Upgrade.Failure.Message, "Leader step-down timed out for pod "+podName)
	}
}

func TestStepDownLeader_VerifiesTransferViaAPIWhenLabelsLag(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	ns := testNamespace
	name := "c1"
	podName := name + "-0"

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
			TLS: openbaov1alpha1.TLSConfig{
				Enabled: true,
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				ExecutionID:   "test-execution",
				TargetVersion: "2.5.0",
				FromVersion:   "2.4.4",
			},
		},
	}

	jobName := rollingStepDownJobName(cluster, podName)
	job := managedRollingJob(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              jobName,
			Annotations:       map[string]string{"openbao.org/upgrade-action": string(upgrade.ExecutorActionRollingStepDownLeader), "openbao.org/upgrade-run-id": upgrade.RollingStepDownRunID(cluster, podName)},
			Namespace:         ns,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-5 * time.Second)),
		},
		Status: batchv1.JobStatus{
			Succeeded: 1,
		},
	}, cluster)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
			Labels: map[string]string{
				portopenbao.LabelActive: "true",
			},
		},
	}

	caSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name + constants.SuffixTLSCA,
			Namespace: ns,
		},
		Data: map[string][]byte{
			"ca.crt": []byte("fake-ca"),
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(job, pod, caSecret).Build()

	var gotConfig portopenbao.ClientConfig
	mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		gotConfig = config
		return &openbaoapi.MockClusterActions{
			IsLeaderFunc: func(ctx context.Context) (bool, error) {
				return false, nil
			},
		}, nil
	}, nil)

	ok, err := mgr.stepDownLeader(context.Background(), testr.New(t), cluster, podName, upgrade.NewMetrics(ns, name))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !ok {
		t.Fatalf("expected step-down to be complete")
	}
	if cluster.Status.Upgrade.LastStepDownTime == nil {
		t.Fatalf("expected LastStepDownTime to be set")
	}
	if gotConfig.BaseURL == "" {
		t.Fatalf("expected client factory to be called with BaseURL")
	}
	if len(gotConfig.CACert) == 0 {
		t.Fatalf("expected client factory to be called with CACert")
	}
}

func TestStepDownLeader_SkipsJobWhenTargetPodIsNotLeader(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	ns := testNamespace
	name := "c1"
	podName := name + "-1"

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				ExecutionID:   "test-execution",
				TargetVersion: "2.5.0",
				FromVersion:   "2.4.4",
			},
		},
	}

	caSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name + constants.SuffixTLSCA,
			Namespace: ns,
		},
		Data: map[string][]byte{
			"ca.crt": []byte("fake-ca"),
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(caSecret).Build()
	mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		return &openbaoapi.MockClusterActions{
			IsLeaderFunc: func(ctx context.Context) (bool, error) {
				return false, nil
			},
		}, nil
	}, nil)

	ok, err := mgr.stepDownLeader(context.Background(), testr.New(t), cluster, podName, upgrade.NewMetrics(ns, name))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !ok {
		t.Fatalf("expected step-down to be treated as complete when target pod is not leader")
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("expected listing jobs to succeed, got %v", err)
	}
	if len(jobList.Items) != 0 {
		t.Fatalf("expected no step-down jobs to be created, got %d", len(jobList.Items))
	}
}

func TestStepDownLeader_PassesStableRevisionToExecutorJob(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	const (
		name           = "c1"
		stableRevision = "active-revision"
	)
	podName := name + "-" + stableRevision + "-0"
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, UID: "cluster-uid"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				Image:       "openbao-upgrade:test",
				JWTAuthRole: "upgrade-role",
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			BlueGreen: &openbaov1alpha1.BlueGreenStatus{BlueRevision: stableRevision},
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				ExecutionID:   "test-execution",
				TargetVersion: "2.6.2",
				FromVersion:   "2.5.5",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		return &openbaoapi.MockClusterActions{IsLeaderFunc: func(context.Context) (bool, error) {
			return true, nil
		}}, nil
	}, nil)

	complete, err := mgr.stepDownLeader(context.Background(), testr.New(t), cluster, podName, upgrade.NewMetrics(testNamespace, name))
	if err != nil {
		t.Fatalf("stepDownLeader() error = %v", err)
	}
	if complete {
		t.Fatal("stepDownLeader() complete = true, want running executor Job")
	}

	job := &batchv1.Job{}
	jobName := rollingStepDownJobName(cluster, podName)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: jobName}, job); err != nil {
		t.Fatalf("get step-down Job: %v", err)
	}
	foundRevision := false
	for _, env := range job.Spec.Template.Spec.Containers[0].Env {
		if env.Name == constants.EnvUpgradeBlueRevision {
			foundRevision = true
			if env.Value != stableRevision {
				t.Fatalf("%s = %q, want %q", constants.EnvUpgradeBlueRevision, env.Value, stableRevision)
			}
		}
	}
	if !foundRevision {
		t.Fatalf("step-down Job is missing %s", constants.EnvUpgradeBlueRevision)
	}
}

func TestEnsureTargetPodLeadershipTransferred_SkipsSingleReplicaStepDown(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	ns := testNamespace
	name := "c1"
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 1,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				TargetVersion: "2.5.0",
				FromVersion:   "2.4.4",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		t.Fatalf("client factory should not be called for single-replica step-down skip")
		return nil, nil
	}, nil)

	ok, err := mgr.ensureTargetPodLeadershipTransferred(
		context.Background(),
		testr.New(t),
		cluster,
		rolloutTargetPod{CurrentPartition: 1, NextPartition: 0, Ordinal: 0, Name: name + "-0"},
		upgrade.NewMetrics(ns, name),
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !ok {
		t.Fatalf("expected leadership transfer to be skipped")
	}

	jobs := &batchv1.JobList{}
	if err := c.List(context.Background(), jobs, client.InNamespace(ns)); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("jobs = %d, want 0", len(jobs.Items))
	}
}

func TestPerformPodByPodUpgrade_ResumesWhenTargetAlreadyRolledOut(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	ns := testNamespace
	name := "c1"
	podName := name + "-0"
	startedAt := metav1.Now()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
			TLS: openbaov1alpha1.TLSConfig{
				Enabled: true,
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				StartedAt:        &startedAt,
				TargetVersion:    "2.5.2",
				FromVersion:      "2.5.1",
				CurrentPartition: 1,
				CompletedPods:    []int32{2, 1},
			},
		},
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{constants.AnnotationClusterGeneration: "0"},
			Name:      name,
			Namespace: ns,
		},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  constants.ContainerBao,
							Image: "openbao/openbao:2.5.2",
						},
					},
				},
			},
		},
		Status: appsv1.StatefulSetStatus{
			UpdateRevision: "rev-new",
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
			Labels: map[string]string{
				appsv1.StatefulSetRevisionLabel: "rev-new",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  constants.ContainerBao,
					Image: "openbao/openbao:2.5.2",
				},
			},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	caSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name + constants.SuffixTLSCA,
			Namespace: ns,
		},
		Data: map[string][]byte{
			"ca.crt": []byte("fake-ca"),
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts, pod, caSecret).Build()
	mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		return &openbaoapi.MockClusterActions{
			IsHealthyFunc: func(ctx context.Context) (bool, error) {
				return true, nil
			},
		}, nil
	}, nil)

	completed, err := mgr.performPodByPodUpgrade(context.Background(), testr.New(t), cluster, upgrade.NewMetrics(ns, name))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !completed {
		t.Fatalf("expected upgrade to be treated as completed")
	}
	if cluster.Status.Upgrade.CurrentPartition != 0 {
		t.Fatalf("CurrentPartition=%d, want 0", cluster.Status.Upgrade.CurrentPartition)
	}
	if len(cluster.Status.Upgrade.CompletedPods) != 3 || cluster.Status.Upgrade.CompletedPods[2] != 0 {
		t.Fatalf("CompletedPods=%v, want [2 1 0]", cluster.Status.Upgrade.CompletedPods)
	}
	updatedSTS := &appsv1.StatefulSet{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(sts), updatedSTS); err != nil {
		t.Fatalf("expected to reload StatefulSet, got %v", err)
	}
	if updatedSTS.Spec.UpdateStrategy.RollingUpdate == nil || updatedSTS.Spec.UpdateStrategy.RollingUpdate.Partition == nil {
		t.Fatalf("expected StatefulSet partition to be set")
	}
	if *updatedSTS.Spec.UpdateStrategy.RollingUpdate.Partition != 0 {
		t.Fatalf("partition=%d, want 0", *updatedSTS.Spec.UpdateStrategy.RollingUpdate.Partition)
	}

	jobList := &batchv1.JobList{}
	if err := c.List(context.Background(), jobList); err != nil {
		t.Fatalf("expected listing jobs to succeed, got %v", err)
	}
	if len(jobList.Items) != 0 {
		t.Fatalf("expected no step-down jobs to be created, got %d", len(jobList.Items))
	}
}

func TestWaitForPodRevisionUpdated_WaitsUntilRevisionMatches(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	ns := testNamespace
	name := "c1"
	podName := name + "-0"
	startedAt := metav1.Now()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				StartedAt: &startedAt,
			},
		},
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{constants.AnnotationClusterGeneration: "0"},
			Name:      name,
			Namespace: ns,
		},
		Status: appsv1.StatefulSetStatus{
			UpdateRevision: "rev-new",
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
			Labels: map[string]string{
				appsv1.StatefulSetRevisionLabel: "rev-old",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts, pod).Build()
	mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		return &openbaoapi.MockClusterActions{}, nil
	}, nil)

	ok, err := mgr.waitForPodRevisionUpdated(context.Background(), testr.New(t), cluster, podName)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if ok {
		t.Fatalf("expected revision check to wait while pod revision is old")
	}
}

func TestWaitForPodRevisionUpdated_SucceedsWhenRevisionMatches(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	ns := testNamespace
	name := "c1"
	podName := name + "-0"
	startedAt := metav1.Now()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				StartedAt: &startedAt,
			},
		},
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{constants.AnnotationClusterGeneration: "0"},
			Name:      name,
			Namespace: ns,
		},
		Status: appsv1.StatefulSetStatus{
			UpdateRevision: "rev-new",
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
			Labels: map[string]string{
				appsv1.StatefulSetRevisionLabel: "rev-new",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts, pod).Build()
	mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		return &openbaoapi.MockClusterActions{}, nil
	}, nil)

	ok, err := mgr.waitForPodRevisionUpdated(context.Background(), testr.New(t), cluster, podName)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !ok {
		t.Fatalf("expected revision check to succeed when revisions match")
	}
}

func TestWaitForPodRevisionUpdated_DeletesStalePodWhenImageMismatchesTemplate(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	ns := testNamespace
	name := "c1"
	podName := name + "-0"
	startedAt := metav1.Now()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				StartedAt: &startedAt,
			},
		},
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{constants.AnnotationClusterGeneration: "0"},
			Name:      name,
			Namespace: ns,
		},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  constants.ContainerBao,
							Image: "openbao/openbao:2.5.0",
						},
					},
				},
			},
		},
		Status: appsv1.StatefulSetStatus{
			UpdateRevision: "rev-new",
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
			Labels: map[string]string{
				appsv1.StatefulSetRevisionLabel: "rev-bad",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  constants.ContainerBao,
					Image: "openbao/openbao:retry-image-does-not-exist",
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts, pod).Build()
	mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		return &openbaoapi.MockClusterActions{}, nil
	}, nil)

	ok, err := mgr.waitForPodRevisionUpdated(context.Background(), testr.New(t), cluster, podName)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if ok {
		t.Fatalf("expected revision check to wait after deleting stale pod")
	}

	deletedPod := &corev1.Pod{}
	getErr := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: podName}, deletedPod)
	if !apierrors.IsNotFound(getErr) {
		t.Fatalf("expected stale pod to be deleted, got err=%v", getErr)
	}
}

func TestTargetPodAlreadyRolledOut_DoesNotDeletePodBeforePartitionAdvance(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	ns := testNamespace
	name := "c1"
	podName := name + "-0"
	startedAt := metav1.Now()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				StartedAt:        &startedAt,
				CurrentPartition: 1,
			},
		},
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{constants.AnnotationClusterGeneration: "0"}, Name: name, Namespace: ns},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  constants.ContainerBao,
						Image: "openbao/openbao:2.6.2",
					}},
				},
			},
		},
		Status: appsv1.StatefulSetStatus{UpdateRevision: "rev-new"},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
			Labels: map[string]string{
				appsv1.StatefulSetRevisionLabel: "rev-old",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  constants.ContainerBao,
				Image: "openbao/openbao:2.5.5",
			}},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts, pod).Build()
	mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		return &openbaoapi.MockClusterActions{}, nil
	}, nil)

	rolledOut, err := mgr.targetPodAlreadyRolledOut(
		context.Background(),
		testr.New(t),
		cluster,
		rolloutTargetPod{CurrentPartition: 1, NextPartition: 0, Ordinal: 0, Name: podName},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if rolledOut {
		t.Fatal("expected the pre-partition resume check to report the old pod as pending")
	}

	remainingPod := &corev1.Pod{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: podName}, remainingPod); err != nil {
		t.Fatalf("expected the pre-partition resume check to preserve the pod, got %v", err)
	}
}

func TestRollingWaitStages_TimeoutsMarkUpgradeFailed(t *testing.T) {
	tests := []struct {
		name       string
		startedAgo time.Duration
		wait       func(*Manager, context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster, string) (bool, error)
		wantReason string
		wantMsg    string
	}{
		{
			name:       "revision update timeout",
			startedAgo: upgrade.DefaultPodReadyTimeout + time.Minute,
			wait: func(m *Manager, ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, podName string) (bool, error) {
				return m.waitForPodRevisionUpdated(ctx, logger, cluster, podName)
			},
			wantReason: upgrade.ReasonPodNotReady,
			wantMsg:    fmt.Sprintf(upgrade.MessagePodNotReady, "c1-0", upgrade.DefaultPodReadyTimeout),
		},
		{
			name:       "pod ready timeout",
			startedAgo: upgrade.DefaultPodReadyTimeout + time.Minute,
			wait: func(m *Manager, ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, podName string) (bool, error) {
				return m.waitForPodReady(ctx, logger, cluster, podName)
			},
			wantReason: upgrade.ReasonPodNotReady,
			wantMsg:    fmt.Sprintf(upgrade.MessagePodNotReady, "c1-0", upgrade.DefaultPodReadyTimeout),
		},
		{
			name:       "pod health timeout",
			startedAgo: upgrade.DefaultPodReadyTimeout + upgrade.DefaultHealthCheckTimeout + time.Minute,
			wait: func(m *Manager, ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, podName string) (bool, error) {
				return m.waitForPodHealthy(ctx, logger, cluster, podName)
			},
			wantReason: upgrade.ReasonHealthCheckFailed,
			wantMsg:    fmt.Sprintf(upgrade.MessageHealthCheckFailed, "c1-0", "timeout"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = appsv1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			_ = openbaov1alpha1.AddToScheme(scheme)

			startedAt := metav1.NewTime(time.Now().Add(-tt.startedAgo))
			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: testNamespace},
				Status: openbaov1alpha1.OpenBaoClusterStatus{
					Upgrade: &openbaov1alpha1.UpgradeProgress{
						StartedAt: &startedAt,
					},
				},
			}

			c := fake.NewClientBuilder().WithScheme(scheme).Build()
			mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
				return &openbaoapi.MockClusterActions{}, nil
			}, nil)

			ok, err := tt.wait(mgr, context.Background(), testr.New(t), cluster, "c1-0")
			if err == nil {
				t.Fatal("expected timeout error")
			}
			if ok {
				t.Fatal("expected wait stage to remain incomplete")
			}
			if cluster.Status.Upgrade == nil {
				t.Fatal("expected upgrade status to remain present")
			}
			if cluster.Status.Upgrade.Failure == nil || cluster.Status.Upgrade.Failure.Reason != tt.wantReason {
				t.Fatalf("Failure=%#v, want reason %q", cluster.Status.Upgrade.Failure, tt.wantReason)
			}
			if cluster.Status.Upgrade.Failure.Message != tt.wantMsg {
				t.Fatalf("Failure.Message=%q, want %q", cluster.Status.Upgrade.Failure.Message, tt.wantMsg)
			}
		})
	}
}
