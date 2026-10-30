#!/usr/bin/env bash
set -euo pipefail

# ==========================================
# GL.iNet Slate AX (GL-AXT1800) router setup
# ==========================================
# Not run by stay-go: run it by hand from any machine with SSH key access
# (key in the router's /etc/dropbear/authorized_keys):
#
#   scripts/slate-ax-router.sh [root@192.168.8.1]
#
# Idempotent. The router side runs under BusyBox ash (no bash there).
#
# Why (2026-10-10): the router hung and needed a hard power cycle. No panic
# (panic=3 would have rebooted it) and the hardware watchdog never fired, so
# it was alive but starved. It has 414 MB usable, no swap, and no zram support
# in the GL kernel. ath11k pins ~95 MB of unreclaimable slab, and with
# Speedify + ZeroTier + Tailscale + AdGuard the commit charge was 523 MB, so
# AdGuard's daily blocklist reload (old + new list in memory) had no headroom.

ROUTER="${1:-root@192.168.8.1}"

ssh -o ConnectTimeout=8 "$ROUTER" sh -s <<'ROUTER_EOF'
set -e

# ------------------------------------------
# Speedify: unlicensed (ERROR_NO_ROUTER_LICENSE every 30s), so it routed
# nothing while holding a large commit reservation.
# ------------------------------------------
for s in speedify sfy-ws-auth; do
    [ -x /etc/init.d/$s ] && { /etc/init.d/$s stop; /etc/init.d/$s disable; }
done
pkgs=$(opkg list-installed | awk '/^(luci-app-speedify|speedify) /{print $1}')
[ -n "$pkgs" ] && opkg remove $pkgs

# ------------------------------------------
# ZeroTier: redundant with Tailscale.
# ------------------------------------------
[ -x /etc/init.d/zerotier ] && { /etc/init.d/zerotier stop; /etc/init.d/zerotier disable; }
killall zerotier-one 2>/dev/null || true
pkgs=$(opkg list-installed | awk '/^(gl-sdk4-ui-zerotierview|gl-sdk4-zerotier|zerotier) /{print $1}')
[ -n "$pkgs" ] && opkg remove $pkgs
rm -rf /etc/config/zerotier /var/lib/zerotier-one_gl
changed=0
for k in zerotier zerotier_lan lan_zerotier zerotier_wan; do
    uci -q delete firewall.$k && changed=1
done
uci -q delete network.zerotier && changed=1
if [ $changed = 1 ]; then
    uci commit firewall
    uci commit network
    /etc/init.d/firewall reload >/dev/null 2>&1
    ubus call network reload
fi

# ------------------------------------------
# memwatch: a memory/load line in syslog every 5 minutes, so a slide toward
# exhaustion shows up in `logread -e memwatch` while it is happening.
# ------------------------------------------
cat > /usr/bin/memwatch <<'SH'
#!/bin/sh
# Logs a memory/load snapshot to syslog. Run from cron every 5 minutes.
m=$(awk '/^(MemAvailable|SUnreclaim|Committed_AS):/{printf "%s=%dM ", $1, $2/1024}' /proc/meminfo | tr -d :)
c=$(cat /proc/sys/net/netfilter/nf_conntrack_count 2>/dev/null)
l=$(cut -d" " -f1-3 /proc/loadavg)
r=""
for p in tailscaled AdGuardHome nginx; do
  kb=0; for pid in $(pidof $p); do kb=$((kb + $(awk '/^VmRSS:/{print $2}' /proc/$pid/status 2>/dev/null || echo 0))); done
  r="$r $p=$((kb / 1024))M"
done
logger -t memwatch "${m}conntrack=$c load=$l rss:$r"
SH
chmod 755 /usr/bin/memwatch
grep -q memwatch /etc/crontabs/root 2>/dev/null || echo '*/5 * * * * /usr/bin/memwatch' >> /etc/crontabs/root
grep -qx /usr/bin/memwatch /etc/sysupgrade.conf || echo /usr/bin/memwatch >> /etc/sysupgrade.conf

