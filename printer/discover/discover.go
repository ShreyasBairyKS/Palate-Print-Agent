// Package discover finds receipt printers on the till's own network, so
// nobody has to look up their IP addresses.
//
// It only ever looks at the networks this PC is on (each narrowed to its
// /24, private ranges only) and the raw printer port 9100, plus the network
// printers Windows already has ports for, and this PC's own port 9100 (the
// demo's fake printer, or a print program on the till). It runs only when
// someone asks: the setup window, the console setup, or "palate-agent printer
// find". The running service never scans.
//
// To tell a receipt printer from anything else listening on 9100, it sends
// the ESC/POS real-time status query DLE EOT 1 (three bytes a receipt printer
// answers and never prints). Office printers ignore it.
//
// It may use package net, but never net/http (arch_test.go checks).
package discover

import (
	"cmp"
	"context"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"
)

// Port is the raw printing port receipt printers listen on.
const Port = 9100

// Found is one device that accepted a connection on the printer port.
type Found struct {
	Addr    string     // "ip:port", as the config holds it
	IP      netip.Addr //
	Answers bool       // answered the ESC/POS status query: a receipt printer
	Windows string     // the Windows printer already set up for this address, if any
}

// Network is one network this PC is on, as it will be searched.
type Network struct {
	Interface string
	Self      netip.Addr   // this PC's address on it
	Prefix    netip.Prefix // what is searched: at most a /24
}

// Options tune a search. The zero value is the normal search.
type Options struct {
	ConnectTimeout time.Duration // per address; default 700ms
	StatusTimeout  time.Duration // wait for a status reply; default 800ms
	Workers        int           // addresses tried at once; default 64
	// Dial replaces net.Dialer.DialContext in tests.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Local is this PC's own printer port, also tried: a print program on the
// till itself, or the demo's fake printer.
const Local = "127.0.0.1:9100"

// MaxNetworks caps how many networks one search covers (254 addresses each).
const MaxNetworks = 4

// Networks lists the networks to search: every connected private IPv4
// network this PC is on, each narrowed to the /24 around this PC so a big
// office network isn't swept.
func Networks() ([]Network, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []Network
	seen := map[netip.Prefix]bool{}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			ones, _ := ipn.Mask.Size()
			if n, ok := narrow(ifc.Name, ip, ones); ok && !seen[n.Prefix] {
				seen[n.Prefix] = true
				out = append(out, n)
			}
		}
	}
	if len(out) > MaxNetworks {
		out = out[:MaxNetworks]
	}
	return out, nil
}

// narrow turns an interface address into the network to search, or false
// when it shouldn't be searched (not private IPv4, or a point-to-point link).
func narrow(name string, ip netip.Addr, ones int) (Network, bool) {
	ip = ip.Unmap()
	if !ip.Is4() || !ip.IsPrivate() || ones > 30 {
		return Network{}, false
	}
	ones = max(ones, 24)
	p, err := ip.Prefix(ones)
	if err != nil {
		return Network{}, false
	}
	return Network{Interface: name, Self: ip, Prefix: p}, true
}

// Hosts lists the addresses on n worth trying: not the network or
// broadcast address, and not this PC.
func Hosts(n Network) []netip.Addr {
	var out []netip.Addr
	first := n.Prefix.Masked().Addr()
	for a := first.Next(); n.Prefix.Contains(a); a = a.Next() {
		if !n.Prefix.Contains(a.Next()) {
			break // the broadcast address
		}
		if a != n.Self {
			out = append(out, a)
		}
	}
	return out
}

// Find searches the networks this PC is on, and the addresses of network
// printers Windows already knows, for receipt printers. It returns what it
// found (receipt printers first) and the networks it searched.
func Find(ctx context.Context, o Options) ([]Found, []Network, error) {
	nets, err := Networks()
	if err != nil {
		return nil, nil, err
	}
	known := windowsPorts()
	var targets []string
	for _, n := range nets {
		for _, a := range Hosts(n) {
			targets = append(targets, netip.AddrPortFrom(a, Port).String())
		}
	}
	for addr := range known {
		targets = append(targets, addr)
	}
	// A printer program on this PC itself (such as the demo's fake printer).
	targets = append(targets, Local)
	found := Scan(ctx, targets, o)
	for i := range found {
		found[i].Windows = known[found[i].Addr]
	}
	return found, nets, ctx.Err()
}

// Scan tries each "ip:port" target, a few dozen at a time, and returns the
// ones that accepted a connection, receipt printers first, then by address.
func Scan(ctx context.Context, targets []string, o Options) []Found {
	workers := cmp.Or(o.Workers, 64)
	jobs := make(chan string)
	var (
		mu    sync.Mutex
		found []Found
		wg    sync.WaitGroup
	)
	seen := map[string]bool{}
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				if f, ok := probe(ctx, t, o); ok {
					mu.Lock()
					found = append(found, f)
					mu.Unlock()
				}
			}
		}()
	}
feed:
	for _, t := range targets {
		if seen[t] {
			continue
		}
		seen[t] = true
		select {
		case jobs <- t:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	slices.SortFunc(found, func(a, b Found) int {
		if a.Answers != b.Answers {
			if a.Answers {
				return -1
			}
			return 1
		}
		if c := a.IP.Compare(b.IP); c != 0 {
			return c
		}
		return cmp.Compare(a.Addr, b.Addr)
	})
	return found
}

// statusQuery is DLE EOT 1, "printer status": a real-time command receipt
// printers answer at once and never print.
var statusQuery = []byte{0x10, 0x04, 1}

func probe(ctx context.Context, target string, o Options) (Found, bool) {
	ap, err := netip.ParseAddrPort(target)
	if err != nil {
		return Found{}, false
	}
	dial := o.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	dctx, cancel := context.WithTimeout(ctx, cmp.Or(o.ConnectTimeout, 700*time.Millisecond))
	conn, err := dial(dctx, "tcp", target)
	cancel()
	if err != nil {
		return Found{}, false
	}
	defer conn.Close()
	f := Found{Addr: target, IP: ap.Addr()}
	conn.SetDeadline(time.Now().Add(cmp.Or(o.StatusTimeout, 800*time.Millisecond)))
	if _, err := conn.Write(statusQuery); err != nil {
		return f, true
	}
	reply := make([]byte, 1)
	if n, _ := conn.Read(reply); n == 1 && validStatus(reply[0]) {
		f.Answers = true
	}
	return f, true
}

// validStatus: every DLE EOT reply has bits 1 and 4 set and bits 0 and 7
// clear (Epson ESC/POS reference), which random bytes rarely match.
func validStatus(b byte) bool { return b&0x12 == 0x12 && b&0x81 == 0 }

// IsPrinterAddr reports whether addr is an address the config would accept
// for a network printer: a private IPv4 literal with a port.
func IsPrinterAddr(addr string) bool {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return false
	}
	ip := ap.Addr().Unmap()
	return ip.Is4() && (ip.IsPrivate() || ip.IsLoopback()) && ap.Port() != 0
}

// joinPort makes "ip:port" from a Windows port's host and port number.
func joinPort(host string, port uint64) (string, bool) {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "", false
	}
	if port == 0 || port > 65535 {
		port = Port
	}
	a := net.JoinHostPort(ip.Unmap().String(), strconv.FormatUint(port, 10))
	return a, IsPrinterAddr(a)
}
