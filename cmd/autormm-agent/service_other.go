//go:build !windows

package main

import "github.com/Paco5687/autormm/agent"

func runAgent(a *agent.Agent) { runInteractive(a) }
