package workload

import (
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/initmanager"
)

const selfInitInputsVolumeName = "self-init-inputs"

func addSelfInitSecretInputs(pod *corev1.PodSpec, inputs []initmanager.SecretInput) {
	if len(inputs) == 0 {
		return
	}
	sources := make([]corev1.VolumeProjection, 0, len(inputs))
	for _, input := range inputs {
		sources = append(sources, corev1.VolumeProjection{
			Secret: &corev1.SecretProjection{
				LocalObjectReference: corev1.LocalObjectReference{Name: input.SecretName},
				// Stale bootstrap Pod templates must still start after completion.
				// bao-config-init requires the files while bootstrap config is active.
				Optional: ptr.To(true),
				Items:    []corev1.KeyToPath{{Key: input.SecretKey, Path: input.FileName}},
			},
		})
	}
	pod.Volumes = append(pod.Volumes, corev1.Volume{
		Name: selfInitInputsVolumeName,
		VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{DefaultMode: ptr.To(secretFileMode), Sources: sources},
		},
	})
	mount := corev1.VolumeMount{
		Name: selfInitInputsVolumeName, MountPath: constants.PathSelfInitInputs, ReadOnly: true,
	}
	for i := range pod.InitContainers {
		if pod.InitContainers[i].Name != "bao-config-init" {
			continue
		}
		pod.InitContainers[i].VolumeMounts = append(pod.InitContainers[i].VolumeMounts, mount)
		for _, input := range inputs {
			pod.InitContainers[i].Args = append(pod.InitContainers[i].Args, "--self-init-secret-file="+input.Path())
		}
	}
	for i := range pod.Containers {
		if pod.Containers[i].Name == constants.ContainerBao {
			pod.Containers[i].VolumeMounts = append(pod.Containers[i].VolumeMounts, mount)
		}
	}
}

// removeSelfInitSecretInputs only reduces bootstrap access. Admission permits
// these removals while keeping all other locked template fields unchanged.
func removeSelfInitSecretInputs(pod *corev1.PodSpec) {
	pod.Volumes = slices.DeleteFunc(slices.Clone(pod.Volumes), func(v corev1.Volume) bool {
		return v.Name == selfInitInputsVolumeName
	})
	removeMount := func(m corev1.VolumeMount) bool { return m.Name == selfInitInputsVolumeName }
	for i := range pod.InitContainers {
		if pod.InitContainers[i].Name != "bao-config-init" {
			continue
		}
		pod.InitContainers[i].VolumeMounts = slices.DeleteFunc(slices.Clone(pod.InitContainers[i].VolumeMounts), removeMount)
		pod.InitContainers[i].Args = slices.DeleteFunc(slices.Clone(pod.InitContainers[i].Args), func(arg string) bool {
			return strings.HasPrefix(arg, "--self-init-secret-file="+constants.PathSelfInitInputs+"/")
		})
	}
	for i := range pod.Containers {
		if pod.Containers[i].Name == constants.ContainerBao {
			pod.Containers[i].VolumeMounts = slices.DeleteFunc(slices.Clone(pod.Containers[i].VolumeMounts), removeMount)
		}
	}
}
