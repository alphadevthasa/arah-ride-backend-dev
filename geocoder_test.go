package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const photonSample = `{"type":"FeatureCollection","features":[
{"geometry":{"type":"Point","coordinates":[106.8227,-6.1754]},"properties":{"name":"Monumen Nasional","street":"Jalan Medan Merdeka","city":"Jakarta Pusat","state":"DKI Jakarta","country":"Indonesia"}},
{"geometry":{"type":"Point","coordinates":[106.83,-6.18]},"properties":{"street":"Jalan Kebon Sirih","housenumber":"12","district":"Gambir","city":"Jakarta","country":"Indonesia"}}]}`

func fakePhoton(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv("ARAH_GEOCODER_URL", server.URL)
	return server, &calls
}

func getPlaces(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	routes(newHub()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestSearchMapsPhotonFeaturesAndForwardsParameters(t *testing.T) {
	var got url.Values
	var path string
	fakePhoton(t, func(w http.ResponseWriter, r *http.Request) {
		got, path = r.URL.Query(), r.URL.Path
		w.Write([]byte(photonSample))
	})
	rec := getPlaces(t, "/v1/places/search?q="+url.QueryEscape("  Monas Jakarta ")+"&lat=-6.2&lon=106.8")
	if rec.Code != 200 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	if path != "/api" || got.Get("q") != "Monas Jakarta" || got.Get("limit") != "8" || got.Get("lang") != "id" || got.Get("lat") != "-6.2" || got.Get("lon") != "106.8" {
		t.Fatalf("upstream %s %v", path, got)
	}
	var body struct{ Results []PlaceResult }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Results) != 2 {
		t.Fatalf("results = %+v", body.Results)
	}
	first, second := body.Results[0], body.Results[1]
	if first.Name != "Monumen Nasional" || first.DisplayName != "Monumen Nasional, Jalan Medan Merdeka, Jakarta Pusat, DKI Jakarta" || first.Point != (Point{-6.1754, 106.8227}) {
		t.Fatalf("first = %+v", first)
	}
	if second.Name != "Jalan Kebon Sirih 12" || second.DisplayName != "Jalan Kebon Sirih 12, Gambir, Jakarta" {
		t.Fatalf("second = %+v", second)
	}
}

func TestSearchRejectsInvalidInputWithoutCallingUpstream(t *testing.T) {
	_, calls := fakePhoton(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(photonSample)) })
	long := strings.Repeat("é", 161)
	for _, q := range []string{"", "   ", "a", " a ", long} {
		if rec := getPlaces(t, "/v1/places/search?q="+url.QueryEscape(q)); rec.Code != 400 {
			t.Fatalf("q=%q status = %d", q, rec.Code)
		}
	}
	for _, extra := range []string{"&lat=abc&lon=106", "&lat=95&lon=106", "&lat=-6"} {
		if rec := getPlaces(t, "/v1/places/search?q=monas"+extra); rec.Code != 400 {
			t.Fatalf("%s status = %d", extra, rec.Code)
		}
	}
	if rec := getPlaces(t, "/v1/places/search?q="+url.QueryEscape(strings.Repeat("é", 160))); rec.Code != 200 {
		t.Fatalf("160 code points status = %d", rec.Code)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("upstream calls = %d, want 1", n)
	}
}

func TestSearchNoFeaturesReturnsEmptyResultsArray(t *testing.T) {
	fakePhoton(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"type":"FeatureCollection","features":[]}`))
	})
	rec := getPlaces(t, "/v1/places/search?q=zzzzzz")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"results":[]}` {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
}

func TestReverseMapsFeatureAndReportsNoMatch(t *testing.T) {
	var got url.Values
	var path string
	fakePhoton(t, func(w http.ResponseWriter, r *http.Request) {
		got, path = r.URL.Query(), r.URL.Path
		if got.Get("lat") == "0" {
			w.Write([]byte(`{"features":[]}`))
			return
		}
		w.Write([]byte(photonSample))
	})
	rec := getPlaces(t, "/v1/places/reverse?lat=-6.1754&lon=106.8227")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var place PlaceResult
	json.Unmarshal(rec.Body.Bytes(), &place)
	if path != "/reverse" || got.Get("lang") != "id" || got.Get("lat") != "-6.1754" || got.Get("lon") != "106.8227" || place.Name != "Monumen Nasional" {
		t.Fatalf("path=%s query=%v place=%+v", path, got, place)
	}
	if rec := getPlaces(t, "/v1/places/reverse?lat=0&lon=0"); rec.Code != 404 {
		t.Fatalf("no-match status = %d", rec.Code)
	}
	for _, q := range []string{"", "?lat=91&lon=0", "?lat=x&lon=0", "?lat=1"} {
		if rec := getPlaces(t, "/v1/places/reverse"+q); rec.Code != 400 {
			t.Fatalf("%q status = %d", q, rec.Code)
		}
	}
}

func TestUpstreamFailuresReturnBadGatewayWithoutProviderText(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"non-2xx":   func(w http.ResponseWriter, r *http.Request) { http.Error(w, "PROVIDER-SECRET-ERROR", 500) },
		"malformed": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{PROVIDER-SECRET-ERROR")) },
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			fakePhoton(t, handler)
			for _, path := range []string{"/v1/places/search?q=monas", "/v1/places/reverse?lat=-6.2&lon=106.8"} {
				rec := getPlaces(t, path)
				if rec.Code != 502 || strings.Contains(rec.Body.String(), "PROVIDER-SECRET") {
					t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body)
				}
			}
		})
	}
}

