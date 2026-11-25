package ingress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"time"
)

func Listen(address string) (net.Listener, error) {
	addr, err := netip.ParseAddrPort(address)
	if err != nil || !addr.Addr().IsLoopback() || addr.Addr().Zone() != "" {
		return nil, errors.New("ingress requires a numeric loopback IP and port")
	}
	return net.Listen("tcp", addr.String())
}

func Serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(stopped)
		grace, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(grace); err != nil {
			_ = server.Close()
		}
	})
	err := server.Serve(listener)
	if !stop() {
		<-stopped
	}
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		return nil
	}
	return err
}
