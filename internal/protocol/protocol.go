// Package protocol defines the wire types shared by the autormm server, agent
// and client. Control traffic is JSON; screen media is a compact binary frame
// format (see frame.go).
package protocol

import "time"

// Control-channel message types (JSON text frames).
const (
	// agent -> server
	TypeRegister = "register" // first message on the agent control socket
	TypeMetrics  = "metrics"  // periodic host telemetry
	TypePong     = "pong"

	// server -> agent
	TypeStartSession = "start_session" // ask the agent to open a media socket
	TypeStopSession  = "stop_session"
	TypePing         = "ping"
	TypeExec         = "exec"       // run a command on the host
	TypeUpdateNow    = "update_now" // ask the agent to check the hub for an update now

	TypeInventory   = "inventory"    // server -> agent: list installed software
	TypeProcRestart = "proc_restart" // server -> agent: stop a process and relaunch its command line

	// agent -> server (command execution)
	TypeExecOut       = "exec_out"       // a chunk of command output
	TypeExecDone      = "exec_done"      // command finished
	TypeInventoryResp = "inventory_resp" // installed-software listing
)

// ProcRestartRequest asks the agent to restart a process: capture its command
// line + working dir, stop it, and relaunch. The result comes back as an
// ExecDone with the same ExecID (ExitCode 0 = success).
type ProcRestartRequest struct {
	Type   string `json:"type"` // TypeProcRestart
	ExecID string `json:"exec_id"`
	PID    int    `json:"pid"`
}

// InventoryRequest asks the agent to enumerate installed software.
type InventoryRequest struct {
	Type  string `json:"type"` // TypeInventory
	ReqID string `json:"req_id"`
}

// Package is one installed software item.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InventoryResponse carries the agent's software listing.
type InventoryResponse struct {
	Type     string    `json:"type"` // TypeInventoryResp
	ReqID    string    `json:"req_id"`
	Source   string    `json:"source"` // dpkg|rpm|windows|brew
	Packages []Package `json:"packages"`
	Err      string    `json:"err,omitempty"`
}

// ExecRequest asks the agent to run a command. Shell is "sh", "bash",
// "powershell", or "cmd"; empty picks the OS default.
type ExecRequest struct {
	Type        string `json:"type"` // TypeExec
	ExecID      string `json:"exec_id"`
	Command     string `json:"command"`
	Shell       string `json:"shell,omitempty"`
	TimeoutSecs int    `json:"timeout_secs,omitempty"`
}

// ExecOutput is one chunk of stdout/stderr from a running command.
type ExecOutput struct {
	Type   string `json:"type"` // TypeExecOut
	ExecID string `json:"exec_id"`
	Stream string `json:"stream"` // "stdout" | "stderr"
	Data   string `json:"data"`
}

// ExecDone reports a command's completion.
type ExecDone struct {
	Type     string `json:"type"` // TypeExecDone
	ExecID   string `json:"exec_id"`
	ExitCode int    `json:"exit_code"`
	Err      string `json:"err,omitempty"`
}

// Envelope is used to peek at the "type" field before decoding the full body.
type Envelope struct {
	Type string `json:"type"`
}

// Register is the agent's hello. It identifies the host and its capabilities.
type Register struct {
	Type         string    `json:"type"` // TypeRegister
	AgentID      string    `json:"agent_id"`
	Hostname     string    `json:"hostname"`
	OS           string    `json:"os"`       // "linux", "windows", ...
	Platform     string    `json:"platform"` // "ubuntu 24.04", "Windows 11", ...
	Arch         string    `json:"arch"`
	AgentVersion string    `json:"agent_version"`
	CanStream    bool      `json:"can_stream"`             // screen capture available on this host
	CanExec      bool      `json:"can_exec"`               // remote command execution enabled
	EncoderCaps  []string  `json:"encoder_caps,omitempty"` // video codecs this agent can produce
	Facts        HostFacts `json:"facts,omitempty"`        // static device info
	Tags         string    `json:"tags,omitempty"`
}

// HostFacts are relatively static host details collected once at registration
// and shown in the dashboard's device info.
type HostFacts struct {
	KernelVersion  string   `json:"kernel_version,omitempty"`
	CPUModel       string   `json:"cpu_model,omitempty"`
	CPUCores       int      `json:"cpu_cores,omitempty"`
	MemTotal       uint64   `json:"mem_total,omitempty"`
	IPs            []string `json:"ips,omitempty"`
	MACs           []string `json:"macs,omitempty"`
	Virtualization string   `json:"virtualization,omitempty"` // "kvm", "vmware", … or "" for bare metal
}

