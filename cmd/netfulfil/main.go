// Command netfulfil is network-fulfillment's OLTP composition root: the
// one place every layer is wired together.
//
// With no configuration at all it runs fully functional on in-memory
// adapters and a stub network gateway — no Postgres, no broker, and no
// network credentials in existence (ADR 0001 §4).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	inboundhttp "github.com/claudioed/network-fulfillment/internal/adapters/inbound/http"
	"github.com/claudioed/network-fulfillment/internal/adapters/inbound/poller"
	"github.com/claudioed/network-fulfillment/internal/adapters/outbound/memory"
	"github.com/claudioed/network-fulfillment/internal/adapters/outbound/network"
	"github.com/claudioed/network-fulfillment/internal/adapters/outbound/ordermanagement"
	"github.com/claudioed/network-fulfillment/internal/adapters/outbound/postgres"
	"github.com/claudioed/network-fulfillment/internal/application/ports"
	"github.com/claudioed/network-fulfillment/internal/application/usecases"
)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type logPublisher struct{ logger *slog.Logger }

func (p logPublisher) Publish(_ context.Context, event any) error {
	p.logger.Info("integration event", "event", event)
	return nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	mode := network.ParseMode(os.Getenv("NETWORK_MODE"))
	gateway, err := network.NewGateway(mode, logger)
	if err != nil {
		// Refusing to boot is deliberate. A deployment that asked for a
		// real network and silently got a stub would look healthy while
		// answering nobody.
		logger.Error("cannot wire network gateway", "mode", mode, "err", err)
		os.Exit(1)
	}
	logger.Info("network gateway wired", "mode", mode)

	// Declarative stub seeding, the same shape as the fleet's
	// PATH_CATALOGUE_FILE. This is how a stub deployment is given
	// something to receive: the network offers no push (ADR 0001 section 5),
	// so the alternative would be a write endpoint that exists only for
	// testing and has no production counterpart.
	if err := seedStubDemand(gateway, logger); err != nil {
		logger.Error("cannot load stub demand", "err", err)
		os.Exit(1)
	}

	translation := memory.NewProductTranslation()
	// The Anti-Corruption Layer's dictionary. Without it the map is empty
	// and EVERY order rejects as untranslatable — the inbound leg looks
	// alive while answering the network in the negative every time, and
	// the cause is invisible because refusing unknown products is also
	// correct behaviour.
	if path := os.Getenv("PRODUCT_TRANSLATION_FILE"); path != "" {
		n, err := memory.LoadProductTranslationFile(translation, path)
		if err != nil {
			logger.Error("cannot load product translation", "err", err)
			os.Exit(1)
		}
		logger.Info("product translation loaded", "file", path, "products", n)
	} else {
		// WARN, not INFO: a deployment with no dictionary is running, and
		// will reject everything the network sends.
		logger.Warn("no PRODUCT_TRANSLATION_FILE set; every network order will be rejected as untranslatable")
	}

	// Loaded before the database is opened, deliberately: every failure
	// path above this line may os.Exit freely, whereas one below it would
	// skip `defer closeOrders()` and leak the pool. The dictionary needs
	// no database, so there is no reason for it to sit after one.
	orders, closeOrders, err := wireOrders(context.Background(), logger)
	if err != nil {
		// Same reasoning as the gateway above: a deployment that asked
		// for a database and silently got an in-memory map would look
		// healthy while forgetting every acknowledgement deadline it owes
		// the moment it restarts.
		logger.Error("cannot wire order repository", "err", err)
		os.Exit(1)
	}
	defer closeOrders()

	omBase := os.Getenv("ORDER_MANAGEMENT_URL")
	if omBase == "" {
		omBase = "http://localhost:8080"
	}
	planner := ordermanagement.NewPlanner(omBase, nil)

	receive := &usecases.ReceiveNetworkDemand{
		Orders:      orders,
		Gateway:     gateway,
		Planner:     planner,
		Translation: translation,
		Events:      logPublisher{logger: logger},
		Clock:       systemClock{},
	}
	sweep := &usecases.SweepAcknowledgementDeadlines{
		Orders:  orders,
		Planner: planner,
		Events:  logPublisher{logger: logger},
		Clock:   systemClock{},
	}
	// The inbound leg. Until now ReceiveNetworkDemand was constructed and
	// DISCARDED (`_ = receive`), so nothing in a deployed environment
	// could create a NetworkOrder at all.
	inbound := poller.New(gateway, receive, systemClock{}, poller.Config{
		Interval: pollInterval(),
	}, logger)

	api := &inboundhttp.Server{
		Orders:      orders,
		Poller:      inbound,
		Clock:       systemClock{},
		NetworkMode: string(mode),
	}

	srv := &http.Server{
		Addr:              addr(),
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go runSweep(ctx, sweep, logger)
	go inbound.Run(ctx)

	go func() {
		logger.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	logger.Info("stopped")
}

// runSweep drives the acknowledgement-deadline sweep on a ticker. An
// unanswered order holds real inventory reservations, so the sweep is
// what keeps a missed SLA from quietly becoming unsellable stock.
func runSweep(ctx context.Context, sweep *usecases.SweepAcknowledgementDeadlines, logger *slog.Logger) {
	ticker := time.NewTicker(sweepInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			res, err := sweep.Execute(ctx)
			if err != nil {
				logger.Error("acknowledgement sweep failed", "err", err)
				continue
			}
			if res.Missed > 0 {
				logger.Warn("acknowledgement deadlines missed", "examined", res.Examined, "missed", res.Missed)
			}
		}
	}
}

// wireOrders chooses the NetworkOrderRepo implementation.
//
// With no DATABASE_URL the service runs on the in-memory repo, which is
// what keeps a local run and the unit suite free of infrastructure (ADR
// 0001 §4). With one set, it MUST reach Postgres: returning an error
// rather than falling back is the whole point, because the fallback is
// silent and its cost is the acknowledgement deadlines this context owes
// the network.
//
// Migrations run here, at startup, matching every sibling service in this
// fleet — the alternative is a separate job that can be forgotten, and a
// schema that lags the binary is how a context starts answering wrongly
// rather than not at all.
func wireOrders(ctx context.Context, logger *slog.Logger) (ports.NetworkOrderRepo, func(), error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		logger.Info("order repository wired", "backend", "memory")
		return memory.NewNetworkOrderRepo(), func() {}, nil
	}

	if err := postgres.RunMigrations(databaseURL, migrationsPath()); err != nil {
		return nil, nil, fmt.Errorf("run migrations: %w", err)
	}
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("open pool: %w", err)
	}
	// ParseConfig/NewWithConfig do not themselves establish a connection,
	// so without this the first real failure would surface inside a
	// request rather than at boot — turning a misconfigured deployment
	// into an intermittent 500 instead of a refusal to start.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("ping: %w", err)
	}

	logger.Info("order repository wired", "backend", "postgres")
	return postgres.NewNetworkOrderRepo(pool), pool.Close, nil
}

