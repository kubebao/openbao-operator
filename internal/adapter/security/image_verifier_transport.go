package security

import "github.com/kubebao/openbao-operator/internal/platform/cryptopolicy"

// Share the registry connection pool across image-verifier instances.
var imageRegistryTransport = cryptopolicy.NewHTTPTransport()
