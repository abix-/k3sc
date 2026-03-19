package tui

import (
	"bufio"
	"context"
	"sort"
	"sync"
	"time"

	"github.com/abix-/k3sc/internal/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

const maxLogLines = 8

const maxScanBuffer = 1 << 20 // 1 MB -- handles long tool/log lines

const (
	initialReconnectDelay = 500 * time.Millisecond
	maxReconnectDelay     = 5 * time.Second
)

type podStream struct {
	podName  string
	agent    string
	issue    int
	mu       sync.Mutex
	lines    []string
	lastTail string // cached last meaningful line for dashboard O(1)
	cancel   context.CancelFunc
	done     chan struct{} // closed when goroutine exits
}

func (ps *podStream) appendLine(line string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.lines = append(ps.lines, line)
	if len(ps.lines) > maxLogLines {
		ps.lines = ps.lines[len(ps.lines)-maxLogLines:]
	}
	// cache last meaningful line (skip entrypoint/tool/result noise)
	if !isMeta(line) {
		ps.lastTail = line
	}
}

func isMeta(line string) bool {
	for _, prefix := range []string{"[entrypoint]", "[tool]", "[result]"} {
		if len(line) >= len(prefix) && line[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func (ps *podStream) snapshot() ([]string, string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := make([]string, len(ps.lines))
	copy(out, ps.lines)
	return out, ps.lastTail
}

// wait blocks until the goroutine exits.
func (ps *podStream) wait() {
	<-ps.done
}

// stop cancels and waits for the goroutine to exit.
func (ps *podStream) stop() {
	ps.cancel()
	ps.wait()
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

func nextReconnectDelay(current time.Duration) time.Duration {
	if current <= 0 {
		return initialReconnectDelay
	}
	next := current * 2
	if next > maxReconnectDelay {
		return maxReconnectDelay
	}
	return next
}

func logOptions(initialAttach bool) corev1.PodLogOptions {
	opts := corev1.PodLogOptions{Follow: true}
	if initialAttach {
		lines := int64(maxLogLines)
		opts.TailLines = &lines
	}
	return opts
}

func waitForReconnect(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Sync starts streams for new running pods and stops streams for gone pods.
func (ls *LogStreamer) Sync(pods []types.AgentPod) {
	ls.mu.Lock()

	// build set of currently running pod names
	running := map[string]types.AgentPod{}
	for _, p := range pods {
		if p.Phase == types.PhaseRunning {
			running[p.Name] = p
		}
	}

	// collect streams to stop (can't hold ls.mu while waiting)
	var toStop []*podStream
	for name, ps := range ls.streams {
		if _, ok := running[name]; !ok {
			toStop = append(toStop, ps)
			delete(ls.streams, name)
		}
	}

	// collect pods that need new streams
	var toStart []types.AgentPod
	for name, pod := range running {
		if _, ok := ls.streams[name]; !ok {
			toStart = append(toStart, pod)
		}
	}

	// start new streams while holding the lock
	for _, pod := range toStart {
		ctx, cancel := context.WithCancel(context.Background())
		ps := &podStream{
			podName: pod.Name,
			agent:   types.AgentName(pod.Family, pod.Slot),
			issue:   pod.Issue,
			cancel:  cancel,
			done:    make(chan struct{}),
		}
		ls.streams[pod.Name] = ps
		go ls.follow(ctx, pod.Name, ps)
	}

	ls.mu.Unlock()

	// stop old streams outside the lock so we can wait without blocking
	for _, ps := range toStop {
		ps.stop()
	}
}

func (ls *LogStreamer) follow(ctx context.Context, podName string, ps *podStream) {
	defer close(ps.done)

	reconnectDelay := initialReconnectDelay
	initialAttach := true
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		opts := logOptions(initialAttach)
		req := ls.cs.CoreV1().Pods(ls.ns).GetLogs(podName, &opts)
		stream, err := req.Stream(ctx)
		if err != nil {
			if !waitForReconnect(ctx, reconnectDelay) {
				return
			}
			reconnectDelay = nextReconnectDelay(reconnectDelay)
			continue
		}
		initialAttach = false
		reconnectDelay = initialReconnectDelay

		scanner := bufio.NewScanner(stream)
		scanner.Buffer(make([]byte, 0, maxScanBuffer), maxScanBuffer)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				stream.Close()
				return
			default:
			}
			line := scanner.Text()
			if line != "" {
				ps.appendLine(line)
			}
		}
		stream.Close()
		if ctx.Err() != nil {
			return
		}
		if scanner.Err() != nil {
			if !waitForReconnect(ctx, reconnectDelay) {
				return
			}
			reconnectDelay = nextReconnectDelay(reconnectDelay)
			continue
		}
		// stream ended (EOF) -- reconnect unless cancelled
		if !waitForReconnect(ctx, reconnectDelay) {
			return
		}
	}
}

// Snapshot returns current log lines for all active streams.
func (ls *LogStreamer) Snapshot() []LiveLog {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	var result []LiveLog
	for _, ps := range ls.streams {
		lines, tail := ps.snapshot()
		result = append(result, LiveLog{
			PodName: ps.podName,
			Issue:   ps.issue,
			Agent:   ps.agent,
			Lines:   lines,
			Tail:    tail,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Agent < result[j].Agent
	})
	return result
}

// Stop cancels all active streams and waits for goroutines to exit.
func (ls *LogStreamer) Stop() {
	ls.mu.Lock()
	var all []*podStream
	for name, ps := range ls.streams {
		all = append(all, ps)
		delete(ls.streams, name)
	}
	ls.mu.Unlock()

	for _, ps := range all {
		ps.stop()
	}
}
