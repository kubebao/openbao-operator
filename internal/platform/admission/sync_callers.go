package admission

import (
	"encoding/json"
	"fmt"
	"strings"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	pvcBindingSyncCallersEnv  = "OPENBAO_ADMISSION_PVC_BINDING_SYNC_CALLERS"
	podMetadataSyncCallersEnv = "OPENBAO_ADMISSION_POD_METADATA_SYNC_CALLERS"
	pvcSyncVariable           = "pvc_binding_sync_callers"
	podSyncVariable           = "pod_metadata_sync_callers"
)

func substituteSyncCallers(spec *admissionv1.ValidatingAdmissionPolicySpec, settings installation) {
	for i := range spec.Variables {
		switch spec.Variables[i].Name {
		case pvcSyncVariable:
			spec.Variables[i].Expression = settings.pvcSyncCallers
		case podSyncVariable:
			spec.Variables[i].Expression = settings.podSyncCallers
		}
	}
}

// Canonicalize only the two supported JSON parameters. Installed values still
// must equal the trusted settings; no other CEL expression is normalized.
func canonicalizeSyncCallers(spec *admissionv1.ValidatingAdmissionPolicySpec) error {
	for i := range spec.Variables {
		variable := &spec.Variables[i]
		var err error
		switch variable.Name {
		case pvcSyncVariable:
			variable.Expression, err = canonicalPVCBindingSyncCallers(variable.Expression)
		case podSyncVariable:
			variable.Expression, err = canonicalPodMetadataSyncCallers(variable.Expression)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", variable.Name, err)
		}
	}
	return nil
}

func canonicalPVCBindingSyncCallers(value string) (string, error) {
	var callers map[string][]string
	if err := json.Unmarshal([]byte(value), &callers); err != nil {
		return "", fmt.Errorf("decode PVC sync caller map: %w", err)
	}
	if callers == nil {
		return "", fmt.Errorf("PVC sync callers must be a JSON object")
	}
	for username, namespaces := range callers {
		if !validSyncUsername(username) || len(namespaces) == 0 {
			return "", fmt.Errorf("PVC sync callers require exact usernames and nonempty namespace lists")
		}
		for _, namespace := range namespaces {
			if len(validation.IsDNS1123Label(namespace)) != 0 {
				return "", fmt.Errorf("PVC sync callers require valid namespace names")
			}
		}
	}
	data, err := json.Marshal(callers)
	if err != nil {
		return "", fmt.Errorf("encode PVC sync caller map: %w", err)
	}
	return string(data), nil
}

func canonicalPodMetadataSyncCallers(value string) (string, error) {
	var callers map[string]map[string][]string
	if err := json.Unmarshal([]byte(value), &callers); err != nil {
		return "", fmt.Errorf("decode Pod sync caller map: %w", err)
	}
	if callers == nil {
		return "", fmt.Errorf("pod sync callers must be a JSON object")
	}
	for username, namespaces := range callers {
		if !validSyncUsername(username) || len(namespaces) == 0 {
			return "", fmt.Errorf("pod sync callers require exact usernames and nonempty namespace maps")
		}
		for namespace, keys := range namespaces {
			if len(validation.IsDNS1123Label(namespace)) != 0 || len(keys) == 0 {
				return "", fmt.Errorf("pod sync callers require valid namespace names and nonempty annotation key lists")
			}
			for _, key := range keys {
				if !validSyncAnnotation(key) {
					return "", fmt.Errorf("pod sync callers require valid, unprotected annotation keys")
				}
			}
		}
	}
	data, err := json.Marshal(callers)
	if err != nil {
		return "", fmt.Errorf("encode Pod sync caller map: %w", err)
	}
	return string(data), nil
}

func validSyncUsername(username string) bool {
	return username != "" && !strings.ContainsAny(username, "*\r\n")
}

func validSyncAnnotation(key string) bool {
	if len(validation.IsQualifiedName(key)) != 0 {
		return false
	}
	domain, _, qualified := strings.Cut(key, "/")
	if !qualified {
		return true
	}
	return domain != "openbao.org" && domain != "vcluster.loft.sh" &&
		domain != "kubernetes.io" && !strings.HasSuffix(domain, ".kubernetes.io") &&
		domain != "k8s.io" && !strings.HasSuffix(domain, ".k8s.io")
}
