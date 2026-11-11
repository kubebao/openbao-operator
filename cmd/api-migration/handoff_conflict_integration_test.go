//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestHandoffPolicyStatusConflictRealAPI(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	require.NotEmpty(t, assets)
	environment := &envtest.Environment{BinaryAssetsDirectory: assets}
	config, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	scheme := runtime.NewScheme()
	require.NoError(t, admissionv1.AddToScheme(scheme))
	c, err := client.NewWithWatch(config, client.Options{Scheme: scheme})
	require.NoError(t, err)
	policy := releasedLockPolicy(t)
	require.NoError(t, c.Create(t.Context(), &policy))
	uid := policy.UID
	for _, open := range []bool{true, false} {
		t.Run(map[bool]string{true: "open", false: "close"}[open], func(t *testing.T) {
			attempts, conflicted := 0, false
			var versions []string
			wrapped := interceptor.NewClient(c, interceptor.Funcs{Patch: func(ctx context.Context, base client.WithWatch,
				object client.Object, patch client.Patch, options ...client.PatchOption) error {
				attempts++
				versions = append(versions, object.GetResourceVersion())
				if attempts == 1 {
					fresh := &admissionv1.ValidatingAdmissionPolicy{}
					require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(object), fresh))
					fresh.Status.TypeChecking = &admissionv1.TypeChecking{ExpressionWarnings: []admissionv1.ExpressionWarning{{
						FieldRef: "spec.validations[0].expression", Warning: fmt.Sprintf("concurrent status update during open=%t", open),
					}}}
					require.NoError(t, base.Status().Update(ctx, fresh))
				}
				err := base.Patch(ctx, object, patch, options...)
				if attempts == 1 {
					conflicted = apierrors.IsConflict(err)
				}
				return err
			}})
			p := &Procedure{kube: &Kube{client: wrapped}, plan: &Plan{Config: Config{
				OperatorNamespace: "openbao-operator-system", Migrator: testMigrator}}}
			_, changed, err := p.patchHandoff(t.Context(), open)
			require.NoError(t, err)
			require.True(t, changed)
			require.True(t, conflicted, "the API server must reject the stale resource version")
			require.GreaterOrEqual(t, attempts, 2)
			require.NotEqual(t, versions[0], versions[1], "the retry must reread the live policy")
			current := &admissionv1.ValidatingAdmissionPolicy{}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(&policy), current))
			require.Equal(t, uid, current.UID)
			actual, principal := handoffPrincipal(current.Spec)
			require.Equal(t, open, actual)
			if open {
				require.Equal(t, testMigrator, principal)
			}
		})
	}
}
