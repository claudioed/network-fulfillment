//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/claudioed/network-fulfillment/internal/adapters/outbound/postgres"
	"github.com/claudioed/network-fulfillment/internal/domain/networkorder"
	"github.com/claudioed/network-fulfillment/internal/domain/shared"
)

func uniqueRef(name string) shared.NetworkRef {
	return shared.NetworkRef(fmt.Sprintf("po-%s-%d", name, time.Now().UnixNano()))
}

func mustLine(t *testing.T, ref, productId, sku string, qty int) networkorder.Line {
	t.Helper()
	l, err := networkorder.NewLine(
		shared.NetworkLineRef(ref),
		shared.NetworkProductId(productId),
		shared.SKU(sku),
		qty,
	)
	if err != nil {
		t.Fatalf("NewLine: %v", err)
	}
	return l
}

// TestNetworkOrderRepo_RoundTripsWholeLifecycle drives ONE aggregate
// through every legal transition, persisting and reloading after each —
// the repo must round-trip every state the use cases can produce, not
// just the one it was first written against.
func TestNetworkOrderRepo_RoundTripsWholeLifecycle(t *testing.T) {
	pool := newDB(t)
	repo := postgres.NewNetworkOrderRepo(pool)
	ctx := context.Background()

	ref := uniqueRef("lifecycle")
	// Truncated to microseconds: Postgres TIMESTAMPTZ has microsecond
	// resolution, so a nanosecond-precision Go time would come back
	// unequal and the comparison below would fail for a reason that has
	// nothing to do with the code under test.
	receivedAt := time.Now().UTC().Truncate(time.Microsecond)
	requiredShipBy := receivedAt.Add(48 * time.Hour)

	lines := []networkorder.Line{
		mustLine(t, "1", "ASIN-AAA", "sku-aaa", 3),
		mustLine(t, "2", "ASIN-BBB", "sku-bbb", 1),
	}

	// Built through the domain's own constructor rather than hand-crafted
	// rows: what is stored must be exactly what the aggregate produces.
	o, err := networkorder.Receive(ref, shared.SiteId("site-1"), requiredShipBy, lines, receivedAt)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if err := repo.Save(ctx, o); err != nil {
		t.Fatalf("Save (new): %v", err)
	}

	got, err := repo.FindByRef(ctx, ref)
	if err != nil {
		t.Fatalf("FindByRef: %v", err)
	}
	if got == nil {
		t.Fatal("FindByRef returned nil for an order that was just saved")
	}
	if got.State() != networkorder.StateNew {
		t.Fatalf("state = %q, want NEW", got.State())
	}
	if got.SiteId() != shared.SiteId("site-1") {
		t.Fatalf("siteId = %q, want site-1", got.SiteId())
	}
	if !got.RequiredShipBy().Equal(requiredShipBy) {
		t.Fatalf("requiredShipBy = %v, want %v", got.RequiredShipBy(), requiredShipBy)
	}
	if !got.ReceivedAt().Equal(receivedAt) {
		t.Fatalf("receivedAt = %v, want %v", got.ReceivedAt(), receivedAt)
	}
	// The deadline is DERIVED at intake and persisted; a reload must not
	// recompute it from load time.
	wantAckBy := receivedAt.Add(networkorder.AcknowledgementWindow)
	if !got.AcknowledgeBy().Equal(wantAckBy) {
		t.Fatalf("acknowledgeBy = %v, want %v", got.AcknowledgeBy(), wantAckBy)
	}
	if got.LocalOrderId() != nil {
		t.Fatalf("localOrderId = %v, want nil before acknowledgement", *got.LocalOrderId())
	}

	assertLines(t, got, map[shared.SKU]int{"sku-aaa": 3, "sku-bbb": 1})

	// ACKNOWLEDGED + linked local order.
	if err := got.Acknowledge(); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	localId := shared.LocalOrderId("ord-abc-123")
	if err := got.LinkLocalOrder(localId); err != nil {
		t.Fatalf("LinkLocalOrder: %v", err)
	}
	if err := repo.Save(ctx, got); err != nil {
		t.Fatalf("Save (acknowledged): %v", err)
	}

	got, err = repo.FindByRef(ctx, ref)
	if err != nil {
		t.Fatalf("FindByRef (acknowledged): %v", err)
	}
	if got.State() != networkorder.StateAcknowledged {
		t.Fatalf("state = %q, want ACKNOWLEDGED", got.State())
	}
	if got.LocalOrderId() == nil || *got.LocalOrderId() != localId {
		t.Fatalf("localOrderId = %v, want %q", got.LocalOrderId(), localId)
	}

	// CONFIRMED.
	if err := got.ConfirmShipment(); err != nil {
		t.Fatalf("ConfirmShipment: %v", err)
	}
	if err := repo.Save(ctx, got); err != nil {
		t.Fatalf("Save (confirmed): %v", err)
	}
	got, err = repo.FindByRef(ctx, ref)
	if err != nil {
		t.Fatalf("FindByRef (confirmed): %v", err)
	}
	if got.State() != networkorder.StateConfirmed {
		t.Fatalf("state = %q, want CONFIRMED", got.State())
	}
	// The local order must survive the confirmation save — losing it here
	// would orphan real allocated work in order-management.
	if got.LocalOrderId() == nil || *got.LocalOrderId() != localId {
		t.Fatalf("localOrderId = %v after confirm, want %q", got.LocalOrderId(), localId)
	}
}

