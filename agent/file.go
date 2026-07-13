package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// fileCtrl is the JSON control protocol on a file-transfer session socket.
// Binary WebSocket frames carry file bytes in either direction.
type fileCtrl struct {
	T    string `json:"t"`              // put|get (client) ; ok|meta|done|err (agent)
	Name string `json:"name,omitempty"` // basename for put/meta
	Path string `json:"path,omitempty"` // get: source path; ok: saved destination
	Size int64  `json:"size,omitempty"` // byte count for put/meta
	Msg  string `json:"msg,omitempty"`  // error text
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
			if err := recvFile(ws, wj, m.Name, m.Size); err != nil {
				wj(fileCtrl{T: "err", Msg: err.Error()})
			}
		case "get":
			if err := sendFile(wj, wb, m.Path); err != nil {
				wj(fileCtrl{T: "err", Msg: err.Error()})
			}
		}
	}
}

// recvFile reads size bytes of binary frames from ws into the incoming dir.
func recvFile(ws *websocket.Conn, wj func(any) error, name string, size int64) error {
	if size < 0 || size > 8<<30 {
		return fmt.Errorf("invalid size")
	}
	dir := incomingDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dest := filepath.Join(dir, filepath.Base(name)) // basename only — no traversal
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
