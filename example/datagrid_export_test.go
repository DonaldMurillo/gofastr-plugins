package main

// Pins the demo export store's bounds: at most demoGridMaxExports files,
// FIFO-evicted, and a per-file byte cap that refuses (never truncates) an
// oversized export — a short CSV that looks complete is the exact failure
// mode the plugin's export path exists to avoid.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DonaldMurillo/gofastr-plugins/datagrid"
)

func resetDemoGridExports() {
	demoGridExports.mu.Lock()
	demoGridExports.byID = nil
	demoGridExports.order = nil
	demoGridExports.mu.Unlock()
}

func demoExportFor(t *testing.T, content string) string {
	t.Helper()
	url, err := demoGridExport(context.Background(), datagrid.ExportRequest{
		Format: "csv",
		CSV:    strings.NewReader(content),
	})
	if err != nil {
		t.Fatalf("demoGridExport: %v", err)
	}
	return url
}

func TestDemoGridExportStoreEvictsOldest(t *testing.T) {
	resetDemoGridExports()
	defer resetDemoGridExports()

	urls := make([]string, 0, demoGridMaxExports+1)
	for i := range demoGridMaxExports + 1 {
		urls = append(urls, demoExportFor(t, fmt.Sprintf("id,amount\n%d,1.0\n", i)))
	}
	demoGridExports.mu.Lock()
	stored := len(demoGridExports.byID)
	_, oldestGone := demoGridExports.byID[strings.TrimPrefix(urls[0], "/datagrid/exported/")]
	_, newestKept := demoGridExports.byID[strings.TrimPrefix(urls[len(urls)-1], "/datagrid/exported/")]
	demoGridExports.mu.Unlock()
	if stored != demoGridMaxExports {
		t.Fatalf("store holds %d exports, want the cap %d", stored, demoGridMaxExports)
	}
	if oldestGone {
		t.Error("the oldest export was not evicted after exceeding the cap")
	}
	if !newestKept {
		t.Error("the newest export is missing from the store")
	}
}

func TestDemoGridExportStoreDedupesAndCapsSize(t *testing.T) {
	resetDemoGridExports()
	defer resetDemoGridExports()

	a := demoExportFor(t, "id\n1\n")
	b := demoExportFor(t, "id\n1\n")
	if a != b {
		t.Fatalf("identical content stored twice: %q vs %q", a, b)
	}

	huge := strings.Repeat("x", demoGridMaxExportBytes+1)
	if _, err := demoGridExport(context.Background(), datagrid.ExportRequest{
		Format: "csv",
		CSV:    strings.NewReader(huge),
	}); err == nil {
		t.Fatal("oversized export accepted, want a loud refusal")
	}
	// Exactly at the cap is fine.
	if _, err := demoGridExport(context.Background(), datagrid.ExportRequest{
		Format: "csv",
		CSV:    strings.NewReader(strings.Repeat("x", demoGridMaxExportBytes)),
	}); err != nil {
		t.Fatalf("export at the cap refused: %v", err)
	}
}
