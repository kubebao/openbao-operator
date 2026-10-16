package entrypoint

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kubebao/openbao-operator/internal/platform/kubernetestls"
	"github.com/kubebao/openbao-operator/internal/platform/observability"
)

// UsageError identifies invalid command-line or environment configuration.
type UsageError struct {
	Err error
}

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

// LoadConfig preserves controller-runtime's configuration precedence without
// reading the kubeconfig flag registered on the global command line.
func LoadConfig(kubeconfig string) (*rest.Config, error) {
	config, err := loadConfig(kubeconfig, rest.InClusterConfig, clientcmd.NewDefaultClientConfigLoadingRules)
	if err != nil {
		return nil, err
	}
	config = kubernetestls.WithPolicy(config)
	config.Wrap(observability.WrapKubernetesTransport)
	return config, nil
}

func loadConfig(
	kubeconfig string,
	inCluster func() (*rest.Config, error),
	defaultRules func() *clientcmd.ClientConfigLoadingRules,
) (*rest.Config, error) {
	if kubeconfig == "" && os.Getenv(clientcmd.RecommendedConfigPathEnvVar) == "" {
		if config, err := inCluster(); err == nil {
			config.QPS = -1
			return config, nil
		}
	}

	// An explicit path must not trigger default-file migration or inspect home files.
	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
	if kubeconfig == "" {
		rules = defaultRules()
		if _, ok := os.LookupEnv("HOME"); !ok {
			currentUser, err := user.Current()
			if err != nil {
				return nil, fmt.Errorf("find home directory for kubeconfig: %w", err)
			}
			rules.Precedence = append(rules.Precedence,
				filepath.Join(currentUser.HomeDir, clientcmd.RecommendedHomeDir, clientcmd.RecommendedFileName))
		}
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	// Match controller-runtime: API priority and fairness controls requests by default.
	if config.QPS == 0 {
		config.QPS = -1
	}
	return config, nil
}
