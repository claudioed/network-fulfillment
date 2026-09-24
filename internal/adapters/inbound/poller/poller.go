// Package poller drives the inbound leg of the network integration.
//
// It is an INBOUND adapter that calls an OUTBOUND port: the network
// offers us no push (ADR 0001 §5), so demand only arrives because we go
// and ask for it. That inversion is why this lives here rather than in
// outbound/network — the gateway knows how to fetch, this knows when to,
// and what to do with what comes back.
package poller

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/claudioed/network-fulfillment/internal/application/contract"
	"github.com/claudioed/network-fulfillment/internal/application/ports"
	"github.com/claudioed/network-fulfillment/internal/domain/networkorder"
)

// Receiver is the use case this poller feeds. Declared here as a local
// interface rather than taking *usecases.ReceiveNetworkDemand, so the
// poller's own tests can drive it without standing up a planner, a
// gateway and a repository just to observe scheduling behaviour.
//
// The returned aggregate is deliberately ignored by this adapter: the
// poller's job is delivery and scheduling, and a rejected order is a
// SUCCESSFUL poll — the answer was sent. Only an error means the unit
// must be re-fetched.
type Receiver interface {
	Execute(ctx context.Context, demand contract.InboundDemand) (*networkorder.NetworkOrder, error)
}

// Poller fetches demand from the network on an interval and hands each
// unit to the receiving use case.
type Poller struct {
	gateway  ports.NetworkGateway
	receiver Receiver
	clock    ports.Clock
	interval time.Duration
	logger   *slog.Logger

	// mu guards since, which the poll loop advances and Stats reads.
	mu sync.Mutex

	// since is the watermark handed to the gateway on the next poll.
	//
	// It advances ONLY on a fully successful pass. A pass where any
	// demand failed leaves it where it was, so the next poll re-fetches
	// that demand and tries again — the use case is idempotent on
	// networkRef precisely so this is safe, and the alternative is
	// losing an order whose 24h clock is already running.
	since time.Time

	polls    int
	received int
	failed   int
}

// Config is what the composition root must decide.
type Config struct {
	Interval time.Duration

	// Since is the initial watermark. The zero value asks the network for
	// everything it still considers outstanding, which is the correct
	// COLD START: on a first boot, or after a long outage, an order we
	// have never seen is exactly the one whose deadline is closest.
	Since time.Time
}

// New builds a Poller.
func New(gateway ports.NetworkGateway, receiver Receiver, clock ports.Clock, cfg Config, logger *slog.Logger) *Poller {
	return &Poller{
		gateway:  gateway,
		receiver: receiver,
		clock:    clock,
		interval: cfg.Interval,
		since:    cfg.Since,
		logger:   logger,
	}
}

// Run polls until ctx is cancelled.
//
// It polls IMMEDIATELY on entry rather than waiting out the first tick.
// A restart is the moment we are most likely to be behind — the 24h
// acknowledgement clock ran while we were down (ADR 0001: a poller
// outage is an SLA breach, not a delayed batch) — and sleeping through
// an interval first would add our own latency to a deadline we did not
// set.
func (p *Poller) Run(ctx context.Context) {
	p.pollOnce(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollOnce(ctx)
		}
	}
}

// PollOnce runs a single pass. Exported so an operator endpoint or a
// test can drive the inbound leg deterministically instead of waiting
// for a tick.
func (p *Poller) PollOnce(ctx context.Context) { p.pollOnce(ctx) }

func (p *Poller) pollOnce(ctx context.Context) {
	p.mu.Lock()
	since := p.since
	p.polls++
	p.mu.Unlock()

	// Read before the call, not after: the watermark must cover
	// everything the network could have accepted by the time it answered
	// us. Taking it afterwards would skip demand that arrived during the
	// call itself.
	pollStartedAt := p.clock.Now()

	demands, err := p.gateway.PollDemand(ctx, since)
	if err != nil {
		// Not fatal and not retried here: the ticker IS the retry, and
		// the watermark is untouched so nothing is skipped. Logged at
		// ERROR because a sustained failure is an SLA breach in the
		// making, not a transient to be swallowed.
		p.logger.Error("poll demand failed", "err", err, "since", since)
		return
	}
	if len(demands) == 0 {
		p.advance(pollStartedAt)
		return
	}

	failures := 0
	for _, d := range demands {
		// ctx is checked between units so a shutdown mid-batch does not
		// start work it cannot finish. The remaining demand is re-fetched
		// on the next boot, because the watermark does not advance.
		if ctx.Err() != nil {
			p.logger.Warn("poll pass abandoned mid-batch", "networkRef", d.NetworkRef)
			return
		}
		if _, err := p.receiver.Execute(ctx, d); err != nil {
			// One bad order must not abort the pass. The others are real
			// demand with their own clocks running, and refusing to
			// process them because an unrelated order failed turns one
			// problem into a batch-wide outage.
			p.logger.Error("receive network demand failed",
				"networkRef", d.NetworkRef, "err", err)
			failures++
			continue
		}
		p.mu.Lock()
		p.received++
		p.mu.Unlock()
	}

	if failures > 0 {
		p.mu.Lock()
		p.failed += failures
		p.mu.Unlock()
		// Watermark deliberately NOT advanced: re-fetch and retry next
		// pass. Idempotency on networkRef makes the already-succeeded
		// orders in this batch a no-op.
		p.logger.Warn("poll pass had failures; watermark not advanced",
			"failures", failures, "of", len(demands), "since", since)
		return
	}

	p.advance(pollStartedAt)
}

func (p *Poller) advance(to time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.since = to
}

// Stats reports what the poller has done. Used by the readiness/status
// surface so "is the inbound leg alive" is answerable without reading
// logs — the poller silently doing nothing is the failure mode that
// costs acknowledgement deadlines.
type Stats struct {
	Polls    int
	Received int
	Failed   int
	Since    time.Time
}

func (p *Poller) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{Polls: p.polls, Received: p.received, Failed: p.failed, Since: p.since}
}