// TestNetworkOrderRepo_SaveNeverMovesTheAcknowledgementDeadline pins the
// upsert's column list. acknowledge_by is the SLA clock the sweep
// measures; if a later save rewrote it, every re-save would silently
// extend the window and the sweep would never report a miss.
//
// The second save carries a DIFFERENT deadline on purpose. The network's
// inbound leg is a poll, so the same purchase order can be delivered
// twice, and ReceiveNetworkDemand builds a fresh aggregate each time —
// whose window is anchored to the LATER arrival. If the upsert accepted
// that value, a counterparty re-sending demand (or an outage that made us
// re-poll) would reset a clock that has already been running, and an
// order 30h past a 24h window would look brand new. Re-saving an
// aggregate that already agrees with the row cannot detect this: the
// values are identical, so the write is invisible.
func TestNetworkOrderRepo_SaveNeverMovesTheAcknowledgementDeadline(t *testing.T) {
	pool := newDB(t)
	repo := postgres.NewNetworkOrderRepo(pool)
	ctx := context.Background()

	ref := uniqueRef("deadline")
	// 30h ago, so the 24h window has ALREADY closed: the order is
	// overdue, and that is the consequence a reset deadline would erase.
	firstArrival := time.Now().UTC().Add(-30 * time.Hour).Truncate(time.Microsecond)
	requiredShipBy := firstArrival.Add(72 * time.Hour)

	o, err := networkorder.Receive(ref, shared.SiteId("site-1"), requiredShipBy,
		[]networkorder.Line{mustLine(t, "1", "ASIN-AAA", "sku-aaa", 1)},
		firstArrival)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if err := repo.Save(ctx, o); err != nil {
		t.Fatalf("Save: %v", err)
	}
	originalDeadline := o.AcknowledgeBy()

	// The same demand, delivered again NOW: a 30h-later arrival, whose
	// window would still be wide open.
	secondArrival := time.Now().UTC().Truncate(time.Microsecond)
	redelivered, err := networkorder.Receive(ref, shared.SiteId("site-1"), requiredShipBy,
		[]networkorder.Line{mustLine(t, "1", "ASIN-AAA", "sku-aaa", 1)},
		secondArrival)
	if err != nil {
		t.Fatalf("Receive (redelivered): %v", err)
	}
	if redelivered.AcknowledgeBy().Equal(originalDeadline) {
		t.Fatal("test precondition: the redelivered order must carry a different deadline")
	}
	if err := repo.Save(ctx, redelivered); err != nil {
		t.Fatalf("Save (redelivered): %v", err)
	}

	after, err := repo.FindByRef(ctx, ref)
	if err != nil {
		t.Fatalf("FindByRef (after re-save): %v", err)
	}
	if !after.AcknowledgeBy().Equal(originalDeadline) {
		t.Fatalf("acknowledgeBy moved on re-save: %v, want %v (the first arrival's window)",
			after.AcknowledgeBy(), originalDeadline)
	}
	if !after.ReceivedAt().Equal(firstArrival) {
		t.Fatalf("receivedAt moved on re-save: %v, want %v", after.ReceivedAt(), firstArrival)
	}

	// Still overdue, which is the consequence that actually matters: the
	// sweep must be able to see this order missed its window.
	if !after.AcknowledgementOverdue(time.Now().UTC()) {
		t.Fatal("an order past its 24h window, redelivered, must still read as overdue")
	}
}

