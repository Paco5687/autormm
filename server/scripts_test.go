package server

import (
	"path/filepath"
	"testing"
	"time"
)

func TestScriptStoreCRUD(t *testing.T) {
	h, err := OpenHistory(filepath.Join(t.TempDir(), "s.db"), time.Hour)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer h.Close()
	ss, err := NewScriptStore(h.DB())
	if err != nil {
		t.Fatalf("script store: %v", err)
	}

	sc, err := ss.SaveScript(Script{Name: "greet", Shell: "sh", Content: "echo hi"})
	if err != nil || sc.ID == "" {
		t.Fatalf("save script: %v", err)
	}
	got, err := ss.GetScript(sc.ID)
	if err != nil || got.Name != "greet" || got.Content != "echo hi" {
		t.Fatalf("get script: %+v %v", got, err)
	}

	// Update in place.
	sc.Content = "echo bye"
	ss.SaveScript(*sc)
	got, _ = ss.GetScript(sc.ID)
	if got.Content != "echo bye" {
		t.Fatalf("update failed: %q", got.Content)
	}

	sch, err := ss.SaveSchedule(Schedule{ScriptID: sc.ID, AgentID: "h1", Cron: "* * * * *", Enabled: true})
	if err != nil || sch.ID == "" {
		t.Fatalf("save schedule: %v", err)
	}
	if schs, _ := ss.ListSchedules(); len(schs) != 1 || !schs[0].Enabled {
		t.Fatalf("schedule list wrong: %+v", schs)
	}

	ss.SaveRun(Run{ScriptID: sc.ID, ScriptName: "greet", AgentID: "h1", ExitCode: 0, Stdout: "hi", Source: "manual"})
	ss.SaveRun(Run{ScriptID: sc.ID, ScriptName: "greet", AgentID: "h2", ExitCode: 1, Stderr: "boom", Source: "schedule"})
	if runs, _ := ss.ListRuns("", 10); len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs, _ := ss.ListRuns("h2", 10); len(runs) != 1 || runs[0].ExitCode != 1 {
		t.Fatalf("filtered runs wrong: %+v", runs)
	}

	// Deleting a script cascades to its schedules.
	if err := ss.DeleteScript(sc.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if schs, _ := ss.ListSchedules(); len(schs) != 0 {
		t.Fatalf("schedules should be gone, got %d", len(schs))
	}
	if scripts, _ := ss.ListScripts(); len(scripts) != 0 {
		t.Fatalf("scripts should be gone, got %d", len(scripts))
	}
}