// Remote-desktop codec capability strings (negotiated per session).
const (
	CapJPEGTile = "jpeg-tile"      // the always-available tiled-JPEG fallback
	CapH264     = "webcodecs-h264" // H.264 for WebCodecs VideoDecoder
)

// MediaCodec is the 1-byte tag prefixed to every binary media message so the
// viewer knows how to decode it (and so the codec can change mid-session).
type MediaCodec byte

const (
	MediaJPEGTile MediaCodec = 0
	MediaH264     MediaCodec = 1
)

// WrapMedia prefixes a payload with its codec tag.
func WrapMedia(codec MediaCodec, payload []byte) []byte {
	return append([]byte{byte(codec)}, payload...)
}

// UnwrapMedia splits the codec tag from a media message.
func UnwrapMedia(msg []byte) (MediaCodec, []byte, bool) {
	if len(msg) == 0 {
		return 0, nil, false
	}
	return MediaCodec(msg[0]), msg[1:], true
}

// Session kinds carried by SessionRequest / StartSession.
const (
	SessionScreen   = "screen"   // remote-desktop screen streaming (default)
	SessionTerminal = "terminal" // interactive PTY shell
	SessionFile     = "file"     // file upload/download to the host
)

// StartSession tells the agent to dial the media endpoint for a remote-desktop
// or terminal session. Token authorises the media socket.
type StartSession struct {
	Type    string `json:"type"` // TypeStartSession
	Session string `json:"session"`
	Token   string `json:"token"`
	Kind    string `json:"kind,omitempty"`  // SessionScreen | SessionTerminal
	Codec   string `json:"codec,omitempty"` // negotiated video codec (CapJPEGTile | CapH264)
	FPS     int    `json:"fps"`
	Quality int    `json:"quality"` // JPEG quality 1-100
}

// CapsMsg is sent from the agent to the viewer at session start listing the
// video codecs this host can produce, so the viewer can offer them.
type CapsMsg struct {
	T      string   `json:"t"` // always "caps"
	Codecs []string `json:"codecs"`
}

// Mode is a selectable display resolution.
type Mode struct {
	W int `json:"w"`
	H int `json:"h"`
}