// migrationsPath is where the migrations live in the container image (see
// Dockerfile), overridable for a local run from the repo root.
func migrationsPath() string {
	if p := os.Getenv("MIGRATIONS_PATH"); p != "" {
		return p
	}
	return "/app/migrations"
}

// seedStubDemand loads NETWORK_SEED_FILE into the stub gateway.
//
// Only meaningful in stub mode, and it type-asserts rather than taking a
// *StubGateway so the composition root keeps depending on the port. A
// seed file set against a real gateway is a configuration MISTAKE worth
// failing on, not something to ignore: it means someone expected demand
// to appear and it silently never would.
func seedStubDemand(gateway ports.NetworkGateway, logger *slog.Logger) error {
	path := os.Getenv("NETWORK_SEED_FILE")
	if path == "" {
		return nil
	}
	stub, ok := gateway.(*network.StubGateway)
	if !ok {
		return fmt.Errorf("NETWORK_SEED_FILE is set but the gateway is not the stub: seeded demand would never be delivered")
	}
	n, err := network.LoadSeedFile(stub, path, time.Now().UTC())
	if err != nil {
		return err
	}
	logger.Info("stub demand seeded", "file", path, "demands", n)
	return nil
}

func pollInterval() time.Duration {
	if v := os.Getenv("POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	// Well under the 24h acknowledgement window, so a restart or a brief
	// outage cannot eat a meaningful fraction of it.
	return time.Minute
}

func sweepInterval() time.Duration {
	if v := os.Getenv("SWEEP_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return time.Minute
}

func addr() string {
	if p := os.Getenv("PORT"); p != "" {
		return ":" + p
	}
	return ":8080"
}
