package main

// The datagrid demo's data layer: 100,000 rows generated deterministically in
// Go (fixed formula, no database, no network), plus the cell-write overlay
// and the one-shot export store. Deterministic on purpose — the e2e journey
// recomputes the same formulas in TypeScript and asserts exact cell values at
// a known row index (row 50,000), which only works if row N is a pure
// function of N.
//
// The formulas (mirrored in e2e/tests/datagrid-journeys.spec.ts — keep in
// sync):
//
//	id     = "ROW-%06d" % n
//	name   = first[n%16] + " " + last[(n/16)%16]
//	email  = "user%d@example.com" % n
//	company= companies[n%32]
//	city   = cities[n%24]
//	amount = "%d.%d" % ((n*7919)%9973 / 10, (n*7919)%9973 % 10)
//	status = statuses[n%4]

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/DonaldMurillo/gofastr-plugins/datagrid"
)

const demoGridRows = 100_000

var (
	demoGridFirst = []string{
		"Alice", "Bruno", "Carmen", "Dara", "Eli", "Farah", "Gustav", "Hana",
		"Ivan", "Jia", "Kofi", "Lena", "Milo", "Nadia", "Omar", "Priya",
	}
	demoGridLast = []string{
		"Alvarez", "Bishop", "Chen", "Duarte", "Eriksen", "Fontaine", "Gruber", "Haddad",
		"Iversen", "Jimenez", "Kowalski", "Lindqvist", "Moreau", "Nakamura", "Okafor", "Petrov",
	}
	demoGridCompanies = []string{
		"Northwind", "Initech", "Acme Corp", "Globex", "Umbrella", "Stark Industries", "Wayne Enterprises",
		"Soylent", "Hooli", "Pied Piper", "Duffin", "Vandelay", "Sterling Cooper", "Massive Dynamic",
		"Tyrell", "Cyberdyne", "Oceanic", "Wonka", "Gekko", "Nakatomi", "Dolores", "Aperture",
		"Abstergo", "Veidt", "Dunder", "Paper", "Prestige", "Greedo", "Luthor", "Gringotts",
		"Flowers", "Blue Sun",
	}
	demoGridCities = []string{
		"Lisbon", "Osaka", "Krakow", "Bogota", "Hanoi", "Cairo", "Tallinn", "Perth",
		"Toronto", "Nairobi", "Oslo", "Quito", "Riga", "Seville", "Taipei", "Valpo",
		"Windhoek", "Yerevan", "Zagreb", "Bergen", "Cusco", "Dakar", "Essaouira", "Faro",
	}
	demoGridStatuses = []string{"active", "pending", "blocked", "expired"}
)

// demoGridDoc is the view state mounted on the demo page before any save:
// the column schema + page size. Editing (data:write) is on for name and
// status so the gallery exercises the whole surface.
func demoGridDoc() datagrid.Doc {
	return datagrid.Doc{
		SchemaVersion: datagrid.SchemaVersion,
		Columns: []datagrid.Column{
		{Field: "id", Header: "ID", Width: 128, Sortable: true},
		{Field: "name", Header: "Name", Width: 160, Sortable: true, Editable: true},
		{Field: "email", Header: "Email", Width: 210},
		{Field: "company", Header: "Company", Width: 150, Sortable: true},
		{Field: "city", Header: "City", Width: 112, Sortable: true},
		{Field: "amount", Header: "Amount", Width: 96, Type: "number", Sortable: true},
		{Field: "status", Header: "Status", Width: 104, Editable: true},
		},
		PageSize: 100,
	}
}

// demoDataset is the plugin's data layer: a deterministic generator plus an
// in-memory cell-edit overlay. Reads sort/filter/page server-side (this is
// the code the frame can never run); writes land in the overlay so an edit
// survives a reload — the demo's persistence story, mirroring what a real
// app does with its database.
type demoDataset struct {
	mu    sync.RWMutex
	edits map[string]map[string]string // rowID → field → value
}

// row builds row n purely from the formulas, then applies any edit overlay.
func (d *demoDataset) row(n int) datagrid.Row {
	t := (n * 7919) % 9973
	id := fmt.Sprintf("ROW-%06d", n)
	cells := map[string]string{
		"id":      id,
		"name":    demoGridFirst[n%16] + " " + demoGridLast[(n/16)%16],
		"email":   fmt.Sprintf("user%d@example.com", n),
		"company": demoGridCompanies[n%32],
		"city":    demoGridCities[n%24],
		"amount":  fmt.Sprintf("%d.%d", t/10, t%10),
		"status":  demoGridStatuses[n%4],
	}
	d.mu.RLock()
	if byField, ok := d.edits[id]; ok {
		for k, v := range byField {
			cells[k] = v
		}
	}
	d.mu.RUnlock()
	return datagrid.Row{ID: id, Cells: cells}
}

