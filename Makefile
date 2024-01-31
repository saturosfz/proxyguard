PREFIX=/usr/local

.PHONY: all client server fmt lint clean install sloc

all: client server

client: cmd/proxyguard-client/main.go
	go build -o proxyguard-$@ eduvpn.org/proxyguard/cmd/proxyguard-client/...

server: cmd/proxyguard-server/main.go
	go build -o proxyguard-$@ eduvpn.org/proxyguard/cmd/proxyguard-server/...

fmt:
	gofumpt -w .

lint:
	golangci-lint run ./... -E stylecheck,revive,gocritic

sloc:
	cloc --include-ext=go .

clean:
	rm -f proxyguard-client
	rm -f proxyguard-server

install-client: client
	install -m 0755 -D proxyguard-client $(DESTDIR)$(PREFIX)/sbin/proxyguard-client

install-server: server
	install -m 0755 -D proxyguard-server $(DESTDIR)$(PREFIX)/sbin/proxyguard-server
	install -m 0755 -D systemd/proxyguard-server.service $(DESTDIR)$(PREFIX)/lib/systemd/system

install: install-client install-server
