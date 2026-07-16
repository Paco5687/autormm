//go:build windows

package main

import (
	"context"
	"log"

	"golang.org/x/sys/windows/svc"

	"github.com/Paco5687/autormm/agent"
)

// runAgent runs the agent under the Windows service control manager when it was
// launched as a service (the elevated LocalSystem helper); otherwise it runs in
// the foreground like any other program.
func runAgent(a *agent.Agent) {
	isSvc, err := svc.IsWindowsService()
	if err != nil || !isSvc {
		runInteractive(a)
		return
	}
	if err := svc.Run("autormm-agent", &agentService{agent: a}); err != nil {
		log.Fatalf("service run: %v", err)
	}
}

type agentService struct{ agent *agent.Agent }

func (s *agentService) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := s.agent.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("agent stopped: %v", err)
		}
	}()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for c := range req {
		switch c.Cmd {
		case svc.Interrogate:
			status <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			cancel()
			status <- svc.Status{State: svc.StopPending}
			return false, 0
		}
	}
	return false, 0
}
