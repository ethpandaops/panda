package devnet

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// enclaveIDLabel is the label Kurtosis puts on an enclave's Kubernetes namespace
// carrying the enclave UUID. We resolve the namespace from it rather than
// assuming a name pattern, because pool-claimed enclaves keep the namespace of
// the idle enclave they were created from (kt-idle-enclave-<uuid>).
const enclaveIDLabel = "kurtosistech.com/enclave-id"

// userServiceContainer is the container name Kurtosis gives every user service's
// main container on Kubernetes (alongside a files-artifact-expander init
// container).
const userServiceContainer = "user-service-container"

// podLogs fetches the last tail lines of each service's pod log in the enclave's
// namespace and writes them to out, prefixed with the service name. A service
// whose pod is missing or has no logs (e.g. a completed one-shot task) is noted
// and skipped rather than failing the whole request.
func podLogs(ctx context.Context, enclaveUUID string, services []string, tail int64, out io.Writer) error {
	clientset, err := newK8sClient()
	if err != nil {
		return err
	}

	namespace, err := enclaveNamespace(ctx, clientset, enclaveUUID)
	if err != nil {
		return err
	}

	for _, svc := range services {
		write := func(line string) { fmt.Fprintln(out, line) }
		if err := streamPodLogs(ctx, clientset, namespace, svc, tail, false, write); err != nil {
			fmt.Fprintf(out, "%s | <logs unavailable: %v>\n", svc, err)
		}
	}

	return nil
}

// followPodLogs streams each service's pod log concurrently until ctx is
// cancelled, serializing writes (and flushes) through a mutex so interleaved
// lines stay intact. flush may be nil.
func followPodLogs(ctx context.Context, enclaveUUID string, services []string, tail int64, out io.Writer, flush func()) error {
	clientset, err := newK8sClient()
	if err != nil {
		return err
	}

	namespace, err := enclaveNamespace(ctx, clientset, enclaveUUID)
	if err != nil {
		return err
	}

	var mu sync.Mutex
	write := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintln(out, line)
		if flush != nil {
			flush()
		}
	}

	var wg sync.WaitGroup
	for _, svc := range services {
		wg.Add(1)
		go func(svc string) {
			defer wg.Done()
			if err := streamPodLogs(ctx, clientset, namespace, svc, tail, true, write); err != nil && ctx.Err() == nil {
				write(fmt.Sprintf("%s | <log stream ended: %v>", svc, err))
			}
		}(svc)
	}
	wg.Wait()

	return nil
}

// streamPodLogs reads a single service pod's log (optionally following) and
// hands each line, already prefixed with the service name, to write.
func streamPodLogs(ctx context.Context, clientset kubernetes.Interface, namespace, service string, tail int64, follow bool, write func(line string)) error {
	req := clientset.CoreV1().Pods(namespace).GetLogs(service, &corev1.PodLogOptions{
		Container: userServiceContainer,
		TailLines: &tail,
		Follow:    follow,
	})

	stream, err := req.Stream(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		write(fmt.Sprintf("%s | %s", service, scanner.Text()))
	}

	return scanner.Err()
}

// enclaveNamespace finds the Kubernetes namespace for an enclave by its UUID
// label.
func enclaveNamespace(ctx context.Context, clientset kubernetes.Interface, enclaveUUID string) (string, error) {
	nss, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", enclaveIDLabel, enclaveUUID),
	})
	if err != nil {
		return "", fmt.Errorf("finding namespace for enclave %s: %w", enclaveUUID, err)
	}
	if len(nss.Items) == 0 {
		return "", fmt.Errorf("no Kubernetes namespace found for enclave %s", enclaveUUID)
	}

	return nss.Items[0].Name, nil
}

// newK8sClient builds a Kubernetes clientset from the current kubeconfig context
// (which EnsureKubeContext points at the configured cluster).
func newK8sClient() (*kubernetes.Clientset, error) {
	// In-cluster first (the prod panda-server runs as a pod with a
	// ServiceAccount); fall back to a kubeconfig for local/bruno use.
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{},
		).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("loading Kubernetes config (no in-cluster config and no kubeconfig): %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building Kubernetes client: %w", err)
	}

	return clientset, nil
}
