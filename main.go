// Command glade is a small status page. It checks websites and ports
// every minute or so, keeps up to 90 days of results, and serves a page
// with their state, bars for their history, and incidents written by hand in
// markdown. The page needs no JavaScript: it reloads itself after every
// round of checks.
//
//	glade                       # glade.toml in this folder
//	glade -config /etc/glade/glade.toml -listen 127.0.0.1:9000
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/obk/glade/internal/config"
	"github.com/obk/glade/internal/incident"
	"github.com/obk/glade/internal/monitor"
	"github.com/obk/glade/internal/server"
)

func main() {
	configPath := flag.String("config", "glade.toml", "the config file")
	listen := flag.String("listen", "", "address to serve on (default: listen in the config, or 127.0.0.1:8080)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: glade [-config glade.toml] [-listen host:port]")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	log.SetFlags(0)
	log.SetPrefix("glade: ")

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	// A broken incident would only show as a line on the page; say so now,
	// while someone is watching.
	if _, err := incident.ReadAll(cfg.Incidents); err != nil {
		log.Fatal(err)
	}
	m, err := monitor.New(cfg)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	looped := make(chan struct{})
	go func() {
		m.Loop(ctx)
		close(looped)
	}()

	// Timeouts, so that slow or idle clients can't hold connections open:
	// nothing glade serves takes long, and no request has a body.
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(cfg, m),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()
	log.Printf("%d checks every %s; serving on http://%s/", len(cfg.Checks), cfg.Interval, cfg.Listen)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
	// Let a round in progress save its results before the database closes.
	<-looped
	if err := m.Close(); err != nil {
		log.Fatal(err)
	}
}