# ------------------------------------------
# Multi-WAN: ethernet (wan/eth0) and repeater wifi (wwan/sta0) as failover
# members with Interface Status Track off ("internet always available when
# the interface is connected").
#
# Failover, not balance: balance mode gives every member with a weight a
# nexthop in a multipath default route (gen_weight_route ignores disabled=),
# which would split traffic across eth0 and the repeater uplink. Failover
# keeps plain metric routing (wwan metric 1, wan metric 2 as of 2026-10-10).
#
# GL bug: kmwan.sh's load_netcell_from_config never reads check='0', so the
# module probes the track IPs anyway. Probe replies on eth0 (an NSS port)
# never reach the module (tcpdump shows them on the wire), so wan sits at
# DEAD and the UI shows it disconnected however it is configured. With no
# tracks the module refuses the cell outright. kmwan-notrack (cron, every
# minute) applies check='0' itself: once such a member is seen online, it
# stops probing (STOP_PROBE freezes the state). ifdown still removes it.
# ------------------------------------------
cat > /usr/bin/kmwan-notrack <<'SH'
#!/bin/sh
# GL kmwan ignores a member's check='0' ("Enable Interface Status Track" off):
# load_netcell_from_config always hands the module the ping targets, so it
# probes anyway, and on this router probe replies on eth0 (an NSS port) never
# reach the module, so the interface sits at DEAD / "disconnected". The
# module's STOP_PROBE op freezes the current state, so once such a member has
# been seen online, stop its probing: it then stays online until ifdown
# removes it (cable unplugged / lease lost), which is what check='0' promises.
# Run from cron every minute (covers boot, ifup hotplug and UI Apply).
[ "$(uci -q get kmwan.global.enable)" = 1 ] || exit 0
. /lib/functions/kmwan.sh
for m in $(uci show kmwan | sed -n "s/^kmwan\.\([^.=]*\)=member$/\1/p"); do
    [ "$(uci -q get kmwan.$m.disabled)" = 1 ] && continue
    [ "$(uci -q get kmwan.$m.check)" = 0 ] || continue
    i=$(uci -q get kmwan.$m.interface)
    probing=$(awk -v i="$i" '$1 == i && NF > 5 {c = 1} c && /^probe_enable:/ {print $1; exit}' /proc/gl-kmwan/status)
    if [ "$probing" = "probe_enable:true" ] && grep -q "^$i:online$" /proc/gl-kmwan/config; then
        stop_probe "$i"
        logger -t kmwan-notrack "$i online; probing stopped (status track disabled)"
    elif [ "$probing" = "probe_enable:false" ] && ! grep -q "^$i:online$" /proc/gl-kmwan/config; then
        # frozen offline (stopped while down): probe again until it is seen up
        start_probe "$i"
    fi
done
SH
chmod 755 /usr/bin/kmwan-notrack
grep -q kmwan-notrack /etc/crontabs/root 2>/dev/null || echo '* * * * * /usr/bin/kmwan-notrack' >> /etc/crontabs/root
grep -qx /usr/bin/kmwan-notrack /etc/sysupgrade.conf || echo /usr/bin/kmwan-notrack >> /etc/sysupgrade.conf

# Members: wan (ethernet, eth0) and wwan (repeater wifi, sta0).
MEMBERS="wan wwan"
want="enable=1 mode=failover"
have="enable=$(uci -q get kmwan.global.enable) mode=$(uci -q get kmwan.global.mode)"
for m in $MEMBERS; do
    want="$want $m.disabled=0 $m.check=0 $m.track_mode=force"
    have="$have $m.disabled=$(uci -q get kmwan.$m.disabled) $m.check=$(uci -q get kmwan.$m.check) $m.track_mode=$(uci -q get kmwan.$m.track_mode)"