func TestUpstreamTimeoutReturnsBadGateway(t *testing.T) {
	old := geocoderTimeout
	geocoderTimeout = 50 * time.Millisecond
	t.Cleanup(func() { geocoderTimeout = old })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	fakePhoton(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	if rec := getPlaces(t, "/v1/places/search?q=monas"); rec.Code != 502 {
		t.Fatalf("status = %d", rec.Code)
	}
}

const nominatimSearchSample = `[
{"lat":"-6.1766","lon":"106.8306","name":"Stasiun Gambir","display_name":"Stasiun Gambir, Jalan Medan Merdeka Timur, Gambir, Jakarta Pusat, Daerah Khusus Ibukota Jakarta, 10110, Indonesia"},
{"lat":"-6.18","lon":"106.83","name":"","display_name":"Jalan Kebon Sirih, Menteng, Jakarta Pusat, Daerah Khusus Ibukota Jakarta, Indonesia"}]`

func fakeNominatim(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Setenv("ARAH_GEOCODER_PROVIDER", "nominatim")
	t.Setenv("ARAH_GEOCODER_URL", server.URL)
	old := nominatimMinInterval
	nominatimMinInterval = 0
	t.Cleanup(func() { nominatimMinInterval = old })
}

func TestNominatimSearchMapsResultsAndSendsPolicyHeaders(t *testing.T) {
	var got url.Values
	var path, agent string
	fakeNominatim(t, func(w http.ResponseWriter, r *http.Request) {
		got, path, agent = r.URL.Query(), r.URL.Path, r.Header.Get("User-Agent")
		w.Write([]byte(nominatimSearchSample))
	})
	rec := getPlaces(t, "/v1/places/search?q="+url.QueryEscape("gambir")+"&lat=-6.2&lon=106.8")
	if rec.Code != 200 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	if path != "/search" || got.Get("q") != "gambir" || got.Get("format") != "jsonv2" || got.Get("limit") != "8" || got.Get("accept-language") != "id" || got.Get("countrycodes") != "id" || got.Get("viewbox") == "" {
		t.Fatalf("upstream %s %v", path, got)
	}
	if !strings.Contains(agent, "ArahRide") {
		t.Fatalf("User-Agent = %q", agent)
	}
	var body struct{ Results []PlaceResult }
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Results) != 2 {
		t.Fatalf("results = %+v", body.Results)
	}
	first, second := body.Results[0], body.Results[1]
	if first.Name != "Stasiun Gambir" || first.DisplayName != "Stasiun Gambir, Jalan Medan Merdeka Timur, Gambir, Jakarta Pusat" || first.Point != (Point{-6.1766, 106.8306}) {
		t.Fatalf("first = %+v", first)
	}
	if second.Name != "Jalan Kebon Sirih" || second.DisplayName != "Jalan Kebon Sirih, Menteng, Jakarta Pusat, Daerah Khusus Ibukota Jakarta" {
		t.Fatalf("second = %+v", second)
	}
}

func TestNominatimReverseMapsPlaceAndNoMatchIs404(t *testing.T) {
	var path string
	fakeNominatim(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if r.URL.Query().Get("lat") == "0" {
			w.Write([]byte(`{"error":"Unable to geocode"}`))
			return
		}
		w.Write([]byte(`{"lat":"-6.1754","lon":"106.8272","name":"","display_name":"Jalan Medan Merdeka Barat, Gambir, Jakarta Pusat, Indonesia"}`))
	})
	rec := getPlaces(t, "/v1/places/reverse?lat=-6.1754&lon=106.8272")
	var place PlaceResult
	json.Unmarshal(rec.Body.Bytes(), &place)
	if rec.Code != 200 || path != "/reverse" || place.Name != "Jalan Medan Merdeka Barat" || place.DisplayName != "Jalan Medan Merdeka Barat, Gambir, Jakarta Pusat" {
		t.Fatalf("status=%d path=%s place=%+v", rec.Code, path, place)
	}
	if rec := getPlaces(t, "/v1/places/reverse?lat=0&lon=0"); rec.Code != 404 {
		t.Fatalf("no-match status = %d", rec.Code)
	}
}

func TestNominatimEmptyAndFailureModes(t *testing.T) {
	fakeNominatim(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	if rec := getPlaces(t, "/v1/places/search?q=zzzz"); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"results":[]}` {
		t.Fatalf("empty status=%d body=%s", rec.Code, rec.Body)
	}
	for name, handler := range map[string]http.HandlerFunc{
		"non-2xx":   func(w http.ResponseWriter, r *http.Request) { http.Error(w, "PROVIDER-SECRET-ERROR", 429) },
		"malformed": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("[PROVIDER-SECRET-ERROR")) },
	} {
		t.Run(name, func(t *testing.T) {
			fakeNominatim(t, handler)
			for _, path := range []string{"/v1/places/search?q=monas", "/v1/places/reverse?lat=-6.2&lon=106.8"} {
				rec := getPlaces(t, path)
				if rec.Code != 502 || strings.Contains(rec.Body.String(), "PROVIDER-SECRET") {
					t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body)
				}
			}
		})
	}
}

func TestNominatimDropsNeighbourhoodNoiseParts(t *testing.T) {
	places := nominatimPlaces([]nominatimItem{
		{Lat: "-6.17", Lon: "106.83", Name: "", DisplayName: "RW 02, Gambir, RT 03, Jakarta Pusat, 10110, Indonesia"},
		{Lat: "-6.17", Lon: "106.83", Name: "Stasiun Gambir", DisplayName: "Stasiun Gambir, Jalan Pejambon, RW 02, Gambir, Jakarta Pusat"},
	})
	if len(places) != 2 || places[0].Name != "Gambir" || places[0].DisplayName != "Gambir, Jakarta Pusat" || places[1].DisplayName != "Stasiun Gambir, Jalan Pejambon, Gambir, Jakarta Pusat" {
		t.Fatalf("places = %+v", places)
	}
}
