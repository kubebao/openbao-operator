package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

const openBaoPort = "8200"

var forwardingLine = regexp.MustCompile(`^Forwarding from 127\.0\.0\.1:(\d+) -> ` + openBaoPort + `$`)

// portForward keeps a kubectl port-forward to one ready OpenBao Pod of a
// Cluster. A port-forward reaches the Pod's own network namespace, so the
// Cluster's NetworkPolicies do not apply. A rolling restart replaces Pods: the
// next dial restarts the forward when its process ended or its Pod changed.
type portForward struct {
	kubectl   []string
	reader    client.Reader
	namespace string
	cluster   string
	voterSet  string

	mu     sync.Mutex
	cmd    *exec.Cmd
	done   chan struct{}
	addr   string
	pod    string
	podUID types.UID
}

func newPortForward(kubeconfig, kubeContext string, reader client.Reader, namespace, cluster, voterSet string) *portForward {
	return &portForward{kubectl: []string{"--kubeconfig", kubeconfig, "--context", kubeContext},
		reader: reader, namespace: namespace, cluster: cluster, voterSet: voterSet}
}

// DialContext ignores the requested address: every connection goes to the Pod.
func (f *portForward) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	addr, err := f.ensure(ctx)
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		f.Close()
		return nil, err
	}
	return conn, nil
}

func (f *portForward) ensure(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cmd != nil {
		select {
		case <-f.done:
			f.cmd = nil
		default:
			if f.podCurrent(ctx) {
				return f.addr, nil
			}
			f.stopLocked()
		}
	}
	pod, err := f.readyPod(ctx)
	if err != nil {
		return "", err
	}
	path, err := exec.LookPath("kubectl")
	if err != nil {
		return "", errors.New("kubectl is required to reach OpenBao through a port-forward")
	}
	args := append(slices.Clone(f.kubectl), "-n", f.namespace, "port-forward", "pod/"+pod.Name,
		":"+openBaoPort, "--address", "127.0.0.1")
	cmd := exec.Command(path, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start port-forward: %w", err)
	}
	ports := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if match := forwardingLine.FindStringSubmatch(scanner.Text()); match != nil {
				ports <- match[1]
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case port := <-ports:
		f.cmd, f.done, f.addr, f.pod, f.podUID = cmd, done, "127.0.0.1:"+port, pod.Name, pod.UID
		return f.addr, nil
	case <-done:
		return "", fmt.Errorf("port-forward to %s/%s exited before it was ready", f.namespace, pod.Name)
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("port-forward to %s/%s was not ready within 30s", f.namespace, pod.Name)
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return "", ctx.Err()
	}
}

// readyPod returns the first ready voter Pod of the Cluster.
func (f *portForward) readyPod(ctx context.Context) (*corev1.Pod, error) {
	pods := &corev1.PodList{}
	if err := f.reader.List(ctx, pods, client.InNamespace(f.namespace),
		client.MatchingLabels{constants.LabelOpenBaoCluster: f.cluster}); err != nil {
		return nil, err
	}
	slices.SortFunc(pods.Items, func(a, b corev1.Pod) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	for i := range pods.Items {
		if pod := &pods.Items[i]; voterPod(pod, f.voterSet) && podReady(pod) {
			return pod, nil
		}
	}
	return nil, fmt.Errorf("no ready OpenBao Pod of %s/%s to reach", f.namespace, f.cluster)
}

func (f *portForward) podCurrent(ctx context.Context) bool {
	pod := &corev1.Pod{}
	err := f.reader.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: f.pod}, pod)
	if apierrors.IsNotFound(err) || err != nil {
		return false
	}
	return pod.UID == f.podUID && podReady(pod)
}

// voterPod checks the pinned active voter StatefulSet. BlueGreen can give it
// a revision suffix; read replicas and inactive revisions are excluded.
func voterPod(pod *corev1.Pod, voterSet string) bool {
	owner := metav1.GetControllerOf(pod)
	return owner != nil && owner.Kind == statefulSetKind && owner.Name == voterSet
}

func podReady(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// Close stops the forward; the next dial starts a new one.
func (f *portForward) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopLocked()
}

func (f *portForward) stopLocked() {
	if f.cmd != nil && f.cmd.Process != nil {
		_ = f.cmd.Process.Kill()
		<-f.done
	}
	f.cmd = nil
}
