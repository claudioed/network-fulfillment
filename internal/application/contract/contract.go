// Package contract holds the data shapes that cross this context's OUT
// ports — the arguments and results of ports.NetworkGateway and
// ports.FulfillmentPlanner.
//
// They live here rather than in ports because the harness architecture
// fitness test requires internal/application/ports to contain
// INTERFACES AND NOTHING ELSE, and because keeping the shapes separate
// from the interfaces makes it obvious that they are part of the
// contract rather than of any one adapter.
//
// Everything here is expressed in OUR vocabulary. No network
// identifier format, no Amazon wire field, no HTTP or SQL type appears
// in this package — that translation is the job of the adapter on the
// far side of the port (ADR 0001 §2).
package contract

import (
	"time"

	"github.com/claudioed/network-fulfillment/internal/domain/shared"
)

// InboundDemand is one unit of demand as it arrives from the network,
// BEFORE translation into the domain. Product ids are still the
// network's, because translating them is precisely what the receiving
// use case does — and a translation failure is a business decision
// (reject the order), not a parse error to be buried in an adapter.
type InboundDemand struct {
	NetworkRef     shared.NetworkRef
	SiteId         shared.SiteId
	RequiredShipBy time.Time
	Lines          []InboundLine
}

// InboundLine is one line of untranslated inbound demand.
type InboundLine struct {
	NetworkLineRef   shared.NetworkLineRef
	NetworkProductId shared.NetworkProductId
	Quantity         int
}

// HeldOrderRequest asks order-management for an allocated-but-held
// order, and for a verdict on whether the network's deadline can be met.
//
// It carries SKUs, quantities, a site and a deadline — and nothing else.
// order-management never learns that a network exists, which is the
// boundary ADR 0001 §2 draws and the reason the Amazon vocabulary cannot
// leak inward.
type HeldOrderRequest struct {
	SiteId         shared.SiteId
	RequiredShipBy time.Time
	Lines          map[shared.SKU]int
}

// HeldOrderResult is order-management's answer.
//
// Feasible is the verdict from its PromisePolicy.FeasibleBy — computed
// there, never recomputed here (ADR 0001 §7). PromisedCutoff is the
// departure it committed to, meaningful only when Feasible is true.
type HeldOrderResult struct {
	LocalOrderId   shared.LocalOrderId
	Feasible       bool
	PromisedCutoff time.Time
}
