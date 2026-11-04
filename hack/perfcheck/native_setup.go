package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const certManagerManifestURL = "https://github.com/cert-manager/cert-manager/releases/download/" +
	"v1.19.1/cert-manager.yaml"

type nativeImageBuild struct {
	target string
	image  string
}

func prepareNativeKindCluster(opts options, cluster string, scenario scenarioSpec) error {
	timeout := opts.ClusterTimeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	setupCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := exportKindKubeconfig(setupCtx, opts, cluster); err != nil {
		return err
	}
	if err := waitForCoreDNS(setupCtx, opts, cluster); err != nil {
		return err
	}
	if err := prepareNativeImages(setupCtx, opts, cluster, scenario); err != nil {
		return err
	}
	if err := installCertManagerIfNeeded(setupCtx, opts, cluster); err != nil {
		return err
	}
	if err := ensureOperatorNamespace(setupCtx, opts, cluster); err != nil {
		return err
	}
	if _, err := runCommand(setupCtx, nativeCommandEnv(opts, cluster), opts.MakeBin, "install"); err != nil {
		return fmt.Errorf("install CRDs: %w", err)
	}
	if err := waitForCRDsEstablished(setupCtx, opts, cluster); err != nil {
		return err
	}
	if _, err := runCommand(
		setupCtx,
		nativeCommandEnv(opts, cluster),
		opts.MakeBin,
		"deploy",
		fmt.Sprintf("IMG=%s", opts.OperatorImage),
	); err != nil {
		return fmt.Errorf("deploy operator: %w", err)
	}
	if err := waitForOperatorDeployments(setupCtx, opts, cluster); err != nil {
		return err
	}
	return nil
}

func prepareNativeExistingCluster(opts options, cluster string) error {
	setupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := waitForCRDsEstablished(setupCtx, opts, cluster); err != nil {
		return err
	}
	if err := waitForOperatorDeployments(setupCtx, opts, cluster); err != nil {
		return err
	}
	return nil
}

func prepareNativeImages(ctx context.Context, opts options, cluster string, scenario scenarioSpec) error {
	if !opts.SkipImageBuild {
		for _, build := range nativeImageBuilds(opts, scenario) {
			if _, err := runCommand(
				ctx,
				nativeCommandEnv(opts, cluster),
				opts.MakeBin,
				build.target,
				fmt.Sprintf("IMG=%s", build.image),
			); err != nil {
				return fmt.Errorf("build image %s: %w", build.image, err)
			}
		}
	}

	images := nativeImages(opts, scenario)
	seen := make(map[string]struct{}, len(images))
	for _, image := range images {
		image = strings.TrimSpace(image)
		if image == "" {
			continue
		}
		if _, ok := seen[image]; ok {
			continue
		}
		seen[image] = struct{}{}
		if err := ensureLocalDockerImage(ctx, image); err != nil {
			return err
		}
		if _, err := runCommand(ctx, nil, opts.KindBin, "load", "docker-image", "--name", cluster, image); err != nil {
			return fmt.Errorf("load image %s into kind cluster %s: %w", image, cluster, err)
		}
	}
	return nil
}

func nativeImageBuilds(opts options, scenario scenarioSpec) []nativeImageBuild {
	builds := []nativeImageBuild{
		{target: "docker-build", image: opts.OperatorImage},
		{target: "docker-build-init", image: opts.ConfigInitImage},
	}
	switch scenario.Name {
	case "backup", "restore":
		builds = append(builds, nativeImageBuild{target: "docker-build-backup", image: opts.BackupExecutorImage})
	case "rolling-upgrade":
		builds = append(builds, nativeImageBuild{target: "docker-build-upgrade", image: opts.UpgradeExecutorImage})
	}
	return builds
}

func nativeImages(opts options, scenario scenarioSpec) []string {
	images := []string{
		opts.OperatorImage,
		opts.ConfigInitImage,
		opts.OpenBaoImage,
	}
	switch scenario.Name {
	case "backup", "restore":
		images = append(images, opts.BackupExecutorImage)
	case "rolling-upgrade":
		images = append(images, opts.UpgradeExecutorImage, opts.UpgradeFromImage, opts.UpgradeToImage)
	}
	return images
}

func ensureLocalDockerImage(ctx context.Context, image string) error {
	if _, err := runCommand(ctx, nil, "docker", "image", "inspect", image); err == nil {
		return nil
	}
	if _, err := runCommand(ctx, nil, "docker", "pull", image); err != nil {
		return fmt.Errorf("pull image %s: %w", image, err)
	}
	return nil
}

