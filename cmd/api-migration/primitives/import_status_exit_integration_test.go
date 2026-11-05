//go:build integration

package primitives

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

// This child exits without running cleanup after the API accepts status. The
// parent owns envtest and reconstructs a client from its own retained config.
func TestImportStatusOwnershipExitHelper(t *testing.T) {
	directory := os.Getenv("KUBEBAO_IMPORT_EXIT_DIRECTORY")
	if directory == "" {
		t.Skip("subprocess helper")
	}
	encoded, err := os.ReadFile(filepath.Join(directory, "request.json"))
	require.NoError(t, err)
	var request ImportRequest
	require.NoError(t, json.Unmarshal(encoded, &request))
	config, err := clientcmd.BuildConfigFromFlags("", filepath.Join(directory, "kubeconfig"))
	require.NoError(t, err)
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, api.AddToScheme(scheme))
	c, err := client.NewWithWatch(config, client.Options{Scheme: scheme})
	require.NoError(t, err)
	wrapped := interceptor.NewClient(c, interceptor.Funcs{
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			os.Exit(73)
			return errors.New("unreachable")
		},
	})
	_, err = Import(t.Context(), wrapped, request)
	t.Fatalf("expected process exit at ownership release; got %v", err)
}

func runImportStatusExit(t *testing.T, config *rest.Config, request ImportRequest) {
	t.Helper()
	directory := t.TempDir()
	input, err := json.Marshal(request)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "request.json"), input, 0o600))
	kubeconfig := clientcmdapi.NewConfig()
	kubeconfig.Clusters["fixture"] = &clientcmdapi.Cluster{Server: config.Host, CertificateAuthorityData: config.CAData}
	kubeconfig.AuthInfos["fixture"] = &clientcmdapi.AuthInfo{
		ClientCertificateData: config.CertData, ClientKeyData: config.KeyData}
	kubeconfig.Contexts["fixture"] = &clientcmdapi.Context{Cluster: "fixture", AuthInfo: "fixture"}
	kubeconfig.CurrentContext = "fixture"
	encoded, err := clientcmd.Write(*kubeconfig)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "kubeconfig"), encoded, 0o600))
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestImportStatusOwnershipExitHelper$")
	command.Env = append(os.Environ(), "KUBEBAO_IMPORT_EXIT_DIRECTORY="+directory)
	err = command.Run()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 73, exit.ExitCode(), "child must reach the nominated post-status boundary")
}
