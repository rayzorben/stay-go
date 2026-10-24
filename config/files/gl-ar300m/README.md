# GL-AR300M (external antennas) — park Wi-Fi uplink box

Reference copy of its UCI config (GL firmware 4.3.28); stay-go does not apply it.
Admin: http://192.168.10.1, `ssh root@192.168.10.1` (PC key in /etc/dropbear/authorized_keys).

Its LAN port is cabled to the Slate AX eth1 (Slate network `wan2`, see ../slate-ax/).
Its LAN was moved off GL's default 192.168.8.1 to 192.168.10.1/24 so it doesn't
clash with the Slate's LAN. It joins the park Wi-Fi as a repeater (`wwan`, 2.4 GHz only).

| File     | On the AR300M        |
|----------|----------------------|
| network  | /etc/config/network  |
| firewall | /etc/config/firewall |
| dhcp     | /etc/config/dhcp     |

/etc/config/wireless is not here: it holds the Wi-Fi passwords.
