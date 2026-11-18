package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

func fixtureRequest(ctx context.Context, bao *OpenBao, method, path string, payload any, interval time.Duration) ([]byte, error) {
	for {
		data, err := bao.request(ctx, method, path, payload, true)
		// Never repeat a mutation after an ambiguous transport failure.
		if err == nil || method != http.MethodGet || !errors.Is(err, errOpenBaoTransport) {
			return data, err
		}
		if err := waitFixturePoll(ctx, interval); err != nil {
			return nil, err
		}
	}
}

func fixtureValue(data []byte, expected string) error {
	var response struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("decode fixture value: %w", err)
	}
	if value, ok := response.Data.Data["value"]; !ok || value != expected {
		return errors.New("fixture value does not match the acknowledged write")
	}
	return nil
}

// Verify the acknowledged value on a node that identifies itself as the
// leader, then allow the selected voter a bounded interval to apply it. No
// write is issued here. A leader 404 or any different value remains a failure.
func verifyFixtureData(ctx context.Context, selected *OpenBao, voters []*OpenBao, path, expected string, interval time.Duration) error {
	for {
		for _, voter := range voters {
			data, err := fixtureRequest(ctx, voter, http.MethodGet, "sys/leader", nil, interval)
			if err != nil {
				return fmt.Errorf("identify fixture leader: %w", err)
			}
			var leader struct {
				IsSelf bool `json:"is_self"`
			}
			if err := json.Unmarshal(data, &leader); err != nil {
				return err
			}
			if !leader.IsSelf {
				continue
			}
			data, err = fixtureRequest(ctx, voter, http.MethodGet, path, nil, interval)
			if err != nil {
				return fmt.Errorf("read acknowledged fixture value on leader: %w", err)
			}
			if err := fixtureValue(data, expected); err != nil {
				return fmt.Errorf("leader: %w", err)
			}
			return awaitFixtureValue(ctx, selected, path, expected, interval)
		}
		if err := waitFixturePoll(ctx, interval); err != nil {
			return fmt.Errorf("no fixture voter identified itself as leader: %w", err)
		}
	}
}

func awaitFixtureValue(ctx context.Context, selected *OpenBao, path, expected string, interval time.Duration) error {
	for {
		data, err := fixtureRequest(ctx, selected, http.MethodGet, path, nil, interval)
		if err == nil {
			return fixtureValue(data, expected)
		}
		var status *openBaoHTTPError
		if !errors.As(err, &status) || status.status != http.StatusNotFound {
			return err
		}
		if err := waitFixturePoll(ctx, interval); err != nil {
			return fmt.Errorf("selected voter did not apply acknowledged fixture value: %w", err)
		}
	}
}

func waitFixturePoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