// TestNetworkOrderRepo_UntranslatableOrderRoundTripsWithNoLines is the
// case a join would break. An untranslatable order is lineless by
// construction and exists only to carry our refusal — it must come back
// as a real order, not as nil.
func TestNetworkOrderRepo_UntranslatableOrderRoundTripsWithNoLines(t *testing.T) {
	pool := newDB(t)
	repo := postgres.NewNetworkOrderRepo(pool)
	ctx := context.Background()

	ref := uniqueRef("untranslatable")
	receivedAt := time.Now().UTC().Truncate(time.Microsecond)
	o, err := networkorder.ReceiveUntranslatable(ref, shared.SiteId("site-1"),
		receivedAt.Add(24*time.Hour), receivedAt)
	if err != nil {
		t.Fatalf("ReceiveUntranslatable: %v", err)
	}
	if err := o.Reject(); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if err := repo.Save(ctx, o); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.FindByRef(ctx, ref)
	if err != nil {
		t.Fatalf("FindByRef: %v", err)
	}
	if got == nil {
		t.Fatal("a lineless untranslatable order must round-trip as an order, not nil")
	}
	if got.State() != networkorder.StateRejected {
		t.Fatalf("state = %q, want REJECTED", got.State())
	}
	if len(got.Lines()) != 0 {
		t.Fatalf("lines = %d, want 0", len(got.Lines()))
	}
}

// TestNetworkOrderRepo_FindByRef_UnknownIsNilNilNotAnError pins this
// fleet's repository convention: "not found" is the application's
// concern, not the repository's.
func TestNetworkOrderRepo_FindByRef_UnknownIsNilNilNotAnError(t *testing.T) {
	pool := newDB(t)
	repo := postgres.NewNetworkOrderRepo(pool)

	got, err := repo.FindByRef(context.Background(), uniqueRef("absent"))
	if err != nil {
		t.Fatalf("FindByRef on an unknown ref must not error, got %v", err)
	}
	if got != nil {
		t.Fatalf("FindByRef on an unknown ref = %v, want nil", got)
	}
}

// TestNetworkOrderRepo_ListUnansweredReturnsOnlyNew is what the sweep
// depends on. An answered order still holding a row must not come back:
// the sweep would otherwise re-reject demand we already committed to.
func TestNetworkOrderRepo_ListUnansweredReturnsOnlyNew(t *testing.T) {
	pool := newDB(t)
	repo := postgres.NewNetworkOrderRepo(pool)
	ctx := context.Background()

	receivedAt := time.Now().UTC().Truncate(time.Microsecond)
	line := func() []networkorder.Line {
		return []networkorder.Line{mustLine(t, "1", "ASIN-AAA", "sku-aaa", 1)}
	}

	newRef := uniqueRef("still-new")
	stillNew, err := networkorder.Receive(newRef, "site-1", receivedAt.Add(48*time.Hour), line(), receivedAt)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if err := repo.Save(ctx, stillNew); err != nil {
		t.Fatalf("Save (new): %v", err)
	}

	ackRef := uniqueRef("acknowledged")
	acked, err := networkorder.Receive(ackRef, "site-1", receivedAt.Add(48*time.Hour), line(), receivedAt)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if err := acked.Acknowledge(); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if err := repo.Save(ctx, acked); err != nil {
		t.Fatalf("Save (acknowledged): %v", err)
	}

	rejRef := uniqueRef("rejected")
	rejected, err := networkorder.Receive(rejRef, "site-1", receivedAt.Add(48*time.Hour), line(), receivedAt)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if err := rejected.Reject(); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if err := repo.Save(ctx, rejected); err != nil {
		t.Fatalf("Save (rejected): %v", err)
	}

	unanswered, err := repo.ListUnanswered(ctx)
	if err != nil {
		t.Fatalf("ListUnanswered: %v", err)
	}

	refs := make(map[shared.NetworkRef]bool, len(unanswered))
	for _, o := range unanswered {
		refs[o.NetworkRef()] = true
		if o.State() != networkorder.StateNew {
			t.Fatalf("ListUnanswered returned a %s order", o.State())
		}
	}
	if !refs[newRef] {
		t.Fatalf("ListUnanswered omitted the NEW order %q", newRef)
	}
	if refs[ackRef] {
		t.Fatalf("ListUnanswered returned the ACKNOWLEDGED order %q", ackRef)
	}
	if refs[rejRef] {
		t.Fatalf("ListUnanswered returned the REJECTED order %q", rejRef)
	}
}

