#!/usr/bin/env bash
# Managed by stay-go. Watches the journal for the Bluetooth headset's audio
# link failing and does what turning the headset off and on used to do.
#
# The Intel AX211 on cachygram drops the headset's audio when a call switches
# it from music (A2DP) to call mode (HFP/SCO). The journal tells that apart
# from the harmless failures:
#   - harmless: "Failure in Bluetooth audio transport .../dev_<MAC>/..." is
#     followed within a second by bluetoothd's ".../dev_<MAC>/sepN/fdN: fd(N)
#     ready" (a call ending, the music link coming back), or comes with
#     "terminated unexpectedly" (the headset was switched off).
#   - broken: a failure with nothing after it. Over Oct 27-30 every one of
#     those ended with the headset being power-cycled by hand 2-48 s later.
# So a failure not answered by "ready" within $grace seconds triggers a
# disconnect + connect. A reconnect's own failures are answered by "ready"
# like any other, so no settle window is needed after one. PipeWire node state
# is no help: mid-switch the nodes sit in idle/suspended, not error.
#
# The chip also occasionally crashes outright ("Bluetooth: hciN: Hardware
# error"); then only a USB unbind/bind of the controller brings it back.
#
# Usage: bt-headset-watchdog <MAC>
set -u

mac=${1:?usage: bt-headset-watchdog <MAC>}
mac_=${mac//:/_}
grace=3       # seconds a failure may go unanswered before it counts as broken
max_tries=3   # reconnects allowed per $window seconds before giving up
window=180

pending=0     # deadline for an unanswered failure; 0 = none
quiet_until=0
tries=()

notify() { notify-send -a Bluetooth "$@" 2>/dev/null || true; }
log() { echo "$*"; }

connected() { bluetoothctl info "$mac" 2>/dev/null | grep -q 'Connected: yes'; }

connect() {
	local i
	for i in 1 2 3 4 5; do
		bluetoothctl connect "$mac" >/dev/null 2>&1 && return 0
		sleep 2
	done
	return 1
}

reconnect() {
	local now=$1 t recent=()
	for t in "${tries[@]}"; do ((now - t < window)) && recent+=("$t"); done
	tries=("${recent[@]}")
	if ((${#tries[@]} >= max_tries)); then
		log "gave up: $max_tries reconnects in ${window}s did not hold"
		notify -u critical "Headset keeps failing" "Reconnected $max_tries times; turn the headset off and on."
		quiet_until=$((now + window))
		return
	fi
	tries+=("$now")
	log "headset audio failed with no recovery, reconnecting $mac (try ${#tries[@]})"
	notify "Headset audio failed" "Reconnecting the headset…"
	bluetoothctl disconnect "$mac" >/dev/null 2>&1
	sleep 2
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
while :; do
	if IFS= read -r -t 1 line; then
		now=$(date +%s)
		case $line in
		*"Hardware error"*)
			pending=0
			[[ $line =~ (hci[0-9]+):\ Hardware\ error ]] && reset_chip "${BASH_REMATCH[1]}"
			;;
		*"terminated unexpectedly"*) # headset switched off; its failure lines follow
			pending=0
			((quiet_until > now + 2)) || quiet_until=$((now + 2))
			;;
		*" ready") pending=0 ;;                    # link came back
		*"Failure in Bluetooth audio transport"*)
			((now < quiet_until || pending)) || pending=$((now + grace))
			;;
		esac
	elif (($? <= 128)); then
		exit 1 # journalctl went away; systemd restarts us
	fi
	if ((pending)) && (($(date +%s) >= pending)); then
		pending=0
		connected && reconnect "$(date +%s)"
	fi
done < <(journalctl -f -n0 -o cat |
	grep --line-buffered -E "dev_$mac_/.*(Failure|terminated unexpectedly|ready$)|Failure in Bluetooth audio transport .*dev_$mac_|Bluetooth: hci[0-9]+: Hardware error")
