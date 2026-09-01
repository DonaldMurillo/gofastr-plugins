package datagrid

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
	"github.com/DonaldMurillo/gofastr/battery/auth"
	"github.com/DonaldMurillo/gofastr/framework"
)

// --- test fixtures ----------------------------------------------------------

// testColumns mirrors the example demo doc's shape at a smaller scale.
func testColumns() []Column {
	return []Column{
		{Field: "id", Header: "ID", Width: 120, Sortable: true},
		{Field: "name", Header: "Name", Sortable: true, Editable: true},
		{Field: "amount", Header: "Amount", Type: "number", Sortable: true},
	}
}

// testDataset is a deterministic 1,000-row source with an edit overlay — the
// example app's shape, shrunk. Sorting is NUMERIC for type:"number" columns
// (parsed from the string cells), substring filter is case-insensitive, and
// cell writes land in an overlay the rows read back. It exists to prove the
// plugin's contract: sort/filter/paging happen in the SOURCE (host side),
// never in the frame.
type testDataset struct {
	mu    sync.RWMutex
	edits map[string]map[string]string // rowID → field → value
}

const testRows = 1000

func (d *testDataset) baseRow(n int) Row {
	amountTenths := (n * 7919) % 9973
	cells := map[string]string{
		"id":     fmt.Sprintf("ROW-%06d", n),
		"name":   fmt.Sprintf("User %d", n),
		"amount": fmt.Sprintf("%d.%d", amountTenths/10, amountTenths%10),
	}
	d.mu.RLock()
	if byField, ok := d.edits[cells["id"]]; ok {
		for k, v := range byField {
			cells[k] = v
		}
	}
	d.mu.RUnlock()
	return Row{ID: cells["id"], Cells: cells}
}

func numericColumns(cols []Column) map[string]bool {
	out := map[string]bool{}
	for _, c := range cols {
		if c.Type == "number" {
			out[c.Field] = true
		}
	}
	return out
}

