package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

// AlertConfig configures the threshold engine and notification sinks.
type AlertConfig struct {
	CPU          float64       // percent; 0 disables
	Mem          float64       // percent; 0 disables
	Disk         float64       // percent; 0 disables
	For          time.Duration // sustained duration before a resource alert fires
	OfflineAfter time.Duration // how long offline before the offline alert fires

	Webhook string // generic JSON POST
	Ntfy    string // ntfy topic URL, e.g. https://ntfy.sh/mytopic
	Discord string // Discord webhook URL
}

const alertMargin = 5.0 // clear a resource alert only once it drops this far below threshold

// Alert is a firing (or just-resolved) condition.
type Alert struct {
	AgentID string    `json:"agent_id"`
	Host    string    `json:"host"`
	Rule    string    `json:"rule"` // cpu|mem|disk|offline
	Message string    `json:"message"`
	Value   float64   `json:"value"`
	Since   time.Time `json:"since"`
	Firing  bool      `json:"firing"`
}

type alertKey struct{ agent, rule string }

type alertState struct {
	firing    bool
	trueSince time.Time // when the raw condition first became true
	since     time.Time // when it started firing
	host      string
	value     float64
}

// Alerter evaluates host telemetry against thresholds with hysteresis and
// dispatches notifications on state transitions.
type Alerter struct {
	cfg      AlertConfig
	notifier *notifier
	now      func() time.Time

	mu     sync.Mutex
	states map[alertKey]*alertState
}

// NewAlerter builds an alerter. It is inert if no thresholds are set.
func NewAlerter(cfg AlertConfig) *Alerter {
	if cfg.For <= 0 {
		cfg.For = 2 * time.Minute
	}
	if cfg.OfflineAfter <= 0 {
		cfg.OfflineAfter = time.Minute
	}
	return &Alerter{
		cfg:      cfg,
		notifier: &notifier{cfg: cfg, http: &http.Client{Timeout: 8 * time.Second}},
		now:      time.Now,
		states:   map[alertKey]*alertState{},
	}
}

// Run evaluates every 10s until ctx is cancelled.
func (a *Alerter) Run(ctx context.Context, store *Store) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, tr := range a.evaluate(store.views()) {
				a.notifier.dispatch(tr)
			}
		}
	}
}

type rule struct {
	name      string
	threshold float64
	value     float64
	active    bool
	forDur    time.Duration
	label     string
}

// evaluate updates internal state from the current views and returns the alerts
// that changed state (to be dispatched).
func (a *Alerter) evaluate(views []protocol.HostView) []Alert {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()

	var transitions []Alert
	for _, v := range views {
		for _, r := range a.rulesFor(v) {
			key := alertKey{v.AgentID, r.name}
			st := a.states[key]
			if st == nil {
				st = &alertState{}
				a.states[key] = st
			}
			st.host, st.value = v.Hostname, r.value

			// Sticky clear: stay active until the value falls a margin below.
			active := r.active
			if st.firing && r.threshold > 0 && r.value >= r.threshold-alertMargin {
				active = true
			}

			if active {
				if st.trueSince.IsZero() {
					st.trueSince = now
				}
				if !st.firing && now.Sub(st.trueSince) >= r.forDur {
					st.firing = true
					st.since = now
					transitions = append(transitions, a.mkAlert(v, r, st, true))
				}
			} else {
				if st.firing {
					st.firing = false
					transitions = append(transitions, a.mkAlert(v, r, st, false))
				}
				st.trueSince = time.Time{}
			}
		}
	}
	return transitions
}

func (a *Alerter) rulesFor(v protocol.HostView) []rule {
	var rules []rule
	// offline
	rules = append(rules, rule{name: "offline", active: !v.Online, forDur: a.cfg.OfflineAfter, label: "offline"})
	if v.Online && v.Metrics != nil {
		m := v.Metrics
		if a.cfg.CPU > 0 {
			rules = append(rules, rule{name: "cpu", threshold: a.cfg.CPU, value: m.CPUPercent, active: m.CPUPercent >= a.cfg.CPU, forDur: a.cfg.For, label: "CPU"})
		}
		if a.cfg.Mem > 0 {
			rules = append(rules, rule{name: "mem", threshold: a.cfg.Mem, value: m.MemPercent, active: m.MemPercent >= a.cfg.Mem, forDur: a.cfg.For, label: "memory"})
		}
		if a.cfg.Disk > 0 {
			var dmax float64
			for _, d := range m.Disks {
				if d.Percent > dmax {
					dmax = d.Percent
				}
			}
			rules = append(rules, rule{name: "disk", threshold: a.cfg.Disk, value: dmax, active: dmax >= a.cfg.Disk, forDur: a.cfg.For, label: "disk"})
		}
	}
	return rules
}

func (a *Alerter) mkAlert(v protocol.HostView, r rule, st *alertState, firing bool) Alert {
	host := v.Hostname
	if host == "" {
		host = v.AgentID
	}
	var msg string
	switch {
	case r.name == "offline" && firing:
		msg = fmt.Sprintf("🔴 %s is offline", host)
	case r.name == "offline":
		msg = fmt.Sprintf("✅ %s is back online", host)
	case firing:
		msg = fmt.Sprintf("🔴 %s: %s at %.0f%% (≥ %.0f%%)", host, r.label, r.value, r.threshold)
	default:
		msg = fmt.Sprintf("✅ %s: %s recovered (%.0f%%)", host, r.label, r.value)
	}
	return Alert{AgentID: v.AgentID, Host: host, Rule: r.name, Message: msg, Value: r.value, Since: st.since, Firing: firing}
}

// Active returns the currently firing alerts, newest first.
func (a *Alerter) Active() []Alert {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Alert
	for k, st := range a.states {
		if st.firing {
			out = append(out, Alert{
				AgentID: k.agent, Host: st.host, Rule: k.rule,
				Value: st.value, Since: st.since, Firing: true,
				Message: fmt.Sprintf("%s: %s", st.host, k.rule),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.After(out[j].Since) })
	return out
}

// ---- notifications ----

type notifier struct {
	cfg  AlertConfig
	http *http.Client
}

func (n *notifier) dispatch(al Alert) {
	log.Printf("alert %s: %s", al.Rule, al.Message)
	if n.cfg.Webhook != "" {
		go n.postJSON(n.cfg.Webhook, al)
	}
	if n.cfg.Discord != "" {
		go n.postJSON(n.cfg.Discord, map[string]string{"content": al.Message})
	}
	if n.cfg.Ntfy != "" {
		go n.postNtfy(al)
	}
}

func (n *notifier) postJSON(url string, body any) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := n.http.Do(req); err == nil {
		resp.Body.Close()
	}
}

func (n *notifier) postNtfy(al Alert) {
	req, err := http.NewRequest(http.MethodPost, n.cfg.Ntfy, bytes.NewReader([]byte(al.Message)))
	if err != nil {
		return
	}
	title := "autormm alert"
	prio := "default"
	if al.Firing {
		prio = "high"
	}
	req.Header.Set("Title", title)
	req.Header.Set("Priority", prio)
	if al.Firing {
		req.Header.Set("Tags", "rotating_light")
	} else {
		req.Header.Set("Tags", "white_check_mark")
	}
	if resp, err := n.http.Do(req); err == nil {
		resp.Body.Close()
	}
}
