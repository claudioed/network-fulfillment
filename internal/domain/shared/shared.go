// Package shared holds value objects and identity types used across the
// network-fulfillment domain — nothing here is an aggregate.
package shared

import "errors"

// NetworkRef is the external network's own identity for a unit of demand
// (for Amazon Vendor Direct Fulfillment, its purchase-order number). It
// is deliberately an OPAQUE string: this context is Conformist upstream,
// so the network owns the format and we never parse, validate a shape,
// or derive meaning from it. A future second network would bring a
// different format and must not require a domain change here.
type NetworkRef string

// NetworkLineRef is the network's identity for one line WITHIN a
// NetworkRef. It is unique only inside its order, never globally — which
// is exactly why it is a distinct type from NetworkRef rather than
// another bare string.
type NetworkLineRef string

// NetworkProductId is the network's product identifier (an ASIN, in
// Amazon's vocabulary). It is NOT a SKU: translating between the two is
// this context's job as an Anti-Corruption Layer, and keeping them as
// separate types is what makes a missing translation a compile error
// rather than a silently wrong lookup in production.
type NetworkProductId string

// SKU is OUR product identity — the vocabulary order-management,
// inventory-storage and every other context in this fleet already speak.
// It appears in this package only as the OUTPUT side of the translation;
// the network never sees it and we never send a NetworkProductId inward.
type SKU string

// LocalOrderId is order-management's OrderId for the local order raised
// from a NetworkOrder. It is stored as an explicit, persisted mapping
// rather than derived by string convention, because this fleet has
// already been bitten once by an identifier that looked like an order
// reference and was not: fulfillment-execution's order_ref carries
// wes-work-planning's per-LINE WorkUnitId, not order-management's
// OrderId (order-management ADR 0018). A network purchase-order line is
// one hop further out and the same class of mistake is available.
type LocalOrderId string

// SiteId identifies the fulfillment site a NetworkOrder is destined for —
// the same identity order-management's PromisePolicy and
// process-path-management's CPTSchedule already key on.
type SiteId string

var (
	// ErrEmptyNetworkRef rejects demand with no external identity. Such a
	// record could never be acknowledged back to the network, since the
	// acknowledgement is addressed BY that reference.
	ErrEmptyNetworkRef = errors.New("network ref must not be empty")

	// ErrEmptyNetworkLineRef rejects a line with no external identity,
	// for the same reason at line granularity.
	ErrEmptyNetworkLineRef = errors.New("network line ref must not be empty")

	// ErrEmptyNetworkProductId rejects a line naming no product.
	ErrEmptyNetworkProductId = errors.New("network product id must not be empty")

	// ErrNonPositiveQuantity rejects a line demanding zero or fewer
	// units. The network would never send one; receiving it means we
	// mis-parsed the payload, and accepting it would put an order into
	// the fleet that can never be fulfilled or meaningfully cancelled.
	ErrNonPositiveQuantity = errors.New("quantity must be positive")

	// ErrNoLines rejects an order with no lines at all.
	ErrNoLines = errors.New("network order must have at least one line")

	// ErrUnknownProduct is returned by a ProductTranslation when a
	// NetworkProductId has no SKU mapping. It is deliberately a domain
	// error rather than an adapter error: an untranslatable product is a
	// business fact — we cannot sell what we cannot identify — and the
	// correct response is to REJECT the order back to the network within
	// the acknowledgement window, not to retry or to crash.
	ErrUnknownProduct = errors.New("no SKU mapping for network product id")
)
