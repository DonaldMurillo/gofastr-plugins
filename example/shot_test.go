package main

import (
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestDogfoodScreenshots captures the editor (light + dark) and the mermaid
// plugin to PNGs for a visual dogfood pass. Opt-in via SHOTS=1 so it doesn't run
// in the normal suite. Output dir from SHOTS_DIR.
func TestDogfoodScreenshots(t *testing.T) {
	if os.Getenv("SHOTS") == "" {
		t.Skip("set SHOTS=1 to capture dogfood screenshots")
	}
	dir := os.Getenv("SHOTS_DIR")
	if dir == "" {
		dir = "."
	}
	app, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Router())
	defer srv.Close()
	ctx, cancel := newChrome(t)
	defer cancel()

	shots := []struct {
		name, url, prep string
	}{
		{"editor-light.png", "/", ""},
		{"editor-dark.png", "/", `document.getElementById('fui-scheme-toggle').click()`},
		{"mermaid-light.png", "/mermaid", ""},
	}
	for _, s := range shots {
		var buf []byte
		tasks := chromedp.Tasks{
			chromedp.Navigate(srv.URL + s.url),
			chromedp.Sleep(3500 * time.Millisecond),
		}
		if s.prep != "" {
			tasks = append(tasks, chromedp.Evaluate(s.prep, nil), chromedp.Sleep(1200*time.Millisecond))
		}
		tasks = append(tasks, chromedp.FullScreenshot(&buf, 90))
		if err := chromedp.Run(ctx, tasks); err != nil {
			t.Errorf("%s: %v", s.name, err)
			continue
		}
		if err := os.WriteFile(dir+"/"+s.name, buf, 0o644); err != nil {
			t.Errorf("write %s: %v", s.name, err)
			continue
		}
		t.Logf("wrote %s/%s (%d bytes)", dir, s.name, len(buf))
	}
}
