package main

import (
	"net/http"
	"strings"
	"testing"
)

// resetDemoDoc persists an EMPTY document for the demo docID so a test starts
// from a blank editor. Needed since the demo page began seeding a welcome/
// feature-tour document on first-ever load — tests that type into a "fresh"
// editor must opt back into emptiness explicitly (the Playwright suite does
// the same in its beforeEach).
func resetDemoDoc(t *testing.T, baseURL string) {
	t.Helper()
	payload := `{"docId":"demo","doc":{"type":"doc","content":[{"type":"paragraph"}]},"markdown":"","schemaVersion":"richtext-v1"}`
	resp, err := http.Post(baseURL+"/__gofastr/plugin/richtext/save", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("reset demo doc: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset demo doc: status %d", resp.StatusCode)
	}
}
