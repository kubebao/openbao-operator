package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

const openBaoResponseLimit = 1 << 20

var errOpenBaoTransport = errors.New("OpenBao request failed; inspect current state before re-running")

// OpenBao is a client for one OpenBao server. Without an administrator token
// it can only log in: the controller-login checks need no token.
type OpenBao struct {
	http   *http.Client
	stream *http.Client
	base   *url.URL
	token  string
}

type openBaoHTTPError struct{ status int }

func (e *openBaoHTTPError) Error() string { return fmt.Sprintf("OpenBao returned HTTP %d", e.status) }

// openBaoEndpoint is how the tool reaches one OpenBao server. Dial carries
// every connection, usually through the tool's port-forward.
type openBaoEndpoint struct {
	URL        string
	ServerName string
	CA         []byte
	Token      string
	Dial       func(context.Context, string, string) (net.Conn, error)
}

func newOpenBao(endpoint openBaoEndpoint) (*OpenBao, error) {
	base, err := url.Parse(endpoint.URL)
	if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.User != nil ||
		(base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("OpenBao URL must be an HTTPS origin without credentials, path, query, or fragment")
	}
	if endpoint.Dial == nil {
		return nil, errors.New("OpenBao endpoint requires a dialer")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(endpoint.CA) {
		return nil, errors.New("OpenBao CA contains no valid certificate")
	}
	if strings.ContainsAny(endpoint.Token, " \t\r\n") {
		return nil, errors.New("OpenBao token contains whitespace")
	}
	transport := &http.Transport{
		TLSClientConfig:     &tls.Config{RootCAs: roots, ServerName: endpoint.ServerName, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
		DisableKeepAlives: true, // No replay on a reused connection after an ambiguous write.
		DialContext:       endpoint.Dial,
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &OpenBao{base: base, token: endpoint.Token,
		http:   &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: noRedirect},
		stream: &http.Client{Transport: transport, CheckRedirect: noRedirect},
	}, nil
}

// readToken reads an administrator token from a private file.
func readToken(path string) (string, error) {
	secret, err := readFileLimited(path, true, 64<<10)
	if err != nil {
		return "", fmt.Errorf("OpenBao token file must be a private regular file: %w", err)
	}
	token := strings.TrimSpace(string(secret))
	clear(secret)
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("OpenBao token file contains no valid token")
	}
	return token, nil
}

func readFileLimited(path string, private bool, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || (private && info.Mode().Perm()&0o077 != 0) {
		return nil, errors.New("invalid input file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("input file exceeds its size limit")
	}
	return data, nil
}

func (b *OpenBao) do(ctx context.Context, client *http.Client, method, path string, payload any,
	authenticated bool,
) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, errors.New("cannot encode OpenBao request")
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := *b.base
	endpoint.Path = "/v1/" + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, errors.New("cannot construct OpenBao request")
	}
	req.GetBody = nil // A mutation cannot be replayed by redirect or transport handling.
	req.Header.Set("Content-Type", "application/json")
	if authenticated {
		if b.token == "" {
			return nil, errors.New("no OpenBao administrator token is configured")
		}
		req.Header.Set("X-Vault-Token", b.token)
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errOpenBaoTransport
	}
	if response.StatusCode < http.StatusOK || response.StatusCode > 299 {
		_ = response.Body.Close()
		return nil, &openBaoHTTPError{status: response.StatusCode}
	}
	return response, nil
}

// Transport retry bounds for reads and logins; variables so tests can shorten them.
var (
	openBaoRetryAttempts = 30
	openBaoRetryDelay    = 2 * time.Second
)

// requestResponse retries only reads and logins on pre-response transport errors.
// HTTP errors and local validation failures are returned without retrying.
func (b *OpenBao) requestResponse(ctx context.Context, client *http.Client, method, path string, payload any,
	authenticated bool,
) (*http.Response, error) {
	retry := method == http.MethodGet || strings.HasSuffix(path, "/login")
	for attempt := 0; ; attempt++ {
		response, err := b.do(ctx, client, method, path, payload, authenticated)
		if err == nil || !retry || !errors.Is(err, errOpenBaoTransport) || attempt+1 >= openBaoRetryAttempts {
			return response, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(openBaoRetryDelay):
		}
	}
}

