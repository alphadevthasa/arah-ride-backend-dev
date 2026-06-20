package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Point struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type Ride struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	Pickup      Point   `json:"pickup"`
	Dropoff     Point   `json:"dropoff"`
	Route       []Point `json:"route"`
	DriverName  string  `json:"driverName"`
	DriverPlate string  `json:"driverPlate"`
	PickupName  string  `json:"pickupName"`
	DropoffName string  `json:"dropoffName"`
}

type Snapshot struct {
	Type           string `json:"type"`
	Sequence       uint64 `json:"sequence"`
	DriverOnline   bool   `json:"driverOnline"`
	DriverLocation Point  `json:"driverLocation"`
	Ride           *Ride  `json:"ride"`
}

type Action struct {
	Role     string  `json:"role"`
	Action   string  `json:"action"`
	Pickup   *Point  `json:"pickup,omitempty"`
	Dropoff  *Point  `json:"dropoff,omitempty"`
	Location *Point  `json:"location,omitempty"`
	Route    []Point `json:"-"`

	PickupName  string `json:"pickupName,omitempty"`
	DropoffName string `json:"dropoffName,omitempty"`
}

type hub struct {
	mu          sync.Mutex
	state       Snapshot
	subscribers map[chan Snapshot]struct{}
}

func newHub() *hub {
	return &hub{state: Snapshot{Type: "snapshot", DriverLocation: Point{-6.2088, 106.8456}}, subscribers: make(map[chan Snapshot]struct{})}
}

func (h *hub) snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return copySnapshot(h.state)
}

func copySnapshot(s Snapshot) Snapshot {
	if s.Ride != nil {
		ride := *s.Ride
		ride.Route = append([]Point(nil), s.Ride.Route...)
		s.Ride = &ride
	}
	return s
}

func placeName(name, fallback string) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	return fallback
}

func validPoint(p *Point) bool {
	return p != nil && p.Latitude >= -90 && p.Latitude <= 90 && p.Longitude >= -180 && p.Longitude <= 180
}

func (h *hub) apply(a Action) (Snapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := &h.state
	r := s.Ride
	switch a.Action {
	case "online", "offline":
		if a.Role != "driver" {
			return Snapshot{}, errors.New("hanya driver yang dapat mengubah status online")
		}
		if a.Action == "offline" && r != nil && (r.Status != "completed" && r.Status != "cancelled") {
			return Snapshot{}, errors.New("pesanan aktif harus selesai atau dibatalkan sebelum offline")
		}
		s.DriverOnline = a.Action == "online"
	case "location":
		if a.Role != "driver" || !s.DriverOnline || !validPoint(a.Location) {
			return Snapshot{}, errors.New("lokasi driver tidak valid")
		}
		if r != nil && r.Status == "in_progress" {
			return Snapshot{}, errors.New("lokasi perjalanan sedang disimulasikan oleh server demo")
		}
		s.DriverLocation = *a.Location
	case "create":
		if a.Role != "customer" || !validPoint(a.Pickup) || !validPoint(a.Dropoff) {
			return Snapshot{}, errors.New("titik jemput atau tujuan tidak valid")
		}
		if !s.DriverOnline {
			return Snapshot{}, errors.New("belum ada driver online")
		}
		if *a.Pickup == *a.Dropoff {
			return Snapshot{}, errors.New("tujuan harus berbeda dari titik jemput")
		}
		if r != nil && r.Status != "completed" && r.Status != "cancelled" {
			return Snapshot{}, errors.New("masih ada pesanan aktif")
		}
		s.Ride = &Ride{ID: time.Now().UTC().Format("20060102150405.000000000"), Status: "requested", Pickup: *a.Pickup, Dropoff: *a.Dropoff, Route: append([]Point(nil), a.Route...), DriverName: "Dimas", DriverPlate: "B 1234 AR", PickupName: placeName(a.PickupName, "Lokasi jemput"), DropoffName: placeName(a.DropoffName, "Lokasi tujuan")}
	case "cancel":
		if a.Role != "customer" || r == nil || (r.Status != "requested" && r.Status != "accepted") {
			return Snapshot{}, errors.New("pesanan tidak dapat dibatalkan")
		}
		r.Status = "cancelled"
	case "accept":
		if a.Role != "driver" || !s.DriverOnline || r == nil || r.Status != "requested" {
			return Snapshot{}, errors.New("pesanan tidak tersedia")
		}
		r.Status = "accepted"
	case "arrive":
		if a.Role != "driver" || r == nil || r.Status != "accepted" {
			return Snapshot{}, errors.New("status pesanan tidak sesuai")
		}
		r.Status = "arrived"
	case "start":
		if a.Role != "driver" || r == nil || r.Status != "arrived" {
			return Snapshot{}, errors.New("status pesanan tidak sesuai")
		}
		r.Status = "in_progress"
		s.DriverLocation = r.Pickup
		go h.simulateRide(r.ID, r.Route, r.Pickup, r.Dropoff)
	case "complete":
		if a.Role != "driver" || r == nil || r.Status != "in_progress" {
			return Snapshot{}, errors.New("status pesanan tidak sesuai")
		}
		r.Status = "completed"
	case "reset":
		if a.Role != "customer" || r == nil || (r.Status != "completed" && r.Status != "cancelled") {
			return Snapshot{}, errors.New("pesanan belum selesai")
		}
		s.Ride = nil
	default:
		return Snapshot{}, errors.New("aksi tidak dikenal")
	}
	s.Sequence++
	result := copySnapshot(*s)
	h.broadcastLocked(result)
	return result, nil
}

func (h *hub) broadcastLocked(result Snapshot) {
	for ch := range h.subscribers {
		select {
		case ch <- result:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- result:
			default:
			}
		}
	}
}

