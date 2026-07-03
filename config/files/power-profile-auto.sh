#!/usr/bin/env bash
# Managed by stay-go — automatic power-profile switching based on AC state.
#   plugged in  -> performance
#   on battery  -> balanced
# Invoked at boot and on every AC hotplug event via the power-profile-auto
# systemd oneshot (see config/hosts/cachygram.yaml).
set -euo pipefail

AC=/sys/class/power_supply/AC0/online

if [[ -r "$AC" && "$(cat "$AC")" -eq 1 ]]; then
    powerprofilesctl set performance
else
    powerprofilesctl set balanced
fi
