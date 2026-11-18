//go:build apimigration_e2e

package main

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Reuse the migration tool's TLS-verified, Pod-identity-aware forwarding. A
// field selector pins each client to a voter; advertised URLs are not followed.
type fixtureVoterReader struct {
	client.Reader
	name string
}

func (r fixtureVoterReader) List(ctx context.Context, list client.ObjectList, options ...client.ListOption) error {
	return r.Reader.List(ctx, list, append(options, client.MatchingFields{"metadata.name": r.name})...)
}

func (f *e2eFixture) fixtureVoterClients() ([]*OpenBao, func()) {
	f.t.Helper()
	_, reader := f.restClient()
	secret := f.getJSON("get", "secret", e2eCluster+"-tls-ca", "-n", e2eDataNS)
	ca, err := base64.StdEncoding.DecodeString(secret["data"].(map[string]any)["ca.crt"].(string))
	require.NoError(f.t, err)
	var voters []*OpenBao
	var forwards []*portForward
	closeClients := func() {
		for _, bao := range voters {
			bao.http.CloseIdleConnections()
			bao.stream.CloseIdleConnections()
		}
		for _, forward := range forwards {
			forward.Close()
		}
	}
	for ordinal := range f.shape.replicas {
		forward := newPortForward(f.kubeconfig, f.context,
			fixtureVoterReader{Reader: reader, name: fmt.Sprintf("%s-%d", f.voterSet, ordinal)},
			e2eDataNS, e2eCluster, f.voterSet)
		forwards = append(forwards, forward)
		bao, err := newOpenBao(openBaoEndpoint{
			URL: f.bao.base.String(), ServerName: f.bao.base.Hostname(),
			CA: ca, Token: f.bao.token, Dial: forward.DialContext,
		})
		if err != nil {
			closeClients()
			require.NoError(f.t, err)
		}
		voters = append(voters, bao)
	}
	return voters, closeClients
}
