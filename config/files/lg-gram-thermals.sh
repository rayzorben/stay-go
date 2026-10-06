#!/usr/bin/env bash
# Managed by stay-go — thermal envelope for the LG gram Pro 16" 2-in-1
# (16T90SP-G, Core Ultra 7 155H). Run at boot and on resume by
# lg-gram-thermals.service; see config/hosts/cachygram.yaml.
#
# This chassis puts a single blower over one heatpipe, good for roughly 15-18 W
# sustained. Stock firmware asks for far more than that (PL1 35 W, PL2 43 W,
# passive trip at 100-106 C), so the die reaches PROCHOT within seconds of any
# multi-threaded load and then oscillates down into 400-800 MHz valleys — tens
# of thousands of package throttle events per minute. Every knob below exists to
# bring the SoC's demand under what the chassis can actually shed:
#
#   power profile  balanced             power-profiles-daemon's `performance`
#                                       profile writes EPP=performance to every
#                                       core, which is the single setting this
#                                       script exists to keep off the machine.
#   fan_mode       1                    EC drives the blower actively instead of
#                                       running its silent/passive curve.
#   no_turbo       1                    Removes the Vcore overshoot that trips
#                                       PROCHOT on single/dual P-core bursts.
#   EPP            balance_performance  Lets unloaded cores drop into C6/C10
#                                       rather than holding a high base clock.
#   PL1            16 W                 Sustained draw matched to the heatpipe.
#   PL2            22 W                 Burst ceiling that does not saturate the
#                                       interface material before the fan ramps.
#
# Measured after: package 68-71 C (was 99-104 C), 9.6 W (was 15.5-19.5 W), zero
# throttle events per 10 s window (was 1500-5700), P-cores steady 890-1300 MHz.
#
# The EC resets fan_mode to 0 across every ACPI sleep state, which is why the
# unit is wanted by the sleep targets as well as by multi-user.target.
#
# Idempotent, and never fatal: a knob this firmware does not expose is logged
# and skipped rather than failing the boot. PL4 (peak_power, 83 W) is left
# alone — it is a sub-millisecond ceiling the cooling never sees.
set -uo pipefail

PL1_UW=16000000              # long_term  constraint
PL2_UW=22000000              # short_term constraint
EPP=balance_performance

FAN_MODE=/sys/devices/platform/lg-laptop/fan_mode
NO_TURBO=/sys/devices/system/cpu/intel_pstate/no_turbo

log() { printf 'lg-gram-thermals: %s\n' "$*"; }

# poke <value> <path> — write unless the node already holds that value.
poke() {
    local value=$1 path=$2
    if [[ ! -e $path ]]; then
        log "skip $path (not present)"
        return 1
    fi
    if [[ $(cat "$path" 2>/dev/null) == "$value" ]]; then
        return 0
    fi
    if printf '%s\n' "$value" > "$path" 2>/dev/null; then
        log "set $path = $value"
        return 0
    fi
    log "FAILED to set $path = $value"
    return 1
}

# lg_laptop and intel_rapl_msr are udev-autoloaded and can land in the same
# second this unit starts, so wait briefly for the nodes they own rather than
# writing into paths that do not exist yet.
for _ in $(seq 1 30); do
    [[ -e $FAN_MODE && -e /sys/class/powercap/intel-rapl:0/name ]] && break
    sleep 0.5
done

# The power profile goes first: PPD rewrites EPP on every profile change, so it
# has to settle on `balanced` before the per-core EPP writes below.
if command -v powerprofilesctl > /dev/null 2>&1; then
    current=$(powerprofilesctl get 2>/dev/null || true)
    if [[ -n $current && $current != balanced ]]; then
        if powerprofilesctl set balanced 2>/dev/null; then
            log "power profile: $current -> balanced"
        else
            log "FAILED to set power profile to balanced (was $current)"
        fi
    fi
fi

poke 1 "$FAN_MODE"
poke 1 "$NO_TURBO"

applied=0
failed=0
for f in /sys/devices/system/cpu/cpu*/cpufreq/energy_performance_preference; do
    [[ -e $f ]] || continue
    if printf '%s\n' "$EPP" > "$f" 2>/dev/null; then
        applied=$((applied + 1))
    else
        failed=$((failed + 1))
    fi
done
if (( failed > 0 )); then
    log "EPP=$EPP on $applied cores, $failed failed"
else
    log "EPP=$EPP on $applied cores"
fi

# Both the MSR (intel-rapl:N) and MMIO (intel-rapl-mmio:N) package domains carry
# the same PL1/PL2 pair and the effective cap is the lower of the two, so write
# both and the limit stops depending on which one the firmware last touched.
# Constraints are matched by name — their indices are not fixed across domains.
for dir in /sys/class/powercap/intel-rapl:*/ /sys/class/powercap/intel-rapl-mmio:*/; do
    [[ -r ${dir}name ]] || continue
    [[ $(cat "${dir}name") == package-* ]] || continue
    for cname_file in "${dir}"constraint_*_name; do
        [[ -r $cname_file ]] || continue
        limit="${cname_file%_name}_power_limit_uw"
        case $(cat "$cname_file") in
            long_term)  poke "$PL1_UW" "$limit" ;;
            short_term) poke "$PL2_UW" "$limit" ;;
        esac
    done
done

exit 0
