//go:build e2e

package e2e

import (
	"context"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/test/e2e/framework"
	"github.com/kubebao/openbao-operator/test/e2e/helpers"
)

// Each Ordered snapshot scenario owns a backend. RustFS runs as root, so it
// uses a separate namespace from the restricted OpenBao tenant namespace.
func newUpgradeStorage(ctx context.Context, c client.Client, cfg *rest.Config) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "upgrade-storage-"}}
	gomega.Expect(c.Create(ctx, ns)).To(gomega.Succeed())
	ginkgo.DeferCleanup(func() {
		if skipCleanup {
			return
		}
		if err := cleanupUpgradeStorage(ctx, c, ns); err != nil {
			skipCleanup = true
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	})
	gomega.Expect(helpers.EnsureRustFS(ctx, c, cfg, upgradeStorageConfig(ns.Name))).To(gomega.Succeed())
	return ns
}

func upgradeStorageConfig(namespace string) helpers.RustFSConfig {
	cfg := helpers.DefaultRustFSConfig()
	cfg.Namespace = namespace
	cfg.AccessKey, cfg.SecretKey = rustfsAccessKey, rustfsSecretKey
	cfg.Buckets = []string{rustfsBucket}
	return cfg
}

func upgradeStorageEndpoint(namespace string) string {
	return fmt.Sprintf("http://rustfs-svc.%s.svc:9000", namespace)
}

func cleanupUpgradeStorage(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	if ns.UID == "" {
		return fmt.Errorf("upgrade storage namespace %s has no ownership UID", ns.Name)
	}
	if err := c.Delete(ctx, ns, client.Preconditions{UID: &ns.UID}); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete upgrade storage namespace %s: %w", ns.Name, err)
	}
	return nil
}

func cleanupUpgradeFixture(ctx context.Context, f *framework.Framework) {
	if skipCleanup || f == nil {
		return
	}
	if err := f.Cleanup(ctx); err != nil {
		skipCleanup = true
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}
