// Package ordermanagement is the outbound adapter for order-management's
// REST API. It is the downstream half of this context's Anti-Corruption
// Layer: everything it sends is already in the fleet's shared
// vocabulary, and nothing the network said reaches it.
package ordermanagement

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/claudioed/network-fulfillment/internal/application/contract"
	"github.com/claudioed/network-fulfillment/internal/domain/shared"
)

// Planner implements ports.FulfillmentPlanner against order-management's
// REST API (its ADR 0020 endpoints).
type Planner struct {
	BaseURL string
	HTTP    *http.Client
}

func NewPlanner(baseURL string, client *http.Client) *Planner {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Planner{BaseURL: baseURL, HTTP: client}
}

type orderLineRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

type receiveOrderRequest struct {
	Lines []orderLineRequest `json:"lines"`
	// ReleaseOnAllocation false is the hold: order-management allocates
	// and computes a promise, but puts no work on the floor until we
	// acknowledge to the network (its ADR 0020 §1).
	ReleaseOnAllocation bool `json:"releaseOnAllocation"`
	// AllowPartialShipment is forced false. The network's protocol is
	// fill-or-kill and order-management REJECTS a held order that allows
	// partial shipment (its ADR 0020 §4) — so this is not a default we
	// are choosing, it is an invariant we must satisfy.
	AllowPartialShipment bool `json:"allowPartialShipment"`
}

type orderResponse struct {
	ID          string     `json:"id"`
	PromiseDate *time.Time `json:"promiseDate"`
	Lines       []struct {
		Status string `json:"status"`
	} `json:"lines"`
}

// RaiseHeldOrder creates the held order and derives feasibility.
//
// HONEST LIMITATION, and the reason this is a comparison rather than a
// call: order-management's PromisePolicy.FeasibleBy — added for exactly
// this purpose in its ADR 0020 phase 2 — is NOT reachable over REST.
// POST /orders accepts no deadline, so it computes the earliest promise
// it can make and returns that; this adapter then compares two instants.
//
// The two agree whenever the earliest promise is the only thing that
// matters, which is every case today. They diverge on the rule
// FeasibleBy actually encodes: pick the LATEST window that still meets
// the deadline, leaving the floor maximum slack. Until order-management
// accepts requiredShipBy on intake, we get the earliest-window answer
// and give up that slack.
//
// This is a known gap, not an oversight: closing it means adding a
// deadline to order-management's intake contract, which is its own ADR
// and its own PR.
func (p *Planner) RaiseHeldOrder(ctx context.Context, req contract.HeldOrderRequest) (contract.HeldOrderResult, error) {
	lines := make([]orderLineRequest, 0, len(req.Lines))
	for sku, qty := range req.Lines {
		lines = append(lines, orderLineRequest{SKU: string(sku), Quantity: qty})
	}

	body := receiveOrderRequest{
		Lines:                lines,
		ReleaseOnAllocation:  false,
		AllowPartialShipment: false,
	}

	var out orderResponse
	if err := p.do(ctx, http.MethodPost, "/orders", body, &out); err != nil {
		return contract.HeldOrderResult{}, err
	}

	// No promise at all means order-management could not place this
	// order in any window it knows about — not feasible, and explicitly
	// NOT an error: "we cannot make your deadline" is a legitimate
	// answer that must reach the network as a rejection.
	if out.PromiseDate == nil {
		return contract.HeldOrderResult{
			LocalOrderId: shared.LocalOrderId(out.ID),
			Feasible:     false,
		}, nil
	}

	return contract.HeldOrderResult{
		LocalOrderId:   shared.LocalOrderId(out.ID),
		Feasible:       !out.PromiseDate.After(req.RequiredShipBy),
		PromisedCutoff: *out.PromiseDate,
	}, nil
}

func (p *Planner) ReleaseHeldOrder(ctx context.Context, id shared.LocalOrderId) error {
	return p.do(ctx, http.MethodPost, fmt.Sprintf("/orders/%s/release", id), nil, nil)
}

// CancelHeldOrder frees the hold. order-management exposes cancellation
// as DELETE /orders/{id}, not a POST sub-resource -- verified against its
// OpenAPI spec on origin/develop rather than assumed, because this is the
// call that releases inventory reserved for demand we refused, and a 404
// here would silently recreate the orphaned-hold gap both ADRs flagged.
func (p *Planner) CancelHeldOrder(ctx context.Context, id shared.LocalOrderId) error {
	return p.do(ctx, http.MethodDelete, fmt.Sprintf("/orders/%s", id), nil, nil)
}

func (p *Planner) do(ctx context.Context, method, path string, in, out any) error {
	var buf *bytes.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		buf = bytes.NewReader(b)
	} else {
		buf = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, p.BaseURL+path, buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("order-management %s %s: status %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
