package primitives

import (
	"fmt"
	"net/http"
	"strings"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewDiscoveryReader creates a direct reader with strict metadata negotiation.
// The default client-go metadata client also accepts full JSON objects as a
// fallback. Removing that fallback makes unsupported servers fail instead of
// returning Secret payloads. The caller's configuration is not modified.
func NewDiscoveryReader(config *rest.Config) (client.Reader, error) {
	if config == nil {
		return nil, fmt.Errorf("discovery Kubernetes configuration is required")
	}
	copy := rest.CopyConfig(config)
	copy.Wrap(func(next http.RoundTripper) http.RoundTripper {
		return discoveryMetadataTransport{next: next}
	})
	reader, err := client.New(copy, client.Options{})
	if err != nil {
		return nil, fmt.Errorf("construct metadata-only discovery reader: %w", err)
	}
	return reader, nil
}

type discoveryMetadataTransport struct{ next http.RoundTripper }

func (transport discoveryMetadataTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	accept := request.Header.Get("Accept")
	kind := ""
	switch {
	case strings.Contains(accept, "as=PartialObjectMetadataList"):
		kind = "PartialObjectMetadataList"
	case strings.Contains(accept, "as=PartialObjectMetadata"):
		kind = "PartialObjectMetadata"
	}
	if kind != "" {
		request = request.Clone(request.Context())
		request.Header.Set("Accept", "application/json;as="+kind+";g=meta.k8s.io;v=v1")
	}
	return transport.next.RoundTrip(request)
}
