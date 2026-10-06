package openbao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	operatorerrors "github.com/dc-tec/openbao-operator/internal/platform/errors"
	portopenbao "github.com/dc-tec/openbao-operator/internal/port/openbao"
)

// InitRequest represents the payload sent to PUT /v1/sys/init.
type InitRequest struct {
	SecretShares    *int `json:"secret_shares,omitempty"`
	SecretThreshold *int `json:"secret_threshold,omitempty"`
}

// InitResponse represents the response from PUT /v1/sys/init.
type InitResponse struct {
	UnsealKeysB64 []string `json:"unseal_keys_b64"`
	RootToken     string   `json:"root_token"`
}

// JWTAuthLoginResponse represents the response from POST /v1/auth/jwt/login.
type JWTAuthLoginResponse struct {
	Auth struct {
		ClientToken string `json:"client_token"`
		LeaseID     string `json:"lease_id"`
		Renewable   bool   `json:"renewable"`
		TTL         int    `json:"ttl"`
	} `json:"auth"`
}

// Snapshot streams the snapshot data directly from OpenBao to the writer.
func (c *Client) Snapshot(ctx context.Context, writer io.Writer) error {
	if err := c.requireAuth("snapshot operation"); err != nil {
		return err
	}

	req, err := c.newRequest(ctx, http.MethodGet, apiPathRaftSnapshot, nil)
	if err != nil {
		return fmt.Errorf("failed to create snapshot request: %w", err)
	}
	if err := c.authorize(req); err != nil {
		return fmt.Errorf("failed to authorize snapshot request: %w", err)
	}

	snapshotClient := &http.Client{
		Transport: c.httpClient.Transport,
		Timeout:   portopenbao.DefaultSnapshotTimeout,
	}

	resp, err := c.doRequest(req, snapshotClient, "failed to execute snapshot request")
	if err != nil {
		return err
	}
	defer func() {
		drainAndClose(resp)
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if c.state != nil {
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				c.state.after(req, false)
				return operatorerrors.WrapTransientRemoteOverloaded(portopenbao.NewAPIError("snapshot request failed", resp.StatusCode, body))
			}
			c.state.after(req, true)
		}
		return portopenbao.NewAPIError("snapshot request failed", resp.StatusCode, body)
	}

	if _, err := io.Copy(writer, resp.Body); err != nil {
		if c.state != nil {
			c.state.after(req, false)
		}
		if operatorerrors.IsTransientConnection(err) {
			return operatorerrors.WrapTransientConnection(fmt.Errorf("failed to write snapshot data: %w", err))
		}
		return fmt.Errorf("failed to write snapshot data: %w", err)
	}

	if c.state != nil {
		c.state.after(req, true)
	}
	return nil
}

// Restore submits a snapshot without following redirects or replaying its body.
// A successful response confirms acceptance, not completion of snapshot application.
// By default, OpenBao verifies compatibility with the target's seal configuration.
func (c *Client) Restore(ctx context.Context, reader io.Reader, options portopenbao.RestoreOptions) error {
	if err := c.requireAuth("restore operation"); err != nil {
		return err
	}

	restorePath := apiPathRaftSnapshot
	if options.Force {
		restorePath = apiPathRaftSnapshotForceRestore
	}

	req, err := c.newRequest(ctx, http.MethodPost, restorePath, reader)
	if err != nil {
		return fmt.Errorf("failed to create restore request: %w", err)
	}
	if err := c.authorize(req); err != nil {
		return fmt.Errorf("failed to authorize restore request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Vault-No-Request-Forwarding", "true")
	// Readers such as bytes.Reader enable replay by default. A destructive
	// snapshot request must not offer the transport a second copy of its body.
	req.GetBody = nil

	restoreClient := &http.Client{
		Transport: c.httpClient.Transport,
		Timeout:   portopenbao.DefaultSnapshotTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	statusCode, body, err := c.doAndReadAll(req, restoreClient, "failed to execute restore request")
	if err != nil {
		return err
	}
	if statusCode != http.StatusOK && statusCode != http.StatusNoContent {
		return portopenbao.NewAPIError("restore request failed", statusCode, body)
	}

	return nil
}

// Init initializes an OpenBao cluster by calling PUT /v1/sys/init.
func (c *Client) Init(ctx context.Context, req InitRequest) (*InitResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal init request: %w", err)
	}

	httpReq, err := c.newRequest(ctx, http.MethodPut, apiPathSysInit, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create init request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	statusCode, respBody, err := c.doAndReadAll(httpReq, c.clientForContextDeadline(ctx), "failed to execute init request")
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		apiErr := portopenbao.NewAPIError("init request failed", statusCode, respBody)
		if statusCode == http.StatusBadRequest && initResponseAlreadyInitialized(respBody) {
			return nil, fmt.Errorf("%w: %w", portopenbao.ErrAlreadyInitialized, apiErr)
		}
		return nil, apiErr
	}

	var initResp InitResponse
	if err := json.Unmarshal(respBody, &initResp); err != nil {
		return nil, fmt.Errorf("failed to parse init response: %w", err)
	}
	return &initResp, nil
}

// Token returns the authentication token.
func (c *Client) Token() string {
	return c.token
}

// BaseURL returns the base URL of the client.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// LoginJWT authenticates to OpenBao using JWT authentication.
func (c *Client) LoginJWT(ctx context.Context, role, jwtToken string) (string, int, error) {
	if role == "" || jwtToken == "" {
		return "", 0, fmt.Errorf("role and jwtToken are required for JWT authentication")
	}

	reqBody := map[string]string{
		"role": role,
		"jwt":  jwtToken,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, fmt.Errorf("failed to marshal JWT auth request: %w", err)
	}

	req, err := c.newRequest(ctx, http.MethodPost, apiPathAuthJWTLogin, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, fmt.Errorf("failed to create JWT auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	statusCode, respBody, err := c.doAndReadAll(req, nil, "failed to execute JWT auth request")
	if err != nil {
		recordAuthLoginError("request_error")
		return "", 0, err
	}
	if statusCode != http.StatusOK {
		recordAuthLoginError(fmt.Sprintf("status_%d", statusCode))
		return "", 0, portopenbao.NewAPIError("JWT auth request failed", statusCode, respBody)
	}

	var authResp JWTAuthLoginResponse
	if err := json.Unmarshal(respBody, &authResp); err != nil {
		recordAuthLoginError("parse_error")
		return "", 0, fmt.Errorf("failed to parse JWT auth response: %w", err)
	}
	if authResp.Auth.ClientToken == "" {
		recordAuthLoginError("missing_client_token")
		return "", 0, fmt.Errorf("JWT auth response missing client_token")
	}

	recordAuthLoginSuccess()
	return authResp.Auth.ClientToken, authResp.Auth.TTL, nil
}

func initResponseAlreadyInitialized(responseBody []byte) bool {
	// OpenBao exposes this init state via the opaque HTTP 400 response body.
	return strings.Contains(strings.ToLower(string(responseBody)), "already initialized")
}
