package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Config is the administrator's input to plan. It never contains credentials:
// tokens are read from the files it names.
type Config struct {
	Kubeconfig string `json:"kubeconfig"`
	Context    string `json:"context"`
	// Release names the operator's Helm release. Leave it empty when the
	// operator is installed from manifests: a chart rendered by Argo CD, the
	// published install.yaml, or a Kustomize overlay.
	Release string `json:"release,omitempty"`
	// ReleaseNamespace is the existing Helm storage namespace. When empty,
	// plan discovers it from a local Flux HelmRelease or uses OperatorNamespace.
	ReleaseNamespace  string `json:"releaseNamespace,omitempty"`
	OperatorNamespace string `json:"operatorNamespace"`
	// SourceVersion is the nominated source operator release.
	SourceVersion string `json:"sourceVersion"`
	// DestinationChart is a local chart directory for a Helm release. Plan
	// copies it into the state directory.
	DestinationChart string `json:"destinationChart,omitempty"`
	// DestinationValues is an optional values file layered over the source release values,
	// for example to select the destination image.
	DestinationValues string `json:"destinationValues,omitempty"`
	// DestinationManifests and SourceManifests optionally name a Kustomize
	// directory or manifest file for each release of a manifest installation.
	// A checkpoint then writes a ready-to-apply kustomization.
	DestinationManifests string `json:"destinationManifests,omitempty"`
	SourceManifests      string `json:"sourceManifests,omitempty"`
	// Migrator optionally names an existing ServiceAccount the tool impersonates
	// for ownership handoff writes. When empty, the tool creates one for this
	// migration and deletes it when the migration completes or is rolled back.
	Migrator string `json:"migrator,omitempty"`
	// Clusters optionally nominates the Clusters to migrate. When set, it must
	// list every one; an entry can add OpenBao administrator access.
	Clusters []ClusterConfig `json:"clusters,omitempty"`
	// ExternalGitOpsSuspended confirms that every GitOps object the tracking
	// metadata names, but which is not in this cluster, is suspended where it
	// runs: an Argo CD or Flux instance that manages this cluster remotely.
	ExternalGitOpsSuspended bool `json:"externalGitOpsSuspended,omitempty"`
}

// ClusterConfig nominates one OpenBaoCluster.
type ClusterConfig struct {
	Namespace string         `json:"namespace"`
	Name      string         `json:"name"`
	OpenBao   *OpenBaoConfig `json:"openbao,omitempty"`
}

// OpenBaoConfig tells the tool how to reach one Cluster's OpenBao, and can add
// administrator evidence: with a token, plan takes a Raft snapshot and
// verification compares Raft membership. The tool reaches OpenBao through its
// own port-forward in every case; a Cluster with external TLS or ACME needs the
// CA and the server name its certificate carries.
type OpenBaoConfig struct {
	TokenFile string `json:"tokenFile,omitempty"`
	// CAFile overrides the CA in the Cluster's <name>-tls-ca Secret.
	CAFile string `json:"caFile,omitempty"`
	// TLSServerName overrides <name>.<namespace>.svc.
	TLSServerName string `json:"tlsServerName,omitempty"`
}

func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var config Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Config{}, errors.New("decode config: expected exactly one JSON object")
	}
	return config, config.validate()
}

func (c Config) validate() error {
	var problems []string
	require := func(ok bool, problem string) {
		if !ok {
			problems = append(problems, problem)
		}
	}
	require(c.Kubeconfig != "" && c.Context != "", "kubeconfig and context are required")
	require(c.Release == "" || len(validation.IsDNS1123Label(c.Release)) == 0, "release must be a Helm release name")
	require(len(validation.IsDNS1123Label(c.OperatorNamespace)) == 0, "operatorNamespace must be a namespace name")
	require(c.SourceVersion == sourceRelease, fmt.Sprintf(
		"sourceVersion must be %s, the only release this command migrates from; upgrade the operator to %s first",
		sourceRelease, sourceRelease))
	require(c.Release == "" || c.DestinationChart != "", "destinationChart is required for a Helm release")
	require(c.ReleaseNamespace == "" || (c.Release != "" && len(validation.IsDNS1123Label(c.ReleaseNamespace)) == 0),
		"releaseNamespace must be a namespace name and applies only to a Helm release")
	require(c.Release != "" || (c.DestinationChart == "" && c.DestinationValues == ""),
		"destinationChart and destinationValues apply only to a Helm release")
	require(c.Release == "" || (c.DestinationManifests == "" && c.SourceManifests == ""),
		"destinationManifests and sourceManifests apply only to a manifest installation")
	require(c.Migrator == "" || validServiceAccountPrincipal(c.Migrator),
		"migrator must be system:serviceaccount:<namespace>:<name>")
	seen := map[string]bool{}
	for _, cluster := range c.Clusters {
		key := cluster.Namespace + "/" + cluster.Name
		require(!seen[key], "duplicate cluster "+key)
		seen[key] = true
		require(len(validation.IsDNS1123Label(cluster.Namespace)) == 0 &&
			len(validation.IsDNS1123Subdomain(cluster.Name)) == 0, "invalid cluster "+key)
		require(cluster.OpenBao == nil || cluster.OpenBao.TokenFile != "" || cluster.OpenBao.CAFile != "" ||
			cluster.OpenBao.TLSServerName != "", key+": openbao needs a tokenFile, caFile, or tlsServerName")
	}
	if len(problems) > 0 {
		return errors.New("invalid config: " + strings.Join(problems, "; "))
	}
	return nil
}

// migratorPrincipal is the configured migrator, or the one the tool manages.
func (c Config) migratorPrincipal() string {
	if c.Migrator != "" {
		return c.Migrator
	}
	return "system:serviceaccount:" + c.OperatorNamespace + ":" + managedMigratorName(c.Release)
}

func managedMigratorName(release string) string {
	if release == "" {
		return "api-migrator"
	}
	return "api-migrator-" + release
}

func validServiceAccountPrincipal(principal string) bool {
	parts := strings.Split(principal, ":")
	return len(parts) == 4 && parts[0] == "system" && parts[1] == "serviceaccount" &&
		len(validation.IsDNS1123Label(parts[2])) == 0 && len(validation.IsDNS1123Subdomain(parts[3])) == 0
}

func (c Config) cluster(namespace, name string) (ClusterConfig, bool) {
	for _, cluster := range c.Clusters {
		if cluster.Namespace == namespace && cluster.Name == name {
			return cluster, true
		}
	}
	return ClusterConfig{}, false
}

// helmNamespace selects where Helm reads and writes the existing release
// records. It does not change the namespace of the installed operator.
func (c Config) helmNamespace() string {
	if c.ReleaseNamespace != "" {
		return c.ReleaseNamespace
	}
	return c.OperatorNamespace
}
