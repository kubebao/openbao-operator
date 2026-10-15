package main

import (
	"fmt"
	"net"
	"strings"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

// childEnvironment formats the operator's Pod IP advertisement at runtime, after
// the kubelet has expanded POD_IP. Kubernetes env expansion cannot add brackets
// conditionally, so a literal IPv6 address needs formatting before OpenBao sees it.
// Preserve other API address values, including admission-injected DNS overrides.
func childEnvironment(env []string) []string {
	var podIP, apiAddr string
	apiAddrIndex := -1
	for i, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case constants.EnvPodIP:
			podIP = value
		case constants.EnvBaoAPIAddr:
			apiAddr, apiAddrIndex = value, i
		}
	}

	if net.ParseIP(podIP) == nil || apiAddr != fmt.Sprintf("https://%s:%d", podIP, constants.PortAPI) {
		return env
	}

	result := append([]string(nil), env...)
	result[apiAddrIndex] = constants.EnvBaoAPIAddr + "=https://" + net.JoinHostPort(podIP, fmt.Sprint(constants.PortAPI))
	return result
}
