// Package qualification applies an opt-in test overlay for isolated qualification helper images.
package qualification

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

const (
	policyFileEnv = "E2E_QUALIFICATION_POLICY_FILE"
	policyHashEnv = "E2E_QUALIFICATION_POLICY_SHA256"
	authFileEnv   = "E2E_QUALIFICATION_AUTH_FILE"
	secretName    = "release-qualification-registry"
	issuer        = "https://token.actions.githubusercontent.com"
	subject       = "https://github.com/kubebao/openbao-operator/.github/workflows/" +
		"release-qualification-pilot.yml@refs/heads/main"
)

type policy struct {
	SchemaVersion int               `json:"schemaVersion"`
	Claim         string            `json:"claim"`
	PlanSHA256    string            `json:"planSHA256"`
	ControllerSHA string            `json:"controllerSHA"`
	Issuer        string            `json:"issuer"`
	Subject       string            `json:"subject"`
	Images        map[string]string `json:"images"`
}

func readBounded(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open qualification input: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return nil, fmt.Errorf("qualification input is unreadable or exceeds 64 KiB")
	}
	return data, nil
}

func decode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid qualification input JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("qualification input must contain one JSON document")
	}
	return nil
}

func load() (*policy, []byte, error) {
	path, expected, authPath := os.Getenv(policyFileEnv), os.Getenv(policyHashEnv), os.Getenv(authFileEnv)
	if path == "" && expected == "" && authPath == "" {
		return nil, nil, nil
	}
	if path == "" || expected == "" || authPath == "" {
		return nil, nil, fmt.Errorf("qualification policy, SHA256 and read-only registry auth are required together")
	}
	data, err := readBounded(path)
	if err != nil {
		return nil, nil, err
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != expected {
		return nil, nil, fmt.Errorf("qualification policy SHA256 differs")
	}
	var selected policy
	if err := decode(data, &selected); err != nil {
		return nil, nil, err
	}
	if selected.SchemaVersion != 1 || selected.Claim != "release-candidate-qualification-pilot" ||
		selected.Issuer != issuer || selected.Subject != subject ||
		!regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(selected.ControllerSHA) ||
		!regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(selected.PlanSHA256) || len(selected.Images) != 4 {
		return nil, nil, fmt.Errorf("unapproved qualification policy identity")
	}
	for _, image := range []string{"operator", "init", "backup", "upgrade"} {
		pattern := `^ghcr\.io/kubebao/openbao-` + image + `-qualification@sha256:[0-9a-f]{64}$`
		if !regexp.MustCompile(pattern).MatchString(selected.Images[image]) {
			return nil, nil, fmt.Errorf("qualification image must use the fixed isolated repository and a digest")
		}
	}
	auth, err := readBounded(authPath)
	if err != nil {
		return nil, nil, err
	}
	var credentials struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := decode(auth, &credentials); err != nil {
		return nil, nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(credentials.Auths["ghcr.io"].Auth)
	username, password, separated := strings.Cut(string(decoded), ":")
	if err != nil || len(credentials.Auths) != 1 || !separated || username == "" || password == "" {
		return nil, nil, fmt.Errorf("qualification auth must contain only the GHCR credential")
	}
	return &selected, auth, nil
}

// PrepareCluster preserves normal test defaults unless the complete qualification overlay is selected.
// The overlay uses existing API fields; it does not change production trust defaults.
func PrepareCluster(ctx context.Context, c client.Client, cluster *openbaov1alpha1.OpenBaoCluster) error {
	selected, auth, err := load()
	if err != nil || selected == nil {
		return err
	}
	if ctx == nil || c == nil || cluster == nil || cluster.Namespace == "" ||
		cluster.Spec.Profile != openbaov1alpha1.ProfileHardened || cluster.Spec.InitContainer == nil ||
		cluster.Spec.InitContainer.Image != selected.Images["init"] ||
		(cluster.Spec.Upgrade != nil && cluster.Spec.Upgrade.Image != selected.Images["upgrade"]) ||
		cluster.Spec.OperatorImageVerification != nil {
		return fmt.Errorf("qualification overlay requires the selected Hardened helper and an unset helper policy")
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: cluster.Namespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: auth},
	}
	if err := c.Create(ctx, secret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create qualification registry credential: %w", err)
		}
		var existing corev1.Secret
		if err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: cluster.Namespace}, &existing); err != nil {
			return fmt.Errorf("read qualification registry credential: %w", err)
		}
		if existing.Type != secret.Type || len(existing.Data) != 1 ||
			!bytes.Equal(existing.Data[corev1.DockerConfigJsonKey], auth) || len(existing.OwnerReferences) != 0 {
			return fmt.Errorf("existing qualification registry credential conflicts; refusing to overwrite")
		}
	}
	ref := corev1.LocalObjectReference{Name: secretName}
	cluster.Spec.OperatorImageVerification = &openbaov1alpha1.ImageVerificationConfig{
		Enabled: true, Issuer: selected.Issuer, Subject: selected.Subject,
		FailurePolicy: "Block", IgnoreTlog: false, ImagePullSecrets: []corev1.LocalObjectReference{ref},
	}
	if !slices.Contains(cluster.Spec.ImagePullSecrets, ref) {
		cluster.Spec.ImagePullSecrets = append(cluster.Spec.ImagePullSecrets, ref)
	}
	return nil
}
