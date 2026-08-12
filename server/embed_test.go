package server

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// Every /static/ asset the dashboard references must be embedded.
//
// A new file under server/web/ is picked up by //go:embed automatically, so the
// failure this guards is the other direction: a script or stylesheet referenced
// by a path that does not exist. It is served as a 404, the browser says nothing
// the operator will see, and the feature is simply missing — which is how a
// whole panel can ship dead.
//
// Derived from the markup rather than a hand-kept list, because a hand-kept
// list is the thing that goes stale.
var assetRef = regexp.MustCompile(`(?:src|href)="(/static/[^"]+)"`)

func TestEveryReferencedAssetIsEmbedded(t *testing.T) {
	pages := []string{"web/index.html", "web/viewer.html", "web/terminal.html"}
	checked := 0
	for _, page := range pages {
		b, err := fs.ReadFile(webFS, page)
		if err != nil {
			t.Errorf("%s is not embedded: %v", page, err)
			continue
		}
		for _, m := range assetRef.FindAllStringSubmatch(string(b), -1) {
			ref := m[1]
			// Strip any cache-busting query before looking it up on disk.
			if i := strings.IndexByte(ref, '?'); i >= 0 {
				ref = ref[:i]
			}
			name := "web/" + strings.TrimPrefix(ref, "/static/")
			if _, err := fs.Stat(webFS, name); err != nil {
				t.Errorf("%s references %s, which is not embedded", page, m[1])
			}
			checked++
		}
	}
	// If the regex ever stops matching, this test would pass by checking
	// nothing at all.
	if checked < 5 {
		t.Errorf("only %d asset references found; the check is not looking at anything", checked)
	}
}
