package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Public demo endpoints: development only. Production needs a managed or self-hosted service.
const (
	defaultPhotonURL    = "https://photon.komoot.io"
	defaultNominatimURL = "https://nominatim.openstreetmap.org"
	geocoderUserAgent   = "ArahRide-demo/0.1 (development)"
)

var (
	geocoderTimeout = 4 * time.Second
	// Nominatim usage policy allows at most one request per second.
	nominatimMinInterval = time.Second
	nominatimMu          sync.Mutex
	nominatimLast        time.Time
)

type PlaceResult struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Point       Point  `json:"point"`
}

type photonResponse struct {
	Features []struct {
		Geometry struct {
			Coordinates []float64 `json:"coordinates"`
		} `json:"geometry"`
		Properties struct {
			Name        string `json:"name"`
			Street      string `json:"street"`
			HouseNumber string `json:"housenumber"`
			District    string `json:"district"`
			City        string `json:"city"`
			State       string `json:"state"`
		} `json:"properties"`
	} `json:"features"`
}

func (p photonResponse) places() []PlaceResult {
	results := []PlaceResult{}
	for _, feature := range p.Features {
		if len(feature.Geometry.Coordinates) < 2 {
			continue
		}
		point := Point{Latitude: feature.Geometry.Coordinates[1], Longitude: feature.Geometry.Coordinates[0]}
		if !validPoint(&point) {
			continue
		}
		props := feature.Properties
		label := strings.TrimSpace(props.Name)
		street := strings.TrimSpace(props.Street)
		if label == "" {
			label = strings.TrimSpace(street + " " + strings.TrimSpace(props.HouseNumber))
		}
		if strings.TrimSpace(props.Name) == "" {
			street = ""
		}
		var parts []string
		seen := map[string]bool{}
		for _, part := range []string{label, street, props.District, props.City, props.State} {
			part = strings.TrimSpace(part)
			if part != "" && !seen[part] {
				seen[part] = true
				parts = append(parts, part)
			}
		}
		if len(parts) == 0 {
			continue
		}
		if label == "" {
			label = parts[0]
		}
		results = append(results, PlaceResult{Name: label, DisplayName: strings.Join(parts, ", "), Point: point})
	}
	return results
}

func geocoderBase(defaultURL string) string {
	if base := os.Getenv("ARAH_GEOCODER_URL"); base != "" {
		return strings.TrimRight(base, "/")
	}
	return defaultURL
}

// fetchGeocoder GETs base+path and decodes a 2xx JSON body into out. It never exposes upstream bodies.
func fetchGeocoder(ctx context.Context, base, path string, params url.Values, out any) error {
	ctx, cancel := context.WithTimeout(ctx, geocoderTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", geocoderUserAgent)
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return errors.New("geocoder upstream status")
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out)
}

func geocodeSearch(ctx context.Context, query string, center *Point) ([]PlaceResult, error) {
	if os.Getenv("ARAH_GEOCODER_PROVIDER") == "nominatim" {
		params := url.Values{"q": {query}, "format": {"jsonv2"}, "limit": {"8"}, "accept-language": {"id"}, "countrycodes": {"id"}}
		if center != nil {
			// Soft bias (bounded=0 by default): roughly a 0.5 degree box around the centre.
			params.Set("viewbox", fmt.Sprintf("%.6f,%.6f,%.6f,%.6f", center.Longitude-0.25, center.Latitude+0.25, center.Longitude+0.25, center.Latitude-0.25))
		}
		var items []nominatimItem
		if err := fetchNominatim(ctx, "/search", params, &items); err != nil {
			return nil, err
		}
		return nominatimPlaces(items), nil
	}
	params := url.Values{"q": {query}, "limit": {"8"}, "lang": {"id"}}
	if center != nil {
		params.Set("lat", strconv.FormatFloat(center.Latitude, 'f', -1, 64))
		params.Set("lon", strconv.FormatFloat(center.Longitude, 'f', -1, 64))
	}
	return fetchPhoton(ctx, "/api", params)
}

