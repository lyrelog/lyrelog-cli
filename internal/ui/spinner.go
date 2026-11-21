package ui

import (
	"io"
	"sync"
	"time"
)

// Spinner shows that the CLI is waiting, on stderr, for people only. It appears after a short
// delay (fast answers don't flicker) and erases itself when stopped.
type Spinner struct {
	w     io.Writer
	th    Theme
	mu    sync.Mutex
	text  string
	stop  chan struct{}
	done  chan struct{}
	shown bool
}

var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// StartSpinner starts one; with enabled false it does nothing (agents, pipes).
func StartSpinner(w io.Writer, th Theme, enabled bool, text string) *Spinner {
	s := &Spinner{w: w, th: th, text: text}
	if !enabled {
		return s
	}
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	go s.run()
	return s
}

func (s *Spinner) run() {
	defer close(s.done)
	delay := time.NewTimer(150 * time.Millisecond)
	defer delay.Stop()
	select {
	case <-s.stop:
		return
	case <-delay.C:
	}
	tick := time.NewTicker(80 * time.Millisecond)
	defer tick.Stop()
	for i := 0; ; i++ {
		s.mu.Lock()
		io.WriteString(s.w, "\r\x1b[2K"+s.th.Primary(frames[i%len(frames)])+" "+s.th.Dim(s.text))
		s.shown = true
		s.mu.Unlock()
		select {
		case <-s.stop:
			return
		case <-tick.C:
		}
	}
}

// Update changes the text.
func (s *Spinner) Update(text string) {
	s.mu.Lock()
	s.text = text
	s.mu.Unlock()
}

// Stop ends it and clears its line. Safe to call twice.
func (s *Spinner) Stop() {
	if s.stop == nil {
		return
	}
	select {
	case <-s.stop:
		return
	default:
		close(s.stop)
	}
	<-s.done
	if s.shown {
		io.WriteString(s.w, "\r\x1b[2K")
	}
}
