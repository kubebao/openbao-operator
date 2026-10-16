package controller

import (
	"context"
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/openbao"
)

const (
	clientStateCleanupInterval = time.Minute
	clientStateCleanupTimeout  = 10 * time.Second
)

func addOpenBaoClientStateCleanup(mgr ctrl.Manager, clients *openbao.ClientManager, namespace string) error {
	if err := mgr.Add(&openBaoClientStateCleanup{
		clients: clients, reader: mgr.GetAPIReader(), namespace: namespace,
	}); err != nil {
		return fmt.Errorf("register OpenBao client state cleanup: %w", err)
	}
	return nil
}

type openBaoClientStateCleanup struct {
	clients   *openbao.ClientManager
	reader    client.Reader
	namespace string
}

// NeedLeaderElection also releases state on replicas that have lost leadership.
func (r *openBaoClientStateCleanup) NeedLeaderElection() bool { return false }

func (r *openBaoClientStateCleanup) Start(ctx context.Context) error {
	defer r.clients.Close()
	ticker := time.NewTicker(clientStateCleanupInterval)
	defer ticker.Stop()
	for {
		if err := r.cleanup(ctx); err != nil && ctx.Err() == nil {
			ctrl.LoggerFrom(ctx).Error(err, "Unable to release deleted OpenBao cluster client state")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *openBaoClientStateCleanup) cleanup(ctx context.Context) error {
	// Snapshot before listing so a cluster first observed during the API read
	// cannot have its newly registered state removed by an older list response.
	keys := r.clients.ClusterKeys()
	if len(keys) == 0 {
		return nil
	}
	listCtx, cancel := context.WithTimeout(ctx, clientStateCleanupTimeout)
	defer cancel()
	clusters := &openbaov1alpha1.OpenBaoClusterList{}
	if err := r.reader.List(listCtx, clusters, client.InNamespace(r.namespace)); err != nil {
		return fmt.Errorf("list OpenBaoClusters for client state cleanup: %w", err)
	}
	live := make(map[string]struct{}, len(clusters.Items))
	for i := range clusters.Items {
		live[string(clusters.Items[i].UID)] = struct{}{}
	}
	for _, key := range keys {
		if _, ok := live[key]; !ok {
			r.clients.Forget(key)
		}
	}
	return nil
}
