package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Script, Schedule and Run mirror the server's JSON shapes.
type Script struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Shell   string `json:"shell"`
	Content string `json:"content"`
	Created int64  `json:"created"`
}

type Schedule struct {
	ID       string `json:"id"`
	ScriptID string `json:"script_id"`
	AgentID  string `json:"agent_id"`
	Cron     string `json:"cron"`
	Enabled  bool   `json:"enabled"`
	LastRun  int64  `json:"last_run"`
}

type Run struct {
	ID         string `json:"id"`
	ScriptID   string `json:"script_id"`
	ScriptName string `json:"script_name"`
	AgentID    string `json:"agent_id"`
	Started    int64  `json:"started"`
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Error      string `json:"error"`
	Source     string `json:"source"`
}

func (c *Client) decode(res *http.Response, err error, out any) error {
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg, _ := io.ReadAll(res.Body)
		return fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (c *Client) ListScripts() ([]Script, error) {
	res, err := c.do(http.MethodGet, "/api/scripts", nil)
	var out []Script
	return out, c.decode(res, err, &out)
}

func (c *Client) SaveScript(sc Script) (*Script, error) {
	res, err := c.do(http.MethodPost, "/api/scripts", sc)
	var out Script
	if e := c.decode(res, err, &out); e != nil {
		return nil, e
	}
	return &out, nil
}

func (c *Client) DeleteScript(id string) error {
	res, err := c.do(http.MethodDelete, "/api/scripts?id="+url.QueryEscape(id), nil)
	return c.decode(res, err, nil)
}

func (c *Client) RunScript(scriptID, agentID string) (*Run, error) {
	res, err := c.do(http.MethodPost, "/api/scripts/run", map[string]string{"script_id": scriptID, "agent_id": agentID})
	var out Run
	if e := c.decode(res, err, &out); e != nil {
		return nil, e
	}
	return &out, nil
}

func (c *Client) ListSchedules() ([]Schedule, error) {
	res, err := c.do(http.MethodGet, "/api/schedules", nil)
	var out []Schedule
	return out, c.decode(res, err, &out)
}

func (c *Client) SaveSchedule(sch Schedule) (*Schedule, error) {
	res, err := c.do(http.MethodPost, "/api/schedules", sch)
	var out Schedule
	if e := c.decode(res, err, &out); e != nil {
		return nil, e
	}
	return &out, nil
}

func (c *Client) DeleteSchedule(id string) error {
	res, err := c.do(http.MethodDelete, "/api/schedules?id="+url.QueryEscape(id), nil)
	return c.decode(res, err, nil)
}

func (c *Client) ListRuns(agentID string, limit int) ([]Run, error) {
	q := "/api/runs?limit=" + strconv.Itoa(limit)
	if agentID != "" {
		q += "&agent=" + url.QueryEscape(agentID)
	}
	res, err := c.do(http.MethodGet, q, nil)
	var out []Run
	return out, c.decode(res, err, &out)
}

// ScriptByRef resolves a script by id or (case-insensitive) name.
func (c *Client) ScriptByRef(ref string) (*Script, error) {
	list, err := c.ListScripts()
	if err != nil {
		return nil, err
	}
	lref := strings.ToLower(ref)
	for i := range list {
		if list[i].ID == ref || strings.ToLower(list[i].Name) == lref {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("no script matching %q", ref)
}
