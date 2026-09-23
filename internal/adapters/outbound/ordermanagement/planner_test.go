package ordermanagement_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/claudioed/network-fulfillment/internal/adapters/outbound/ordermanagement"
	"github.com/claudioed/network-fulfillment/internal/application/contract"
	"github.com/claudioed/network-fulfillment/internal/domain/shared"
)

func now() time.Time { return time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC) }

// recorder captures the method+path of every request, so these tests
// assert the WIRE CONTRACT against order-management's real REST API
// rather than against whatever this adapter happens to send. An invented
// endpoint compiles perfectly and 404s in production.
type recorder struct {
	calls   []string
	bodies  []map[string]any
	handler func(w http.ResponseWriter, r *http.Request)
}

func (rec *recorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.calls = append(rec.calls, r.Method+" "+r.URL.Path)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec.bodies = append(rec.bodies, body)
		rec.handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRaiseHeldOrder_SendsAHeldShipCompleteOrder(t *testing.T) {
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ord-1","promiseDate":"2026-09-24T08:00:00Z","lines":[]}`))
	}}
	srv := rec.server(t)

	p := ordermanagement.NewPlanner(srv.URL, srv.Client())
	res, err := p.RaiseHeldOrder(context.Background(), contract.HeldOrderRequest{
		SiteId:         "site-1",
		RequiredShipBy: now().Add(48 * time.Hour),
		Lines:          map[shared.SKU]int{"SKU-1": 2},
	})
	if err != nil {
		t.Fatalf("RaiseHeldOrder: %v", err)
	}

	if res.LocalOrderId != "ord-1" {
		t.Fatalf("localOrderId = %q, want ord-1", res.LocalOrderId)
	}
	if got, want := rec.calls[0], "POST /orders"; got != want {
		t.Fatalf("call = %q, want %q", got, want)
	}

	body := rec.bodies[0]
	// Both flags are load-bearing, and both are the OPPOSITE of what a
	// careless default would send: the hold is the entire point, and
	// order-management REJECTS a held order that allows partial shipment
	// (its ADR 0020 §4).
	if body["releaseOnAllocation"] != false {
		t.Fatalf("releaseOnAllocation = %v, want false — the hold is the point", body["releaseOnAllocation"])
	}
	if body["allowPartialShipment"] != false {
		t.Fatalf("allowPartialShipment = %v, want false — network demand is fill-or-kill", body["allowPartialShipment"])
	}
}

func TestRaiseHeldOrder_FeasibilityComparesThePromiseToTheDeadline(t *testing.T) {
	cases := []struct {
		name         string
		promiseDate  string
		wantFeasible bool
	}{
		{"promise before the deadline", `"2026-09-24T08:00:00Z"`, true},
		{"promise exactly at the deadline", `"2026-09-25T08:00:00Z"`, true},
		{"promise after the deadline", `"2026-09-26T08:00:00Z"`, false},
		{"no promise at all", `null`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"ord-1","promiseDate":` + tc.promiseDate + `,"lines":[]}`))
			}}
			srv := rec.server(t)

			p := ordermanagement.NewPlanner(srv.URL, srv.Client())
			res, err := p.RaiseHeldOrder(context.Background(), contract.HeldOrderRequest{
				RequiredShipBy: now().Add(48 * time.Hour), // 2026-09-25T08:00:00Z
			})
			if err != nil {
				t.Fatalf("RaiseHeldOrder: %v", err)
			}
			if res.Feasible != tc.wantFeasible {
				t.Fatalf("feasible = %v, want %v", res.Feasible, tc.wantFeasible)
			}
			if res.LocalOrderId != "ord-1" {
				t.Fatalf("localOrderId = %q, want ord-1", res.LocalOrderId)
			}
		})
	}
}

func TestRaiseHeldOrder_AbsentPromiseIsNotAnError(t *testing.T) {
	// "We cannot make your deadline" is a legitimate answer that must
	// reach the network as a rejection, not an error that aborts the
	// poll and leaves the order unanswered until its window closes.
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ord-2","promiseDate":null,"lines":[]}`))
	}}
	srv := rec.server(t)

	p := ordermanagement.NewPlanner(srv.URL, srv.Client())
	res, err := p.RaiseHeldOrder(context.Background(), contract.HeldOrderRequest{RequiredShipBy: now()})
	if err != nil {
		t.Fatalf("an unpromisable order must not be an error, got %v", err)
	}
	if res.Feasible {
		t.Fatal("feasible = true, want false when there is no promise")
	}
}

func TestReleaseAndCancel_HitOrderManagementsRealRoutes(t *testing.T) {
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}}
	srv := rec.server(t)
	p := ordermanagement.NewPlanner(srv.URL, srv.Client())

	if err := p.ReleaseHeldOrder(context.Background(), "ord-1"); err != nil {
		t.Fatalf("ReleaseHeldOrder: %v", err)
	}
	// Cancellation is DELETE /orders/{id} — NOT a POST sub-resource.
	// Verified against order-management's OpenAPI spec; an invented
	// endpoint would 404 exactly where inventory gets freed.
	if err := p.CancelHeldOrder(context.Background(), "ord-1"); err != nil {
		t.Fatalf("CancelHeldOrder: %v", err)
	}

	want := []string{"POST /orders/ord-1/release", "DELETE /orders/ord-1"}
	for i, w := range want {
		if rec.calls[i] != w {
			t.Fatalf("call[%d] = %q, want %q", i, rec.calls[i], w)
		}
	}
}

func TestDo_NonSuccessStatusIsAnError(t *testing.T) {
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}}
	srv := rec.server(t)
	p := ordermanagement.NewPlanner(srv.URL, srv.Client())

	if err := p.ReleaseHeldOrder(context.Background(), "ord-1"); err == nil {
		t.Fatal("a 409 must surface as an error, not be swallowed")
	}
}
