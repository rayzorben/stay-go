#!/bin/sh
# Managed by stay-go (config/files/lg-kbd-backlight.sh).
#
# Keyboard backlight control for the 2024 LG Gram (16T90SP). Writes to the
# lg_laptop driver's /sys/class/leds/kbd_backlight change nothing on this
# firmware. The ACPI WMI method \_SB.MAP1.WMAB, sub-function 0x0400, drives the
# backlight through the Embedded Controller:
#   WMAB 0x0400 0x01 0          get the last value set (the firmware global \KBBS)
#   WMAB 0x0400 0x02 <value>    send it to the EC (BKLC -> EC command 0xC8)
# The firmware names the values 0xA4 off, 0xA2 low, 0x80 high, but on this
# keyboard they do not mean that. Watching the keys while sending each one
# showed 0xA4 and 0xA2 both light it at full brightness and 0x80 turns it off;
# there is no dim level. The EC holds whatever was sent (it stayed lit for days
# with nothing re-sending it), so there is no idle timeout to fight.
# Calls go through the acpi_call module's /proc/acpi/call, which stay-go leaves
# world-writable so the niri bind can run this as the user.
#
# Usage: lg-kbd-backlight [on|off|toggle|status]   (default: on)

set -eu

ACPI_NODE=/proc/acpi/call
WMAB='\_SB.MAP1.WMAB 0x0400'
ON=0xA4
OFF=0x80

acpi_call() {
    printf '%s' "$1" > "$ACPI_NODE"
    tr -d '\0' < "$ACPI_NODE"
}

current() {
    case "$(acpi_call "$WMAB 0x01 0")" in
        0x80) echo off ;;
        *)    echo on ;;
    esac
}

set_state() {
    case "$1" in
        on)  acpi_call "$WMAB 0x02 $ON" > /dev/null ;;
        off) acpi_call "$WMAB 0x02 $OFF" > /dev/null ;;
    esac
}

if [ ! -e "$ACPI_NODE" ]; then
    modprobe acpi_call 2>/dev/null || {
        echo "lg-kbd-backlight: acpi_call module not loaded" >&2
        exit 1
    }
fi

cmd=${1:-on}
case "$cmd" in
    on|off) set_state "$cmd" ;;
    toggle)
        case "$(current)" in
            on)  set_state off ;;
            off) set_state on ;;
        esac
        ;;
    status) current ;;
    *)
        echo "usage: $0 [on|off|toggle|status]" >&2
        exit 2
        ;;
esac
