package config

import (
	"fmt"
	"path"
	"strings"

	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclwrite"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func buildStorageBlock(cluster *openbaov1alpha1.OpenBaoCluster, infra InfrastructureDetails) *hclwrite.Block {
	storageAttrs := hclStorageRaft{
		Type:   "raft",
		Path:   openBaoPathData,
		NodeID: configNodeIDTemplate,
	}

	if cluster.Spec.Configuration != nil && cluster.Spec.Configuration.Raft != nil {
		storageAttrs.PerformanceMultiplier = cluster.Spec.Configuration.Raft.PerformanceMultiplier
	}

	var autoJoinExpr string
	switch {
	case strings.TrimSpace(infra.RetryJoinLabelSelector) != "":
		autoJoinExpr = fmt.Sprintf(
			`provider=k8s namespace=%s label_selector="%s"`,
			infra.Namespace,
			strings.TrimSpace(infra.RetryJoinLabelSelector),
		)
	case infra.TargetRevisionForJoin != "":
		autoJoinExpr = fmt.Sprintf(
			`provider=k8s namespace=%s label_selector="%s=%s,%s=%s"`,
			infra.Namespace,
			openBaoLabelCluster,
			cluster.Name,
			openBaoLabelRevision,
			infra.TargetRevisionForJoin,
		)
	default:
		autoJoinExpr = fmt.Sprintf(
			`provider=k8s namespace=%s label_selector="%s=%s"`,
			infra.Namespace,
			openBaoLabelCluster,
			cluster.Name,
		)
	}

	if infra.RetryJoinAsNonVoter || infra.TargetRevisionForJoin != "" {
		storageAttrs.RetryJoinAsNonVoter = boolPtrValue(true)
	}

	retryJoinAttrs := hclRetryJoin{
		AutoJoin:            autoJoinExpr,
		LeaderTLSServerName: portopenbao.ComputeTLSServerName(cluster),
	}

	if cluster.Spec.TLS.Mode != openbaov1alpha1.TLSModeACME {
		retryJoinAttrs.LeaderCACertFile = stringPtr(openBaoPathTLSCACert)
		retryJoinAttrs.LeaderClientCertFile = stringPtr(openBaoPathTLSServerCert)
		retryJoinAttrs.LeaderClientKeyFile = stringPtr(openBaoPathTLSServerKey)
	} else if cluster.Spec.Configuration != nil && strings.TrimSpace(cluster.Spec.Configuration.ACMECARoot) != "" {
		acmeCARootDir := path.Dir(strings.TrimSpace(cluster.Spec.Configuration.ACMECARoot))
		retryJoinAttrs.LeaderCACertFile = stringPtr(path.Join(acmeCARootDir, "pki-ca.crt"))
	}

	storageBlock := hclwrite.NewBlock("storage", []string{storageAttrs.Type})
	gohcl.EncodeIntoBody(storageAttrs, storageBlock.Body())

	if cluster.Annotations[constants.AnnotationRestoreOrigin] != "" {
		return storageBlock
	}

	retryJoinBlock := hclwrite.NewBlock("retry_join", nil)
	gohcl.EncodeIntoBody(retryJoinAttrs, retryJoinBlock.Body())
	storageBlock.Body().AppendBlock(retryJoinBlock)

	return storageBlock
}

func validateInfrastructureDetails(cluster *openbaov1alpha1.OpenBaoCluster, infra InfrastructureDetails) (InfrastructureDetails, error) {
	headlessSvcName := infra.HeadlessServiceName
	if strings.TrimSpace(headlessSvcName) == "" {
		headlessSvcName = cluster.Name
	}
	namespace := infra.Namespace
	if strings.TrimSpace(namespace) == "" {
		return InfrastructureDetails{}, fmt.Errorf("infrastructure namespace is required to render config.hcl")
	}

	infra.HeadlessServiceName = headlessSvcName
	infra.Namespace = namespace
	return infra, nil
}
