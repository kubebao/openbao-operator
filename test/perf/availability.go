package perf

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

type availabilityProbe struct {
	At                time.Time               `json:"at"`
	DurationSeconds   float64                 `json:"durationSeconds"`
	Available         bool                    `json:"available"`
	HTTPStatus        int                     `json:"httpStatus,omitempty"`
	ResponseBody      string                  `json:"responseBody,omitempty"`
	Error             string                  `json:"error,omitempty"`
	ObservationErrors []string                `json:"observationErrors,omitempty"`
	Pods              []availabilityProbePod  `json:"pods,omitempty"`
	EndpointSlices    []availabilityEndpoints `json:"endpointSlices,omitempty"`
}

type availabilityProbePod struct {
	Name     string    `json:"name"`
	UID      types.UID `json:"uid"`
	IP       string    `json:"ip,omitempty"`
	Version  string    `json:"version,omitempty"`
	Ready    bool      `json:"ready"`
	Deleting bool      `json:"deleting"`
}

type availabilityEndpoints struct {
	Name        string                     `json:"name"`
	AddressType discoveryv1.AddressType    `json:"addressType"`
	Ports       []discoveryv1.EndpointPort `json:"ports"`
	Endpoints   []discoveryv1.Endpoint     `json:"endpoints"`
}

func (n *nativeScenarioContext) createAvailabilityProbeLog() (*os.File, error) {
	dir := filepath.Join(n.opts.ArtifactDir, "scenarios", n.scenario.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create availability artifact directory: %w", err)
	}
	f, err := os.CreateTemp(dir, n.runID+"-availability-probes-*.jsonl")
	if err != nil {
		return nil, fmt.Errorf("create availability probe artifact: %w", err)
	}
	if n.artifacts == nil {
		n.artifacts = make(map[string]string)
	}
	n.artifacts["availabilityProbes"] = f.Name()
	return f, nil
}

func (n *nativeScenarioContext) probeClusterAvailability(
	ctx context.Context, clusterName string,
) (probe availabilityProbe) {
	probe.At = time.Now().UTC()
	defer func() { probe.DurationSeconds = time.Since(probe.At).Seconds() }()
	transport, err := rest.TransportFor(n.cfg)
	if err != nil {
		probe.Error = fmt.Sprintf("create API transport: %v", err)
		return probe
	}
	httpClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	url := fmt.Sprintf(
		"%s/api/v1/namespaces/%s/services/https:%s-public:8200/proxy/v1/sys/health?standbyok=true&perfstandbyok=true",
		n.cfg.Host, n.namespace, clusterName,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		probe.Error = fmt.Sprintf("build health probe request: %v", err)
		return probe
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	defer func() { _ = resp.Body.Close() }()
	probe.HTTPStatus = resp.StatusCode
	switch resp.StatusCode {
	case http.StatusOK, http.StatusTooManyRequests, 472, 473:
		probe.Available = true
	}
	if !probe.Available {
		// The fixed health endpoint and API proxy return health or routing details.
		// Bound error bodies; do not collect credentials, headers, or arbitrary paths.
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		probe.ResponseBody = string(body)
		if err != nil {
			probe.Error = fmt.Sprintf("read health probe response: %v", err)
		}
	}
	return probe
}

func (n *nativeScenarioContext) observeFailedAvailabilityProbe(
	ctx context.Context, clusterName string, probe *availabilityProbe,
) {
	observationCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	pods, err := n.clusterPods(observationCtx, clusterName)
	if err != nil {
		probe.ObservationErrors = append(probe.ObservationErrors, err.Error())
	}
	for i := range pods {
		pod := &pods[i]
		probe.Pods = append(probe.Pods, availabilityProbePod{
			Name: pod.Name, UID: pod.UID, IP: pod.Status.PodIP,
			Version: pod.Labels[portopenbao.LabelVersion], Ready: !podReadyTransitionTime(pod).IsZero(),
			Deleting: pod.DeletionTimestamp != nil,
		})
	}
	slices := &discoveryv1.EndpointSliceList{}
	if err := n.client.List(observationCtx, slices, client.InNamespace(n.namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: clusterName + "-public"}); err != nil {
		probe.ObservationErrors = append(probe.ObservationErrors, fmt.Sprintf("list public Service EndpointSlices: %v", err))
	}
	for _, slice := range slices.Items {
		probe.EndpointSlices = append(probe.EndpointSlices, availabilityEndpoints{
			Name: slice.Name, AddressType: slice.AddressType, Ports: slice.Ports, Endpoints: slice.Endpoints,
		})
	}
}
