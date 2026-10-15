package workload

import (
	"fmt"
	"path"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func buildInitContainers(cluster *openbaov1alpha1.OpenBaoCluster, verifiedInitContainerDigest string, disableSelfInit bool) ([]corev1.Container, error) {
	renderedConfigDir := path.Dir(openBaoRenderedConfig)

	args := []string{
		"--template", configTemplatePath,
		"--output", openBaoRenderedConfig,
	}

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      configVolumeName,
			MountPath: openBaoConfigMountPath,
			ReadOnly:  true,
		},
		{
			Name:      configRenderedVolumeName,
			MountPath: renderedConfigDir,
		},
		{
			Name:      utilsVolumeName,
			MountPath: "/utils",
		},
	}

	// If self-init is enabled, mount the self-init ConfigMap and pass the path to the init container
	// But check disableSelfInit first (e.g. for Green pods which should join instead of ensure self-init)
	if cluster.Spec.SelfInit != nil && cluster.Spec.SelfInit.Enabled && !disableSelfInit {
		selfInitPath := configInitTemplatePath
		args = append(args, "--self-init", selfInitPath)
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      configInitVolumeName,
			MountPath: path.Dir(configInitTemplatePath),
			ReadOnly:  true,
		})
	}

	initImage, err := getInitContainerImage(cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get init container image: %w", err)
	}
	if verifiedInitContainerDigest != "" {
		initImage = verifiedInitContainerDigest
	}

	return []corev1.Container{
		{
			Name:  "bao-config-init",
			Image: initImage,
			// The init container is responsible for rendering the final
			// config.hcl from the template using environment variables
			// such as HOSTNAME and POD_IP. It writes the result to a
			// shared volume mounted at openBaoRenderedConfig.
			// If self-init is enabled, it also appends self-init blocks
			// for pod-0.
			SecurityContext: &corev1.SecurityContext{
				// Prevent privilege escalation (sudo, setuid binaries)
				AllowPrivilegeEscalation: ptr.To(false),
				// Drop ALL capabilities.
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
				// Read-only root filesystem to prevent runtime modification
				ReadOnlyRootFilesystem: ptr.To(true),
				// Run as non-root (inherited from PodSecurityContext, but explicit here is safe)
				RunAsNonRoot: ptr.To(true),
			},
			// Use bao-init-config to copy wrapper and render config (no shell needed)
			Command: []string{"/bao-init-config"},
			Args: append([]string{
				"-copy-wrapper=/bao-wrapper",
				"-copy-probe=/bao-probe",
			}, args...),
			Env: []corev1.EnvVar{
				{
					Name: constants.EnvHostname,
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.name",
						},
					},
				},
				{
					Name: constants.EnvPodIP,
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "status.podIP",
						},
					},
				},
			},
			VolumeMounts: volumeMounts,
		},
	}, nil
}

// buildContainerEnv builds the environment variables for the OpenBao container.
// It includes standard variables and conditionally adds GCP credentials path
// when using GCP Cloud KMS seal.
func buildContainerEnv(cluster *openbaov1alpha1.OpenBaoCluster) []corev1.EnvVar {
	sealEnv := newSealWiringProvider(cluster).EnvVars()
	env := make([]corev1.EnvVar, 0, 6+len(sealEnv))
	env = append(env,
		corev1.EnvVar{
			Name: constants.EnvHostname,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.name",
				},
			},
		},
		corev1.EnvVar{
			// Required for OpenBao Kubernetes service registration.
			Name: constants.EnvBaoK8sPodName,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.name",
				},
			},
		},
		corev1.EnvVar{
			// Required for OpenBao Kubernetes service registration.
			Name: constants.EnvBaoK8sNamespace,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		},
		corev1.EnvVar{
			Name: constants.EnvPodIP,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "status.podIP",
				},
			},
		},
		corev1.EnvVar{
			// bao-wrapper formats this Pod IP URL after kubelet expansion so IPv6
			// literals have brackets while IPv4 advertisements remain unchanged.
			Name:  constants.EnvBaoAPIAddr,
			Value: fmt.Sprintf("https://$(%s):%d", constants.EnvPodIP, constants.PortAPI),
		},
		corev1.EnvVar{
			// Set umask to 0077 to ensure Raft FSM database files are created
			// with 0600 permissions (owner read/write only) instead of 0660.
			// This matches OpenBao's security expectations for sensitive data files.
			Name:  "UMASK",
			Value: "0077",
		},
	)

	env = append(env, sealEnv...)

	return env
}

// usesACMEMode returns true if the cluster is configured to use ACME for TLS.
func usesACMEMode(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster.Spec.TLS.Enabled && cluster.Spec.TLS.Mode == openbaov1alpha1.TLSModeACME
}

