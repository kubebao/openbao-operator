package statusops

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestApplyEvaluatedConditionAllowsKnownReasonStatusPairs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		conditionType openbaov1alpha1.ConditionType
		results       []ConditionResult
		apply         func(*openbaov1alpha1.OpenBaoCluster, ConditionResult)
	}{
		{
			name:          "acme integration",
			conditionType: openbaov1alpha1.ConditionACMEIntegrationReady,
			apply:         ApplyACMEIntegrationReadyCondition,
			results: []ConditionResult{
				conditionResult(metav1.ConditionTrue, constants.ReasonACMEIntegrationReady),
				conditionResult(metav1.ConditionFalse, constants.ReasonGatewayAPIMissing),
				conditionResult(metav1.ConditionFalse, constants.ReasonACMEGatewayNotConfiguredForPassthrough),
				conditionResult(metav1.ConditionFalse, constants.ReasonACMEDomainNotResolvable),
				conditionResult(metav1.ConditionFalse, constants.ReasonPrerequisitesMissing),
				conditionResult(metav1.ConditionUnknown, reasonPaused),
				conditionResult(metav1.ConditionUnknown, ReasonProfileNotSet),
				conditionResult(metav1.ConditionUnknown, reasonUnknown),
			},
		},
		{
			name:          "gateway integration",
			conditionType: openbaov1alpha1.ConditionGatewayIntegrationReady,
			apply:         ApplyGatewayIntegrationReadyCondition,
			results: []ConditionResult{
				conditionResult(metav1.ConditionTrue, constants.ReasonGatewayIntegrationReady),
				conditionResult(metav1.ConditionFalse, constants.ReasonGatewayAPIMissing),
				conditionResult(metav1.ConditionFalse, constants.ReasonGatewayReferenceMissing),
				conditionResult(metav1.ConditionFalse, constants.ReasonGatewayClassMissing),
				conditionResult(metav1.ConditionFalse, constants.ReasonGatewayListenerIncompatible),
				conditionResult(metav1.ConditionFalse, constants.ReasonGatewayClassNotAccepted),
				conditionResult(metav1.ConditionFalse, constants.ReasonGatewayVersionUnsupported),
				conditionResult(metav1.ConditionFalse, constants.ReasonGatewayFeatureUnsupported),
				conditionResult(metav1.ConditionFalse, constants.ReasonGatewayNotProgrammed),
				conditionResult(metav1.ConditionFalse, constants.ReasonGatewayRouteNotAccepted),
				conditionResult(metav1.ConditionFalse, constants.ReasonGatewayRouteReferencesUnresolved),
				conditionResult(metav1.ConditionUnknown, constants.ReasonGatewayClassPending),
				conditionResult(metav1.ConditionUnknown, constants.ReasonGatewayCapabilitiesUnknown),
				conditionResult(metav1.ConditionUnknown, constants.ReasonGatewayProgrammingPending),
				conditionResult(metav1.ConditionUnknown, constants.ReasonGatewayRoutePending),
				conditionResult(metav1.ConditionUnknown, reasonPaused),
				conditionResult(metav1.ConditionUnknown, ReasonProfileNotSet),
				conditionResult(metav1.ConditionUnknown, reasonUnknown),
			},
		},
		{
			name:          "ingress integration",
			conditionType: openbaov1alpha1.ConditionIngressIntegrationReady,
			apply:         ApplyIngressIntegrationReadyCondition,
			results: []ConditionResult{
				conditionResult(metav1.ConditionTrue, constants.ReasonIngressIntegrationReady),
				conditionResult(metav1.ConditionFalse, constants.ReasonIngressClassMissing),
				conditionResult(metav1.ConditionUnknown, constants.ReasonIngressCapabilitiesUnknown),
				conditionResult(metav1.ConditionUnknown, constants.ReasonIngressObjectPending),
				conditionResult(metav1.ConditionUnknown, constants.ReasonIngressLoadBalancerPending),
				conditionResult(metav1.ConditionUnknown, reasonPaused),
				conditionResult(metav1.ConditionUnknown, ReasonProfileNotSet),
				conditionResult(metav1.ConditionUnknown, reasonUnknown),
			},
		},
		{
			name:          "api server network",
			conditionType: openbaov1alpha1.ConditionAPIServerNetworkReady,
			apply:         ApplyAPIServerNetworkReadyCondition,
			results: []ConditionResult{
				conditionResult(metav1.ConditionTrue, constants.ReasonAPIServerNetworkReady),
				conditionResult(metav1.ConditionUnknown, constants.ReasonAPIServerEndpointIPsRecommended),
				conditionResult(metav1.ConditionUnknown, constants.ReasonAPIServerEgressPolicyPending),
				conditionResult(metav1.ConditionUnknown, constants.ReasonAPIServerNetworkManagedExternally),
				conditionResult(metav1.ConditionFalse, constants.ReasonAPIServerNetworkConfigurationInvalid),
				conditionResult(metav1.ConditionUnknown, reasonPaused),
				conditionResult(metav1.ConditionUnknown, ReasonProfileNotSet),
				conditionResult(metav1.ConditionUnknown, reasonUnknown),
			},
		},
		{
			name:          "tls",
			conditionType: openbaov1alpha1.ConditionTLSReady,
			apply:         ApplyTLSReadyCondition,
			results: []ConditionResult{
				conditionResult(metav1.ConditionTrue, ReasonDisabled),
				conditionResult(metav1.ConditionTrue, reasonReady),
				conditionResult(metav1.ConditionFalse, reasonTLSSecretMissing),
				conditionResult(metav1.ConditionFalse, reasonTLSSecretInvalid),
				conditionResult(metav1.ConditionUnknown, reasonPaused),
				conditionResult(metav1.ConditionUnknown, ReasonProfileNotSet),
				conditionResult(metav1.ConditionUnknown, reasonUnknown),
			},
		},
		{
			name:          "acme cache",
			conditionType: openbaov1alpha1.ConditionACMECacheReady,
			apply:         ApplyACMECacheReadyCondition,
			results: []ConditionResult{
				conditionResult(metav1.ConditionTrue, reasonACMECacheReady),
				conditionResult(metav1.ConditionFalse, reasonACMECacheNotConfigured),
				conditionResult(metav1.ConditionFalse, reasonACMECacheMissing),
				conditionResult(metav1.ConditionFalse, reasonACMECachePending),
				conditionResult(metav1.ConditionFalse, reasonACMECacheInvalidAccessMode),
				conditionResult(metav1.ConditionUnknown, reasonUnknown),
			},
		},
		{
			name:          "audit file storage",
			conditionType: openbaov1alpha1.ConditionAuditFileStorageReady,
			apply:         ApplyAuditFileStorageReadyCondition,
			results: []ConditionResult{
				conditionResult(metav1.ConditionTrue, reasonAuditFileStorageReady),
				conditionResult(metav1.ConditionFalse, reasonAuditFileStorageMissing),
				conditionResult(metav1.ConditionFalse, reasonAuditFileStoragePending),
				conditionResult(metav1.ConditionFalse, reasonAuditFileStorageInvalidAccessMode),
				conditionResult(metav1.ConditionFalse, constants.ReasonAuditFileStorageStatefulSetRecreateRequired),
				conditionResult(metav1.ConditionUnknown, reasonPaused),
				conditionResult(metav1.ConditionUnknown, ReasonProfileNotSet),
				conditionResult(metav1.ConditionUnknown, reasonUnknown),
			},
		},
		{
			name:          "backup configuration",
			conditionType: openbaov1alpha1.ConditionBackupConfigurationReady,
			apply:         ApplyBackupConfigurationReadyCondition,
			results: []ConditionResult{
				conditionResult(metav1.ConditionTrue, reasonReady),
				conditionResult(metav1.ConditionTrue, constants.ReasonAmbientIdentityAssumed),
				conditionResult(metav1.ConditionTrue, constants.ReasonWorkloadIdentityConfigured),
				conditionResult(metav1.ConditionFalse, constants.ReasonAuthenticationRequired),
				conditionResult(metav1.ConditionFalse, constants.ReasonTokenSecretMissing),
				conditionResult(metav1.ConditionFalse, constants.ReasonCredentialsSecretMissing),
				conditionResult(metav1.ConditionFalse, constants.ReasonNetworkEgressRulesRequired),
				conditionResult(metav1.ConditionFalse, constants.ReasonSecurityViolation),
				conditionResult(metav1.ConditionUnknown, reasonPaused),
				conditionResult(metav1.ConditionUnknown, ReasonProfileNotSet),
				conditionResult(metav1.ConditionUnknown, reasonUnknown),
			},
		},
		{
			name:          "cloud unseal identity",
			conditionType: openbaov1alpha1.ConditionCloudUnsealIdentityReady,
			apply:         ApplyCloudUnsealIdentityReadyCondition,
			results: []ConditionResult{
				conditionResult(metav1.ConditionTrue, reasonReady),
				conditionResult(metav1.ConditionTrue, constants.ReasonAmbientIdentityAssumed),
				conditionResult(metav1.ConditionTrue, constants.ReasonWorkloadIdentityConfigured),
				conditionResult(metav1.ConditionFalse, constants.ReasonCredentialsSecretMissing),
				conditionResult(metav1.ConditionFalse, constants.ReasonPrerequisitesMissing),
				conditionResult(metav1.ConditionUnknown, reasonPaused),
				conditionResult(metav1.ConditionUnknown, ReasonProfileNotSet),
				conditionResult(metav1.ConditionUnknown, reasonUnknown),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for _, result := range tt.results {
				cluster := newOpenBaoClusterStatusTestObject()
				cluster.Generation = 17

				tt.apply(cluster, result)

				condition := meta.FindStatusCondition(cluster.Status.Conditions, string(tt.conditionType))
				if condition == nil {
					t.Fatalf("condition %s is missing for result %#v", tt.conditionType, result)
				}
				if condition.Status != result.Status || condition.Reason != result.Reason || condition.Message != result.Message {
					t.Errorf("condition = %#v, want status=%s reason=%q message=%q", condition, result.Status, result.Reason, result.Message)
				}
				if condition.ObservedGeneration != cluster.Generation {
					t.Errorf("observed generation = %d, want %d", condition.ObservedGeneration, cluster.Generation)
				}
				if condition.LastTransitionTime.IsZero() {
					t.Error("last transition time is zero")
				}
			}
		})
	}
}

