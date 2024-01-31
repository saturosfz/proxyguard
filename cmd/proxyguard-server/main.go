package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"eduvpn.org/proxyguard"
)

type ServerLogger struct{}

func (sl *ServerLogger) Logf(msg string, params ...interface{}) {
	log.Printf(fmt.Sprintf("[Server] %s\n", msg), params...)
}

func (sl *ServerLogger) Log(msg string) {
	log.Printf("[Server] %s\n", msg)
}

func main() {
	listen := flag.String("listen", "", "The IP:PORT to listen for TCP traffic.")
	to := flag.String("to", "", "The IP:PORT to which to send the converted UDP traffic to. Specify the WireGuard destination.")
	version := flag.Bool("version", false, "Show version information")
	flag.Parse()

	if *version {
		fmt.Printf("proxyguard-server\n%s", proxyguard.Version())
		os.Exit(0)
	}
	// listen and to flags are mandatory
	if *listen == "" {
		fmt.Fprintln(os.Stderr, "Invalid invaction error: Please supply the --listen flag")
		flag.PrintDefaults()
		os.Exit(1)
	}
	if *to == "" {
		fmt.Fprintln(os.Stderr, "Invalid invocation error: Please supply the --to flag")
		flag.PrintDefaults()
		os.Exit(1)
	}
	err := proxyguard.Server(context.Background(), *listen, *to)
	if err != nil {
		log.Fatalf("error occurred when setting up a server: %v", err)
	}
}

func init() {
	proxyguard.UpdateLogger(&ServerLogger{})
}
