#!/bin/sh

# whether or not to allow traffic to devices on the LAN
ALLOW_LAN=yes
#ALLOW_LAN=no

if [ -z ${1} ]; then
    echo "ERROR: specify WireGuard VPN configuration file"
    exit 1
fi

VPN_CONF_FILE=${1}
if ! [ -f ${VPN_CONF_FILE} ]; then
    echo "ERROR: Missing file '${VPN_CONF_FILE}'"
    exit 1
fi

VPN_NAME=$(basename ${VPN_CONF_FILE} .conf)

if [ -f /etc/debian_version ]; then
    # Debian, Ubuntu
    CFG_FILE=/etc/default/proxyguard-client
elif [ -f /etc/redhat-release ]; then
    # Fedora, EL
    CFG_FILE=/etc/sysconfig/proxyguard-client
else
    echo "ERROR: OS not supported"
    exit 1
fi

nmcli con del ${VPN_NAME}

# extract ProxyEndpoint, and comment the field
PROXY_ENDPOINT=$(cat ${VPN_CONF_FILE} | grep 'ProxyEndpoint =' | head -1 | awk {'print $3'})
sed -i 's/^ProxyEndpoint =/#ProxyEndpoint =/' ${VPN_CONF_FILE}

# determine the "Peer IPs"
DNS_HOST=$(echo ${PROXY_ENDPOINT} | cut -d '/' -f 3)
PEER_IPS=$(host ${DNS_HOST} | grep "address" | awk {'print $NF'} | tr "\n" "," | sed 's/,$//')
if [ "" == "${PEER_IPS}" ]; then
    echo "ERROR: unable to determine IP address(es) of ProxyEndpoint"
    exit 1
fi

# configure proxyguard-client
echo "LISTEN_PORT=54321" | sudo tee ${CFG_FILE} > /dev/null
echo "TO=${PROXY_ENDPOINT}" | sudo tee -a ${CFG_FILE} > /dev/null
echo "PEER_IPS=${PEER_IPS}" | sudo tee -a ${CFG_FILE} > /dev/null

nmcli con import type wireguard file ${VPN_CONF_FILE}
nmcli con down ${VPN_NAME}

nmcli con modify ${VPN_NAME} wireguard.listen-port 54321
nmcli con modify ${VPN_NAME} wireguard.fwmark "$(printf %x 54321)"
nmcli con modify ${VPN_NAME} wireguard.ip4-auto-default-route 0
nmcli con modify ${VPN_NAME} wireguard.ip6-auto-default-route 0
nmcli con modify ${VPN_NAME} ipv4.route-table 54321
nmcli con modify ${VPN_NAME} ipv6.route-table 54321
if [ "${ALLOW_LAN}" == "yes" ]; then
    nmcli con modify ${VPN_NAME} ipv4.routing-rules "priority 1 from all lookup main suppress_prefixlength 0"
    nmcli con modify ${VPN_NAME} ipv6.routing-rules "priority 1 from all lookup main suppress_prefixlength 0"
fi
nmcli con modify ${VPN_NAME} +ipv4.routing-rules "priority 2 not fwmark 54321 table 54321"
nmcli con modify ${VPN_NAME} +ipv6.routing-rules "priority 2 not fwmark 54321 table 54321"

if grep "0.0.0.0/0" ${VPN_CONF_FILE}; then
    # prevent DNS leak outside of VPN tunnel when "default gateway VPN"
    nmcli con modify ${VPN_NAME} ipv4.dns-search "~."
    nmcli con modify ${VPN_NAME} ipv6.dns-search "~."
    nmcli con modify ${VPN_NAME} ipv4.dns-priority -1
    nmcli con modify ${VPN_NAME} ipv6.dns-priority -1
fi

sudo systemctl restart proxyguard-client
nmcli con up ${VPN_NAME}
