package tui

import "testing"

func TestLogOptionsInitialAttachUsesTail(t *testing.T) {
	opts := logOptions(true)
	if !opts.Follow {
		t.Fatalf("initial attach should follow logs")
	}
	if opts.TailLines == nil {
		t.Fatalf("initial attach should request a tail replay")
	}
	if got, want := *opts.TailLines, int64(maxLogLines); got != want {
		t.Fatalf("initial attach TailLines = %d, want %d", got, want)
	}
}

func TestLogOptionsReconnectOmitsTail(t *testing.T) {
	opts := logOptions(false)
	if !opts.Follow {
		t.Fatalf("reconnect should still follow logs")
	}
	if opts.TailLines != nil {
		t.Fatalf("reconnect should not replay tail lines")
	}
}

func TestNextReconnectDelayCaps(t *testing.T) {
	delay := initialReconnectDelay
	for i := 0; i < 8; i++ {
		delay = nextReconnectDelay(delay)
	}
	if delay != maxReconnectDelay {
		t.Fatalf("reconnect delay = %v, want cap %v", delay, maxReconnectDelay)
	}
}
