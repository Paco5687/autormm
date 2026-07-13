//go:build !windows

package client

import (
	"os"
	"os/signal"
	"syscall"
)

// watchResize calls onResize whenever the terminal window changes size.
// Close the returned channel to stop watching.
func watchResize(_ int, onResize func()) chan struct{} {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	stop := make(chan struct{})
	go func() {
		defer signal.Stop(winch)
		for {
			select {
			case <-winch:
				onResize()
			case <-stop:
				return
			}
		}
	}()
	return stop
}
