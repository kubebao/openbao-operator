package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/cache"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/clock"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func controllerJWTTestIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "operator-system")
	t.Setenv("OPERATOR_SERVICE_ACCOUNT_NAME", "controller")
	t.Setenv("POD_NAME", "controller-pod")
	t.Setenv("POD_UID", "pod-uid")
}

func targetJWTCluster(uid string) *openbaov1alpha1.OpenBaoCluster {
	return &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "bao", Namespace: "tenant", UID: types.UID(uid)},
		Spec:       openbaov1alpha1.OpenBaoClusterSpec{ControllerJWTMode: openbaov1alpha1.ControllerJWTModeTarget},
	}
}

func TestControllerTokenSource_TargetIsolationAndRefresh(t *testing.T) {
	controllerJWTTestIdentity(t)
	clientset := kubernetesfake.NewClientset()
	var calls atomic.Int32
	clientset.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		require.Equal(t, "token", action.GetSubresource())
		require.Equal(t, "operator-system", action.GetNamespace())
		create := action.(k8stesting.CreateAction)
		request := create.GetObject().(*authenticationv1.TokenRequest)
		require.Len(t, request.Spec.Audiences, 1)
		require.Equal(t, int64(600), *request.Spec.ExpirationSeconds)
		require.Equal(t, &authenticationv1.BoundObjectReference{
			APIVersion: "v1", Kind: "Pod", Name: "controller-pod", UID: "pod-uid",
		}, request.Spec.BoundObjectRef)
		n := calls.Add(1)
		return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{
			Token:               fmt.Sprintf("%s-%d", request.Spec.Audiences[0], n),
			ExpirationTimestamp: metav1.NewTime(time.Now().Add(10 * time.Minute)),
		}}, nil
	})
	source := NewControllerTokenSource(clientset)
	testClock := &controllerJWTTestClock{now: time.Now()}
	source.tokens = cache.NewExpiringWithClock(testClock)
	source.readFile = func(string) ([]byte, error) { t.Error("Target read the shared credential"); return nil, nil }
	a, b := targetJWTCluster("a"), targetJWTCluster("b")
	first, err := source.Token(t.Context(), a)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			token, err := source.Token(context.Background(), a)
			if err != nil || token != first {
				t.Errorf("cached token differs: %v", err)
			}
		})
	}
	wg.Wait()
	require.Equal(t, int32(1), calls.Load())
	second, err := source.Token(t.Context(), b)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	require.Equal(t, int32(2), calls.Load())
	testClock.now = testClock.now.Add(8 * time.Minute)
	cached, err := source.Token(t.Context(), a)
	require.NoError(t, err)
	require.Equal(t, first, cached)
	testClock.now = testClock.now.Add(2 * time.Minute)
	refreshed, err := source.Token(t.Context(), a)
	require.NoError(t, err)
	require.NotEqual(t, first, refreshed)
	require.Equal(t, int32(3), calls.Load())
	// A replacement CR with the same name must not reuse the old credential.
	replacement, err := source.Token(t.Context(), targetJWTCluster("replacement"))
	require.NoError(t, err)
	require.NotEqual(t, first, replacement)
}

func TestControllerTokenSource_FailsClosed(t *testing.T) {
	for _, scenario := range []string{"forbidden", "empty", "expired", "near-expiry", "missing-identity", "missing-uid", "invalid-mode"} {
		t.Run(scenario, func(t *testing.T) {
			controllerJWTTestIdentity(t)
			cluster := targetJWTCluster("a")
			clientset := kubernetesfake.NewClientset()
			clientset.PrependReactor("create", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
				if scenario == "forbidden" {
					return true, nil, errors.New("forbidden")
				}
				token, expiry := "token", time.Now().Add(10*time.Minute)
				switch scenario {
				case "empty":
					token = ""
				case "expired":
					expiry = time.Now().Add(-time.Minute)
				case "near-expiry":
					expiry = time.Now().Add(30 * time.Second)
				}
				return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{
					Token: token, ExpirationTimestamp: metav1.NewTime(expiry),
				}}, nil
			})
			if scenario == "missing-identity" {
				t.Setenv("POD_UID", "")
			}
			if scenario == "missing-uid" {
				cluster.UID = ""
			}
			if scenario == "invalid-mode" {
				cluster.Spec.ControllerJWTMode = "invalid"
			}
			source := NewControllerTokenSource(clientset)
			source.readFile = func(string) ([]byte, error) { t.Error("fell back to shared credential"); return []byte("shared"), nil }
			token, err := source.Token(t.Context(), cluster)
			require.Error(t, err)
			require.Empty(t, token)
		})
	}
}

func TestControllerTokenSource_SharedCompatibility(t *testing.T) {
	for _, mode := range []openbaov1alpha1.ControllerJWTMode{"", openbaov1alpha1.ControllerJWTModeShared} {
		t.Run(string(mode), func(t *testing.T) {
			source := NewControllerTokenSource(nil)
			cluster := &openbaov1alpha1.OpenBaoCluster{Spec: openbaov1alpha1.OpenBaoClusterSpec{ControllerJWTMode: mode}}
			for _, projected := range []string{"first", "rotated"} {
				source.readFile = func(string) ([]byte, error) { return []byte(" " + projected + "\n"), nil }
				token, err := source.Token(t.Context(), cluster)
				require.NoError(t, err)
				require.Equal(t, projected, token)
			}
			for _, readErr := range []error{nil, errors.New("missing file")} {
				source.readFile = func(string) ([]byte, error) { return []byte("\n"), readErr }
				_, err := source.Token(t.Context(), cluster)
				require.Error(t, err)
			}
		})
	}
}

func TestControllerTokenSource_RefreshFailureDoesNotReuseCachedCredential(t *testing.T) {
	controllerJWTTestIdentity(t)
	clientset := kubernetesfake.NewClientset()
	calls := 0
	clientset.PrependReactor("create", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 2 {
			return true, nil, errors.New("TokenRequest temporarily rejected")
		}
		return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{
			Token: fmt.Sprintf("credential-%d", calls), ExpirationTimestamp: metav1.NewTime(time.Now().Add(10 * time.Minute)),
		}}, nil
	})
	source := NewControllerTokenSource(clientset)
	testClock := &controllerJWTTestClock{now: time.Now()}
	source.tokens = cache.NewExpiringWithClock(testClock)
	source.readFile = func(string) ([]byte, error) { t.Fatal("Target refresh read the Shared credential"); return nil, nil }
	cluster := targetJWTCluster("a")
	first, err := source.Token(t.Context(), cluster)
	require.NoError(t, err)
	testClock.now = testClock.now.Add(9*time.Minute - time.Second)
	cached, err := source.Token(t.Context(), cluster)
	require.NoError(t, err)
	require.Equal(t, first, cached)
	require.Equal(t, 1, calls)
	testClock.now = testClock.now.Add(2 * time.Second)
	token, err := source.Token(t.Context(), cluster)
	require.ErrorContains(t, err, "temporarily rejected")
	require.Empty(t, token, "a refresh failure must not reuse the previous credential")
	token, err = source.Token(t.Context(), cluster)
	require.NoError(t, err)
	require.NotEqual(t, first, token)
	require.Equal(t, 3, calls, "a failed refresh must allow a new TokenRequest")
}

// Expiring uses Now; no timers run while this test clock advances.
type controllerJWTTestClock struct {
	clock.RealClock
	now time.Time
}

func (c *controllerJWTTestClock) Now() time.Time { return c.now }