func TestApplyEvaluatedConditionRejectsUnexpectedResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		conditionType openbaov1alpha1.ConditionType
		apply         func(*openbaov1alpha1.OpenBaoCluster, ConditionResult)
		reason        string
	}{
		{openbaov1alpha1.ConditionACMEIntegrationReady, ApplyACMEIntegrationReadyCondition, constants.ReasonACMEIntegrationReady},
		{openbaov1alpha1.ConditionGatewayIntegrationReady, ApplyGatewayIntegrationReadyCondition, constants.ReasonGatewayIntegrationReady},
		{openbaov1alpha1.ConditionIngressIntegrationReady, ApplyIngressIntegrationReadyCondition, constants.ReasonIngressIntegrationReady},
		{openbaov1alpha1.ConditionAPIServerNetworkReady, ApplyAPIServerNetworkReadyCondition, constants.ReasonAPIServerNetworkReady},
		{openbaov1alpha1.ConditionTLSReady, ApplyTLSReadyCondition, reasonReady},
		{openbaov1alpha1.ConditionACMECacheReady, ApplyACMECacheReadyCondition, reasonACMECacheReady},
		{openbaov1alpha1.ConditionAuditFileStorageReady, ApplyAuditFileStorageReadyCondition, reasonAuditFileStorageReady},
		{openbaov1alpha1.ConditionBackupConfigurationReady, ApplyBackupConfigurationReadyCondition, reasonReady},
		{openbaov1alpha1.ConditionCloudUnsealIdentityReady, ApplyCloudUnsealIdentityReadyCondition, reasonReady},
	}

	for _, tt := range tests {
		t.Run(string(tt.conditionType), func(t *testing.T) {
			t.Parallel()

			cluster := newOpenBaoClusterStatusTestObject()
			cluster.Generation = 29

			tt.apply(cluster, ConditionResult{
				Status:  metav1.ConditionFalse,
				Reason:  " " + tt.reason + " ",
				Message: " contract message ",
			})

			condition := meta.FindStatusCondition(cluster.Status.Conditions, string(tt.conditionType))
			if condition == nil {
				t.Fatalf("condition %s is missing", tt.conditionType)
			}
			if condition.Status != metav1.ConditionUnknown || condition.Reason != reasonUnknown {
				t.Errorf("condition = %#v, want status=%s reason=%s", condition, metav1.ConditionUnknown, reasonUnknown)
			}
			wantMessage := "Controller rejected unexpected " + string(tt.conditionType) + " result: status=False reason=\"" + tt.reason + "\": contract message"
			if condition.Message != wantMessage {
				t.Errorf("message = %q, want %q", condition.Message, wantMessage)
			}
			if condition.ObservedGeneration != cluster.Generation {
				t.Errorf("observed generation = %d, want %d", condition.ObservedGeneration, cluster.Generation)
			}
		})
	}
}

