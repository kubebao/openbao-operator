package adminopsstatus

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
)

func TestMutateWithReader_OwnershipPolicy(t *testing.T) {
	t.Parallel()
	versionConflict := apierrors.NewConflict(schema.GroupResource{Group: "kubebao.org", Resource: "openbaoclusters"}, "ownership", errors.New("stale resource version"))
	ownershipConflict := apierrors.NewApplyConflict([]metav1.StatusCause{{Type: metav1.CauseTypeFieldManagerConflict, Field: ".status.backup.lastFailureReason"}}, "another manager owns the field")
	unavailable := errors.New("API unavailable")
	attempts := retry.DefaultRetry.Steps
	tests := []struct {
		name         string
		ownership    adminops.OwnershipPolicy
		applyErr     error
		failures     int
		wantUnforced int
		wantForced   int
		wantErr      error
	}{
		{name: "respect retries stale versions", applyErr: versionConflict, failures: 1, wantUnforced: 2},
		{name: "respect returns ownership conflicts", applyErr: ownershipConflict, failures: attempts, wantUnforced: attempts, wantErr: ownershipConflict},
		{name: "fallback succeeds without force", ownership: adminops.ForceOwnershipOnConflict, wantUnforced: 1},
		{name: "fallback retries stale versions without force", ownership: adminops.ForceOwnershipOnConflict, applyErr: versionConflict, failures: 1, wantUnforced: 2},
		{name: "fallback forces after ownership conflicts", ownership: adminops.ForceOwnershipOnConflict, applyErr: ownershipConflict, failures: attempts, wantUnforced: attempts, wantForced: 1},
		{name: "fallback also forces after exhausted version conflicts", ownership: adminops.ForceOwnershipOnConflict, applyErr: versionConflict, failures: attempts, wantUnforced: attempts, wantForced: 1},
		{name: "fallback stops after forced conflicts", ownership: adminops.ForceOwnershipOnConflict, applyErr: versionConflict, failures: 2 * attempts, wantUnforced: attempts, wantForced: attempts, wantErr: versionConflict},
		{name: "fallback does not force other errors", ownership: adminops.ForceOwnershipOnConflict, applyErr: unavailable, failures: 1, wantUnforced: 1, wantErr: unavailable},
		{name: "force applies immediately", ownership: adminops.ForceOwnership, wantForced: 1},
		{name: "force retries stale versions", ownership: adminops.ForceOwnership, applyErr: versionConflict, failures: 1, wantForced: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scheme := runtime.NewScheme()
			require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
			cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "ownership", Namespace: "default"}}
			var forces []bool
			reads, mutations := 0, 0
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(cluster).WithObjects(cluster.DeepCopy()).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						reads++
						return c.Get(ctx, key, obj, opts...)
					},
					SubResourceApply: func(ctx context.Context, c client.Client, subresource string, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
						options := (&client.SubResourceApplyOptions{}).ApplyOpts(opts)
						forces = append(forces, options.Force != nil && *options.Force)
						if len(forces) <= tt.failures {
							return tt.applyErr
						}
						return c.SubResource(subresource).Apply(ctx, obj, opts...)
					},
				}).Build()
			before := cluster.DeepCopy()
			err := MutateWithReader(t.Context(), c, c, cluster, func(obj *openbaov1alpha1.OpenBaoCluster) error {
				mutations++
				require.Equal(t, mutations, reads, "each mutation needs a fresh read")
				obj.Status.Backup = &openbaov1alpha1.BackupStatus{LastFailureReason: "persisted"}
				return nil
			}, tt.ownership)

			require.Len(t, forces, tt.wantUnforced+tt.wantForced)
			for i, force := range forces {
				require.Equal(t, i >= tt.wantUnforced, force, "force ownership on attempt %d", i+1)
			}
			require.Equal(t, len(forces), mutations)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Equal(t, before, cluster)
				require.Equal(t, mutations, reads)
			} else {
				require.NoError(t, err)
				require.NotNil(t, cluster.Status.Backup)
				require.Equal(t, "persisted", cluster.Status.Backup.LastFailureReason)
				require.NotEmpty(t, cluster.ResourceVersion)
				require.Equal(t, mutations+1, reads, "successful writes require read-back")
			}
		})
	}
}

func TestMutateWithReader_RejectsUnknownOwnershipPolicy(t *testing.T) {
	t.Parallel()
	err := MutateWithReader(t.Context(), nil, nil, &openbaov1alpha1.OpenBaoCluster{}, func(*openbaov1alpha1.OpenBaoCluster) error {
		t.Fatal("invalid policy must be rejected before mutation")
		return nil
	}, adminops.OwnershipPolicy(255))
	require.ErrorContains(t, err, "unsupported adminops ownership policy 255")
}

func TestNewMutator_SyncsCallerOnlyAfterSuccessfulReadBack(t *testing.T) {
	t.Parallel()
	for _, readFails := range []bool{false, true} {
		name := "successful read-back updates only owned fields and resource version"
		if readFails {
			name = "failed read-back leaves caller unchanged"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			scheme := runtime.NewScheme()
			if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			stored := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "readback", Namespace: "default", ResourceVersion: "1"},
				Status:     openbaov1alpha1.OpenBaoClusterStatus{CurrentVersion: "2.5.0"},
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(stored).WithObjects(stored).Build()
			readErr := errors.New("read-back unavailable")
			reads := 0
			reader := interceptor.NewClient(c, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					reads++
					if reads == 2 && readFails {
						return readErr
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			cluster := stored.DeepCopy()
			cluster.Status.CurrentVersion = "2.4.4"
			before := cluster.DeepCopy()
			mutateStatus := NewMutator(reader, c)
			err := mutateStatus(context.Background(), cluster,
				func(obj *openbaov1alpha1.OpenBaoCluster) error {
					obj.Status.Backup = &openbaov1alpha1.BackupStatus{LastFailureReason: "persist-me"}
					return nil
				}, adminops.RespectOwnership)

			persisted := &openbaov1alpha1.OpenBaoCluster{}
			if getErr := c.Get(context.Background(), client.ObjectKeyFromObject(cluster), persisted); getErr != nil {
				t.Fatal(getErr)
			}
			if persisted.Status.Backup == nil || persisted.Status.Backup.LastFailureReason != "persist-me" {
				t.Fatalf("persisted backup = %+v, want successful apply", persisted.Status.Backup)
			}
			want := before.DeepCopy()
			if readFails {
				if !errors.Is(err, readErr) {
					t.Fatalf("error = %v, want read-back error", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				want.ResourceVersion = persisted.ResourceVersion
				want.Status.Backup = persisted.Status.Backup
			}
			if !reflect.DeepEqual(cluster, want) {
				t.Fatalf("caller = %+v, want %+v", cluster, want)
			}
		})
	}
}
