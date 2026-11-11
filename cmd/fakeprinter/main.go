// Command fakeprinter pretends to be a thermal printer on raw TCP 9100.
//
//	fakeprinter                          # healthy printer on 127.0.0.1:9100
//	fakeprinter -mode single -dump       # one connection at a time, decode each ticket
//	fakeprinter -mode drop -after 200    # reset every connection after 200 bytes
//	fakeprinter -status paper-out        # answers DLE EOT with "paper end"
//
// Everything kept is appended to -out, so escpos-dump can read it later.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/escpos"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fakeprinter"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9100", "address to listen on")
	modeFlag := flag.String("mode", "normal", "normal, refuse, discard, single, stall, midstream or drop")
	after := flag.Int("after", 64, "bytes to read before midstream/drop misbehave")
	statusFlag := flag.String("status", "ok", "DLE EOT answers: ok, paper-out or none")
	out := flag.String("out", "fake-printer.bin", "file to append kept bytes to (empty for none)")
	dump := flag.Bool("dump", false, "print a decoded dump of each connection")
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

	p, err := fakeprinter.New(fakeprinter.Config{
		Mode:      mode,
		StopAfter: *after,
		Status:    status,
		OnConn: func(c fakeprinter.Conn) {
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