// rows is the plugin's WithRowsSource: filter → sort → page, all here.
func (d *demoDataset) rows(_ context.Context, q datagrid.RowsQuery) (datagrid.RowsPage, error) {
	numeric := map[string]bool{}
	for _, c := range q.Columns {
		if c.Type == "number" {
			numeric[c.Field] = true
		}
	}
	filter := strings.ToLower(q.Filter)
	idx := make([]int, 0, demoGridRows)
	for n := range demoGridRows {
		if filter != "" {
			row := d.row(n)
			hit := false
			for _, v := range row.Cells {
				if strings.Contains(strings.ToLower(v), filter) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		idx = append(idx, n)
	}
	// Stable chained sort: cmp == 0 on equal keys is what makes the chain
	// work — the NEXT key (and ultimately the stable insertion order)
	// decides ties. Returning non-zero for equals silently disables every
	// secondary sort key.
	sort.SliceStable(idx, func(a, b int) bool {
		ra, rb := d.row(idx[a]), d.row(idx[b])
		for _, s := range q.Sort {
			va, vb := ra.Cells[s.Field], rb.Cells[s.Field]
			var cmp int
			if numeric[s.Field] {
				fa, _ := strconv.ParseFloat(va, 64)
				fb, _ := strconv.ParseFloat(vb, 64)
				switch {
				case fa < fb:
					cmp = -1
				case fa > fb:
					cmp = 1
				default:
					cmp = 0
				}
			} else {
				cmp = strings.Compare(va, vb)
			}
			if s.Dir == "desc" {
				cmp = -cmp
			}
			if cmp != 0 {
				return cmp < 0
			}
		}
		return false
	})
	lastRow := len(idx)
	if q.StartRow >= len(idx) {
		return datagrid.RowsPage{Rows: []datagrid.Row{}, LastRow: lastRow}, nil
	}
	end := q.EndRow
	if end > len(idx) {
		end = len(idx)
	}
	page := make([]datagrid.Row, 0, end-q.StartRow)
	for _, n := range idx[q.StartRow:end] {
		page = append(page, d.row(n))
	}
	return datagrid.RowsPage{Rows: page, LastRow: lastRow}, nil
}

// writeCell is the plugin's WithCellWriteHandler. NOTE (docs/datagrid.md):
// pluginhost.Allow is a capability gate, NOT authentication — it passes for
// anonymous callers. This demo runs unauthenticated with WithDevGrantAll, so
// the overlay accepts everything; a production host checks the session HERE
// before persisting.
func (d *demoDataset) writeCell(_ context.Context, req datagrid.CellWriteRequest) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.edits == nil {
		d.edits = map[string]map[string]string{}
	}
	if d.edits[req.RowID] == nil {
		d.edits[req.RowID] = map[string]string{}
	}
	d.edits[req.RowID][req.Field] = req.Value
	return nil
}

// demoGridDataset is the single shared instance behind the demo routes (the
// mount and the export store must see the same overlay).
var demoGridDataset = &demoDataset{}

// The demo export store is bounded on both axes, or a gallery left running
// would grow forever: at most demoGridMaxExports files (FIFO-evicted) and at
// most demoGridMaxExportBytes each (a bigger export is refused loudly, never
// truncated — a short CSV that looks complete is the failure mode the plugin
// exists to avoid).
const (
	demoGridMaxExports     = 8
	demoGridMaxExportBytes = 32 << 20 // 32 MiB — the 100k-row demo CSV is ~6 MiB
)

// demoGridExport is the example app's datagrid.WithExportHandler: it streams
// the CSV the plugin produced (req.CSV) into an in-memory content-addressed
// store and hands back a URL that serves it — the pdf demoExport pattern,
func demoGridExport(_ context.Context, req datagrid.ExportRequest) (string, error) {
	// The spill file is closed when this handler returns, so consume it
	// fully here.
	b, err := io.ReadAll(io.LimitReader(req.CSV, demoGridMaxExportBytes+1))
	if err != nil {
		return "", fmt.Errorf("demo export: reading CSV stream: %w", err)
	}
	if len(b) > demoGridMaxExportBytes {
		return "", fmt.Errorf("demo export: %d bytes exceeds the %d-byte demo store cap",
			len(b), demoGridMaxExportBytes)
	}
	demoGridExports.mu.Lock()
	defer demoGridExports.mu.Unlock()
	if demoGridExports.byID == nil {
		demoGridExports.byID = map[string][]byte{}
	}
	sum := sha256.Sum256(b)
	id := hex.EncodeToString(sum[:8])
	if _, exists := demoGridExports.byID[id]; !exists {
		// New content: retain in FIFO order, evicting the oldest when the
		// store is full.
		if len(demoGridExports.order) >= demoGridMaxExports {
			oldest := demoGridExports.order[0]
			delete(demoGridExports.byID, oldest)
			demoGridExports.order = demoGridExports.order[1:]
		}
		demoGridExports.order = append(demoGridExports.order, id)
		demoGridExports.byID[id] = b
	}
	return "/datagrid/exported/" + id, nil
}

var demoGridExports struct {
	mu    sync.Mutex
	byID  map[string][]byte
	order []string // insertion order, oldest first
}

// registerDemoGridExportRoute serves what demoGridExport stored. Registered
// alongside the gallery shell so it lives on the same router as the plugin
// routes.
func registerDemoGridExportRoute(rt interface {
	Get(string, http.Handler)
}) {
	rt.Get("/datagrid/exported/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		demoGridExports.mu.Lock()
		b, ok := demoGridExports.byID[r.PathValue("id")]
		demoGridExports.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", `attachment; filename="datagrid-export.csv"`)
		w.Header().Set("Cache-Control", "no-store, max-age=0")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	}))
}
