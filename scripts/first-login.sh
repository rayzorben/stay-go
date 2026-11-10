#!/usr/bin/env bash
# Finish stay-go's first run on a freshly bootstrapped machine.
#
# bootstrap.sh runs stay-go once inside its chroot, where anything that needs a
# running system fails by design: systemd --user units, `systemctl --user`
# commands, daemons (tailscale, keyd), containers. It also installs the user
# unit stay-go-first-login.service, which opens this script in a terminal at
# the first graphical login. Once stay-go exits 0 the unit removes itself;
# until then it opens again at every login.
set -uo pipefail

unit=stay-go-first-login.service
unit_dir="$HOME/.config/systemd/user"
config="$HOME/stay/config"

cat <<EOF
stay-go first login
-------------------
The install ran stay-go once in a chroot. This run finishes what needs a
running session (user services, daemons, session commands). It will ask for
the secrets password once; the login keyring keeps it after that.

EOF
read -rp "Press Enter to run stay-go (or close this window to skip until next login)... "

# The chroot run builds and installs the binary; build it here when that run
# was skipped (no secrets password given) or failed before installing it.
if ! command -v stay-go >/dev/null; then
    echo "[*] stay-go is not installed; building it..."
    if ! (cd "$HOME/stay" && make build && sudo install -m 755 bin/stay-go /usr/local/bin/stay-go); then
        read -rp "Build failed. Press Enter to close."
        exit 1
    fi
fi

if stay-go -c "$config"; then
    systemctl --user disable "$unit" 2>/dev/null
    rm -f "$unit_dir/$unit" "$unit_dir/graphical-session.target.wants/$unit"
    systemctl --user daemon-reload
    echo
    echo "Done. This window will not open again at login."
else
    echo
    echo "stay-go did not finish cleanly; log: ~/.local/share/stay-go/logs/latest.log"
    echo "This window opens again at the next login, or run: stay-go -c $config"
fi
read -rp "Press Enter to close."
