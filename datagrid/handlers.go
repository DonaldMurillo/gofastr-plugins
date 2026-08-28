package datagrid

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
)

// handlers.go implements the four RPC routes (protocol-v1.md §10):
//
//   - POST /rows   one page of rows          (gate data:read)
//   - POST /cell   one cell edit             (gate data:write)
//   - POST /export host-side CSV export      (gate data:export)
//   - POST /save   view-state doc persist    (gate data:write)
//
// A capability denial is 403 + E_CAPABILITY_DENIED on every route, via the
// platform's pluginhost.WriteCapabilityDenied (the doc/implementation
// divergence is recorded in DECISIONS.md — every shipped plugin writes 403).
//
// pluginhost.Allow is a capability gate, NOT authentication: it passes for
// anonymous callers (and for unscoped sessions it is bounded only by the
// plugin's grant set). Any route that WRITES — /cell above all — therefore
// relies on the host's own handler to check the session before persisting.
// The demo's WithDevGrantAll skips the gate entirely and MUST NOT survive
// into a production mount.
//
// Every route also fails CLOSED on an unwired handler (a clear error
// response, never a nil-deref): WithDevGrantAll bypasses the grant side of
// the gate, so it must not be able to reach a nil handler either — a panic
// inside an HTTP handler is a denial of service on the whole host process.

// Bounds on the request envelopes and the bridge page size. The page-size cap
// is load-bearing for the plugin's whole claim: a single requestRows can never
// ask for the whole table, so the frame can only ever hold pages.
const (
	maxEnvelopeBytes int64 = 64 << 10 // 64 KiB — query envelopes are tiny
	maxPageSize            = 500      // one bridge round trip ≤ 500 rows
	maxSortKeys            = 4        // chained sorts beyond this are noise
	maxFilterLen           = 256      // substring filter, not a query language
	maxColumns             = 64       // a doc wider than this is a mistake
	exportPageSize         = 5000     // internal scan chunk for CSV export
)

// --- /rows -----------------------------------------------------------------

// handleRows implements POST RowsURL. The host adapter relays the frame's
// requestRows event here and answers it with a rowsResult event; this route is
// the Go half of that round trip. startRow/endRow are the AG Grid infinite
// model's half-open page range; sort/filter are applied by the host's rows
// source, never in the frame.
func (p *Plugin) handleRows(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, "data:read") {
		writeJSONCapabilityDenied(w, "data:read")
		return
	}
	if p.rowsSource == nil {
		// Unreachable via New (a missing source is a construction panic);
		// the guard keeps the route failing closed anyway.
		writeJSONError(w, http.StatusInternalServerError, "E_ROWS",
			"no rows source configured (supply WithRowsSource)")
		return
	}
	body, ok := decodeRowsBody(w, r)
	if !ok {
		return
	}
	page, err := p.rowsSource(r.Context(), RowsQuery{
		DocID:    body.DocID,
		StartRow: body.StartRow,
		EndRow:   body.EndRow,
		Sort:     body.Sort,
		Filter:   body.Filter,
		Columns:  body.Columns,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_ROWS", err.Error())
		return
	}
	// Defensive clamp: the rows source is host code, but the reply crosses
	// the bridge, so a source returning more than the requested window is
	// truncated rather than shipped wholesale.
	want := body.EndRow - body.StartRow
	if want > 0 && len(page.Rows) > want {
		page.Rows = page.Rows[:want]
	}
	page = projectRows(page, body.Columns)
	if page.Rows == nil {
		page.Rows = []Row{}
	}
	writeJSON(w, http.StatusOK, page)
}

// projectRows strips each row's cells down to the requested column set
// before the page crosses the bridge: a request for one column must not
// ship every field the source happens to return into the untrusted frame.
// An empty column list is "no projection" (the source's default fields) —
// the frame always sends its doc columns, which are exactly what it renders.
func projectRows(page RowsPage, cols []Column) RowsPage {
	if len(cols) == 0 || len(page.Rows) == 0 {
		return page
	}
	for i := range page.Rows {
		cells := make(map[string]string, len(cols))
		for _, c := range cols {
			if v, ok := page.Rows[i].Cells[c.Field]; ok {
				cells[c.Field] = v
			}
		}
		page.Rows[i].Cells = cells
	}
	return page
}

// rowsBody is the decoded POST /rows envelope.
type rowsBody struct {
	DocID    string      `json:"docId"`
	StartRow int         `json:"startRow"`
	EndRow   int         `json:"endRow"`
	Sort     []SortModel `json:"sort"`
	Filter   string      `json:"filter"`
	Columns  []Column    `json:"columns"`
}