// TestNetworkOrderRepo_ListUnansweredCarriesLinesAndDeadlines matters
// because the sweep does more than count: it reads each order's deadline
// to decide overdue-ness and cancels its local order. An order returned
// without its lines or with a zero deadline would make the sweep either
// miss real misses or cancel work it should not.
func TestNetworkOrderRepo_ListUnansweredCarriesLinesAndDeadlines(t *testing.T) {
	pool := newDB(t)
	repo := postgres.NewNetworkOrderRepo(pool)
	ctx := context.Background()

	ref := uniqueRef("sweep-shape")
	receivedAt := time.Now().UTC().Add(-30 * time.Hour).Truncate(time.Microsecond)
	o, err := networkorder.Receive(ref, "site-1", receivedAt.Add(72*time.Hour),
		[]networkorder.Line{
			mustLine(t, "1", "ASIN-AAA", "sku-aaa", 2),
			mustLine(t, "2", "ASIN-BBB", "sku-bbb", 5),
		}, receivedAt)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if err := repo.Save(ctx, o); err != nil {
		t.Fatalf("Save: %v", err)
	}

	unanswered, err := repo.ListUnanswered(ctx)
	if err != nil {
		t.Fatalf("ListUnanswered: %v", err)
	}

	var found *networkorder.NetworkOrder
	for _, candidate := range unanswered {
		if candidate.NetworkRef() == ref {
			found = candidate
			break
		}
	}
	if found == nil {
		t.Fatalf("ListUnanswered omitted %q", ref)
	}

	assertLines(t, found, map[shared.SKU]int{"sku-aaa": 2, "sku-bbb": 5})

	// Received 30h ago with a 24h window, so the sweep must see this as
	// overdue NOW. This asserts the deadline survived the list path, using
	// the same domain predicate the sweep itself calls.
	if !found.AcknowledgementOverdue(time.Now().UTC()) {
		t.Fatalf("order received %v with a %v window must read as overdue (acknowledgeBy=%v)",
			receivedAt, networkorder.AcknowledgementWindow, found.AcknowledgeBy())
	}
}

func assertLines(t *testing.T, o *networkorder.NetworkOrder, want map[shared.SKU]int) {
	t.Helper()
	got := o.SKUQuantities()
	if len(got) != len(want) {
		t.Fatalf("SKUQuantities = %v, want %v", got, want)
	}
	for sku, qty := range want {
		if got[sku] != qty {
			t.Fatalf("SKUQuantities[%q] = %d, want %d (all: %v)", sku, got[sku], qty, got)
		}
	}
	// The network's own identifiers must survive the round trip: the
	// outbound acknowledgement leg is addressed in the network's terms and
	// cannot be reconstructed from our SKUs.
	for _, l := range o.Lines() {
		if l.NetworkLineRef() == "" {
			t.Fatalf("line for sku %q came back with no network line ref", l.SKU())
		}
		if l.NetworkProductId() == "" {
			t.Fatalf("line for sku %q came back with no network product id", l.SKU())
		}
	}
}
