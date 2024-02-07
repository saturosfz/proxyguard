package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"codeberg.org/eduVPN/proxyguard"
)

type ServerLogger struct{}

func (sl *ServerLogger) Logf(msg string, params ...interface{}) {
	log.Printf(fmt.Sprintf("[Server] %s\n", msg), params...)
}

func (sl *ServerLogger) Log(msg string) {
	log.Printf("[Server] %s\n", msg)
}

func main() {
	listen := flag.String("listen", "127.0.0.1:51820", "The IP:PORT to listen for Websocket traffic.")
	to := flag.String("to", "127.0.0.1:51820", "The IP:PORT to which to send the converted UDP traffic to. Specify the WireGuard destination.")
	version := flag.Bool("version", false, "Show version information")
	flag.Parse()

	if *version {
		fmt.Printf("proxyguard-server\n%s", proxyguard.Version())
		os.Exit(0)
	}
	sl := &ServerLogger{}
	proxyguard.UpdateLogger(sl)
	ctx, cancel := context.WithCancel(context.Background())
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	defer func() {
		signal.Stop(c)
		cancel()
	}()
	go func() {
		select {
		case <-c:
			cancel()
		case <-ctx.Done():
			// do nothing
		}
	}()

	err := proxyguard.Server(ctx, *listen, *to)
	if err != nil {
		select {
		case <-ctx.Done():
			sl.Log("exiting...")
		default:
			sl.Logf("error occurred when setting up a server: %v", err)
		}
	}
}