// decodeRowsBody parses + validates the /rows envelope, writing the error
// response itself. ok=false means the response is already written.
func decodeRowsBody(w http.ResponseWriter, r *http.Request) (rowsBody, bool) {
	var body rowsBody
	if !decodeEnvelope(w, r, &body) {
		return body, false
	}
	if body.DocID == "" {
		body.DocID = defaultDocID
	}
	if body.StartRow < 0 || body.EndRow <= body.StartRow {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST",
			"need 0 <= startRow < endRow")
		return body, false
	}
	if body.EndRow-body.StartRow > maxPageSize {
		// This cap is the point of the plugin: one request can never pull
		// the whole table across the bridge.
		writeJSONError(w, http.StatusBadRequest, "E_PAGE_TOO_LARGE",
			"page size exceeds the bridge limit")
		return body, false
	}
	if err := validateSortModel(body.Sort); err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", err.Error())
		return body, false
	}
	if len(body.Filter) > maxFilterLen {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", "filter too long")
		return body, false
	}
	cols, err := normalizeColumns(body.Columns)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", err.Error())
		return body, false
	}
	body.Columns = cols
	return body, true
}

// validateSortModel enforces the shared sort bounds (key count, non-empty
// fields, asc/desc) for every route that accepts a sort — /rows, /export and
// /save must not disagree about what a sort is.
func validateSortModel(sort []SortModel) error {
	if len(sort) > maxSortKeys {
		return errors.New("too many sort keys")
	}
	for _, s := range sort {
		if strings.TrimSpace(s.Field) == "" {
			return errors.New("empty sort field")
		}
		if s.Dir != "asc" && s.Dir != "desc" {
			return errors.New("sort dir must be asc or desc")
		}
	}
	return nil
}

// normalizeColumns sanitises the doc/request column list: non-empty bounded
// fields, header defaulting to the field, and a type whitelist. The same
// normalisation serves /rows, /export and /save so the three routes cannot
// disagree about what a column is.
func normalizeColumns(cols []Column) ([]Column, error) {
	if len(cols) > maxColumns {
		return nil, errors.New("too many columns")
	}
	out := make([]Column, 0, len(cols))
	for _, c := range cols {
		field := strings.TrimSpace(c.Field)
		if field == "" {
			return nil, errors.New("empty column field")
		}
		if len(field) > 64 {
			return nil, errors.New("column field too long")
		}
		header := strings.TrimSpace(c.Header)
		if header == "" {
			header = field
		}
		if c.Type != "" && c.Type != "text" && c.Type != "number" {
			return nil, errors.New("column type must be text or number")
		}
		if c.Width < 0 || c.Width > 4096 {
			c.Width = 0
		}
		c.Field, c.Header = field, header
		out = append(out, c)
	}
	return out, nil
}

// --- /cell -----------------------------------------------------------------