func exportKindKubeconfig(ctx context.Context, opts options, cluster string) error {
	kubeconfigPath := nativeKubeconfigPath(opts, cluster)
	if err := os.MkdirAll(filepath.Dir(kubeconfigPath), 0o755); err != nil {
		return fmt.Errorf("create kubeconfig directory: %w", err)
	}
	if _, err := runCommand(
		ctx,
		nil,
		opts.KindBin,
		"export",
		"kubeconfig",
		"--name",
		cluster,
		"--kubeconfig",
		kubeconfigPath,
	); err != nil {
		return fmt.Errorf("export kind kubeconfig for %s: %w", cluster, err)
	}
	return nil
}

func waitForCoreDNS(ctx context.Context, opts options, cluster string) error {
	_, err := nativeKubectl(ctx, opts, cluster,
		"wait",
		"--for=condition=Available",
		"deployment/coredns",
		"-n",
		"kube-system",
		"--timeout=2m",
	)
	if err != nil {
		return fmt.Errorf("wait for CoreDNS: %w", err)
	}
	return nil
}

func installCertManagerIfNeeded(ctx context.Context, opts options, cluster string) error {
	if _, err := nativeKubectl(ctx, opts, cluster, "get", "crd", "certificates.cert-manager.io"); err == nil {
		return nil
	}
	if _, err := nativeKubectl(ctx, opts, cluster, "apply", "-f", certManagerManifestURL); err != nil {
		return fmt.Errorf("install cert-manager: %w", err)
	}
	if _, err := nativeKubectl(ctx, opts, cluster,
		"wait",
		"--for",
		"condition=Established",
		"crd/certificates.cert-manager.io",
		"crd/issuers.cert-manager.io",
		"--timeout",
		"2m",
	); err != nil {
		return fmt.Errorf("wait for cert-manager CRDs: %w", err)
	}
	if _, err := nativeKubectl(ctx, opts, cluster,
		"wait",
		"deployment.apps/cert-manager",
		"deployment.apps/cert-manager-cainjector",
		"deployment.apps/cert-manager-webhook",
		"--for",
		"condition=Available",
		"--namespace",
		"cert-manager",
		"--timeout",
		"10m",
	); err != nil {
		return fmt.Errorf("wait for cert-manager deployments: %w", err)
	}
	return nil
}

func ensureOperatorNamespace(ctx context.Context, opts options, cluster string) error {
	if _, err := nativeKubectl(ctx, opts, cluster, "create", "ns", opts.OperatorNS); err != nil {
		errText := strings.ToLower(err.Error())
		if !strings.Contains(errText, "alreadyexists") && !strings.Contains(errText, "already exists") {
			return fmt.Errorf("create operator namespace: %w", err)
		}
	}
	if _, err := nativeKubectl(
		ctx,
		opts,
		cluster,
		"label",
		"--overwrite",
		"ns",
		opts.OperatorNS,
		"pod-security.kubernetes.io/enforce=restricted",
	); err != nil {
		return fmt.Errorf("label operator namespace: %w", err)
	}
	return nil
}

func waitForCRDsEstablished(ctx context.Context, opts options, cluster string) error {
	for _, crd := range []string{
		"openbaoclusters.kubebao.org",
		"openbaotenants.kubebao.org",
		"openbaorestores.kubebao.org",
	} {
		if _, err := nativeKubectl(
			ctx,
			opts,
			cluster,
			"wait",
			"--for=condition=Established",
			"crd/"+crd,
			"--timeout=2m",
		); err != nil {
			return fmt.Errorf("wait for CRD %s: %w", crd, err)
		}
	}
	return nil
}

func waitForOperatorDeployments(ctx context.Context, opts options, cluster string) error {
	if _, err := nativeKubectl(
		ctx,
		opts,
		cluster,
		"wait",
		"--for=condition=Available",
		"deployment",
		"-l",
		"app.kubernetes.io/name=openbao-operator",
		"-n",
		opts.OperatorNS,
		"--timeout=5m",
	); err != nil {
		return fmt.Errorf("wait for operator deployments: %w", err)
	}
	return nil
}

func nativeKubectl(ctx context.Context, opts options, cluster string, args ...string) (string, error) {
	allArgs := append([]string{"--context", kubeContext(opts, cluster)}, args...)
	return runCommand(ctx, nativeCommandEnv(opts, cluster), "kubectl", allArgs...)
}

func nativeCommandEnv(opts options, cluster string) map[string]string {
	env := map[string]string{}
	if opts.ExistingClusterContext == "" {
		env["KUBECONFIG"] = nativeKubeconfigPath(opts, cluster)
	}
	return env
}

func nativeKubeconfigPath(opts options, cluster string) string {
	return filepath.Join(opts.ArtifactDir, "kubeconfigs", cluster+".yaml")
}
