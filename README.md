# ProxyGuard

Proxy UDP connections over HTTP(s). The main use case is to proxy WireGuard packets.

It does this by doing a HTTP upgrade request similar to how websockets work.

This means we can tunnel the protocol behind a reverse proxy.

# Dependencies

- Go (target: >= 1.19)

# Building

Run: `make`

> **_NOTE:_**  You can build static binaries with `CGO_ENABLED=0 make`

# Installing

Run: `make install`

This will install the daemons under `/usr/local` and add the `systemd` 
service files. Do not forget to run `systemctl daemon-reload`. After that you
can start the `proxyguard-server` and `proxyguard-client` services.

To configure them, use e.g. `systemctl edit proxyguard-server` and override the
variables you see there with your own values as needed, for example if 
WireGuard is listening on port 443, you can add the following:

```ini
[Service]
Environment=TO=127.0.0.1:443
```

# Running
This tool is focused on a client-server model. This proxy thus needs to run for every client and for a server. The server mode accepts multiple clients.

## Client example

This example listens on local UDP port 1337 and forwards TCP packets (with a HTTP Upgrade handshake) to vpn.example.com

```bash
proxyguard-client --listen 127.0.0.1:1337 --to http://vpn.example.com
```

> **_NOTE:_**  If you test the client on Linux, you might also need to add --fwmark 51820 (or some other number that the WG connection configures) to mark that the packets going out of this proxy are encrypted, preventing a routing loop. 51820 is the default for WireGuard. Note that this needs root.

> **_NOTE:_**  The default HTTP client source port is the same as the UDP listen port, 1337 in this case. To change this, pass --tcpport. You can set it to 0 to automatically pick an available TCP source port.

The received packets from the server (in this case `vpn.example.com`) are forwarded back to the address of the first received UDP packet. So if the proxy receives an UDP packet from port x first, it will remember this as the destination for received packets. This is so that WireGuard can use a dynamic port.


To then use this with WireGuard, you need to change the endpoint in the WireGuard config:

```ini
[Peer]
PublicKey = pubkeyhere
AllowedIPs = 0.0.0.0/0, ::0
Endpoint = 127.0.0.1:1337
```

## Server example

This example starts a HTTP server on TCP port 1337 and forwards UDP packets to localhost 51820, the WireGuard port.

```bash
proxyguard-server --listen 0.0.0.0:1337 --to 127.0.0.1:51820
```

# Acknowledgements & Thanks

The following projects gave me an idea how I could implement this best, most notably how to do the UDP <-> TCP conversion with a custom header. This project started with a UDP to TCP approach, but later moved to HTTP for use behind a reverse proxy and improved obfuscation.
- https://github.com/mullvad/udp-over-tcp
- https://github.com/rfc1036/udptunnel
- https://nhooyr.io/websocket

# License
[MIT](./LICENSE)
