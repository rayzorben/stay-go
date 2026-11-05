#!/bin/bash
# Managed by stay-go. Keeps the Sedanos tunnel up (sedanos-vpn-watchdog.service).
#
# charon already restarts the tunnel on DPD timeout or a gateway close, but the
# FortiGate can also stop answering ESP while IKE (and so DPD) stays healthy,
# e.g. after a rekey this side initiated. So the tunnel is judged by whether a
# Sedanos host answers through it: after FAILS failed probes in a row it is
# torn down and connected afresh. Nothing is tried while the gateway has no
# route (offline, resuming from suspend).

GATEWAY=12.43.107.130
PROBE=10.200.0.1
INTERVAL=20
FAILS=3

connected() {
	swanctl --list-sas --ike sedanos 2>/dev/null | grep -q INSTALLED
}

reconnect() {
	echo "$1; reconnecting"
	swanctl --terminate --ike sedanos --force --timeout 10 >/dev/null 2>&1
	if swanctl --initiate --child sedanos --timeout 60 >/dev/null 2>&1; then
		echo "connected"
	else
		echo "connect failed; retrying in ${INTERVAL}s"
	fi
}

fails=0
while :; do
	if ! ip route get "$GATEWAY" >/dev/null 2>&1; then
		fails=0
	elif ! connected; then
		fails=0
		reconnect "tunnel not established"
	elif ping -c2 -W3 -q "$PROBE" >/dev/null 2>&1; then
		fails=0
	elif ((++fails >= FAILS)); then
		fails=0
		reconnect "$PROBE unreachable through the tunnel $FAILS times"
	fi
	sleep "$INTERVAL"
done