func TestApplyEvaluatedConditionTrimsAllowedResult(t *testing.T) {
	t.Parallel()

	cluster := newOpenBaoClusterStatusTestObject()
	ApplyTLSReadyCondition(cluster, ConditionResult{
		Status:  metav1.ConditionTrue,
		Reason:  " Ready ",
		Message: " TLS assets are ready ",
	})

	condition := meta.FindStatusCondition(cluster.Status.Conditions, string(openbaov1alpha1.ConditionTLSReady))
	if condition == nil {
		t.Fatal("TLSReady condition is missing")
	}
	if condition.Reason != reasonReady || condition.Message != "TLS assets are ready" {
		t.Errorf("condition = %#v, want trimmed reason and message", condition)
	}
}

func TestApplyACMECacheReadyConditionRejectsPausedAndProfileNotSet(t *testing.T) {
	t.Parallel()

	for _, reason := range []string{reasonPaused, ReasonProfileNotSet} {
		t.Run(reason, func(t *testing.T) {
			cluster := newOpenBaoClusterStatusTestObject()
			ApplyACMECacheReadyCondition(cluster, ConditionResult{
				Status:  metav1.ConditionUnknown,
				Reason:  reason,
				Message: "blocked",
			})

			condition := meta.FindStatusCondition(cluster.Status.Conditions, string(openbaov1alpha1.ConditionACMECacheReady))
			if condition == nil {
				t.Fatal("ACMECacheReady condition is missing")
			}
			if condition.Status != metav1.ConditionUnknown || condition.Reason != reasonUnknown {
				t.Errorf("condition = %#v, want status=%s reason=%s", condition, metav1.ConditionUnknown, reasonUnknown)
			}
		})
	}
}