func (h *hub) simulateRide(rideID string, route []Point, pickup, dropoff Point) {
	const duration = 16 * time.Second
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	started := time.Now()
	for range ticker.C {
		fraction := time.Since(started).Seconds() / duration.Seconds()
		if fraction > 1 {
			fraction = 1
		}
		point := pointAlongRoute(route, pickup, dropoff, fraction)
		if !h.publishSimulatedLocation(rideID, point) || fraction >= 1 {
			return
		}
	}
}

func pointAlongRoute(route []Point, pickup, dropoff Point, fraction float64) Point {
	if len(route) < 2 {
		return Point{
			Latitude:  pickup.Latitude + (dropoff.Latitude-pickup.Latitude)*fraction,
			Longitude: pickup.Longitude + (dropoff.Longitude-pickup.Longitude)*fraction,
		}
	}
	distances := make([]float64, len(route))
	for i := 1; i < len(route); i++ {
		distances[i] = distances[i-1] + distanceMeters(route[i-1], route[i])
	}
	target := distances[len(distances)-1] * fraction
	for i := 1; i < len(route); i++ {
		if distances[i] >= target {
			segmentDistance := distances[i] - distances[i-1]
			segmentFraction := 0.0
			if segmentDistance > 0 {
				segmentFraction = (target - distances[i-1]) / segmentDistance
			}
			return Point{
				Latitude:  route[i-1].Latitude + (route[i].Latitude-route[i-1].Latitude)*segmentFraction,
				Longitude: route[i-1].Longitude + (route[i].Longitude-route[i-1].Longitude)*segmentFraction,
			}
		}
	}
	return route[len(route)-1]
}

func distanceMeters(a, b Point) float64 {
	const earthRadius = 6371000.0
	lat1, lat2 := a.Latitude*math.Pi/180, b.Latitude*math.Pi/180
	dLat := (b.Latitude - a.Latitude) * math.Pi / 180
	dLon := (b.Longitude - a.Longitude) * math.Pi / 180
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadius * math.Asin(math.Sqrt(h))
}

type osrmRouteResponse struct {
	Code   string `json:"code"`
	Routes []struct {
		Geometry struct {
			Coordinates [][]float64 `json:"coordinates"`
		} `json:"geometry"`
	} `json:"routes"`
}

func fetchRoute(ctx context.Context, routingURL string, pickup, dropoff Point) ([]Point, error) {
	if routingURL == "" {
		routingURL = "https://router.project-osrm.org"
	}
	endpoint := fmt.Sprintf("%s/route/v1/driving/%.6f,%.6f;%.6f,%.6f?overview=full&geometries=geojson&steps=false",
		strings.TrimRight(routingURL, "/"), pickup.Longitude, pickup.Latitude, dropoff.Longitude, dropoff.Latitude)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("membuat permintaan rute: %w", err)
	}
	client := &http.Client{Timeout: 8 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("mengambil rute jalan: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("layanan rute mengembalikan HTTP %d", response.StatusCode)
	}
	var result osrmRouteResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("membaca geometri rute: %w", err)
	}
	if result.Code != "Ok" || len(result.Routes) == 0 || len(result.Routes[0].Geometry.Coordinates) < 2 {
		return nil, errors.New("rute jalan tidak ditemukan")
	}
	coordinates := result.Routes[0].Geometry.Coordinates
	route := make([]Point, 0, len(coordinates))
	for _, coordinate := range coordinates {
		if len(coordinate) < 2 {
			return nil, errors.New("geometri rute tidak valid")
		}
		point := Point{Latitude: coordinate[1], Longitude: coordinate[0]}
		if !validPoint(&point) {
			return nil, errors.New("geometri rute berisi koordinat tidak valid")
		}
		route = append(route, point)
	}
	route = append([]Point{pickup}, route...)
	route = append(route, dropoff)
	return route, nil
}

func (h *hub) publishSimulatedLocation(rideID string, point Point) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state.Ride == nil || h.state.Ride.ID != rideID || h.state.Ride.Status != "in_progress" {
		return false
	}
	h.state.DriverLocation = point
	h.state.Sequence++
	h.broadcastLocked(copySnapshot(h.state))
	return true
}

func (h *hub) subscribe() chan Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan Snapshot, 1)
	h.subscribers[ch] = struct{}{}
	ch <- copySnapshot(h.state)
	return ch
}

func (h *hub) unsubscribe(ch chan Snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscribers, ch)
	close(ch)
}

func routes(h *hub) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/state", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, h.snapshot()) })
	mux.HandleFunc("POST /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var action Action
		if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON tidak valid"})
			return
		}
		if action.Action == "create" && action.Role == "customer" && validPoint(action.Pickup) && validPoint(action.Dropoff) {
			route, err := fetchRoute(r.Context(), os.Getenv("ARAH_ROUTING_URL"), *action.Pickup, *action.Dropoff)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Tidak bisa membuat rute jalan: " + err.Error()})
				return
			}
			action.Route = route
		}
		state, err := h.apply(action)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, state)
	})
	mux.HandleFunc("GET /v1/places/search", searchPlacesHandler)
	mux.HandleFunc("GET /v1/places/reverse", reversePlaceHandler)
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		updates := h.subscribe()
		defer h.unsubscribe(updates)
		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case state := <-updates:
				payload, err := json.Marshal(state)
				if err != nil {
					return
				}
				writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err = conn.Write(writeCtx, websocket.MessageText, payload)
				cancel()
				if err != nil {
					return
				}
			}
		}
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func main() {
	address := os.Getenv("ARAH_ADDR")
	if address == "" {
		address = "127.0.0.1:18080"
	}
	log.Printf("ArahRide demo backend listening on %s", address)
	server := &http.Server{Addr: address, Handler: routes(newHub()), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}
