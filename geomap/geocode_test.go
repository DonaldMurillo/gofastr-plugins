package geomap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DonaldMurillo/gofastr/battery/auth"
)

// geocodeBody is the {"results":[…]} envelope map.js reads.
type geocodeBody struct {
	Results []GeocodeResult `json:"results"`
}

func getGeocode(t *testing.T, srv *httptest.Server, query string) (int, geocodeBody) {
	t.Helper()
	resp, err := http.Get(srv.URL + GeocodeURL + "?q=" + query)
	if err != nil {
		t.Fatalf("GET %s: %v", GeocodeURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var body geocodeBody
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body
}

// TestGeocodeRouteAbsentWithoutSearch is the opt-in guarantee: a plugin that
// never calls WithSearch must not expose an egress endpoint at all. This is the
// difference between "search is off" and "search is on but empty".
func TestGeocodeRouteAbsentWithoutSearch(t *testing.T) {
	app, p := newTestApp(t, WithDevGrantAll())
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	status, _ := getGeocode(t, srv, "london")
	if status == http.StatusOK {
		t.Errorf("geocode route answered %d with search disabled; it must not be registered", status)
	}
	if p.DefaultConfig().SearchURL != "" {
		t.Errorf("SearchURL=%q with search disabled; want empty so map.js renders no search control",
			p.DefaultConfig().SearchURL)
	}
	for _, c := range p.Capabilities() {
		if c == CapGeocode {
			t.Errorf("capability %q advertised with search disabled", CapGeocode)
		}
	}
}

// TestGeocodeReturnsResults covers the happy path through a host-supplied
// geocoder, including the wire shape the search control depends on.
func TestGeocodeReturnsResults(t *testing.T) {
	app, p := newTestApp(t, WithDevGrantAll(), WithGeocoder(
		func(_ context.Context, q string) ([]GeocodeResult, error) {
			return []GeocodeResult{{Label: "Tokyo, Japan (" + q + ")", Lat: 35.6762, Lng: 139.6503}}, nil
		}))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	status, body := getGeocode(t, srv, "tokyo")
	if status != http.StatusOK {
		t.Fatalf("status=%d want 200", status)
	}
	if len(body.Results) != 1 {
		t.Fatalf("got %d results want 1", len(body.Results))
	}
	got := body.Results[0]
	if got.Lat != 35.6762 || got.Lng != 139.6503 {
		t.Errorf("coords=(%v,%v) want (35.6762,139.6503)", got.Lat, got.Lng)
	}
	if !strings.Contains(got.Label, "Tokyo") {
		t.Errorf("label=%q want it to carry the place name", got.Label)
	}
	// WithGeocoder implies WithSearch, so the mount config must advertise the URL.
	if p.DefaultConfig().SearchURL != GeocodeURL {
		t.Errorf("SearchURL=%q want %q", p.DefaultConfig().SearchURL, GeocodeURL)
	}
}

// TestGeocodeRejectsEmptyQuery — an empty q is a client bug, not an empty result
// set; answering 200 [] would hide it.
func TestGeocodeRejectsEmptyQuery(t *testing.T) {
	app, _ := newTestApp(t, WithDevGrantAll(), WithGeocoder(
		func(context.Context, string) ([]GeocodeResult, error) {
			t.Error("geocoder must not be called for an empty query")
			return nil, nil
		}))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	if status, _ := getGeocode(t, srv, ""); status != http.StatusBadRequest {
		t.Errorf("status=%d want 400 for an empty query", status)
	}
}

// TestGeocodeCapabilityGate proves the endpoint is gated exactly like save: an
// authenticated caller whose scopes do not grant geocode:search is refused, and
// the geocoder is never reached. Search is network egress, so an ungated proxy
// would be an open relay to the upstream geocoder. (Mirrors
// TestSaveDeniedWithoutCapability's shape — the gate only engages for a caller
// that HAS an authority to intersect against.)
func TestGeocodeCapabilityGate(t *testing.T) {
	var calls int32
	p := New(WithGeocoder(func(context.Context, string) ([]GeocodeResult, error) {
		atomic.AddInt32(&calls, 1)
		return nil, nil
	}))

	deniedCtx := auth.WithTokenScopes(context.Background(), []string{"posts:read"})
	req := httptest.NewRequest(http.MethodGet, GeocodeURL+"?q=london", nil).WithContext(deniedCtx)
	rr := httptest.NewRecorder()
	p.handleGeocode(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 for a non-granting token", rr.Code)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("geocoder called %d times behind a closed gate; want 0", n)
	}

	// And the capability IS advertised, so a caller that holds it gets through.
	grantedCtx := auth.WithTokenScopes(context.Background(), []string{CapGeocode})
	grantedReq := httptest.NewRequest(http.MethodGet, GeocodeURL+"?q=london", nil).WithContext(grantedCtx)
	rr = httptest.NewRecorder()
	p.handleGeocode(rr, grantedReq)
	if rr.Code != http.StatusOK {
		t.Errorf("status=%d want 200 for a caller holding %s", rr.Code, CapGeocode)
	}
}

// TestGeocodeCachesResults — Nominatim's usage policy explicitly asks callers to
// cache. A repeated query must not reach the geocoder twice.
func TestGeocodeCachesResults(t *testing.T) {
	var calls int32
	app, _ := newTestApp(t, WithDevGrantAll(), WithGeocoder(
		func(context.Context, string) ([]GeocodeResult, error) {
			atomic.AddInt32(&calls, 1)
			return []GeocodeResult{{Label: "London", Lat: 51.5, Lng: -0.12}}, nil
		}))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	for i := 0; i < 3; i++ {
		if status, body := getGeocode(t, srv, "london"); status != http.StatusOK || len(body.Results) != 1 {
			t.Fatalf("call %d: status=%d results=%d", i, status, len(body.Results))
		}
	}
	// Case-insensitively too — "London" and "london" are the same lookup.
	if status, _ := getGeocode(t, srv, "London"); status != http.StatusOK {
		t.Fatalf("mixed-case query status=%d", status)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("geocoder called %d times for the same query; want 1 (cached)", n)
	}
}

// TestNominatimGeocodeParsesStringCoords is the regression guard for the trap in
// Nominatim's response: lat/lon come back as STRINGS. Decoding them straight
// into float64 fields yields silent zeros — a pin off the coast of Africa.
func TestNominatimGeocodeParsesStringCoords(t *testing.T) {
	var gotUA, gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotQuery = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"display_name":"Reykjavík, Iceland","lat":"64.1466","lon":"-21.9426"},
		                           {"display_name":"broken","lat":"not-a-number","lon":"0"}]`)
	}))
	defer upstream.Close()

	app, _ := newTestApp(t, WithDevGrantAll(),
		WithGeocodeEndpoint(upstream.URL),
		WithGeocodeUserAgent("geomap-test/1.0 (+https://example.test)"))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	status, body := getGeocode(t, srv, "reykjavik")
	if status != http.StatusOK {
		t.Fatalf("status=%d want 200", status)
	}
	// The unparseable row is dropped, not surfaced as (0,0).
	if len(body.Results) != 1 {
		t.Fatalf("got %d results want 1 (the row with a non-numeric lat must be dropped)", len(body.Results))
	}
	if body.Results[0].Lat != 64.1466 || body.Results[0].Lng != -21.9426 {
		t.Errorf("coords=(%v,%v) want (64.1466,-21.9426)", body.Results[0].Lat, body.Results[0].Lng)
	}
	if gotQuery != "reykjavik" {
		t.Errorf("upstream q=%q want %q", gotQuery, "reykjavik")
	}
	// Nominatim blocks anonymous traffic; the identifying UA is not optional.
	if gotUA != "geomap-test/1.0 (+https://example.test)" {
		t.Errorf("upstream User-Agent=%q want the configured identity", gotUA)
	}
}

// TestNominatimUpstreamFailureIs502 — an upstream that errors is a gateway
// failure, not "no matches". The distinction is what lets the search box say
// "search failed" instead of silently showing nothing.
func TestNominatimUpstreamFailureIs502(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	app, _ := newTestApp(t, WithDevGrantAll(), WithGeocodeEndpoint(upstream.URL))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	if status, _ := getGeocode(t, srv, "nowhere"); status != http.StatusBadGateway {
		t.Errorf("status=%d want 502 for an upstream failure", status)
	}
}

// TestDefaultGeocodeUserAgentIsSet guards the policy floor: the default path must
// never go out anonymous, even if the host forgets WithGeocodeUserAgent.
func TestDefaultGeocodeUserAgentIsSet(t *testing.T) {
	p := New(WithSearch())
	if p.geocodeUA == "" {
		t.Fatal("default User-Agent is empty; Nominatim blocks anonymous traffic")
	}
	if !strings.Contains(p.geocodeUA, Version) {
		t.Errorf("default User-Agent=%q want it to carry the plugin version", p.geocodeUA)
	}
	if p.geocodeEndpoint != defaultGeocodeEndpoint {
		t.Errorf("endpoint=%q want %q", p.geocodeEndpoint, defaultGeocodeEndpoint)
	}
}

// TestReserveUpstreamSlotSpacesRequests covers the 1-request-per-second budget
// Nominatim's policy imposes on the whole application. The first caller goes
// immediately; the next is handed a slot one interval later.
func TestReserveUpstreamSlotSpacesRequests(t *testing.T) {
	p := New(WithSearch())
	ctx := context.Background()

	start := time.Now()
	if err := p.reserveUpstreamSlot(ctx); err != nil {
		t.Fatalf("first slot: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("first slot waited %v; want immediate", elapsed)
	}

	start = time.Now()
	if err := p.reserveUpstreamSlot(ctx); err != nil {
		t.Fatalf("second slot: %v", err)
	}
	// Allow generous slack for a loaded CI box; the point is that it WAITED.
	if elapsed := time.Since(start); elapsed < geocodeMinInterval-50*time.Millisecond {
		t.Errorf("second slot waited %v; want ~%v", elapsed, geocodeMinInterval)
	}
}

// TestReserveUpstreamSlotRefusesLongQueue — under a burst, requests that would
// queue past the ceiling are refused rather than piling up sleeping goroutines.
func TestReserveUpstreamSlotRefusesLongQueue(t *testing.T) {
	p := New(WithSearch())
	// Saturate the schedule well past the ceiling without actually sleeping.
	p.geoNext = time.Now().Add(geocodeMaxWait + time.Second)

	if err := p.reserveUpstreamSlot(context.Background()); err == nil {
		t.Fatal("reserveUpstreamSlot succeeded with a saturated schedule; want errGeocodeBusy")
	}
}

// TestGeocodeRateLimitedIs429 maps the busy path onto the status a client can
// act on — "back off", not "no results" and not "we broke".
func TestGeocodeRateLimitedIs429(t *testing.T) {
	app, p := newTestApp(t, WithDevGrantAll(), WithGeocodeEndpoint("http://127.0.0.1:1/never-called"))
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	p.geoNext = time.Now().Add(geocodeMaxWait + time.Second)

	status, _ := getGeocode(t, srv, "busy")
	if status != http.StatusTooManyRequests {
		t.Errorf("status=%d want 429 when the rate-limit queue is saturated", status)
	}
}
