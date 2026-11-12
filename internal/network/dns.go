// Package network provides local, opt-in DNS fallback for outbound connections.
package network

import (
	"context"
	"errors"
	"net"
	"net/http/httptrace"
	"sync"
	"time"
)

// DNS keeps the system resolver first. Servers are validated bootstrap IP:port
// addresses. TCP avoids requiring UDP on restricted NAT hosts.
type DNS struct {
	resolvers      []*net.Resolver
	attemptTimeout time.Duration
}

func NewDNS(servers []string) *DNS {
	d := &DNS{resolvers: []*net.Resolver{net.DefaultResolver}, attemptTimeout: time.Second}
	for _, server := range servers {
		d.resolvers = append(d.resolvers, &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", server)
		}})
	}
	return d
}

func (d *DNS) attempt(ctx context.Context) (context.Context, context.CancelFunc) {
	if len(d.resolvers) == 1 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d.attemptTimeout)
}

func (d *DNS) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	var last error
	for _, resolver := range d.resolvers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		attempt, cancel := d.attempt(ctx)
		addresses, err := resolver.LookupIPAddr(attempt, host)
		cancel()
		if err == nil {
			return addresses, nil
		}
		last = err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, last
}

// DialContext lets net.Dialer retain address-family filtering, multiple address
// attempts and Happy Eyeballs. Only a DNS error permits changing resolver;
// connection refusal or TLS errors never trigger DNS fallback.
func (d *DNS) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var last error
	for _, resolver := range d.resolvers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conn, err := d.dial(ctx, resolver, network, address)
		if err == nil {
			return conn, nil
		}
		last = err
		var dnsErr *net.DNSError
		if !errors.As(err, &dnsErr) {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, last
}

// Limit only DNS work, leaving the caller's full remaining connection budget
// available to net.Dialer for multiple addresses and Happy Eyeballs.
func (d *DNS) dial(ctx context.Context, resolver *net.Resolver, network, address string) (net.Conn, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if len(d.resolvers) > 1 {
		var mu sync.Mutex
		var timer *time.Timer
		stop := func() {
			mu.Lock()
			defer mu.Unlock()
			if timer != nil {
				timer.Stop()
			}
		}
		defer stop()
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
			DNSStart: func(httptrace.DNSStartInfo) {
				mu.Lock()
				defer mu.Unlock()
				timer = time.AfterFunc(d.attemptTimeout, cancel)
			},
			DNSDone: func(httptrace.DNSDoneInfo) { stop() },
		})
	}
	return (&net.Dialer{Resolver: resolver, KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
}
