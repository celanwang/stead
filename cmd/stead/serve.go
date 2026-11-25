package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/celanwang/stead/internal/httpapi"
	"github.com/celanwang/stead/internal/ledger"
)

func runServe(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	address := flags.String("listen", "127.0.0.1:8080", "HTTP listen address")
	cert := flags.String("tls-cert", "", "optional PEM certificate path")
	key := flags.String("tls-key", "", "optional PEM key path")
	if err := parseFlags(flags, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if (*cert == "") != (*key == "") {
		return errors.New("supply both TLS certificate and key")
	}
	model, err := configuredModel()
	if err != nil {
		return err
	}
	return withLedger(ctx, func(ctx context.Context, l *ledger.Ledger) error {
		api, err := httpapi.New(l, model, os.Getenv("STEAD_API_TOKENS"))
		if err != nil {
			return err
		}
		ln, err := net.Listen("tcp", *address)
		if err != nil {
			return err
		}
		defer ln.Close()
		server := &http.Server{
			Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
			WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
		}
		done := make(chan error, 1)
		go func() {
			if *cert != "" {
				done <- server.ServeTLS(ln, *cert, *key)
			} else {
				done <- server.Serve(ln)
			}
		}()
		fmt.Fprintf(os.Stderr, "Serving Stead API on %s\n", ln.Addr())
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := server.Shutdown(stop); err != nil {
				return errors.Join(err, server.Close())
			}
			return nil
		}
	})
}
