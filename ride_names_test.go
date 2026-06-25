package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func createRideWithNames(t *testing.T, h *hub, pickupName, dropoffName string) {
	t.Helper()
	pickup, dropoff := Point{-6.2088, 106.8456}, Point{-6.1754, 106.8272}
	if _, err := h.apply(Action{Role: "driver", Action: "online"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.apply(Action{Role: "customer", Action: "create", Pickup: &pickup, Dropoff: &dropoff, PickupName: pickupName, DropoffName: dropoffName}); err != nil {
		t.Fatal(err)
	}
}

func TestRideSnapshotsCarryUnicodeNamesOverHTTPAndEvents(t *testing.T) {
	h := newHub()
	createRideWithNames(t, h, "  Stasiun Gambir, Jakarta Pusat ", "Pasar Mangga Dua — Pademangan")
	rec := httptest.NewRecorder()
	routes(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/state", nil))
	var viaHTTP Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &viaHTTP); err != nil {
		t.Fatal(err)
	}
	events := h.subscribe()
	defer h.unsubscribe(events)
	payload, _ := json.Marshal(<-events)
	var viaEvent Snapshot
	json.Unmarshal(payload, &viaEvent)
	for _, ride := range []*Ride{viaHTTP.Ride, viaEvent.Ride} {
		if ride.PickupName != "Stasiun Gambir, Jakarta Pusat" || ride.DropoffName != "Pasar Mangga Dua — Pademangan" {
			t.Fatalf("names = %q / %q", ride.PickupName, ride.DropoffName)
		}
	}
	if !strings.Contains(string(payload), `"pickupName"`) || !strings.Contains(string(payload), `"dropoffName"`) {
		t.Fatalf("payload lacks name fields: %s", payload)
	}
}

func TestRideWithoutNamesUsesGenericLabels(t *testing.T) {
	for _, names := range [][2]string{{"", ""}, {"   ", "\t"}} {
		h := newHub()
		createRideWithNames(t, h, names[0], names[1])
		ride := h.snapshot().Ride
		if ride.PickupName != "Lokasi jemput" || ride.DropoffName != "Lokasi tujuan" {
			t.Fatalf("names = %q / %q", ride.PickupName, ride.DropoffName)
		}
	}
}

func TestNamesSurviveUnrelatedStateChanges(t *testing.T) {
	h := newHub()
	createRideWithNames(t, h, "Monas", "Kota Tua")
	for _, step := range []Action{{Role: "driver", Action: "accept"}, {Role: "driver", Action: "arrive"}} {
		if _, err := h.apply(step); err != nil {
			t.Fatal(err)
		}
	}
	ride := h.snapshot().Ride
	if ride.PickupName != "Monas" || ride.DropoffName != "Kota Tua" {
		t.Fatalf("names = %q / %q", ride.PickupName, ride.DropoffName)
	}
}
