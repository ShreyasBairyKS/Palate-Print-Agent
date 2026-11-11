// Command escpos-dump shows an ESC/POS byte stream as hex beside a human
// reading of each command.
//
//	escpos-dump fake-printer.bin
//	escpos-dump < ticket.bin
//	escpos-dump -raw ticket.bin    # plain hexdump, no decoding
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"palate-print-agent/internal/escpos"
)

func main() {
	raw := flag.Bool("raw", false, "print a plain hexdump instead of decoding")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: escpos-dump [-raw] [file]   (reads stdin when no file is given)")
		flag.PrintDefaults()
	}
	flag.Parse()

	var in io.Reader = os.Stdin
	if flag.NArg() > 1 {
		flag.Usage()
		os.Exit(2)
	}
	if flag.NArg() == 1 {
		f, err := os.Open(flag.Arg(0))
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		in = f
	}

	data, err := io.ReadAll(in)
	if err != nil {
		log.Fatal(err)
	}
	if *raw {
		fmt.Print(hex.Dump(data))
		return
	}
	fmt.Print(escpos.Format(escpos.Decode(data)))
}
