package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"

	"codeberg.org/eduVPN/proxyguard"
)

type ClientLogger struct{}

func (cl *ClientLogger) Logf(msg string, params ...interface{}) {
	log.Printf(fmt.Sprintf("[Client] %s\n", msg), params...)
}

func (cl *ClientLogger) Log(msg string) {
	log.Printf("[Client] %s\n", msg)
}

func main() {
	fwmark := flag.Int("fwmark", -1, "[Linux only] The fwmark/SO_MARK to use on the underlying TCP socket. -1 is disable.")
	listen := flag.String("listen", "", "The IP:PORT to listen for UDP traffic.")
	tcpsp := flag.Int("tcpport", -1, "The PORT to use as the TCP source port. The default is -1, which indicates the same port as the UDP listen. Set this to zero to allocate a freely available port.")
	to := flag.String("to", "", "The IP:PORT to which to send the converted TCP traffic to. Specify the server endpoint which also runs Proxyguard.")
	version := flag.Bool("version", false, "Show version information")
	flag.Parse()

	if *version {
		fmt.Printf("proxyguard-client\n%s", proxyguard.Version())
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
	if *fwmark != -1 && runtime.GOOS != "linux" {
		fmt.Fprintln(os.Stderr, "Invalid invocation warning: The --fwmark flag is a NO-OP when you're not using Linux. We will ignore it...")
		*fwmark = -1
	}

	cl := &ClientLogger{}
	proxyguard.UpdateLogger(cl)

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
	err := proxyguard.Client(ctx, *listen, *tcpsp, *to, *fwmark)
	if err != nil {
		select {
		case <-ctx.Done():
			cl.Log("exiting...")
		default:
			cl.Logf("error occurred when setting up a client: %v", err)
		}
	}
}