done
if [ "$want" != "$have" ]; then
    uci set kmwan.global.enable='1'
    uci set kmwan.global.mode='failover'
    for m in $MEMBERS; do
        uci set kmwan.$m.disabled='0'
        uci set kmwan.$m.check='0'
        uci set kmwan.$m.track_mode='force'
        # the module drops a cell with no tracks, so keep GL's defaults
        uci -q get kmwan.$m.tracks >/dev/null || for t in 1.1.1.1 1.0.0.1 8.8.8.8 9.9.9.9; do
            uci add_list kmwan.$m.tracks="ping,$t"
        done
    done
    uci commit kmwan
    /etc/init.d/kmwan restart
fi
/etc/init.d/cron enable
/etc/init.d/cron restart

# ------------------------------------------
# SQM: cake on the upload of both uplinks; download left unshaped.
#
# Measured 2026-10-10 on the repeater uplink (Motorcoach_Guest): download
# under full load stayed at idle latency (35 ms), but a saturated upload took
# the worst case from 71 ms to 197 ms, which lags anything interactive. With
# cake at 3400 kbit (~80% of the ~4.2 Mbit/s measured upload) the loaded
# worst case is 48 ms, same as idle. download '0' = no ingress shaping.
# nat + dual-srchost: fairness per LAN device behind the masquerade;
# ack-filter: thins redundant ACKs on the thin uplink.
# GL's own "QoS" (gl-sdk4-qos / gl_eqos) is only per-device HTB rate caps,
# no AQM, so it is left disabled. Retune upload if the uplink changes.
# ------------------------------------------
for p in sqm-scripts kmod-sched-cake; do
    opkg list-installed | grep -q "^$p " || need="$need $p"
done
if [ -n "$need" ]; then
    opkg update >/dev/null
    opkg install $need
fi
uci -q delete sqm.eth1 || true  # sqm-scripts' unused example queue
for q in sta0 eth0; do
    uci set sqm.$q=queue
    uci set sqm.$q.enabled='1'
    uci set sqm.$q.interface="$q"
    uci set sqm.$q.download='0'
    uci set sqm.$q.upload='3400'
    uci set sqm.$q.qdisc='cake'
    uci set sqm.$q.script='piece_of_cake.qos'
    uci set sqm.$q.linklayer='none'
    uci set sqm.$q.qdisc_advanced='1'
    uci set sqm.$q.squash_dscp='1'
    uci set sqm.$q.squash_ingress='1'
    uci set sqm.$q.qdisc_really_really_advanced='1'
    uci set sqm.$q.eqdisc_opts='nat dual-srchost ack-filter'
done
if [ -n "$(uci changes sqm)" ] || ! tc qdisc show dev sta0 | grep -q "^qdisc cake"; then
    uci commit sqm
    /etc/init.d/sqm enable
    /etc/init.d/sqm restart
fi

# ------------------------------------------
# Syslog stays in RAM only (no remote target, no flash file).
# ------------------------------------------
changed=0
for o in log_ip log_port log_proto; do
    uci -q delete system.@system[0].$o && changed=1
done
if [ $changed = 1 ]; then
    uci commit system
    /etc/init.d/log restart
fi

/usr/bin/memwatch
echo "--- router state"
opkg list-installed | grep -i -E 'speedify|zerotier' || echo "speedify/zerotier: not installed"
logread -e memwatch | tail -1
# a kmwan restart above starts probing again; give kmwan-notrack up to ~90s
for n in $(seq 1 30); do
    /usr/bin/kmwan-notrack
    [ "$(grep -c ':online$' /proc/gl-kmwan/config)" = 2 ] \
        && [ "$(grep -c 'probe_enable:false' /proc/gl-kmwan/status)" = 2 ] && break
    sleep 3
done
echo "multi-wan: $(tr '\n' ' ' < /proc/gl-kmwan/config)| $(grep probe_enable /proc/gl-kmwan/status | tr '\n' ' ')"
for q in sta0 eth0; do echo "sqm $q: $(tc qdisc show dev $q | awk '/^qdisc cake/{print "cake", $7, $8}')"; done
ROUTER_EOF
