package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRideLifecycle(t *testing.T) {
	h := newHub()
	pickup := Point{-6.2088, 106.8456}
	dropoff := Point{-6.1754, 106.8272}
	if _, err := h.apply(Action{Role: "customer", Action: "create", Pickup: &pickup, Dropoff: &dropoff}); err == nil {
		t.Fatal("expected offline rejection")
	}
	if _, err := h.apply(Action{Role: "driver", Action: "online"}); err != nil {
		t.Fatal(err)
	}
	steps := []Action{
		{Role: "customer", Action: "create", Pickup: &pickup, Dropoff: &dropoff},
		{Role: "driver", Action: "accept"},
		{Role: "driver", Action: "arrive"},
		{Role: "driver", Action: "start"},
		{Role: "driver", Action: "complete"},
	}
	for _, step := range steps {
		if _, err := h.apply(step); err != nil {
			t.Fatalf("%s: %v", step.Action, err)
		}
	}
	if got := h.snapshot().Ride.Status; got != "completed" {
		t.Fatalf("status = %s", got)
	}
	if _, err := h.apply(Action{Role: "customer", Action: "reset"}); err != nil {
		t.Fatal(err)
	}
	if h.snapshot().Ride != nil {
		t.Fatal("ride remains after reset")
	}
}

func TestInvalidTransitionsAndCoordinates(t *testing.T) {
	h := newHub()
	h.apply(Action{Role: "driver", Action: "online"})
	invalid := Point{Latitude: 91, Longitude: 0}
	valid := Point{-6.2, 106.8}
	if _, err := h.apply(Action{Role: "customer", Action: "create", Pickup: &invalid, Dropoff: &valid}); err == nil {
		t.Fatal("expected invalid point rejection")
	}
	if _, err := h.apply(Action{Role: "driver", Action: "start"}); err == nil {
		t.Fatal("expected transition rejection")
	}
	h.apply(Action{Role: "customer", Action: "create", Pickup: &valid, Dropoff: &Point{-6.3, 106.9}})
	if _, err := h.apply(Action{Role: "driver", Action: "offline"}); err == nil {
		t.Fatal("expected active-ride offline rejection")
	}
	if _, err := h.apply(Action{Role: "driver", Action: "complete"}); err == nil {
		t.Fatal("expected premature completion rejection")
	}
}

func TestWebSocketReceivesLocationUpdates(t *testing.T) {
	h := newHub()
	server := httptest.NewServer(routes(h))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/events?role=customer", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	point := Point{-6.20, 106.85}
	h.apply(Action{Role: "driver", Action: "online"})
	h.apply(Action{Role: "driver", Action: "location", Location: &point})
	var latest Snapshot
	for latest.Sequence < 2 {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(payload, &latest); err != nil {
			t.Fatal(err)
		}
	}
	if latest.DriverLocation != point {
		t.Fatalf("location = %+v", latest.DriverLocation)
	}
}
