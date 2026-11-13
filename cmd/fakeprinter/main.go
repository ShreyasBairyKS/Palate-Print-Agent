// Command fakeprinter pretends to be a thermal printer on raw TCP 9100.
//
//	fakeprinter                          # healthy printer on 127.0.0.1:9100
//	fakeprinter -mode single -dump       # one connection at a time, decode each ticket
//	fakeprinter -mode drop -after 200    # reset every connection after 200 bytes
//	fakeprinter -status paper-out        # answers DLE EOT with "paper end"
//	fakeprinter -name Tandoor -control 127.0.0.1:19190   # demo page can switch modes and see tickets
//
// Everything kept is appended to -out, so escpos-dump can read it later.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/escpos"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fakeprinter"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9100", "address to listen on")
	modeFlag := flag.String("mode", "normal", "normal, refuse, discard, single, stall, midstream, drop or off")
	after := flag.Int("after", 64, "bytes to read before midstream/drop misbehave")
	statusFlag := flag.String("status", "ok", "DLE EOT answers: ok, paper-out or none")
	out := flag.String("out", "fake-printer.bin", "file to append kept bytes to (empty for none)")
	dump := flag.Bool("dump", false, "print a decoded dump of each connection")
	name := flag.String("name", "", "printer name shown on the demo page")
	control := flag.String("control", "", "loopback address for the control API used by the demo page (off when empty)")
	flag.Parse()

	mode, err := fakeprinter.ParseMode(*modeFlag)
	if err != nil {
		log.Fatal(err)
	}
	status, err := fakeprinter.ParseStatus(*statusFlag)
	if err != nil {
		log.Fatal(err)
	}

	var sink *os.File
	if *out != "" {
		sink, err = os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatal(err)
		}
		defer sink.Close()
	}

	ctl := &fakeprinter.Control{Name: *name, Addr: *addr}
	p, err := fakeprinter.New(fakeprinter.Config{
		Mode:      mode,
		StopAfter: *after,
		Status:    status,
		OnConn: func(c fakeprinter.Conn) {
			ctl.Record(c)
			log.Printf("conn #%d from %s: read %d bytes, kept %d, %s (%s)",
				c.Seq, c.Remote, c.Read, len(c.Data), c.Note, c.Ended.Sub(c.Started).Round(1e6))
			if sink != nil && len(c.Data) > 0 {
				if _, err := sink.Write(c.Data); err != nil {
					log.Printf("writing %s: %v", *out, err)
				}
			}
			if *dump && len(c.Data) > 0 {
				fmt.Print(escpos.Format(escpos.Decode(c.Data)))
			}
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("fake printer on %s, mode %s, status %s", ln.Addr(), mode, status)

	if *control != "" {
		host, _, err := net.SplitHostPort(*control)
		if ip, perr := netip.ParseAddr(host); err != nil || perr != nil || !ip.IsLoopback() {
			log.Fatalf("-control must be a loopback address such as 127.0.0.1:19190, not %q", *control)
		}
		ctl.Printer = p
		srv := &http.Server{Addr: *control, Handler: ctl.Handler(), ReadHeaderTimeout: 5 * time.Second}
		go func() { log.Fatal(srv.ListenAndServe()) }()
		log.Printf("control API on http://%s", *control)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		<-stop
		log.Print("stopping")
		p.Close()
	}()
	if err := p.Serve(ln); err != nil {
		log.Fatal(err)
	}
}