// buildContainerVolumeMounts builds the volume mounts for the OpenBao container.
// It conditionally includes the unseal volume mount only when using static seal.
// It conditionally excludes the TLS volume mount when using ACME mode.
func buildContainerVolumeMounts(cluster *openbaov1alpha1.OpenBaoCluster, renderedConfigDir string) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{
		{
			Name:      configVolumeName,
			MountPath: openBaoConfigMountPath,
			ReadOnly:  true,
		},
		{
			Name:      configRenderedVolumeName,
			MountPath: renderedConfigDir,
		},
		{
			Name:      dataVolumeName,
			MountPath: openBaoDataPath,
		},
		{
			Name:      tmpVolumeName,
			MountPath: "/tmp",
		},
	}

	// Mount the ServiceAccount token only into the OpenBao container. We disable
	// automounting at the Pod level and instead use an explicit projected volume
	// to minimize token exposure.
	if cluster.Annotations[constants.AnnotationRestoreOrigin] == "" {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      kubeAPIAccessVolumeName,
			MountPath: serviceAccountMountPath,
			ReadOnly:  true,
		})
	}

	// Only mount TLS volume when not using ACME mode (ACME stores certs in OpenBao's
	// internal ACME cache rather than in a mounted Kubernetes TLS Secret).
	if !usesACMEMode(cluster) {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      tlsVolumeName,
			MountPath: openBaoTLSMountPath,
			ReadOnly:  true,
		})
	}

	if portopenbao.HasACMESharedCache(cluster) {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      acmeCacheVolumeName,
			MountPath: portopenbao.ACMESharedCacheMountPath,
		})
	}

	if portopenbao.AuditFileStorageClaimName(cluster) != "" {
		mounts = append(mounts, corev1.VolumeMount{
			Name:        auditFileStorageVolumeName,
			MountPath:   portopenbao.AuditFileStorageMountPath(cluster),
			SubPathExpr: portopenbao.AuditFileStoragePodSubPathExpr,
		})
	}

	if usesDeclarativeOCIPluginDownload(cluster) {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      pluginVolumeName,
			MountPath: openBaoPluginPath,
		})
	}

	mounts = append(mounts, newSealWiringProvider(cluster).VolumeMounts()...)

	// Add utils volume mount (Read-Only for security)
	mounts = append(mounts, corev1.VolumeMount{
		Name:      utilsVolumeName,
		MountPath: "/utils",
		ReadOnly:  true,
	})

	return mounts
}

// buildContainers builds the container list for the OpenBao pod.
// The OpenBao container uses a wrapper binary as the entrypoint that manages
// the OpenBao process and watches for TLS certificate changes before OpenBao 2.7.
func buildContainers(cluster *openbaov1alpha1.OpenBaoCluster, spec StatefulSetSpec, renderedConfigDir string, probes probeExecActions) []corev1.Container {
	// Add utils volume mount (Read-Only for security)
	mainVolumeMounts := buildContainerVolumeMounts(cluster, renderedConfigDir)

	// Construct the wrapper command
	// We pass the actual OpenBao command as arguments to the wrapper
	cmd := []string{constants.PathWrapperBinary}

	// Configure wrapper args
	args := []string{}

	// OpenBao 2.7 and later watch certificate and key files without SIGHUP.
	if !usesACMEMode(cluster) && !portopenbao.UsesNativeTLSAutoReload(cluster) {
		args = append(args, fmt.Sprintf("-watch-file=%s/tls.crt", constants.PathTLS))
	}

	// Separator for the child command
	args = append(args, "--")

	// The actual OpenBao command
	args = append(args, openBaoBinaryName, "server", fmt.Sprintf("-config=%s", getOpenBaoConfigPath(cluster)))

	ports := []corev1.ContainerPort{
		{
			Name:          "api",
			ContainerPort: int32(constants.PortAPI),
			Protocol:      corev1.ProtocolTCP,
		},
		{
			Name:          "cluster",
			ContainerPort: int32(constants.PortCluster),
			Protocol:      corev1.ProtocolTCP,
		},
	}
	if metricsOnlyListenerEnabled(cluster) {
		ports = append(ports, corev1.ContainerPort{
			Name:          "metrics",
			ContainerPort: metricsOnlyListenerPort(cluster),
			Protocol:      corev1.ProtocolTCP,
		})
	}

	containers := []corev1.Container{
		{
			Name:  constants.ContainerBao,
			Image: getContainerImage(cluster, spec.Image),
			SecurityContext: &corev1.SecurityContext{
				// Prevent privilege escalation (sudo, setuid binaries)
				AllowPrivilegeEscalation: ptr.To(false),
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
				// Read-only root filesystem. Attackers cannot write tools/scripts to the container disk.
				// OpenBao writes to mounted volumes (/bao/data, /etc/bao/config, etc.) which are already mounted.
				ReadOnlyRootFilesystem: ptr.To(true),
			},
			Command:      cmd,
			Args:         args,
			Env:          buildContainerEnv(cluster),
			Resources:    buildContainerResources(cluster, spec),
			Ports:        ports,
			VolumeMounts: mainVolumeMounts,
			StartupProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					Exec: probes.startup,
				},
				InitialDelaySeconds: 10,
				TimeoutSeconds:      10,
				PeriodSeconds:       5,
				FailureThreshold:    60,
			},
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					Exec: probes.liveness,
				},
				TimeoutSeconds:   5,
				PeriodSeconds:    10,
				FailureThreshold: 6,
			},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					Exec: probes.readiness,
				},
				InitialDelaySeconds: 20,
				TimeoutSeconds:      10,
				PeriodSeconds:       10,
				FailureThreshold:    6,
			},
		},
	}

	return containers
}

func buildContainerResources(cluster *openbaov1alpha1.OpenBaoCluster, spec StatefulSetSpec) corev1.ResourceRequirements {
	if spec.Pool == constants.LabelValueOpenBaoWorkloadPoolReadReplica &&
		cluster.Spec.ReadReplicas != nil &&
		cluster.Spec.ReadReplicas.Template != nil &&
		cluster.Spec.ReadReplicas.Template.Resources != nil {
		return *cluster.Spec.ReadReplicas.Template.Resources.DeepCopy()
	}
	if spec.Pool == constants.LabelValueOpenBaoWorkloadPoolVoter && cluster.Spec.Resources != nil {
		return *cluster.Spec.Resources.DeepCopy()
	}
	return corev1.ResourceRequirements{}
}
