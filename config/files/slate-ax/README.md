# Slate AX (GL-AXT1800) router config

Reference copy of the travel router's UCI config. stay-go does not apply it
(the router runs OpenWrt, not a stay-go host); restore by hand with scp.

| File      | On the router          |
|-----------|------------------------|
| network   | /etc/config/network    |
| firewall  | /etc/config/firewall   |
| dhcp      | /etc/config/dhcp       |
| wan2.sh   | applies the wan2 change below (idempotent; `revert` undoes it) |

Ports: WAN eth0 = Starlink (`wan`). LAN eth1 = **wan2**, a second wired WAN
from the GL-AR300M Wi-Fi uplink box (DHCP, metric 4, wan firewall zone; the
AR300M serves 192.168.10.0/24, not 192.168.8.0/24). LAN eth2 stays in br-lan.

    ssh root@192.168.8.1 'sh -s' < wan2.sh          # apply
    ssh root@192.168.8.1 'sh -s revert' < wan2.sh   # back to a LAN port

Source of truth for the script: 007 repo, deploy/router/wan2.sh.
Wireless config and /etc/net-bond-007/client.json are not here: they hold
Wi-Fi keys and tokens.
