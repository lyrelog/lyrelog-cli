package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// signalContext ends the context on Ctrl-C or SIGTERM (follow mode exits cleanly).
func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
