#!/bin/bash
# Managed by stay-go. strongSwan updown hook for the Sedanos tunnel.
#
# The tunnel is route-based: its SAs carry if_id 0x5ed, so only packets routed
# into xfrm-sedanos are encrypted. Charon is told not to install routes or the
# virtual IP itself (/etc/strongswan.d/sedanos.conf); this script adds the
# interface, the virtual IP, one route per Sedanos subnet and per-link DNS for
# the Sedanos domains. Everything else, internet included, stays off the tunnel.

IFACE=xfrm-sedanos
IF_ID=0x5ed
SUBNETS=(10.200.0.0/16 10.0.0.0/16)
DOMAINS=(~sedanos.com)
DNS_SERVERS=()
# IKE_SA that last brought the interface up. Reauthentication is
# make-before-break: the new SA's up-client runs before the old SA's
# down-client, which must then leave the interface alone.
OWNER=/run/sedanos-vpn.owner

case "$PLUTO_VERB" in
up-client)
	echo "$PLUTO_UNIQUEID" >"$OWNER"
	ip link del "$IFACE" 2>/dev/null
	ip link add "$IFACE" type xfrm if_id "$IF_ID"
	ip addr add "$PLUTO_MY_SOURCEIP/32" dev "$IFACE"
	ip link set "$IFACE" up
	for net in "${SUBNETS[@]}"; do
		ip route replace "$net" dev "$IFACE" src "$PLUTO_MY_SOURCEIP"
	done

	# The resolve plugin writes the gateway's DNS servers here before the
	# CHILD_SA comes up (/etc/strongswan.d/sedanos.conf). Only servers routed
	# through the tunnel are used: Sedanos currently pushes FortiGuard's public
	# resolvers (96.45.45.45/96.45.46.46), which know no internal names, so
	# DNS_SERVERS is where an internal resolver goes.
	dns=("${DNS_SERVERS[@]}")
	while read -r key addr _; do
		[[ $key == nameserver ]] || continue
		ip route get "$addr" | grep -q " dev $IFACE " && dns+=("$addr")
	done </run/sedanos-vpn.dns 2>/dev/null
	if ((${#dns[@]})); then
		resolvectl dns "$IFACE" "${dns[@]}"
		resolvectl domain "$IFACE" "${DOMAINS[@]}"
		resolvectl default-route "$IFACE" no
	fi
	;;
down-client)
	[[ $(cat "$OWNER" 2>/dev/null) == "$PLUTO_UNIQUEID" ]] || exit 0
	rm -f "$OWNER"
	# Deleting the link drops its routes and its resolved DNS scope with it.
	ip link del "$IFACE" 2>/dev/null
	;;
esac
exit 0
