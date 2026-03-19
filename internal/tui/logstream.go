package tui

import (
	"bufio"
	"context"
	"sync"

	"github.com/abix-/k3sc/internal/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

const maxLogLines = 8

type podStream struct {
	agent  string
	issue  int
	mu     sync.Mutex
	lines  []string
	cancel context.CancelFunc
}

func (ps *podStream) appendLine(line string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.lines = append(ps.lines, line)
	if len(ps.lines) > maxLogLines {
		ps.lines = ps.lines[len(ps.lines)-maxLogLines:]
	}
}

func (ps *podStream) snapshot() []string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := make([]string, len(ps.lines))
	copy(out, ps.lines)
	return out
}

// LogStreamer manages persistent Follow log streams for running pods.
type LogStreamer struct {
	mu      sync.Mutex
	streams map[string]*podStream
	cs      *kubernetes.Clientset
	ns      string
}

func NewLogStreamer(cs *kubernetes.Clientset, ns string) *LogStreamer {
	return &LogStreamer{
		streams: make(map[string]*podStream),
		cs:      cs,
		ns:      ns,
	}
}

// Sync starts streams for new running pods and stops streams for gone pods.
func (ls *LogStreamer) Sync(pods []types.AgentPod) {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	// build set of currently running pod names
	running := map[string]types.AgentPod{}
	for _, p := range pods {
		if p.Phase == types.PhaseRunning {
			running[p.Name] = p
		}
	}

	// stop streams for pods no longer running
	for name, ps := range ls.streams {
		if _, ok := running[name]; !ok {
			ps.cancel()
			delete(ls.streams, name)
		}
	}

	// start streams for new running pods
	for name, pod := range running {
		if _, ok := ls.streams[name]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		ps := &podStream{
			agent:  types.AgentName(pod.Family, pod.Slot),
			issue:  pod.Issue,
			cancel: cancel,
		}
		ls.streams[name] = ps
		go ls.follow(ctx, name, ps)
	}
}

func (ls *LogStreamer) follow(ctx context.Context, podName string, ps *podStream) {
	var tailLines int64 = maxLogLines
	req := ls.cs.CoreV1().Pods(ls.ns).GetLogs(podName, &corev1.PodLogOptions{
		Follow:    true,
		TailLines: &tailLines,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return
	}
	defer stream.Close()

	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := scanner.Text()
		if line != "" {
			ps.appendLine(line)
		}
	}
}

// Snapshot returns current log lines for all active streams.
func (ls *LogStreamer) Snapshot() []LiveLog {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	var result []LiveLog
	for _, ps := range ls.streams {
		lines := ps.snapshot()
		if len(lines) > 0 {
			result = append(result, LiveLog{
				Issue: ps.issue,
				Agent: ps.agent,
				Lines: lines,
			})
		}
	}
	return result
}

// Stop cancels all active streams.
func (ls *LogStreamer) Stop() {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	for name, ps := range ls.streams {
		ps.cancel()
		delete(ls.streams, name)
	}
}
