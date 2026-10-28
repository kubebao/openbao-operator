package qualification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func selectPolicy(t *testing.T, mutate func(*policy)) (*openbaov1alpha1.OpenBaoCluster, []byte) {
	t.Helper()
	selected := policy{
		SchemaVersion: 1, Claim: "release-candidate-qualification-pilot", PlanSHA256: strings.Repeat("a", 64),
		ControllerSHA: strings.Repeat("b", 40), Issuer: issuer, Subject: subject, Images: map[string]string{},
	}
	for _, image := range []string{"operator", "init", "backup", "upgrade"} {
		selected.Images[image] = "ghcr.io/kubebao/openbao-" + image + "-qualification@sha256:" + strings.Repeat("c", 64)
	}
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "hardened", Namespace: "test", UID: types.UID("original-uid")},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileHardened, ControllerJWTMode: openbaov1alpha1.ControllerJWTModeTarget,
			InitContainer:    &openbaov1alpha1.InitContainerConfig{Enabled: true, Image: selected.Images["init"]},
			SelfInit:         &openbaov1alpha1.SelfInitConfig{Enabled: true},
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "existing-registry"}},
		},
	}
	if mutate != nil {
		mutate(&selected)
	}
	data, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "policy.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	auth := []byte(`{"auths":{"ghcr.io":{"auth":"cmVhZGVyOmZpeHR1cmU="}}}`)
	authPath := filepath.Join(root, "auth.json")
	if err := os.WriteFile(authPath, auth, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	t.Setenv(policyFileEnv, path)
	t.Setenv(policyHashEnv, hex.EncodeToString(hash[:]))
	t.Setenv(authFileEnv, authPath)
	return cluster, auth
}

func TestNoOverlayPreservesDefaultTrust(t *testing.T) {
	t.Setenv(policyFileEnv, "")
	t.Setenv(policyHashEnv, "")
	t.Setenv(authFileEnv, "")
	cluster := &openbaov1alpha1.OpenBaoCluster{Spec: openbaov1alpha1.OpenBaoClusterSpec{
		Profile: openbaov1alpha1.ProfileHardened, SelfInit: &openbaov1alpha1.SelfInitConfig{Enabled: true},
	}}
	before := cluster.DeepCopy()
	if err := PrepareCluster(context.Background(), nil, cluster); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, cluster) {
		t.Fatal("default fixture was modified")
	}
}

func TestOverlayRetainsBlockingKeylessVerificationAndIdentity(t *testing.T) {
	cluster, auth := selectPolicy(t, nil)
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	if err := PrepareCluster(context.Background(), c, cluster); err != nil {
		t.Fatal(err)
	}
	config := cluster.Spec.OperatorImageVerification
	if config == nil || !config.Enabled || config.Issuer != issuer || config.Subject != subject ||
		config.IgnoreTlog || config.FailurePolicy != "Block" || config.IssuerRegExp != "" || config.SubjectRegExp != "" {
		t.Fatal("experimental fixture did not retain strict keyless verification")
	}
	if cluster.UID != "original-uid" || cluster.Spec.ControllerJWTMode != openbaov1alpha1.ControllerJWTModeTarget ||
		!cluster.Spec.SelfInit.Enabled || cluster.Spec.ImageVerification != nil ||
		len(cluster.Spec.ImagePullSecrets) != 2 || cluster.Spec.ImagePullSecrets[0].Name != "existing-registry" {
		t.Fatal("overlay changed unrelated cluster identity or configuration")
	}
	var secret corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: secretName, Namespace: "test"}, &secret); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(secret.Data, map[string][]byte{corev1.DockerConfigJsonKey: auth}) ||
		secret.Type != corev1.SecretTypeDockerConfigJson || len(secret.OwnerReferences) != 0 {
		t.Fatal("registry transport credential identity differs")
	}
}

func TestPolicyRejectsWrongIdentityImagesAndMissingBindings(t *testing.T) {
	for name, mutate := range map[string]func(*policy){
		"issuer":     func(p *policy) { p.Issuer = "https://other.invalid" },
		"subject":    func(p *policy) { p.Subject += "/untrusted" },
		"controller": func(p *policy) { p.ControllerSHA = "main" },
		"plan":       func(p *policy) { p.PlanSHA256 = "missing" },
		"schema":     func(p *policy) { p.SchemaVersion = 2 },
		"official": func(p *policy) {
			p.Images["init"] = "ghcr.io/kubebao/openbao-init@sha256:" + strings.Repeat("c", 64)
		},
		"tag":     func(p *policy) { p.Images["init"] = "ghcr.io/kubebao/openbao-init-qualification:latest" },
		"missing": func(p *policy) { delete(p.Images, "backup") },
	} {
		t.Run(name, func(t *testing.T) {
			cluster, _ := selectPolicy(t, mutate)
			if err := PrepareCluster(context.Background(), nil, cluster); err == nil {
				t.Fatal("unapproved experimental policy was accepted")
			}
		})
	}
}

func TestConflictingCredentialIsNotOverwritten(t *testing.T) {
	cluster, _ := selectPolicy(t, nil)
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	original := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "test"},
		Type: corev1.SecretTypeDockerConfigJson, Data: map[string][]byte{corev1.DockerConfigJsonKey: []byte("preserve")}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(original).Build()
	if err := PrepareCluster(context.Background(), c, cluster); err == nil {
		t.Fatal("conflicting credential was accepted")
	}
	var retained corev1.Secret
	key := types.NamespacedName{Name: secretName, Namespace: "test"}
	if err := c.Get(context.Background(), key, &retained); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original.Data, retained.Data) || cluster.Spec.OperatorImageVerification != nil {
		t.Fatal("conflict altered credential or cluster policy")
	}
}

func TestWrongPolicyHashOrIncompleteOptInIsRejected(t *testing.T) {
	cluster, _ := selectPolicy(t, nil)
	t.Setenv(policyHashEnv, strings.Repeat("0", 64))
	if err := PrepareCluster(context.Background(), nil, cluster); err == nil {
		t.Fatal("changed policy bytes were accepted")
	}
	t.Setenv(policyHashEnv, "")
	if err := PrepareCluster(context.Background(), nil, cluster); err == nil {
		t.Fatal("incomplete overlay was accepted")
	}
}
