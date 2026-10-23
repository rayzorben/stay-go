#!/bin/sh
# Turn the Slate AX's LAN port eth1 into a second wired WAN ("wan2") for the
# GL-AR300M Wi-Fi uplink box. Starlink stays on the WAN port (eth0).
#
#   eth1 leaves br-lan, gets DHCP from the AR300M, joins the wan firewall
#   zone (masquerade, input DROP). Metric 4 keeps it behind wan (1), wwan (2)
#   and tethering (3) in the main table; net-bond-007 auto-adds it as link
#   "eth1" (interfaces.autoAdd, not in the exclude list).
#
# The AR300M must hand out a subnet other than 192.168.8.0/24 on the port
# facing the Slate (its GL default LAN is 192.168.8.1, same as the Slate).
#
# Idempotent. Run on the router:
#   ssh root@192.168.8.1 'sh -s' < deploy/router/wan2.sh            # apply
#   ssh root@192.168.8.1 'sh -s revert' < deploy/router/wan2.sh     # undo
set -eu

PORT=${PORT:-eth1}
IFACE=wan2

wan_zone() {
	uci show firewall | sed -n "s/^firewall\.\(@zone\[[0-9]*\]\)\.name='wan'$/\1/p" | head -n1
}

lan_bridge() {
	uci show network | sed -n "s/^network\.\(@device\[[0-9]*\]\)\.name='br-lan'$/\1/p" | head -n1
}

reload() {
	uci commit network
	uci commit firewall
	/etc/init.d/network reload
	/etc/init.d/firewall reload >/dev/null 2>&1
}

Z=$(wan_zone)
BR=$(lan_bridge)
[ -n "$Z" ] && [ -n "$BR" ] || { echo "wan zone or br-lan not found" >&2; exit 1; }

if [ "${1:-}" = revert ]; then
	uci -q del_list firewall.$Z.network=$IFACE || true
	uci -q delete network.$IFACE || true
	uci -q del_list network.$BR.ports=$PORT || true
	uci add_list network.$BR.ports=$PORT
	reload
	logger -t wan2 "reverted: $PORT back in br-lan"
	echo "reverted: $PORT back in br-lan"
	exit 0
fi

uci -q del_list network.$BR.ports=$PORT || true
uci set network.$IFACE=interface
uci set network.$IFACE.device=$PORT
uci set network.$IFACE.proto=dhcp
uci set network.$IFACE.metric=4
uci set network.$IFACE.ipv6=0
uci set network.$IFACE.classlessroute=0
uci -q del_list firewall.$Z.network=$IFACE || true
uci add_list firewall.$Z.network=$IFACE
reload
logger -t wan2 "applied: $PORT is $IFACE (dhcp, metric 4, wan zone)"
echo "applied: $PORT is $IFACE (dhcp, metric 4, wan zone)"
