//go:build linux

package capture

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/kbinani/screenshot"

	"github.com/Paco5687/autormm/internal/protocol"
)

func xrandrPath() string {
	p, _ := exec.LookPath("xrandr")
	return p
}

var connectedRe = regexp.MustCompile(`^(\S+) connected\D*(\d+)x(\d+)\+(\d+)\+(\d+)`)

// outputForDisplay maps a display index to its xrandr output name by matching
// the current geometry offset (+X+Y).
func outputForDisplay(index int, query string) (string, bool) {
	b := screenshot.GetDisplayBounds(index)
	wantX, wantY := strconv.Itoa(b.Min.X), strconv.Itoa(b.Min.Y)
	for _, line := range strings.Split(query, "\n") {
		m := connectedRe.FindStringSubmatch(line)
		if m != nil && m[4] == wantX && m[5] == wantY {
			return m[1], true
		}
	}
	return "", false
}

func xrandrQuery() (string, bool) {
	x := xrandrPath()
	if x == "" {
		return "", false
	}
	out, err := exec.Command(x, "--query").Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

var modeLineRe = regexp.MustCompile(`^\s+(\d+)x(\d+)\s`)

func displayModes(index int) []protocol.Mode {
	q, ok := xrandrQuery()
	if !ok {
		return nil
	}
	name, ok := outputForDisplay(index, q)
	if !ok {
		return nil
	}
	var modes []protocol.Mode
	inBlock := false
	for _, line := range strings.Split(q, "\n") {
		if strings.HasPrefix(line, name+" ") {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			break // reached the next output's block
		}
		if m := modeLineRe.FindStringSubmatch(line); m != nil {
			w, _ := strconv.Atoi(m[1])
			h, _ := strconv.Atoi(m[2])
			modes = append(modes, protocol.Mode{W: w, H: h})
		}
	}
	return dedupeSortModes(modes)
}

func setDisplayMode(index, w, h int) error {
	x := xrandrPath()
	if x == "" {
		return errUnsupportedRes
	}
	q, ok := xrandrQuery()
	if !ok {
		return errUnsupportedRes
	}
	name, ok := outputForDisplay(index, q)
	if !ok {
		return errUnsupportedRes
	}
	return exec.Command(x, "--output", name, "--mode", strconv.Itoa(w)+"x"+strconv.Itoa(h)).Run()
}
