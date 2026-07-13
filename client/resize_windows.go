//go:build windows

package client

import (
	"time"

	"golang.org/x/term"
)

// watchResize polls the console size on Windows (no SIGWINCH) and calls
// onResize when it changes. Close the returned channel to stop.
func watchResize(fd int, onResize func()) chan struct{} {
	stop := make(chan struct{})
	go func() {
		lastW, lastH, _ := term.GetSize(fd)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if w, h, err := term.GetSize(fd); err == nil && (w != lastW || h != lastH) {
					lastW, lastH = w, h
					onResize()
				}
			case <-stop:
				return
			}
		}
	}()
	return stop
}
