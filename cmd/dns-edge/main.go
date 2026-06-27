package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	mdns "github.com/miekg/dns"
	"go.uber.org/zap"

	"dns-edge/config"
	"dns-edge/internal/api"
	dnshandler "dns-edge/internal/dns"
	"dns-edge/internal/geo"
	"dns-edge/internal/iface"
	"dns-edge/internal/pg"
	"dns-edge/internal/store"
	"dns-edge/internal/syncer"
	"dns-edge/internal/weight"
)

func main() {
	corefilePath := flag.String("config", "Corefile", "path to Corefile config")
	autoMigrate := flag.Bool("auto-migrate", false, "run SQL schema before starting (idempotent, requires PG DSN)")
	flag.Parse()

	log, _ := zap.NewProduction()
	defer log.Sync() //nolint:errcheck

	cfg, err := config.ParseFile(*corefilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	log.Info("config loaded",
		zap.String("listen", cfg.Listen),
		zap.Int("workers", cfg.Workers),
		zap.Bool("tcp", cfg.TCP),
	)

	// ── dependency wiring ────────────────────────────────────────────────────
	zoneStore := store.New()
	var weightProvider iface.WeightProvider = weight.Null{} // overridden below when PG+Nacos

	// startup context — separate from the signal context so that a SIGTERM
	// during a slow PG load does not abort the load mid-way.
	startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer startCancel()

	var apiSrv *api.Server
	var pgSyncer *syncer.PGSyncer

	if cfg.PG.DSN != "" {
		pgStore, err := pg.New(startCtx, cfg.PG.DSN, log)
		if err != nil {
			log.Fatal("pg connect", zap.Error(err))
		}
		defer pgStore.Close()

		if *autoMigrate {
			if err := pgStore.EnsureSchema(startCtx); err != nil {
				log.Fatal("pg schema", zap.Error(err))
			}
		}

		// Record the time just before LoadAll so the first incremental sync
		// catches any records written during the load window.
		syncSince := time.Now()
		if err := pgStore.LoadAll(startCtx, zoneStore); err != nil {
			log.Fatal("pg load", zap.Error(err))
		}

		// Weight provider: Nacos (with static fallback) or just static weights.
		// Initialised after LoadAll so nacos.Start can register listeners for
		// every record that was just loaded.
		weightProvider = selectWeightProvider(cfg.Nacos, zoneStore, log)

		pgSyncer = syncer.New(pgStore, zoneStore, syncSince, cfg.Sync, log)

		// Start API server with PG-backed record store.
		apiSrv = api.New(cfg.API, pgStore, zoneStore, log)
		if err := apiSrv.Start(); err != nil {
			log.Fatal("api server start", zap.Error(err))
		}
	} else if cfg.API.Listen != "" {
		// No PG — start API server with nil record store.
		// /api/v1 endpoints (PG-backed CRUD) are unavailable, but
		// edgeDNSAPI, goedge provider, /healthz, and /metrics all work.
		apiSrv = api.New(cfg.API, nil, zoneStore, log)
		if err := apiSrv.Start(); err != nil {
			log.Fatal("api server start", zap.Error(err))
		}
	} else {
		log.Warn("no PG DSN configured — loading seed zone for testing (Phase 1 mode)")
		if err := seedTestZone(zoneStore); err != nil {
			log.Warn("seed zone failed", zap.Error(err))
		}
	}

	// pgSyncer is nil in no-PG mode; handler treats nil syncer as disabled.
	var syncerIface iface.Syncer
	if pgSyncer != nil {
		syncerIface = pgSyncer
	}

	// geo-routing (Phase 13): load xdb if configured
	var geoRouter dnshandler.GeoLookup // interface — stays nil when xdb not configured
	var geoUpdater *geo.Updater
	if cfg.Geo.XDBPath != "" {
		r, geoErr := geo.New(cfg.Geo.XDBPath)
		if geoErr != nil {
			log.Warn("geo-routing disabled: failed to load xdb", zap.String("path", cfg.Geo.XDBPath), zap.Error(geoErr))
		} else {
			log.Info("geo-routing enabled", zap.String("xdb", cfg.Geo.XDBPath))
			defer r.Close()
			geoRouter = r

			if cfg.Geo.AutoUpdate {
				geoUpdater = geo.NewUpdater(geo.UpdaterConfig{
					GithubToken:     cfg.Geo.GithubToken,
					Interval:        cfg.Geo.UpdateInterval,
					DownloadTimeout: 10 * time.Minute,
				}, cfg.Geo.XDBPath, r, log)
				// startup check in background (non-blocking)
				go func() {
					if err := geoUpdater.CheckAndUpdate(false); err != nil {
						log.Warn("ip2region startup update check failed", zap.Error(err))
					}
				}()
			}
		}
	}

	handler := dnshandler.NewHandler(zoneStore, weightProvider, syncerIface, cfg.Sync.Prob, log, geoRouter)

	mux := mdns.NewServeMux()
	mux.Handle(".", handler)

	// ── start servers ────────────────────────────────────────────────────────
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if pgSyncer != nil {
		go pgSyncer.Start(ctx)
	}
	if geoUpdater != nil {
		go geoUpdater.Start(ctx)
	}

	udpSrv := &mdns.Server{Net: "udp", Addr: cfg.Listen, Handler: mux}
	go func() {
		log.Info("DNS/UDP listening", zap.String("addr", cfg.Listen))
		if err := udpSrv.ListenAndServe(); err != nil {
			log.Error("UDP server stopped", zap.Error(err))
		}
	}()

	var tcpSrv *mdns.Server
	if cfg.TCP {
		tcpSrv = &mdns.Server{Net: "tcp", Addr: cfg.Listen, Handler: mux}
		go func() {
			log.Info("DNS/TCP listening", zap.String("addr", cfg.Listen))
			if err := tcpSrv.ListenAndServe(); err != nil {
				log.Error("TCP server stopped", zap.Error(err))
			}
		}()
	}

	log.Info("dns-edge running",
		zap.String("listen", cfg.Listen),
		zap.String("api", cfg.API.Listen),
	)

	<-ctx.Done()
	log.Info("shutdown signal received")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()

	if apiSrv != nil {
		_ = apiSrv.Shutdown(shutCtx)
	}
	_ = udpSrv.ShutdownContext(shutCtx)
	if tcpSrv != nil {
		_ = tcpSrv.ShutdownContext(shutCtx)
	}
	log.Info("dns-edge stopped")
}

// selectWeightProvider builds the appropriate WeightProvider based on config:
//   - Nacos addr set: NacosWeightProvider primary + Static fallback (CompositeWeightProvider)
//   - Nacos addr empty: StaticWeightProvider only
//
// Call after LoadAll so nacos.Start registers listeners for all loaded records.
func selectWeightProvider(cfg config.NacosConfig, store iface.ZoneStore, log *zap.Logger) iface.WeightProvider {
	static := weight.NewStatic(store)
	if cfg.Addr == "" {
		return static
	}
	nacos, err := weight.NewNacosWeightProvider(cfg, log)
	if err != nil {
		log.Warn("nacos weight provider unavailable, using static weights", zap.Error(err))
		return static
	}
	nacos.Start(store)
	return weight.NewComposite(nacos, static)
}

// seedTestZone pre-loads a minimal zone when no PG DSN is configured.
// Seeded records:
//
//	www.example.com.  300 IN A  1.2.3.4
//	api.example.com.   10 IN A  1.2.3.4  (weight 70)
//	api.example.com.   10 IN A  5.6.7.8  (weight 30)
func seedTestZone(s iface.ZoneStore) error {
	parse := func(s string) (mdns.RR, error) { return mdns.NewRR(s) }

	wwwRR, err := parse("www.example.com. 300 IN A 1.2.3.4")
	if err != nil {
		return err
	}
	apiRR1, err := parse("api.example.com. 10 IN A 1.2.3.4")
	if err != nil {
		return err
	}
	apiRR2, err := parse("api.example.com. 10 IN A 5.6.7.8")
	if err != nil {
		return err
	}

	return s.Update(&iface.Zone{
		Name: "example.com.",
		Records: map[iface.RecordKey][]*iface.Record{
			{Name: "www.example.com.", Qtype: mdns.TypeA}: {
				{Name: "www.example.com.", Type: mdns.TypeA, TTL: 300, Value: "1.2.3.4", RR: wwwRR},
			},
			{Name: "api.example.com.", Qtype: mdns.TypeA}: {
				{Name: "api.example.com.", Type: mdns.TypeA, TTL: 10, Value: "1.2.3.4", Weight: 70, RR: apiRR1},
				{Name: "api.example.com.", Type: mdns.TypeA, TTL: 10, Value: "5.6.7.8", Weight: 30, RR: apiRR2},
			},
		},
	})
}
