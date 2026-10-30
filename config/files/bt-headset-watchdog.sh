#!/usr/bin/env bash
# Managed by stay-go. Watches the journal for the Bluetooth headset's audio
# link failing and does what turning the headset off and on used to do.
#
# The Intel AX211 on cachygram drops the headset's call link (HFP/SCO) mid-call:
# PipeWire logs "Failure in Bluetooth audio transport .../dev_<MAC>/...", its
# nodes go to "error" and stay there while bluez still shows the headset as
# connected. A disconnect + connect rebuilds the link. The chip also
# occasionally crashes outright ("Bluetooth: hciN: Hardware error"); then only a
# USB unbind/bind of the controller brings it back.
#
# Usage: bt-headset-watchdog <MAC>
set -u

mac=${1:?usage: bt-headset-watchdog <MAC>}
mac_=${mac//:/_}
cooldown=60 # seconds; a reconnect logs failures of its own
last=0

notify() { notify-send -a Bluetooth "$@" 2>/dev/null || true; }
log() { echo "$*"; }

connected() { bluetoothctl info "$mac" 2>/dev/null | grep -q 'Connected: yes'; }

# True when bluez has the headset connected but PipeWire has no working node
# for it: none at all, or one stuck in "error".
broken() {
	connected || return 1
	local states
	states=$(pw-dump 2>/dev/null | jq -r --arg m "$mac" '.[]
		| select(.type == "PipeWire:Interface:Node")
		| select(.info.props["api.bluez5.address"] == $m)
		| .info.state')
	[ -z "$states" ] || grep -qx error <<<"$states"
}

connect() {
	local i
	for i in 1 2 3 4 5; do
		bluetoothctl connect "$mac" >/dev/null 2>&1 && return 0
		sleep 3
	done
	return 1
}

reconnect() {
	log "headset audio broken, reconnecting $mac"
	notify "Headset audio failed" "Reconnecting the headset…"
	bluetoothctl disconnect "$mac" >/dev/null 2>&1
	sleep 3
	if connect; then
		log "reconnected $mac"
	else
		log "reconnect failed"
		notify -u critical "Headset reconnect failed" "Turn the headset off and on."
	fi
}

# USB unbind/bind of the controller, e.g. 3-10. Resolved from sysfs rather than
# hardcoded: the kernel may renumber the adapter (hci0 -> hci1) after a reset.
reset_chip() {
	local hci=$1 dev
	dev=$(basename "$(dirname "$(readlink -f "/sys/class/bluetooth/$hci/device")")")
	log "$hci hardware error, resetting USB device $dev"
	notify "Bluetooth chip crashed" "Resetting it and reconnecting the headset…"
	if ! sudo -n sh -c "echo $dev > /sys/bus/usb/drivers/usb/unbind; sleep 3; echo $dev > /sys/bus/usb/drivers/usb/bind"; then
		notify -u critical "Bluetooth reset failed" "sudo -n was refused; reset the chip by hand."
		return
	fi
	sleep 5
	bluetoothctl power on >/dev/null 2>&1
	connect || notify -u critical "Headset reconnect failed" "Turn the headset off and on."
}

log "watching headset $mac"
journalctl -f -n0 -o cat |
	grep --line-buffered -E "Failure in Bluetooth audio transport .*dev_$mac_|Bluetooth: hci[0-9]+: Hardware error" |
	while read -r line; do
		now=$(date +%s)
		((now - last < cooldown)) && continue
		if [[ $line =~ (hci[0-9]+):\ Hardware\ error ]]; then
			last=$now
			reset_chip "${BASH_REMATCH[1]}"
			continue
		fi
		# A profile switch (music -> call) also tears transports down, and its
		# replacement nodes appear within a second or two. Only act if the
		# headset is still broken after a grace period, checked twice.
		sleep 6
		broken || continue
		sleep 4
		broken || continue
		last=$(date +%s)
		reconnect
	done