// Display describes one monitor on a host.
type Display struct {
	Index   int    `json:"index"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	W       int    `json:"w"`
	H       int    `json:"h"`
	Primary bool   `json:"primary"`
	Modes   []Mode `json:"modes,omitempty"` // selectable resolutions for this display
}

// DisplaysMsg is sent from the agent to the viewer at session start (text frame
// on the media socket) describing the available displays and which is selected.
// Current is -1 when the whole desktop (all displays) is being captured.
type DisplaysMsg struct {
	T       string    `json:"t"` // always "displays"
	List    []Display `json:"list"`
	Current int       `json:"current"`
}

// CursorMsg is sent from the agent to the viewer (text frame on the media
// socket) with the host pointer position, so the viewer can draw it as an
// overlay. Coordinates are absolute pixels in the captured screen's resolution.
type CursorMsg struct {
	T   string `json:"t"` // always "cursor"
	X   int    `json:"x"`
	Y   int    `json:"y"`
	Vis bool   `json:"vis"`
}

// TermMsg is the terminal media protocol (viewer/CLI <-> agent). Output from
// the agent is sent as raw binary frames; these JSON messages carry input and
// resize events from the client.
type TermMsg struct {
	T    string `json:"t"`           // "in" | "resize"
	D    string `json:"d,omitempty"` // input bytes for T=="in"
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// StopSession asks the agent to tear a session down.
type StopSession struct {
	Type    string `json:"type"` // TypeStopSession
	Session string `json:"session"`
}

// Simple keepalive.
type Ping struct {
	Type string `json:"type"`
}
type Pong struct {
	Type string `json:"type"`
}

// Metrics is a point-in-time snapshot of a host, pushed periodically by the
// agent and also carried inside HostView for the dashboard.
type Metrics struct {
	Type       string     `json:"type,omitempty"` // TypeMetrics on the wire
	Timestamp  time.Time  `json:"timestamp"`
	UptimeSecs uint64     `json:"uptime_secs"`
	CPUPercent float64    `json:"cpu_percent"`
	CPUCores   int        `json:"cpu_cores"`
	Load1      float64    `json:"load1"`
	Load5      float64    `json:"load5"`
	Load15     float64    `json:"load15"`
	MemTotal   uint64     `json:"mem_total"`
	MemUsed    uint64     `json:"mem_used"`
	MemPercent float64    `json:"mem_percent"`
	SwapTotal  uint64     `json:"swap_total"`
	SwapUsed   uint64     `json:"swap_used"`
	Disks      []Disk     `json:"disks"`
	NetRecv    uint64     `json:"net_recv"` // bytes/sec since last sample
	NetSent    uint64     `json:"net_sent"`
	Procs      []ProcInfo `json:"procs"` // top processes by CPU
	Users      []string   `json:"users,omitempty"`
}

type Disk struct {
	Mount   string  `json:"mount"`
	FSType  string  `json:"fstype"`
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Percent float64 `json:"percent"`
}

type ProcInfo struct {
	PID    int32   `json:"pid"`
	Name   string  `json:"name"`
	CPU    float64 `json:"cpu"`
	MemRSS uint64  `json:"mem_rss"`
}

// ---- REST DTOs (client <-> server) ----

// HostView is what the dashboard/CLI sees for one host.
type HostView struct {
	AgentID      string    `json:"agent_id"`
	Hostname     string    `json:"hostname"`
	OS           string    `json:"os"`
	Platform     string    `json:"platform"`
	Arch         string    `json:"arch"`
	AgentVersion string    `json:"agent_version"`
	CanStream    bool      `json:"can_stream"`
	CanExec      bool      `json:"can_exec"`
	Tags         string    `json:"tags,omitempty"`
	Online       bool      `json:"online"`
	LastSeen     time.Time `json:"last_seen"`
	Facts        HostFacts `json:"facts,omitempty"`
	Metrics      *Metrics  `json:"metrics,omitempty"`
	Alerts       []string  `json:"alerts,omitempty"`
	CPUHistory   []float64 `json:"cpu_history,omitempty"`
	MemHistory   []float64 `json:"mem_history,omitempty"`
}

// SessionRequest is POSTed by a client to start a remote-desktop or terminal
// session.
type SessionRequest struct {
	AgentID string `json:"agent_id"`
	Kind    string `json:"kind,omitempty"` // SessionScreen (default) | SessionTerminal
	FPS     int    `json:"fps,omitempty"`
	Quality int    `json:"quality,omitempty"`
}

// SessionResponse hands back a short-lived ticket the viewer uses to open the
// media socket.
type SessionResponse struct {
	Session string `json:"session"`
	Token   string `json:"token"`
	WSPath  string `json:"ws_path"`
}

// ---- Input events (viewer -> agent, JSON text frames on the media socket) ----

const (
	InputMouseMove = "mmove"
	InputMouseDown = "mdown"
	InputMouseUp   = "mup"
	InputScroll    = "scroll"
	InputKeyDown   = "kdown"
	InputKeyUp     = "kup"
	InputSetParams = "params"  // change fps/quality mid-session
	InputDisplay   = "display" // switch the captured display
	InputSetCodec  = "codec"   // switch the video codec mid-session
	InputClipboard = "clip"    // viewer -> host: set the host clipboard (text)
	InputSetRes    = "setres"  // viewer -> host: change a display's resolution
	InputType      = "type"    // viewer -> host: type a string (Unicode, e.g. from a soft keyboard)
)

// ClipMsg carries clipboard text from the host to the viewer (text frame on the
// media socket) when the host clipboard changes, so copy/paste works both ways.
type ClipMsg struct {
	T string `json:"t"` // always "clip"
	D string `json:"d"` // clipboard text
}

// InputEvent is sent from the viewer to the agent. Coordinates are absolute
// pixels in the remote screen's resolution.
type InputEvent struct {
	T       string `json:"t"`
	X       int    `json:"x,omitempty"`
	Y       int    `json:"y,omitempty"`
	Button  int    `json:"button,omitempty"` // 0=left 1=middle 2=right
	DX      int    `json:"dx,omitempty"`
	DY      int    `json:"dy,omitempty"`
	Code    string `json:"code,omitempty"` // JS KeyboardEvent.code, e.g. "KeyA","Enter"
	FPS     int    `json:"fps,omitempty"`
	Quality int    `json:"quality,omitempty"`
	Display int    `json:"display,omitempty"` // for InputDisplay: -1 all, 0..N-1 one
	Codec   string `json:"codec,omitempty"`   // for InputSetCodec: CapJPEGTile | CapH264
	Clip    string `json:"clip,omitempty"`    // for InputClipboard: text to set on the host
	W       int    `json:"w,omitempty"`       // for InputSetRes: target width
	H       int    `json:"h,omitempty"`       // for InputSetRes: target height
	Text    string `json:"text,omitempty"`    // for InputType: Unicode text to type
}
