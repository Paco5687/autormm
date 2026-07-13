//go:build linux

package capture

import (
	"fmt"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

type x11Cursor struct {
	conn *xgb.Conn
	root xproto.Window
}

func newCursor() (Cursor, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("cursor: X connect failed: %w", err)
	}
	root := xproto.Setup(conn).DefaultScreen(conn).Root
	return &x11Cursor{conn: conn, root: root}, nil
}

func (c *x11Cursor) Pos() (int, int, bool, bool) {
	r, err := xproto.QueryPointer(c.conn, c.root).Reply()
	if err != nil || r == nil {
		return 0, 0, false, false
	}
	// SameScreen is false when the pointer is on a different screen/root.
	return int(r.RootX), int(r.RootY), r.SameScreen, true
}

func (c *x11Cursor) Close() error {
	c.conn.Close()
	return nil
}
