package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// fileCtrl is the JSON control protocol on a file-transfer session socket.
// Binary WebSocket frames carry file bytes in either direction.
type fileCtrl struct {
	T    string `json:"t"`              // put|get|ls (client) ; ok|meta|done|err|list (agent)
	Name string `json:"name,omitempty"` // basename for put/meta
	Path string `json:"path,omitempty"` // get/ls: target path; ok: saved destination; list: resolved directory
	Dir  string `json:"dir,omitempty"`  // put: destination directory ("" = autormm-incoming)
	Size int64  `json:"size,omitempty"` // byte count for put/meta
	Msg  string `json:"msg,omitempty"`  // error text

	// list reply
	Parent  string      `json:"parent,omitempty"`
	Entries []fileEntry `json:"entries,omitempty"`
}

// fileEntry is one row of a directory listing.
type fileEntry struct {
	Name string `json:"name"`
	Size int64  `json:"size,omitempty"`
	Dir  bool   `json:"dir,omitempty"`
	Mod  int64  `json:"mod,omitempty"` // unix seconds
}

// incomingDir is where uploaded files land on the host.
func incomingDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, "autormm-incoming")
}

// runFileSession serves file uploads (put) and downloads (get) over ws. Operations
// are sequential; each put is followed by its binary frames, each get streams the
// file back as binary frames between meta and done.
func (a *Agent) runFileSession(ctx context.Context, ws *websocket.Conn) {
	ws.SetReadLimit(16 << 20)
	var wmu sync.Mutex
	wj := func(v any) error {
		wmu.Lock()
		defer wmu.Unlock()
		ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
		return ws.WriteJSON(v)
	}
	wb := func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
		return ws.WriteMessage(websocket.BinaryMessage, b)
	}

	for {
		if ctx.Err() != nil {
			return
		}
		ws.SetReadDeadline(time.Now().Add(5 * time.Minute))
		mt, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		var m fileCtrl
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.T {
		case "put":
			if err := recvFile(ws, wj, m.Name, m.Size, m.Dir); err != nil {
				wj(fileCtrl{T: "err", Msg: err.Error()})
			}
		case "get":
			if err := sendFile(wj, wb, m.Path); err != nil {
				wj(fileCtrl{T: "err", Msg: err.Error()})
			}
		case "ls":
			if err := listDir(wj, m.Path); err != nil {
				wj(fileCtrl{T: "err", Msg: err.Error()})
			}
		}
	}
}

// recvFile reads size bytes of binary frames from ws into destDir, or the
// incoming dir when none is given.
//
// The destination directory must already exist: the browser only offers
// directories it has listed, so a missing one means a typo or a race, and
// silently creating whatever arrives would let a stray path scatter folders
// around the host.
func recvFile(ws *websocket.Conn, wj func(any) error, name string, size int64, destDir string) error {
	if size < 0 || size > 8<<30 {
		return fmt.Errorf("invalid size")
	}
	dir := destDir
	if dir == "" {
		dir = incomingDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	} else if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a directory on this host", dir)
	}
	dest := filepath.Join(dir, filepath.Base(name)) // basename only -- no traversal
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	var got int64
	for got < size {
		ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		mt, data, err := ws.ReadMessage()
		if err != nil {
			os.Remove(dest)
			return err
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		if _, err := f.Write(data); err != nil {
			return err
		}
		got += int64(len(data))
	}
	return wj(fileCtrl{T: "ok", Path: dest, Name: filepath.Base(dest), Size: got})
}

// sendFile streams path back to the viewer as meta + binary frames + done.
func sendFile(wj func(any) error, wb func([]byte) error, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if err := wj(fileCtrl{T: "meta", Name: filepath.Base(path), Size: st.Size()}); err != nil {
		return err
	}
	buf := make([]byte, 256<<10)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if e := wb(buf[:n]); e != nil {
				return e
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return wj(fileCtrl{T: "done"})
}

// listDir replies with the entries of a directory. An empty path lists the
// host user's home, which is where a browse naturally starts.
//
// This deliberately allows any path the agent's user can read: it is the same
// boundary as the terminal and the existing download-by-path, both gated by
// the host's --allow-exec setting. What it does *not* do is follow the listing
// into unreadable territory silently — errors come back as errors.
func listDir(wj func(any) error, path string) error {
	if path == "" || path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = home
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		return err
	}

	// Bounded: a directory with a million files (node_modules, a mail spool)
	// must not become a hundred-megabyte JSON frame. The UI says when the
	// listing was cut.
	const maxEntries = 2000
	list := make([]fileEntry, 0, min(len(ents), maxEntries))
	truncated := false
	for _, e := range ents {
		if len(list) >= maxEntries {
			truncated = true
			break
		}
		fe := fileEntry{Name: e.Name(), Dir: e.IsDir()}
		if info, err := e.Info(); err == nil {
			if !fe.Dir {
				fe.Size = info.Size()
			}
			fe.Mod = info.ModTime().Unix()
		}
		list = append(list, fe)
	}
	// Directories first, then files, each alphabetically — the order every
	// file manager uses, because it is the order people scan.
	sort.Slice(list, func(i, j int) bool {
		if list[i].Dir != list[j].Dir {
			return list[i].Dir
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})

	parent := filepath.Dir(abs)
	if parent == abs {
		parent = "" // filesystem root: nowhere further up
	}
	reply := fileCtrl{T: "list", Path: abs, Parent: parent, Entries: list}
	if truncated {
		reply.Msg = fmt.Sprintf("showing first %d entries", maxEntries)
	}
	return wj(reply)
}
