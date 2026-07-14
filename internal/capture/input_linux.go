//go:build linux

package capture

import (
	"fmt"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// X event type codes (from the core protocol).
const (
	evKeyPress      = 2
	evKeyRelease    = 3
	evButtonPress   = 4
	evButtonRelease = 5
	evMotion        = 6
)

type x11Injector struct {
	mu   sync.Mutex
	conn *xgb.Conn
	root xproto.Window
	// keysym -> keycode, built from the server's keyboard mapping.
	k2c     map[xproto.Keysym]xproto.Keycode
	per     int            // keysyms per keycode
	spareKC xproto.Keycode // an unused keycode borrowed for Unicode typing (0 = none)
}

func newInjector() (Injector, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("capture: X connect failed (DISPLAY/XAUTHORITY set?): %w", err)
	}
	if err := xtest.Init(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("capture: XTEST extension unavailable: %w", err)
	}
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	inj := &x11Injector{conn: conn, root: screen.Root, k2c: map[xproto.Keysym]xproto.Keycode{}}
	inj.loadKeymap(setup)
	return inj, nil
}

func (in *x11Injector) loadKeymap(setup *xproto.SetupInfo) {
	lo, hi := setup.MinKeycode, setup.MaxKeycode
	count := int(hi) - int(lo) + 1
	if count <= 0 {
		return
	}
	reply, err := xproto.GetKeyboardMapping(in.conn, lo, byte(count)).Reply()
	if err != nil || reply == nil || reply.KeysymsPerKeycode == 0 {
		return
	}
	per := int(reply.KeysymsPerKeycode)
	in.per = per
	for i := 0; i < count; i++ {
		kc := xproto.Keycode(int(lo) + i)
		empty := true
		for j := 0; j < per; j++ {
			ks := reply.Keysyms[i*per+j]
			if ks == 0 {
				continue
			}
			empty = false
			if _, ok := in.k2c[ks]; !ok {
				in.k2c[ks] = kc // first (usually unshifted) mapping wins
			}
		}
		// Remember an unused keycode we can borrow for Unicode typing.
		if empty && in.spareKC == 0 {
			in.spareKC = kc
		}
	}
}

func (in *x11Injector) fake(typ, detail byte, x, y int) error {
	return xtest.FakeInputChecked(in.conn, typ, detail, xproto.TimeCurrentTime,
		in.root, int16(x), int16(y), 0).Check()
}

func (in *x11Injector) MouseMove(x, y int) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.fake(evMotion, 0, x, y)
}

func (in *x11Injector) MouseButton(button int, down bool) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	var b byte
	switch button {
	case 0:
		b = 1 // left
	case 1:
		b = 2 // middle
	case 2:
		b = 3 // right
	default:
		return nil
	}
	typ := byte(evButtonRelease)
	if down {
		typ = evButtonPress
	}
	return in.fake(typ, b, 0, 0)
}

func (in *x11Injector) Scroll(dx, dy int) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	click := func(b byte, n int) error {
		for i := 0; i < n; i++ {
			if err := in.fake(evButtonPress, b, 0, 0); err != nil {
				return err
			}
			if err := in.fake(evButtonRelease, b, 0, 0); err != nil {
				return err
			}
		}
		return nil
	}
	if dy != 0 {
		b := byte(4) // wheel up
		n := -dy
		if dy > 0 {
			b, n = 5, dy // wheel down
		}
		if err := click(b, clampSteps(n)); err != nil {
			return err
		}
	}
	if dx != 0 {
		b := byte(6) // wheel left
		n := -dx
		if dx > 0 {
			b, n = 7, dx // wheel right
		}
		if err := click(b, clampSteps(n)); err != nil {
			return err
		}
	}
	return nil
}

func (in *x11Injector) Key(code string, down bool) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	ks, ok := codeToKeysym[code]
	if !ok {
		return nil
	}
	kc, ok := in.k2c[xproto.Keysym(ks)]
	if !ok {
		return nil
	}
	typ := byte(evKeyRelease)
	if down {
		typ = evKeyPress
	}
	return in.fake(typ, byte(kc), 0, 0)
}

// TypeText injects Unicode text with no external dependency: it borrows an
// unused keycode, remaps it to each character's keysym, fakes a key press via
// XTEST, then restores the keycode. Works for any layout.
func (in *x11Injector) TypeText(text string) error {
	if text == "" {
		return nil
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.spareKC == 0 || in.per == 0 {
		return fmt.Errorf("no spare keycode available for Unicode typing")
	}
	for _, r := range text {
		ks := runeKeysym(r)
		if ks == 0 {
			continue
		}
		syms := make([]xproto.Keysym, in.per)
		syms[0] = ks // unshifted
		if in.per > 1 {
			syms[1] = ks // shifted (so modifier state doesn't matter)
		}
		if err := xproto.ChangeKeyboardMappingChecked(in.conn, 1, in.spareKC, byte(in.per), syms).Check(); err != nil {
			return err
		}
		in.sync() // let the server apply the remap before we fake the key
		in.fake(evKeyPress, byte(in.spareKC), 0, 0)
		in.fake(evKeyRelease, byte(in.spareKC), 0, 0)
		in.sync()
	}
	// Restore the borrowed keycode to NoSymbol.
	xproto.ChangeKeyboardMapping(in.conn, 1, in.spareKC, byte(in.per), make([]xproto.Keysym, in.per))
	in.sync()
	return nil
}

// sync round-trips so queued requests (e.g. the keymap change) are applied.
func (in *x11Injector) sync() { xproto.GetInputFocus(in.conn).Reply() }

// runeKeysym maps a rune to an X keysym: Latin-1 codepoints are their own
// keysym; everything else uses the Unicode keysym range (0x01000000 + cp).
func runeKeysym(r rune) xproto.Keysym {
	switch {
	case r == 0:
		return 0
	case r >= 0x20 && r <= 0xff:
		return xproto.Keysym(r)
	default:
		return xproto.Keysym(0x01000000 + uint32(r))
	}
}

func (in *x11Injector) Close() error {
	in.conn.Close()
	return nil
}

func clampSteps(n int) int {
	if n < 1 {
		return 1
	}
	if n > 10 {
		return 10
	}
	return n
}