func (b *OpenBao) request(ctx context.Context, method, path string, payload any, authenticated bool) ([]byte, error) {
	response, err := b.requestResponse(ctx, b.http, method, path, payload, authenticated)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, openBaoResponseLimit+1))
	if err != nil || len(data) > openBaoResponseLimit {
		return nil, errors.New("OpenBao response cannot be read within limit")
	}
	return data, nil
}

// RaftPeers returns the sorted node IDs and voter flags of the Raft configuration.
func (b *OpenBao) RaftPeers(ctx context.Context) ([]string, error) {
	data, err := b.request(ctx, http.MethodGet, "sys/storage/raft/configuration", nil, true)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Data struct {
			Config struct {
				Servers []struct {
					NodeID string `json:"node_id"`
					Voter  bool   `json:"voter"`
				} `json:"servers"`
			} `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || len(envelope.Data.Config.Servers) == 0 {
		return nil, errors.New("OpenBao Raft configuration response is invalid")
	}
	peers := make([]string, 0, len(envelope.Data.Config.Servers))
	for _, server := range envelope.Data.Config.Servers {
		peers = append(peers, server.NodeID+"/voter="+strconv.FormatBool(server.Voter))
	}
	slices.Sort(peers)
	return peers, nil
}

// Snapshot streams a Raft snapshot to a new private file.
func (b *OpenBao) Snapshot(ctx context.Context, path string) error {
	response, err := b.requestResponse(ctx, b.stream, http.MethodGet, "sys/storage/raft/snapshot", nil, true)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, response.Body)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written == 0 {
		_ = os.Remove(path)
		return errors.New("raft snapshot download failed")
	}
	return nil
}

// Login reports whether a JWT authenticates against role. A denial is returned
// as an openBaoHTTPError so callers can distinguish it from transport errors.
func (b *OpenBao) Login(ctx context.Context, mount, role, jwt string) error {
	data, err := b.request(ctx, http.MethodPost, "auth/"+mount+"/login",
		map[string]string{"role": role, "jwt": jwt}, false)
	if err != nil {
		return err
	}
	var response struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(data, &response); err != nil || response.Auth.ClientToken == "" {
		return errors.New("OpenBao login returned no client token")
	}
	return nil
}

// verifyControllerLogin requests fresh Pod-bound tokens for the running
// controller and checks which audiences the JWT role accepts.
func verifyControllerLogin(ctx context.Context, kube kubernetes.Interface, bao *OpenBao,
	pod *corev1.Pod, accepted, denied []string,
) error {
	const mount, role = "jwt-operator", "openbao-operator"
	login := func(audience string) error {
		request := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{
			Audiences: []string{audience}, ExpirationSeconds: ptr.To(int64(600)),
			BoundObjectRef: &authenticationv1.BoundObjectReference{
				APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID,
			},
		}}
		issued, err := kube.CoreV1().ServiceAccounts(pod.Namespace).
			CreateToken(ctx, pod.Spec.ServiceAccountName, request, metav1.CreateOptions{})
		if err != nil || issued.Status.Token == "" {
			return fmt.Errorf("request a Pod-bound controller token: %w", err)
		}
		return bao.Login(ctx, mount, role, issued.Status.Token)
	}
	for _, audience := range accepted {
		if err := login(audience); err != nil {
			return fmt.Errorf("controller login with audience %s failed: %w", audience, err)
		}
	}
	for _, audience := range denied {
		err := login(audience)
		var httpErr *openBaoHTTPError
		if !errors.As(err, &httpErr) || (httpErr.status != http.StatusBadRequest && httpErr.status != http.StatusForbidden) {
			return fmt.Errorf("controller login with audience %s was not denied", audience)
		}
	}
	return nil
}