// handleCellWrite implements POST CellWriteURL, the host half of the frame's
// requestCellWrite → cellWriteResult round trip. The gate checks data:write;
// AUTHORIZATION is the handler's job — Allow passes for anonymous callers, so
// a production host's WithCellWriteHandler must check the session itself
// before persisting (see docs/datagrid.md).
func (p *Plugin) handleCellWrite(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, CapDataWrite) {
		writeJSONCapabilityDenied(w, CapDataWrite)
		return
	}
	if p.cellWriteHandl == nil {
		// Fail closed: reachable only under WithDevGrantAll (New panics on
		// a granted data:write without a handler), but a bypassed gate must
		// never land on a nil handler — a panic here is a DoS on the host.
		writeJSONError(w, http.StatusInternalServerError, "E_WRITE",
			"no cell write handler configured (supply WithCellWriteHandler)")
		return
	}
	var body struct {
		DocID string `json:"docId"`
		RowID string `json:"rowId"`
		Field string `json:"field"`
		Value string `json:"value"`
	}
	if !decodeEnvelope(w, r, &body) {
		return
	}
	if body.DocID == "" {
		body.DocID = defaultDocID
	}
	if strings.TrimSpace(body.RowID) == "" || strings.TrimSpace(body.Field) == "" {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST",
			"rowId and field are required")
		return
	}
	if len(body.Value) > maxFilterLen {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", "value too long")
		return
	}
	if err := p.cellWriteHandl(r.Context(), CellWriteRequest{
		DocID: body.DocID,
		RowID: body.RowID,
		Field: body.Field,
		Value: body.Value,
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_WRITE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- /export ---------------------------------------------------------------

// handleExport implements POST ExportURL, the host half of the frame's
// requestExport → exportResult round trip. CSV export runs HERE, in the host
// process: a sandboxed frame cannot start a download (no allow-downloads, no
// popups), which is the same reason pdf makes export a host capability.
//
// The plugin pages through the rows source under the request's sort/filter,
// spilling CSV to a temp file chunk by chunk (peak memory: one chunk,
// whatever the table size), and hands the handler the file as a reader — the
// handler streams it into storage and returns a URL. A mid-scan source error
// aborts the export with an error response: a failed export must never look
// like a successful short one. Bytes never cross the postMessage bridge —
// only the URL does.
func (p *Plugin) handleExport(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, CapDataExport) {
		writeJSONCapabilityDenied(w, CapDataExport)
		return
	}
	if p.exportHandler == nil {
		// Fail closed: unreachable via the gate (a grant implying
		// data:export without a handler is a construction panic), but
		// WithDevGrantAll bypasses the gate — guard anyway.
		writeJSONError(w, http.StatusInternalServerError, "E_EXPORT",
			"no export handler configured (supply WithExportHandler)")
		return
	}
	var body struct {
		DocID   string      `json:"docId"`
		Format  string      `json:"format"`
		Columns []Column    `json:"columns"`
		Sort    []SortModel `json:"sort"`
		Filter  string      `json:"filter"`
	}
	if !decodeEnvelope(w, r, &body) {
		return
	}
	if body.DocID == "" {
		body.DocID = defaultDocID
	}
	if strings.ToLower(strings.TrimSpace(body.Format)) != "csv" {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST",
			"unknown export format (only \"csv\")")
		return
	}
	if err := validateSortModel(body.Sort); err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", err.Error())
		return
	}
	if len(body.Filter) > maxFilterLen {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", "filter too long")
		return
	}
	cols, err := normalizeColumns(body.Columns)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_REQUEST", err.Error())
		return
	}

	tmp, rowCount, err := p.streamCSV(r.Context(), body.DocID, cols, body.Sort, body.Filter)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_EXPORT",
			"rows source failed during the export scan: "+err.Error())
		return
	}
	// The spill file is the export's whole life: the handler reads it to the
	// end (io.Copy / io.ReadAll), and it is gone the moment the handler
	// returns — success or failure.
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	url, err := p.exportHandler(r.Context(), ExportRequest{
		DocID:    body.DocID,
		Format:   "csv",
		Columns:  cols,
		Sort:     body.Sort,
		Filter:   body.Filter,
		CSV:      tmp,
		RowCount: rowCount,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_EXPORT", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url, "rowCount": rowCount})
}

// streamCSV pages through the rows source under the given sort/filter and
// serialises the whole result as CSV into a fresh temp file, returned rewound
// and open for reading. Paging with a running file keeps peak memory at one
// chunk regardless of table size, and is safe because a deterministic source
// under a fixed sort/filter returns identical slices per window. Every
// header and value passes [sanitizeCSVCell] on the way in.
func (p *Plugin) streamCSV(ctx context.Context, docID string, cols []Column, sort []SortModel, filter string) (*os.File, int, error) {
	f, err := os.CreateTemp("", "datagrid-export-*.csv")
	if err != nil {
		return nil, 0, fmt.Errorf("create export spill file: %w", err)
	}
	discard := func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}
	w := csv.NewWriter(f)
	if len(cols) > 0 {
		header := make([]string, len(cols))
		for i, c := range cols {
			header[i] = sanitizeCSVCell(c.Header)
		}
		_ = w.Write(header)
	}
	total := 0
	for start := 0; ; start += exportPageSize {
		page, err := p.rowsSource(ctx, RowsQuery{
			DocID:    docID,
			StartRow: start,
			EndRow:   start + exportPageSize,
			Sort:     sort,
			Filter:   filter,
			Columns:  cols,
		})
		if err != nil {
			discard()
			return nil, 0, err
		}
		for _, row := range page.Rows {
			rec := make([]string, len(cols))
			for i, c := range cols {
				rec[i] = sanitizeCSVCell(row.Cells[c.Field])
			}
			_ = w.Write(rec)
			total++
		}
		if len(page.Rows) == 0 {
			break
		}
		if page.LastRow >= 0 && start+len(page.Rows) >= page.LastRow {
			break
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		discard()
		return nil, 0, fmt.Errorf("write export spill file: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		discard()
		return nil, 0, fmt.Errorf("rewind export spill file: %w", err)
	}
	return f, total, nil
}

// sanitizeCSVCell neutralises CSV formula injection: a field that starts with
// =, +, -, @, a tab or a carriage return is evaluated as a formula by
// spreadsheet clients (Excel, Sheets) when the exported file is opened, so
// such fields are prefixed with a single quote — the standard mitigation.
// Headers get the same treatment as values: both are attacker-controllable
// (headers ride in the doc). The tradeoff is that legitimately signed-looking
// strings (a leading "-") export quoted; these are display strings, and
// spreadsheet safety wins.
func sanitizeCSVCell(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// --- /save -----------------------------------------------------------------

// handleSave implements POST SaveURL: the view-state doc persist signal. It
// gates on data:write — a mount without cell editing does not persist view
// state either; the doc still round-trips through the hidden form field.
//
// The persisted record is the VALIDATED, normalised doc: columns normalised,
// pageSize clamped, sort/filter bounds applied (the same bounds /rows
// enforces). Persisting the raw body instead would let a saved pageSize of
// 100000 return 200 here and then explode on the next load, when the frame
// asks for a block /rows must refuse.
func (p *Plugin) handleSave(w http.ResponseWriter, r *http.Request) {
	if !p.allow(r, CapDataWrite) {
		writeJSONCapabilityDenied(w, CapDataWrite)
		return
	}
	if p.cellWriteHandl == nil || p.saveHandler == nil {
		// Fail closed: view-state persistence is data:write egress, backed
		// by the same wiring as /cell (New panics on a granted data:write
		// without a cell-write handler, and the save store defaults to
		// memSave). WithDevGrantAll bypasses the gate — it must not
		// silently write through a default store nobody wired.
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE",
			"view-state persistence is not wired (supply WithCellWriteHandler)")
		return
	}
	var body struct {
		DocID         string          `json:"docId"`
		Doc           json.RawMessage `json:"doc"`
		SchemaVersion string          `json:"schemaVersion"`
	}
	if !decodeEnvelope(w, r, &body) {
		return
	}
	if body.DocID == "" {
		body.DocID = defaultDocID
	}
	if body.SchemaVersion == "" {
		body.SchemaVersion = SchemaVersion
	}
	// A malformed doc IS rejected: unlike pdf's annotation overlay, this
	// plugin fully owns the schema, so there is no "save verbatim anyway"
	// for a shape it cannot parse.
	var doc Doc
	if len(body.Doc) > 0 && string(body.Doc) != "null" {
		if err := json.Unmarshal(body.Doc, &doc); err != nil {
			writeJSONError(w, http.StatusBadRequest, "E_BAD_DOC", err.Error())
			return
		}
	}
	// The same bounds /rows enforces, applied on the save path: a doc that
	// could not be served must not be persistable either.
	if err := validateSortModel(doc.Sort); err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_DOC", err.Error())
		return
	}
	if len(doc.Filter) > maxFilterLen {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_DOC", "filter too long")
		return
	}
	cols, err := normalizeColumns(doc.Columns)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_DOC", err.Error())
		return
	}
	doc.Columns = cols
	if doc.PageSize < 1 || doc.PageSize > maxPageSize {
		doc.PageSize = defaultPageSize
	}
	if doc.SchemaVersion == "" {
		doc.SchemaVersion = body.SchemaVersion
	}
	// Persist what was validated: canonical JSON of the normalised doc, not
	// the raw request body.
	docJSON, err := json.Marshal(doc)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE", err.Error())
		return
	}
	if err := p.saveHandler(r.Context(), SaveRequest{
		DocID:         body.DocID,
		Doc:           doc,
		DocJSON:       string(docJSON),
		SchemaVersion: body.SchemaVersion,
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "E_SAVE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "docId": body.DocID})
}

// --- shared helpers --------------------------------------------------------

// decodeEnvelope reads ONE JSON value into dst under the envelope cap and
// rejects any non-whitespace after it — a second object or stray bytes.
// Decoding once and never checking for EOF would accept a valid value followed
// by 60 KiB of trailing noise; the envelope is exactly one value.
//
// Trailing WHITESPACE is allowed. A trailing newline is what `curl -d @body.json`
// and most pretty-printers send, and refusing it buys nothing: the DoS concern
// is bytes on the wire, and MaxBytesReader already caps those at
// maxEnvelopeBytes whether they are padding or payload. ok=false means the
// error response is already written.
func decodeEnvelope(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxEnvelopeBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_JSON", err.Error())
		return false
	}
	rest, err := io.ReadAll(io.MultiReader(dec.Buffered(), r.Body))
	if err != nil || len(bytes.TrimSpace(rest)) > 0 {
		writeJSONError(w, http.StatusBadRequest, "E_BAD_JSON",
			"trailing data after the JSON value")
		return false
	}
	return true
}

// writeJSON emits a JSON response. nil slices become [] / null per the
// encoder; payload types above pre-normalise what the frame expects.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONError emits the canonical {error, message?} error envelope. Every
// route denies with a stable machine-readable code so the adapter and tests
// can branch on it without parsing free text.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	body := map[string]string{"error": code}
	if message != "" {
		body["message"] = message
	}
	writeJSON(w, status, body)
}

// writeJSONCapabilityDenied delegates to the platform helper so every route
// denies uniformly with the offending capability named.
func writeJSONCapabilityDenied(w http.ResponseWriter, capability string) {
	pluginhost.WriteCapabilityDenied(w, capability)
}