func (d *testDataset) rows(_ context.Context, q RowsQuery) (RowsPage, error) {
	idx := make([]int, 0, testRows)
	for n := range testRows {
		if q.Filter != "" {
			row := d.baseRow(n)
			hit := false
			for _, v := range row.Cells {
				if strings.Contains(strings.ToLower(v), strings.ToLower(q.Filter)) {
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
	num := numericColumns(q.Columns)
	sort.SliceStable(idx, func(a, b int) bool {
		ra, rb := d.baseRow(idx[a]), d.baseRow(idx[b])
		for _, s := range q.Sort {
			va, vb := ra.Cells[s.Field], rb.Cells[s.Field]
			var cmp int
			if num[s.Field] {
				fa, _ := strconv.ParseFloat(va, 64)
				fb, _ := strconv.ParseFloat(vb, 64)
				switch {
				case fa < fb:
					cmp = -1
				case fa > fb:
					cmp = 1
				default:
					cmp = 0 // equal numeric keys: fall through to the NEXT key
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
		return RowsPage{Rows: []Row{}, LastRow: lastRow}, nil
	}
	end := q.EndRow
	if end > len(idx) {
		end = len(idx)
	}
	page := make([]Row, 0, end-q.StartRow)
	for _, n := range idx[q.StartRow:end] {
		page = append(page, d.baseRow(n))
	}
	return RowsPage{Rows: page, LastRow: lastRow}, nil
}

func (d *testDataset) writeCell(_ context.Context, req CellWriteRequest) error {
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

func newTestApp(t *testing.T, opts ...Option) (*framework.App, *Plugin) {
	t.Helper()
	p := New(opts...)
	app := framework.NewApp(framework.WithConfig(framework.AppConfig{Name: "datagrid-test"}))
	app.RegisterPlugin(p)
	if err := app.InitPlugins(); err != nil {
		t.Fatalf("InitPlugins: %v", err)
	}
	return app, p
}

// fullTestApp wires every handler so the demo page and all routes exist.
func fullTestApp(t *testing.T) (*framework.App, *Plugin, *testDataset) {
	t.Helper()
	ds := &testDataset{}
	app, p := newTestApp(t,
		WithDevGrantAll(),
		WithDemoPage(),
		WithRowsSource(ds.rows),
		WithCellWriteHandler(ds.writeCell),
		WithExportHandler(func(_ context.Context, req ExportRequest) (string, error) {
			return "/datagrid/exported/test", nil
		}),
		WithDemoDoc(Doc{SchemaVersion: SchemaVersion, Columns: testColumns(), PageSize: 100}),
	)
	return app, p, ds
}

func postJSON(t *testing.T, url string, body any) (*http.Response, string) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resp, err := http.Post(url, "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

// postRaw posts a literal body string — for envelope-strictness tests that
// need bytes a struct marshal can never produce (trailing garbage).
func postRaw(t *testing.T, url string, body string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

// --- assets -----------------------------------------------------------------

func TestInitServesAssetsWithCorrectContentTypes(t *testing.T) {
	app, _, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct{ path, wantCT string }{
		{GridHTMLURL, "text/html; charset=utf-8"},
		{GridJSURL, "text/javascript; charset=utf-8"},
		{GridCSSURL, "text/css; charset=utf-8"},
		{AdapterScriptURL, "text/javascript; charset=utf-8"},
		{ConfigScriptURL, "text/javascript; charset=utf-8"},
	}
	for _, c := range cases {
		resp, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatalf("GET %s: %v", c.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.Header.Get("Content-Type") != c.wantCT {
			t.Errorf("%s: content-type=%q want %q", c.path, resp.Header.Get("Content-Type"), c.wantCT)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status=%d body=%s", c.path, resp.StatusCode, body)
		}
	}
}

// TestFramedAssetsCarryHeaderRelaxation proves the platform AssetServer carries
// the framing/CORP/CSP relaxation that lets the host frame its OWN grid
// document, AND that the fixed framedCSP carries connect-src 'none' + sandbox
// allow-scripts — the directives that make every page of rows cross the
// bridge instead of being fetched by the frame.
//
// form-action is checked alongside them because it closes the one exfiltration
// path connect-src cannot: a form submits by NAVIGATING, so a frame granted
// allow-forms could POST what it had read to any origin it liked. The
// directives are three halves of one guarantee, and a regression in any of
// them would leave the other two looking fine.
func TestFramedAssetsCarryHeaderRelaxation(t *testing.T) {
	app, _, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for _, path := range []string{GridHTMLURL, GridJSURL, GridCSSURL} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status=%d", path, resp.StatusCode)
		}
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "connect-src 'none'") {
			t.Errorf("%s: framed CSP missing connect-src 'none': %q", path, csp)
		}
		if !strings.Contains(csp, "sandbox allow-scripts") {
			t.Errorf("%s: framed CSP missing sandbox allow-scripts: %q", path, csp)
		}
		if !strings.Contains(csp, "form-action 'none'") {
			t.Errorf("%s: framed CSP missing form-action 'none': %q", path, csp)
		}
		if resp.Header.Get("Cross-Origin-Resource-Policy") == "" {
			t.Errorf("%s: missing CORP relaxation", path)
		}
	}
}

func TestDemoPageContainsMountAndBroker(t *testing.T) {
	app, _, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, err := http.Get(srv.URL + DemoURL)
	if err != nil {
		t.Fatalf("GET demo: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)
	for _, want := range []string{
		`data-fui-plugin="datagrid"`,
		pluginhost.BrokerScriptURL,
		AdapterScriptURL,
		ConfigScriptURL,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("demo page missing %q", want)
		}
	}
}

// TestMountPublishesFieldName pins the MountConfig.Field wiring: the mount
// marker must carry the hidden-input name (data-fui-plugin-field) so the
// adapter mirrors the doc into the field THIS mount named — a custom name
// that never reaches the adapter silently loses its view state on submit.
func TestMountPublishesFieldName(t *testing.T) {
	custom := Mount(MountConfig{DocID: "orders", Field: "orders_view", Doc: `{"pageSize":50}`})
	html := string(custom)
	for _, want := range []string{
		`data-fui-plugin-field="orders_view"`,
		`name="orders_view"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("Mount(custom field) missing %q in:\n%s", want, html)
		}
	}
	def := Mount(MountConfig{DocID: "orders"})
	if !strings.Contains(string(def), `data-fui-plugin-field="datagrid_doc"`) {
		t.Errorf("Mount(default) missing data-fui-plugin-field=\"datagrid_doc\":\n%s", def)
	}
}

// --- /rows: server-side sort, filter, paging --------------------------------

func TestRowsRoundTripSortsAndFiltersServerSide(t *testing.T) {
	app, _, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	// Sort by amount asc: (n*7919)%9973 is minimized at n=0 (0.0), so the
	// first row of the sorted view must be ROW-000000 — proof the sort ran
	// in the SOURCE, not wherever the caller sits.
	resp, raw := postJSON(t, srv.URL+RowsURL, map[string]any{
		"docId": "demo", "startRow": 0, "endRow": 3,
		"sort":    []map[string]any{{"field": "amount", "dir": "asc"}},
		"columns": testColumns(),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rows status=%d body=%s", resp.StatusCode, raw)
	}
	var page RowsPage
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	if len(page.Rows) != 3 || page.LastRow != testRows {
		t.Fatalf("page len=%d lastRow=%d", len(page.Rows), page.LastRow)
	}
	if page.Rows[0].ID != "ROW-000000" || page.Rows[0].Cells["amount"] != "0.0" {
		t.Fatalf("server-side sort asc: first row = %v", page.Rows[0])
	}
	// Numeric, not lexicographic: "997.2" must sort above "100.5"-style
	// strings that lexicographic order would misplace.
	// Numeric desc: brute-force the expected max straight from the formula
	// (independent of the sort under test), then demand the first row of the
	// desc-sorted page matches it.
	maxT, maxN := -1, -1
	for n := range testRows {
		if v := (n * 7919) % 9973; v > maxT {
			maxT, maxN = v, n
		}
	}
	resp, raw = postJSON(t, srv.URL+RowsURL, map[string]any{
		"docId": "demo", "startRow": 0, "endRow": 1,
		"sort":    []map[string]any{{"field": "amount", "dir": "desc"}},
		"columns": testColumns(),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rows desc status=%d", resp.StatusCode)
	}
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	wantID := fmt.Sprintf("ROW-%06d", maxN)
	wantAmount := fmt.Sprintf("%d.%d", maxT/10, maxT%10)
	if page.Rows[0].ID != wantID || page.Rows[0].Cells["amount"] != wantAmount {
		t.Fatalf("numeric desc: first row = %s/%s, want %s/%s",
			page.Rows[0].ID, page.Rows[0].Cells["amount"], wantID, wantAmount)
	}

	// Filter: "User 42" matches ROW-000042 (and any name containing it).
	resp, raw = postJSON(t, srv.URL+RowsURL, map[string]any{
		"docId": "demo", "startRow": 0, "endRow": 100,
		"filter": "USER 42", "columns": testColumns(),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rows filtered status=%d", resp.StatusCode)
	}
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	if page.LastRow < 1 || page.Rows[0].ID != "ROW-000042" {
		t.Fatalf("filter: lastRow=%d first=%v", page.LastRow, page.Rows[0])
	}
}

// TestRowsProjectedToRequestedColumns pins the projection: a request for one
// column ships exactly that column's cells across the bridge, not every field
// the source happens to return — the frame is untrusted and gets what it
// asked for, nothing more.
func TestRowsProjectedToRequestedColumns(t *testing.T) {
	app, _, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, raw := postJSON(t, srv.URL+RowsURL, map[string]any{
		"docId": "demo", "startRow": 0, "endRow": 5,
		"columns": []map[string]any{{"field": "name", "header": "Name"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rows status=%d body=%s", resp.StatusCode, raw)
	}
	var page RowsPage
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	if len(page.Rows) != 5 {
		t.Fatalf("page len=%d", len(page.Rows))
	}
	for _, row := range page.Rows {
		if len(row.Cells) != 1 {
			t.Fatalf("row %s cells=%v, want exactly the requested column", row.ID, row.Cells)
		}
		if row.Cells["name"] == "" {
			t.Fatalf("row %s missing the requested name cell", row.ID)
		}
	}
}

// TestTwoKeySortAppliesSecondaryKeyOnTies pins the source's chained-sort
// contract: when the FIRST key ties, the second key decides. The fixture's
// amount formula yields distinct values, so the tie is created through the
// edit overlay — three rows pinned to the same amount, ordered by the second
// key (name asc → User 1 before User 2 before User 3). A comparator that
// returns non-zero for equal numeric keys never applies any secondary key.
func TestTwoKeySortAppliesSecondaryKeyOnTies(t *testing.T) {
	ds := &testDataset{}
	for _, n := range []int{1, 2, 3} {
		if err := ds.writeCell(context.Background(), CellWriteRequest{
			RowID: fmt.Sprintf("ROW-%06d", n), Field: "amount", Value: "42.0",
		}); err != nil {
			t.Fatalf("writeCell: %v", err)
		}
	}
	page, err := ds.rows(context.Background(), RowsQuery{
		StartRow: 0, EndRow: testRows,
		Sort:    []SortModel{{Field: "amount", Dir: "asc"}, {Field: "name", Dir: "asc"}},
		Columns: testColumns(),
	})
	if err != nil {
		t.Fatalf("rows: %v", err)
	}
	pos := map[string]int{}
	for i, row := range page.Rows {
		pos[row.ID] = i
	}
	for _, id := range []string{"ROW-000001", "ROW-000002", "ROW-000003"} {
		if _, ok := pos[id]; !ok {
			t.Fatalf("row %s missing from the sorted page", id)
		}
	}
	if !(pos["ROW-000001"] < pos["ROW-000002"] && pos["ROW-000002"] < pos["ROW-000003"]) {
		t.Fatalf("secondary sort key not applied on amount ties: pos(1)=%d pos(2)=%d pos(3)=%d",
			pos["ROW-000001"], pos["ROW-000002"], pos["ROW-000003"])
	}
}

// TestRowsPageTooLarge pins the bridge limit: a single request can never ask
// for the whole table, which is the integrity behind the e2e volume test.
func TestRowsPageTooLarge(t *testing.T) {
	app, _, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, raw := postJSON(t, srv.URL+RowsURL, map[string]any{
		"docId": "demo", "startRow": 0, "endRow": 100000, "columns": testColumns(),
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", resp.StatusCode, raw)
	}
	if !strings.Contains(raw, "E_PAGE_TOO_LARGE") {
		t.Fatalf("body=%s, want E_PAGE_TOO_LARGE", raw)
	}
}

func TestRowsValidation(t *testing.T) {
	app, _, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	cases := []struct {
		name string
		body map[string]any
	}{
		{"inverted range", map[string]any{"startRow": 10, "endRow": 10, "columns": testColumns()}},
		{"negative start", map[string]any{"startRow": -1, "endRow": 10, "columns": testColumns()}},
		{"bad sort dir", map[string]any{
			"startRow": 0, "endRow": 10,
			"sort":    []map[string]any{{"field": "amount", "dir": "up"}},
			"columns": testColumns()}},
		{"empty sort field", map[string]any{
			"startRow": 0, "endRow": 10,
			"sort":    []map[string]any{{"field": "", "dir": "asc"}},
			"columns": testColumns()}},
	}
	for _, c := range cases {
		c.body["docId"] = "demo"
		resp, raw := postJSON(t, srv.URL+RowsURL, c.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status=%d body=%s, want 400", c.name, resp.StatusCode, raw)
		}
	}
}

// --- envelope strictness ------------------------------------------------------

// TestEnvelopeRejectsTrailingData pins the one-value envelope on every route:
// a valid JSON object followed by a second object, or by stray non-whitespace,
// is a 400 rather than a silently accepted body. Decoding once without an EOF
// check accepted exactly this.
//
// Trailing whitespace is deliberately fine: `curl -d @body.json` sends a
// newline and rejecting it would break real clients for no security gain, so
// the newline case below asserts acceptance, not rejection.
func TestEnvelopeRejectsTrailingData(t *testing.T) {
	app, _, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	valid := map[string]any{
		"rows":   map[string]any{"docId": "demo", "startRow": 0, "endRow": 5, "columns": testColumns()},
		"cell":   map[string]any{"docId": "demo", "rowId": "ROW-000001", "field": "name", "value": "x"},
		"export": map[string]any{"docId": "demo", "format": "csv", "columns": testColumns()},
		"save":   map[string]any{"docId": "demo", "doc": map[string]any{"schemaVersion": SchemaVersion, "columns": testColumns()}},
	}
	route := map[string]string{
		"rows":   RowsURL,
		"cell":   CellWriteURL,
		"export": ExportURL,
		"save":   SaveURL,
	}
	for key, url := range route {
		base, err := json.Marshal(valid[key])
		if err != nil {
			t.Fatalf("marshal %s body: %v", key, err)
		}
		for _, trailer := range []string{
			`{"second":"object"}`,
			"garbage",
		} {
			resp, raw := postRaw(t, srv.URL+url, string(base)+trailer)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s + trailing %q: status=%d body=%s, want 400", key, trailer, resp.StatusCode, raw)
			}
			if !strings.Contains(raw, "E_BAD_JSON") {
				t.Errorf("%s + trailing %q: body=%s, want E_BAD_JSON", key, trailer, raw)
			}
		}
		// The bare value still passes — strictness must not reject the
		// legitimate shape. Nor may a trailing newline or padding, which is
		// what ordinary HTTP clients send.
		for _, trailer := range []string{"", "\n", "   \n\t "} {
			resp, raw := postRaw(t, srv.URL+url, string(base)+trailer)
			if resp.StatusCode == http.StatusBadRequest && strings.Contains(raw, "E_BAD_JSON") {
				t.Errorf("%s + whitespace %q: clean body rejected: %s", key, trailer, raw)
			}
		}
	}
}

// --- /cell: the write path ---------------------------------------------------

func TestCellWriteRoundTripAndOverlay(t *testing.T) {
	app, _, ds := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, raw := postJSON(t, srv.URL+CellWriteURL, map[string]any{
		"docId": "demo", "rowId": "ROW-000007", "field": "name", "value": "Edited Name",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cell write status=%d body=%s", resp.StatusCode, raw)
	}

	// The next page read reflects the edit — the overlay is the source's
	// job (host side), which is exactly what makes an edit survive reload.
	page, err := ds.rows(context.Background(), RowsQuery{StartRow: 7, EndRow: 8, Columns: testColumns()})
	if err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Cells["name"] != "Edited Name" {
		t.Fatalf("overlay missing: %+v", page.Rows)
	}
}

func TestCellWriteValidation(t *testing.T) {
	app, _, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for _, body := range []map[string]any{
		{"rowId": "", "field": "name", "value": "x"},
		{"rowId": "ROW-000001", "field": "", "value": "x"},
	} {
		resp, raw := postJSON(t, srv.URL+CellWriteURL, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("cell write %v: status=%d body=%s, want 400", body, resp.StatusCode, raw)
		}
	}
}

// --- /export: host-side CSV --------------------------------------------------

func TestExportRunsHostSideWithSortedCSV(t *testing.T) {
	var gotBytes []byte
	var gotCount int
	var handlerErr string
	ds := &testDataset{}
	app, _ := newTestApp(t,
		WithDevGrantAll(),
		WithRowsSource(ds.rows),
		WithExportHandler(func(_ context.Context, req ExportRequest) (string, error) {
			b, err := io.ReadAll(req.CSV)
			if err != nil {
				handlerErr = err.Error()
				return "", err
			}
			gotBytes, gotCount = b, req.RowCount
			return "/datagrid/exported/x", nil
		}),
	)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, raw := postJSON(t, srv.URL+ExportURL, map[string]any{
		"docId": "demo", "format": "csv",
		"sort":    []map[string]any{{"field": "amount", "dir": "asc"}},
		"filter":  "User 7",
		"columns": testColumns(),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export status=%d body=%s", resp.StatusCode, raw)
	}
	if handlerErr != "" {
		t.Fatalf("export handler failed to read the CSV stream: %s", handlerErr)
	}
	var out struct {
		URL      string `json:"url"`
		RowCount int    `json:"rowCount"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if out.URL != "/datagrid/exported/x" {
		t.Fatalf("url=%q", out.URL)
	}
	// Expected match set computed straight from the name formula:
	// "User 7" is a substring of User 7, User 70..79, User 700..799.
	var wantIDs []string
	wantAmountByID := map[string]string{}
	for n := range testRows {
		name := fmt.Sprintf("User %d", n)
		if !strings.Contains(name, "User 7") {
			continue
		}
		id := fmt.Sprintf("ROW-%06d", n)
		t2 := (n * 7919) % 9973
		wantIDs = append(wantIDs, id)
		wantAmountByID[id] = fmt.Sprintf("%d.%d", t2/10, t2%10)
	}
	if out.RowCount != len(wantIDs) || gotCount != len(wantIDs) {
		t.Fatalf("rowCount out=%d handler=%d, want %d", out.RowCount, gotCount, len(wantIDs))
	}
	lines := strings.Split(strings.TrimSpace(string(gotBytes)), "\n")
	if len(lines) != len(wantIDs)+1 { // header + matches
		t.Fatalf("csv lines=%d want %d\n%s", len(lines), len(wantIDs)+1, gotBytes)
	}
	if lines[0] != "ID,Name,Amount" {
		t.Fatalf("csv header=%q", lines[0])
	}
	// Server-side sort held through the export scan: the whole CSV is in
	// ascending numeric amount order (computed independently here).
	prev := -1.0
	for _, line := range lines[1:] {
		parts := strings.Split(line, ",")
		amt, ok := wantAmountByID[parts[0]]
		if !ok || parts[2] != amt {
			t.Fatalf("csv row %q not in the expected match set", line)
		}
		v, _ := strconv.ParseFloat(amt, 64)
		if prev != -1.0 && v < prev {
			t.Fatalf("csv not sorted ascending: %f after %f", v, prev)
		}
		prev = v
	}

	// Unknown format is a 400, not a policy refusal.
	resp, raw = postJSON(t, srv.URL+ExportURL, map[string]any{"format": "xlsx", "columns": testColumns()})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown format status=%d body=%s, want 400", resp.StatusCode, raw)
	}
}

// TestExportSanitizesFormulaCells pins the CSV formula-injection mitigation:
// a cell (or header) starting with =, +, -, @, tab or CR is exported with a
// leading quote so spreadsheet clients render it as text instead of
// evaluating it. The reviewer demonstrated `=1+1` surviving to the file.
func TestExportSanitizesFormulaCells(t *testing.T) {
	var gotBytes []byte
	ds := &testDataset{}
	edits := map[string]string{
		"=1+1":            "'=1+1",
		"+SUM(A1:A9)":     "'+SUM(A1:A9)",
		"-2+3":            "'-2+3",
		"@cmd|' /C calc'": "'@cmd|' /C calc'",
		"\ttab-leading":   "'\ttab-leading",
		"\rCR-leading":    "'\rCR-leading",
		"plain value":     "plain value",
		"mid=equals":      "mid=equals",
	}
	app, _ := newTestApp(t,
		WithDevGrantAll(),
		WithRowsSource(ds.rows),
		WithExportHandler(func(_ context.Context, req ExportRequest) (string, error) {
			b, err := io.ReadAll(req.CSV)
			if err != nil {
				return "", err
			}
			gotBytes = b
			return "/x", nil
		}),
	)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	// One edited cell per formula shape, plus a formula-shaped HEADER.
	var wantIDs []string
	wantVal := map[string]string{}
	i := 0
	for payload := range edits {
		id := fmt.Sprintf("ROW-%06d", i)
		if err := ds.writeCell(context.Background(), CellWriteRequest{
			RowID: id, Field: "name", Value: payload,
		}); err != nil {
			t.Fatalf("writeCell: %v", err)
		}
		wantIDs = append(wantIDs, id)
		wantVal[id] = payload
		i++
	}
	resp, raw := postJSON(t, srv.URL+ExportURL, map[string]any{
		"docId": "demo", "format": "csv",
		"columns": []map[string]any{{"field": "id"}, {"field": "name", "header": "=HEADER"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export status=%d body=%s", resp.StatusCode, raw)
	}
	// Parse the CSV back (rather than string-splitting) so csv quoting is
	// unwrapped by the same rules that wrote it; then pin the sanitised
	// value of every edited row and the untouched rest.
	recs, err := csv.NewReader(bytes.NewReader(gotBytes)).ReadAll()
	if err != nil {
		t.Fatalf("parse exported csv: %v\n%s", err, gotBytes)
	}
	if len(recs) != 1+testRows {
		t.Fatalf("csv records=%d want %d (header + every row)", len(recs), 1+testRows)
	}
	if recs[0][1] != "'=HEADER" {
		t.Fatalf("header not sanitised: %v", recs[0])
	}
	checked := 0
	for _, rec := range recs[1:] {
		wantPayload, edited := wantVal[rec[0]]
		if !edited {
			continue
		}
		checked++
		if got := rec[1]; got != edits[wantPayload] {
			t.Errorf("cell %q exported as %q, want %q", wantPayload, got, edits[wantPayload])
		}
	}
	if checked != len(edits) {
		t.Fatalf("checked %d edited rows, want all %d", checked, len(edits))
	}
}

// TestExportSourceErrorFailsRequest pins the export error contract: a rows
// source that fails MID-SCAN produces an error response and the export
// handler is never called — never a stored, truncated CSV that reports
// success.
func TestExportSourceErrorFailsRequest(t *testing.T) {
	handlerCalled := false
	app, _ := newTestApp(t,
		WithDevGrantAll(),
		WithRowsSource(func(_ context.Context, q RowsQuery) (RowsPage, error) {
			if q.StartRow > 0 {
				return RowsPage{}, fmt.Errorf("disk on fire at row %d", q.StartRow)
			}
			// First chunk: one row and an UNKNOWN total (LastRow -1), so
			// the scan must come back for a second chunk — which fails.
			return RowsPage{Rows: []Row{{ID: "ROW-000000", Cells: map[string]string{"id": "ROW-000000"}}}, LastRow: -1}, nil
		}),
		WithExportHandler(func(_ context.Context, req ExportRequest) (string, error) {
			handlerCalled = true
			return "/x", nil
		}),
	)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, raw := postJSON(t, srv.URL+ExportURL, map[string]any{
		"docId": "demo", "format": "csv", "columns": testColumns(),
	})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("export status=%d body=%s, want 500", resp.StatusCode, raw)
	}
	if !strings.Contains(raw, "E_EXPORT") || !strings.Contains(raw, "disk on fire") {
		t.Fatalf("body=%s, want E_EXPORT naming the source error", raw)
	}
	if handlerCalled {
		t.Fatal("export handler was called despite the mid-scan source error")
	}
}

// --- /save: view state -------------------------------------------------------

func TestSaveRoundTripRestoresViewState(t *testing.T) {
	app, p, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	doc := map[string]any{
		"schemaVersion": SchemaVersion,
		"columns":       testColumns(),
		"sort":          []map[string]any{{"field": "amount", "dir": "desc"}},
		"filter":        "User 1",
		"pageSize":      50,
	}
	resp, raw := postJSON(t, srv.URL+SaveURL, map[string]any{
		"docId": "demo", "doc": doc, "schemaVersion": SchemaVersion,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status=%d body=%s", resp.StatusCode, raw)
	}
	got, ok := p.LoadDoc(context.Background(), "demo")
	if !ok || !strings.Contains(got, `"filter":"User 1"`) || !strings.Contains(got, `"pageSize":50`) {
		t.Fatalf("LoadDoc=%q", got)
	}

	// The demo page now mounts the SAVED view state, not the demo doc.
	resp2, err := http.Get(srv.URL + DemoURL)
	if err != nil {
		t.Fatalf("GET demo: %v", err)
	}
	body, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if !strings.Contains(string(body), `User 1`) {
		t.Fatal("demo page does not carry the saved view state")
	}
}

// TestSaveNormalizesDocBeforePersist pins the save path to the same bounds
// /rows enforces: an over-large pageSize is clamped (and the CLAMPED value is
// what gets persisted), and out-of-bound sort/filter docs are rejected — a
// doc /rows would refuse must not be saveable only to explode on the next
// load when the frame asks for a block the bridge rejects.
func TestSaveNormalizesDocBeforePersist(t *testing.T) {
	app, p, _ := fullTestApp(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	resp, raw := postJSON(t, srv.URL+SaveURL, map[string]any{
		"docId": "demo",
		"doc": map[string]any{
			"schemaVersion": SchemaVersion,
			"columns":       testColumns(),
			"pageSize":      100000,
		},
		"schemaVersion": SchemaVersion,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status=%d body=%s", resp.StatusCode, raw)
	}
	got, ok := p.LoadDoc(context.Background(), "demo")
	if !ok {
		t.Fatal("LoadDoc: nothing saved")
	}
	if !strings.Contains(got, `"pageSize":100`) {
		t.Fatalf("persisted doc not normalised (pageSize should clamp to 100): %s", got)
	}
	if strings.Contains(got, "100000") {
		t.Fatalf("persisted doc carries the unnormalised request value: %s", got)
	}

	// Out-of-bound docs are rejected outright.
	tooManySorts := make([]map[string]any, maxSortKeys+1)
	for i := range tooManySorts {
		tooManySorts[i] = map[string]any{"field": "amount", "dir": "asc"}
	}
	for name, doc := range map[string]map[string]any{
		"too many sort keys": {
			"schemaVersion": SchemaVersion, "columns": testColumns(),
			"sort": tooManySorts,
		},
		"filter too long": {
			"schemaVersion": SchemaVersion, "columns": testColumns(),
			"filter": strings.Repeat("x", maxFilterLen+1),
		},
		"bad sort dir": {
			"schemaVersion": SchemaVersion, "columns": testColumns(),
			"sort": []map[string]any{{"field": "amount", "dir": "up"}},
		},
	} {
		resp, raw := postJSON(t, srv.URL+SaveURL, map[string]any{
			"docId": "demo", "doc": doc, "schemaVersion": SchemaVersion,
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status=%d body=%s, want 400", name, resp.StatusCode, raw)
		}
		if !strings.Contains(raw, "E_BAD_DOC") {
			t.Errorf("%s: body=%s, want E_BAD_DOC", name, raw)
		}
	}
}

// --- capability gates --------------------------------------------------------

// TestCapabilityGate proves the auth.HasScope reuse: a token-authenticated
// request whose scopes do not grant the capability is denied, while
// WithDevGrantAll short-circuits the gate.
func TestCapabilityGate(t *testing.T) {
	enforcing := New(WithRowsSource((&testDataset{}).rows))
	granted := New(WithDevGrantAll(), WithRowsSource((&testDataset{}).rows))

	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})

	for _, cap := range []string{"data:read", CapDataWrite, CapDataExport} {
		deniedReq := httptest.NewRequest(http.MethodPost, RowsURL, nil).WithContext(deniedCtx)
		if enforcing.allow(deniedReq, cap) {
			t.Errorf("enforcing plugin should DENY a non-granting token for %s", cap)
		}
		if !granted.allow(deniedReq, cap) {
			t.Errorf("WithDevGrantAll should ALLOW %s regardless of token scopes", cap)
		}
	}
}

// TestRoutesDeniedWithoutCapability proves the gate is wired into every route:
// a token whose scopes lack the capability gets 403 + E_CAPABILITY_DENIED.
func TestRoutesDeniedWithoutCapability(t *testing.T) {
	ds := &testDataset{}
	app, _ := newTestApp(t,
		WithRowsSource(ds.rows),
		WithCellWriteHandler(ds.writeCell),
		WithExportHandler(func(_ context.Context, req ExportRequest) (string, error) {
			return "/x", nil
		}),
	)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	post := func(url string, body string) *http.Response {
		req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body)).WithContext(deniedCtx)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Config.Handler.ServeHTTP(rec, req) //nolint — direct handler use keeps ctx scopes
		resp := rec.Result()
		defer resp.Body.Close()
		return resp
	}
	for _, url := range []string{RowsURL, CellWriteURL, ExportURL, SaveURL} {
		resp := post(url, `{"startRow":0,"endRow":10}`)
		var body struct {
			Error      string `json:"error"`
			Capability string `json:"capability"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if resp.StatusCode != http.StatusForbidden || body.Error != "E_CAPABILITY_DENIED" {
			t.Errorf("%s: status=%d error=%q capability=%q, want 403 E_CAPABILITY_DENIED", url, resp.StatusCode, body.Error, body.Capability)
		}
	}
}

// TestCapabilityGateRealRequests drives the gated routes with REAL requests:
// a denied token gets 403 on all four routes (including /save — the
// unauthorized-default-save hole), and an anonymous caller (Allow's documented
// pass-for-anonymous semantics, bounded by the plugin grant) reaches the wired
// handlers. The nil-handler panic behind /cell shipped precisely because no
// test ever posted to it.
func TestCapabilityGateRealRequests(t *testing.T) {
	ds := &testDataset{}
	cellCalls := 0
	saveCalls := 0
	exportCalls := 0
	app, _ := newTestApp(t,
		WithRowsSource(ds.rows),
		WithCellWriteHandler(func(_ context.Context, req CellWriteRequest) error {
			cellCalls++
			return nil
		}),
		WithSaveHandler(func(_ context.Context, req SaveRequest) error {
			saveCalls++
			return nil
		}),
		WithExportHandler(func(_ context.Context, req ExportRequest) (string, error) {
			exportCalls++
			return "/x", nil
		}),
	)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	rowsBody := `{"docId":"demo","startRow":0,"endRow":5,"columns":[]}`
	cellBody := `{"docId":"demo","rowId":"ROW-000001","field":"name","value":"x"}`
	exportBody := `{"docId":"demo","format":"csv","columns":[]}`
	saveBody := `{"docId":"demo","doc":{"schemaVersion":"` + SchemaVersion + `"},"schemaVersion":"` + SchemaVersion + `"}`

	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	post := func(url, body string, ctx context.Context) (*http.Response, string) {
		req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Config.Handler.ServeHTTP(rec, req) //nolint — direct handler use keeps ctx scopes
		resp := rec.Result()
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(raw)
	}

	for url, body := range map[string]string{
		RowsURL:      rowsBody,
		CellWriteURL: cellBody,
		ExportURL:    exportBody,
		SaveURL:      saveBody,
	} {
		resp, raw := post(url, body, deniedCtx)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(raw, "E_CAPABILITY_DENIED") {
			t.Errorf("denied token POST %s: status=%d body=%s, want 403 E_CAPABILITY_DENIED", url, resp.StatusCode, raw)
		}
	}
	if cellCalls+saveCalls+exportCalls != 0 {
		t.Fatalf("denied token reached a handler: cell=%d save=%d export=%d", cellCalls, saveCalls, exportCalls)
	}

	// Anonymous callers pass the caller-authority side (no token) and are
	// bounded by the plugin grant — data:read/write/export are all granted
	// here because the handlers are wired.
	for url, body := range map[string]string{
		RowsURL:      rowsBody,
		CellWriteURL: cellBody,
		ExportURL:    exportBody,
		SaveURL:      saveBody,
	} {
		resp, raw := post(url, body, context.Background())
		if resp.StatusCode != http.StatusOK {
			t.Errorf("anonymous POST %s: status=%d body=%s, want 200", url, resp.StatusCode, raw)
		}
	}
	if cellCalls != 1 || saveCalls != 1 || exportCalls != 1 {
		t.Fatalf("anonymous calls: cell=%d save=%d export=%d, want 1 each", cellCalls, saveCalls, exportCalls)
	}
}

// TestWildcardGrantsReachWiredHandlers pins the wildcard path end to end: a
// grant set using the scope grammar's wildcards ("data:*", "*:*") passes the
// gate exactly as the matcher promises — but only because construction
// required the handlers (see TestNewPanicsOnWildcardGrantWithoutHandler). A
// wildcard grant with its handlers wired routes to them like the literal
// capability does.
func TestWildcardGrantsReachWiredHandlers(t *testing.T) {
	ds := &testDataset{}
	for _, caps := range [][]string{
		{"data:*", "theme:read"},
		{"*:*"},
		{"*:write"},
	} {
		cellCalls := 0
		exportCalls := 0
		app, _ := newTestApp(t,
			WithCapabilities(caps...),
			WithRowsSource(ds.rows),
			WithCellWriteHandler(func(_ context.Context, req CellWriteRequest) error {
				cellCalls++
				return nil
			}),
			WithExportHandler(func(_ context.Context, req ExportRequest) (string, error) {
				exportCalls++
				return "/x", nil
			}),
		)
		srv := httptest.NewServer(app.Router())
		resp, raw := postJSON(t, srv.URL+CellWriteURL, map[string]any{
			"docId": "demo", "rowId": "ROW-000001", "field": "name", "value": "x",
		})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("caps=%v POST /cell: status=%d body=%s, want 200", caps, resp.StatusCode, raw)
		}
		resp, raw = postJSON(t, srv.URL+ExportURL, map[string]any{
			"docId": "demo", "format": "csv", "columns": []map[string]any{{"field": "name"}},
		})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("caps=%v POST /export: status=%d body=%s, want 200", caps, resp.StatusCode, raw)
		}
		srv.Close()
		if cellCalls != 1 || exportCalls != 1 {
			t.Errorf("caps=%v: handlers not reached (cell=%d export=%d)", caps, cellCalls, exportCalls)
		}
	}
}

// TestRoutesFailClosedWithoutHandlers pins the WithDevGrantAll contract: the
// bypass skips the gate (its job) but must never reach a nil handler — every
// write route returns a clear error instead of panicking. A panic in an HTTP
// handler is a denial of service on the host process.
func TestRoutesFailClosedWithoutHandlers(t *testing.T) {
	app, _ := newTestApp(t,
		WithDevGrantAll(),
		WithRowsSource((&testDataset{}).rows),
		// No WithCellWriteHandler / WithExportHandler / WithSaveHandler.
	)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for url, body := range map[string]string{
		CellWriteURL: `{"docId":"demo","rowId":"ROW-000001","field":"name","value":"x"}`,
		ExportURL:    `{"docId":"demo","format":"csv","columns":[]}`,
		SaveURL:      `{"docId":"demo","doc":{"schemaVersion":"` + SchemaVersion + `"},"schemaVersion":"` + SchemaVersion + `"}`,
	} {
		resp, raw := postJSON(t, srv.URL+url, json.RawMessage(body))
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("POST %s without handler: status=%d body=%s, want 500 (fail closed, no panic)", url, resp.StatusCode, raw)
		}
		if !strings.Contains(raw, "E_") {
			t.Errorf("POST %s without handler: body=%s, want a stable error code", url, raw)
		}
	}
	// /rows needs no optional wiring and still works.
	resp, raw := postJSON(t, srv.URL+RowsURL, map[string]any{
		"docId": "demo", "startRow": 0, "endRow": 5, "columns": testColumns(),
	})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("POST /rows without optional handlers: status=%d body=%s, want 200", resp.StatusCode, raw)
	}
}

// --- optional capabilities + construction guards -----------------------------

func TestOptionalCapabilitiesGrantedByHandlers(t *testing.T) {
	ds := &testDataset{}
	plain := New(WithRowsSource(ds.rows))
	full := New(
		WithRowsSource(ds.rows),
		WithCellWriteHandler(ds.writeCell),
		WithExportHandler(func(_ context.Context, req ExportRequest) (string, error) { return "/x", nil }),
	)
	if got := plain.Capabilities(); len(got) != 2 {
		t.Fatalf("plain caps = %v, want exactly data:read+theme:read", got)
	}
	got := full.Capabilities()
	for _, want := range []string{"data:read", "theme:read", CapDataWrite, CapDataExport} {
		if !containsCap(got, want) {
			t.Errorf("full caps %v missing %s", got, want)
		}
	}
	// The manifest mirrors the granted set (the broker registers it).
	m := full.Manifest().Capabilities
	if !containsCap(m, CapDataExport) {
		t.Errorf("manifest capabilities %v missing data:export", m)
	}
}

func TestNewPanicsOnMissingSourceOrHandlerlessCapability(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
		want string
	}{
		{"no rows source", nil, "no rows source"},
		{
			"write without handler",
			[]Option{
				WithRowsSource((&testDataset{}).rows),
				WithCapabilities("data:read", "theme:read", CapDataWrite),
			},
			"data:write granted but no cell write handler",
		},
		{
			"export without handler",
			[]Option{
				WithRowsSource((&testDataset{}).rows),
				WithCapabilities("data:read", "theme:read", CapDataExport),
			},
			"data:export granted but no export handler",
		},
		// The wildcard cases: pluginhost.Allow matches grants with the
		// scope grammar, so "data:*" / "*:*" / "*:write" all imply
		// data:write at request time. Construction must agree, or these
		// compile, run, and nil-panic behind /cell.
		{
			"resource wildcard write without handler",
			[]Option{
				WithRowsSource((&testDataset{}).rows),
				WithCapabilities("data:*"),
			},
			"data:write granted but no cell write handler",
		},
		{
			"grant-all without handlers",
			[]Option{
				WithRowsSource((&testDataset{}).rows),
				WithCapabilities("*:*"),
			},
			"data:write granted but no cell write handler",
		},
		{
			"verb wildcard write without handler",
			[]Option{
				WithRowsSource((&testDataset{}).rows),
				WithCapabilities("*:write"),
			},
			"data:write granted but no cell write handler",
		},
		{
			"verb wildcard export without handler",
			[]Option{
				WithRowsSource((&testDataset{}).rows),
				WithCapabilities("*:export"),
			},
			"data:export granted but no export handler",
		},
	}
	for _, c := range cases {
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Errorf("%s: expected panic", c.name)
					return
				}
				if msg := fmt.Sprint(r); !strings.Contains(msg, c.want) {
					t.Errorf("%s: panic=%q, want substring %q", c.name, msg, c.want)
				}
			}()
			_ = New(c.opts...)
		}()
	}
}

func TestConfigScriptPublishesOptionalCaps(t *testing.T) {
	ds := &testDataset{}
	plain := New(WithRowsSource(ds.rows))
	full := New(WithRowsSource(ds.rows), WithCellWriteHandler(ds.writeCell),
		WithExportHandler(func(_ context.Context, req ExportRequest) (string, error) { return "/x", nil }))
	if s := string(plain.configScriptBytes()); !strings.Contains(s, `"writeEnabled":false`) {
		t.Errorf("plain config script = %s", s)
	}
	if s := string(full.configScriptBytes()); !strings.Contains(s, `"writeEnabled":true`) || !strings.Contains(s, `"exportEnabled":true`) {
		t.Errorf("full config script = %s", s)
	}
	// A wildcard grant with handlers wired advertises them too — the frame
	// learns the same set the gate will enforce.
	wild := New(WithRowsSource(ds.rows), WithCapabilities("data:*"),
		WithCellWriteHandler(ds.writeCell),
		WithExportHandler(func(_ context.Context, req ExportRequest) (string, error) { return "/x", nil }))
	if s := string(wild.configScriptBytes()); !strings.Contains(s, `"writeEnabled":true`) || !strings.Contains(s, `"exportEnabled":true`) {
		t.Errorf("wildcard config script = %s", s)
	}
}

// TestManifestInvariants pins the platform contract the registry tests also
// enforce from plugins.json: opaque sandbox, no allow-same-origin, schema.
func TestManifestInvariants(t *testing.T) {
	app, p, _ := fullTestApp(t)
	_ = app
	m := p.Manifest()
	if m.Isolation != pluginhost.IsolationSandboxOpaque {
		t.Fatalf("isolation=%q", m.Isolation)
	}
	if got := m.SandboxString(); got != "allow-scripts" {
		t.Fatalf("sandbox=%q, want allow-scripts only", got)
	}
	if m.Schema != SchemaVersion {
		t.Fatalf("schema=%q", m.Schema)
	}
	if m.Entry != GridHTMLURL {
		t.Fatalf("entry=%q", m.Entry)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