func geocodeReverse(ctx context.Context, point Point) ([]PlaceResult, error) {
	lat, lon := strconv.FormatFloat(point.Latitude, 'f', -1, 64), strconv.FormatFloat(point.Longitude, 'f', -1, 64)
	if os.Getenv("ARAH_GEOCODER_PROVIDER") == "nominatim" {
		var item nominatimItem
		if err := fetchNominatim(ctx, "/reverse", url.Values{"lat": {lat}, "lon": {lon}, "format": {"jsonv2"}, "accept-language": {"id"}}, &item); err != nil {
			return nil, err
		}
		return nominatimPlaces([]nominatimItem{item}), nil
	}
	return fetchPhoton(ctx, "/reverse", url.Values{"lat": {lat}, "lon": {lon}, "lang": {"id"}, "limit": {"1"}})
}

func fetchPhoton(ctx context.Context, path string, params url.Values) ([]PlaceResult, error) {
	var decoded photonResponse
	if err := fetchGeocoder(ctx, geocoderBase(defaultPhotonURL), path, params, &decoded); err != nil {
		return nil, err
	}
	return decoded.places(), nil
}

type nominatimItem struct {
	Lat         string `json:"lat"`
	Lon         string `json:"lon"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

func fetchNominatim(ctx context.Context, path string, params url.Values, out any) error {
	if nominatimMinInterval > 0 {
		nominatimMu.Lock()
		wait := nominatimMinInterval - time.Since(nominatimLast)
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				nominatimMu.Unlock()
				return ctx.Err()
			}
		}
		nominatimLast = time.Now()
		nominatimMu.Unlock()
	}
	return fetchGeocoder(ctx, geocoderBase(defaultNominatimURL), path, params, out)
}

// isNominatimNoise reports postcodes and RT/RW neighbourhood units, which add nothing to a label.
func isNominatimNoise(part string) bool {
	if len(part) == 5 && strings.Trim(part, "0123456789") == "" {
		return true
	}
	for _, prefix := range []string{"RW ", "RT "} {
		if rest, ok := strings.CutPrefix(part, prefix); ok && rest != "" && strings.Trim(rest, "0123456789") == "" {
			return true
		}
	}
	return false
}

// nominatimPlaces maps items to results, skipping unusable ones (including reverse's {"error": ...}).
func nominatimPlaces(items []nominatimItem) []PlaceResult {
	results := []PlaceResult{}
	for _, item := range items {
		lat, latErr := strconv.ParseFloat(item.Lat, 64)
		lon, lonErr := strconv.ParseFloat(item.Lon, 64)
		point := Point{Latitude: lat, Longitude: lon}
		if latErr != nil || lonErr != nil || !validPoint(&point) {
			continue
		}
		var parts []string
		for _, part := range strings.Split(item.DisplayName, ",") {
			part = strings.TrimSpace(part)
			if part == "" || part == "Indonesia" || isNominatimNoise(part) {
				continue
			}
			parts = append(parts, part)
		}
		if len(parts) == 0 {
			continue
		}
		if len(parts) > 4 {
			parts = parts[:4]
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = parts[0]
		}
		results = append(results, PlaceResult{Name: name, DisplayName: strings.Join(parts, ", "), Point: point})
	}
	return results
}

func parseCoordinates(latText, lonText string) (Point, bool) {
	lat, latErr := strconv.ParseFloat(latText, 64)
	lon, lonErr := strconv.ParseFloat(lonText, 64)
	point := Point{Latitude: lat, Longitude: lon}
	return point, latErr == nil && lonErr == nil && validPoint(&point)
}

func searchPlacesHandler(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if n := utf8.RuneCountInString(query); n < 2 || n > 160 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Kata kunci harus 2 sampai 160 karakter"})
		return
	}
	var center *Point
	latText, lonText := r.URL.Query().Get("lat"), r.URL.Query().Get("lon")
	if latText != "" || lonText != "" {
		point, ok := parseCoordinates(latText, lonText)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Titik pusat pencarian tidak valid"})
			return
		}
		center = &point
	}
	results, err := geocodeSearch(r.Context(), query, center)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Layanan pencarian lokasi sedang tidak tersedia"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func reversePlaceHandler(w http.ResponseWriter, r *http.Request) {
	latText, lonText := r.URL.Query().Get("lat"), r.URL.Query().Get("lon")
	point, ok := parseCoordinates(latText, lonText)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Titik lokasi tidak valid"})
		return
	}
	results, err := geocodeReverse(r.Context(), point)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Layanan pencarian lokasi sedang tidak tersedia"})
		return
	}
	if len(results) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Lokasi tidak ditemukan"})
		return
	}
	writeJSON(w, http.StatusOK, results[0])
}
