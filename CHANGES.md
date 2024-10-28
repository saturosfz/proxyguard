# UNRELEASED
- Client:
    - Add support for setting the UserAgent
    - Remove code that dynamically waits for an UDP packet
    - The listen (`ip:port` combo) flag has been changed to `listen-port` as ProxyGuard always uses localhost for the IP
    - New mandatory option: `forward-port` to indicate which port WireGuard is sending traffic from, and thus where the traffic needs to be forwarded back to
    - Go API Client struct: 
      - `UserAgent` new option to set the user agent for the HTTP handshake
      - `Listen` has been replaced by `ListenPort`
      - `Ready` callback has been removed as setting up DNS is now separated with `SetupDNS` function
      - `Peer` new option to specify the peer
      - `PeerIPs` new option to specify the DNS IPs of the peer
      - `setupSocket` callback no longer takes the peer IPs as the second argument, but only contains the file descriptor
      - The tunnel function no longer takes the peer and peer IPs, but takes the port WireGuard is listening on (the forward port)
    - Only re-establish a handshake once traffic has been sent
    - Only support TLS >= 1.3
    - Copy over settings from the default HTTP transport
- Add some helpful scripts in the `contrib` directory
- README:
  - Add technical docs
  - Document the goal
- Makefile:
  - Use `tokei` in `sloc` target if available
  - Fix `make install`
- Workflows:
  - Initial Forgejo CI

# 1.0.1 (2024-04-05)

- Client: Remove the abort on max restart and instead wait 10s

# 1.0.0 (2024-04-04)

- Client: Ensure the hostname is used for a DNS request instead of the host:port
- Client: Mark some errors as 'fatal' such that retrying does not happen
- Client: Loop using a more fancy restart loop that uses a variable wait time and max restarts
- Server: Do not spam log by not logging EOF, TCP reader or clean exits
- Server: Set Upgrade and Connection headers sooner
- Client+Server: Compare headers case insensitive
- Client+Server: Set Upgrade protocol from wireguard to UoTLV/1: UDP over TCP Length Value version 1
- General: Rename Proxyguard to ProxyGuard

# 0.4.0 (2024-03-11)

- Client: Make it a struct type for a nicer Go API
- Client: Set the default source port to 0
- Client + Server: Make the TCP reader timeout after 60 seconds
- Client: Ability to re-use source port on Linux, for reconnecting purposes
- Client: Do a DNS request inside Proxyguard if no --peer-ips are set and cache it so that reconnecting cannot fail

# 0.3.0 (2024-02-22)

- Client: Implement a --peer-ips flag to bypass DNS resolution
- Client: Log each IP that is being connected to in the --peer-ip case
- Client: Add a callback when the proxy is ready

# 0.2.0 (2024-02-13)

- Add a file descriptor callback mechanism to the client for eduvpn-common + Android integration
- Add a basic reconnect/retry mechanism to the client (fixes: #15)
- Update systemd files for the HTTP change
- Proxy over HTTP such that you can use it with a reverse proxy, e.g. Apache. This is done similar to Websockets with HTTP upgrade requests
- Add systemd files for Proxyguard client mode. Make sure it starts after the network is up

# 0.1.0 (2024-01-29)

- initial release of Proxyguard. A Go program and library that tunnels UDP over TCP, to use with WireGuard
