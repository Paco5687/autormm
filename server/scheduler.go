package server

import (
	"context"
	"log"
	"time"

	"github.com/robfig/cron/v3"
)

const scriptRunTimeout = 120 // seconds

// runScript executes a stored script on a host and records the run.
func (s *Server) runScript(sc *Script, agentID, source string) *Run {
	run := Run{
		ScriptID: sc.ID, ScriptName: sc.Name, AgentID: agentID,
		Started: time.Now().Unix(), Source: source,
	}
	res, err := s.runOnAgent(agentID, sc.Content, sc.Shell, scriptRunTimeout)
	if err != nil {
		run.ExitCode = -1
		run.Error = err.Error()
	} else {
		run.Stdout, run.Stderr, run.ExitCode = res.Stdout, res.Stderr, res.ExitCode
		if res.Err != "" {
			run.Error = res.Err
		}
	}
	log.Printf("AUDIT script run=%q agent=%s source=%s exit=%d", sc.Name, agentID, source, run.ExitCode)
	return s.scripts.SaveRun(run)
}

// schedulerLoop fires due schedules once per minute.
func (s *Server) schedulerLoop(ctx context.Context) {
	if s.scripts == nil {
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runDueSchedules(time.Now())
		}
	}
}

// runDueSchedules runs every enabled schedule whose next cron time has passed
// since its last run.
func (s *Server) runDueSchedules(now time.Time) {
	schedules, err := s.scripts.ListSchedules()
	if err != nil {
		return
	}
	for _, sch := range schedules {
		if !sch.Enabled {
			continue
		}
		spec, err := cron.ParseStandard(sch.Cron)
		if err != nil {
			log.Printf("schedule %s: invalid cron %q: %v", sch.ID, sch.Cron, err)
			continue
		}
		prev := sch.LastRun
		if prev == 0 {
			// New schedule: don't backfire; start counting from the last minute.
			prev = now.Add(-time.Minute).Unix()
		}
		if spec.Next(time.Unix(prev, 0)).After(now) {
			continue // not due yet
		}
		sc, err := s.scripts.GetScript(sch.ScriptID)
		if err != nil {
			continue
		}
		s.scripts.markScheduleRun(sch.ID, now.Unix())
		go s.runScript(sc, sch.AgentID, "schedule")
	}
}