func TestApplyEvaluatedConditionPreservesTransitionTimeWhenStatusDoesNotChange(t *testing.T) {
	t.Parallel()

	originalTransitionTime := metav1.Unix(123, 0)
	cluster := newOpenBaoClusterStatusTestObject()
	cluster.Generation = 41
	cluster.Status.Conditions = []metav1.Condition{
		{
			Type:               string(openbaov1alpha1.ConditionTLSReady),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 40,
			LastTransitionTime: originalTransitionTime,
			Reason:             ReasonDisabled,
			Message:            "TLS was disabled",
		},
	}

	ApplyTLSReadyCondition(cluster, ConditionResult{
		Status:  metav1.ConditionTrue,
		Reason:  reasonReady,
		Message: "TLS assets are ready",
	})

	condition := meta.FindStatusCondition(cluster.Status.Conditions, string(openbaov1alpha1.ConditionTLSReady))
	if condition == nil {
		t.Fatal("TLSReady condition is missing")
	}
	if !condition.LastTransitionTime.Equal(&originalTransitionTime) {
		t.Errorf("last transition time = %v, want %v", condition.LastTransitionTime, originalTransitionTime)
	}
	if condition.ObservedGeneration != cluster.Generation {
		t.Errorf("observed generation = %d, want %d", condition.ObservedGeneration, cluster.Generation)
	}
}

func conditionResult(status metav1.ConditionStatus, reason string) ConditionResult {
	return ConditionResult{Status: status, Reason: reason, Message: "contract message"}
}
