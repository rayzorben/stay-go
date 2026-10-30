#!/usr/bin/env bash
set -euo pipefail

# ==========================================
# CONFIGURATION VARIABLES
# ==========================================
# Usage: bootstrap.sh <host>     (host: cachy-vm | cachygram)
VG_NAME="vg_cachyos"
LUKS_NAME="cryptlvm"
USERNAME="rayben"
TIMEZONE="America/New_York"
LOCALE="en_US.UTF-8"
REPO_URL="https://github.com/rayzorben/stay-go"

STATE_FILE="/tmp/cachyos_installer_state"
VARS_FILE="/tmp/cachyos_installer_vars"
HOST_FILE="/tmp/cachyos_installer_host"

# Per-host profile. Everything that differs between the QEMU test VM and real
# hardware lives here; the steps below only read these variables.
#
#   ROOT_LV_SIZE   btrfs root: both kernels + headers, firmware, the pacman
#                  cache (@pkg) and every snapper snapshot share it.
#   SWAP_LV_SIZE   disk swap behind zram ("" = none). Priority 10, so zram
#                  (100) is always used first and this only takes the overflow.
#   HOME_LV_EXTENTS  XFS cannot shrink, so /home deliberately does not take the
#                  whole VG: the remainder stays unallocated for growing root
#                  (lvextend + btrfs filesystem resize) or /home later.
#   ZRAM_SIZE      zram-generator expression.
#   EXTRA_CMDLINE  appended to the kernel command line.
#   INITRAMFS_MODULES  pinned in mkinitcpio MODULES=() ("" = autodetect only).
#   EXTRA_PKGS / EXTRA_SERVICES  host-only packages and units.
load_host_profile() {
    case "$1" in
        cachy-vm)
            DISK="/dev/vda"
            HOSTNAME="cachy-vm"
            EFI_SIZE="1G"
            ROOT_LV_SIZE="16G"
            SWAP_LV_SIZE="2G"
            HOME_LV_EXTENTS="95%FREE"
            ZRAM_SIZE="min(ram / 2, 4096)"
            EXTRA_CMDLINE=""
            # 'autodetect' probes the *running* installer kernel, so a guest
            # whose root lives on virtio can otherwise end up without them.
            INITRAMFS_MODULES="virtio virtio_blk virtio_pci virtio_net virtio_scsi"
            EXTRA_PKGS="qemu-guest-agent"
            EXTRA_SERVICES="qemu-guest-agent.service"
            ;;
        cachygram)
            # LG Gram 16T90SP: 1TB SK hynix NVMe, Meteor Lake (i915), 32G RAM.
            DISK="/dev/nvme0n1"
            HOSTNAME="cachygram"
            # 4G like the original install: two kernels + fallback images, plus
            # the kernels limine-snapper-sync keeps for bootable snapshots.
            EFI_SIZE="4G"
            ROOT_LV_SIZE="128G"
            SWAP_LV_SIZE="32G"
            HOME_LV_EXTENTS="90%FREE"
            ZRAM_SIZE="ram"
            # Carried over from the original install's /etc/default/limine.
            EXTRA_CMDLINE="nowatchdog i915.enable_psr=0 i915.fastboot=0"
            INITRAMFS_MODULES=""
            EXTRA_PKGS=""
            EXTRA_SERVICES=""
            ;;
        *)
            echo "[!] Unknown host '$1'. Usage: $0 <cachy-vm|cachygram>"
            exit 1
            ;;
    esac
}

# ==========================================
# HELPER FUNCTIONS
# ==========================================
get_checkpoint() {
    if [[ -f "$STATE_FILE" ]]; then
        cat "$STATE_FILE"
    else
        echo "0"
    fi
}

set_checkpoint() {
    echo "$1" > "$STATE_FILE"
}

# ==========================================
# PRE-CHECKS & RESUME LOGIC
# ==========================================
if [[ $EUID -ne 0 ]]; then
   echo "[!] This script must be run as root (sudo)."
   exit 1
fi

CURRENT_STEP=$(get_checkpoint)

if [[ "$CURRENT_STEP" -gt 0 ]]; then
    echo "========================================================"
    echo " Detected previous interrupted run at STEP $CURRENT_STEP."
    echo "========================================================"
    read -rp "Do you want to RESUME from checkpoint $CURRENT_STEP? [Y/n]: " RESUME_CHOICE
    if [[ "$RESUME_CHOICE" =~ ^[Nn]$ ]]; then
        echo "[*] Clearing checkpoints and starting fresh..."
        rm -f "$STATE_FILE" "$VARS_FILE" "$HOST_FILE"
        CURRENT_STEP=0
    fi
fi

# The host is fixed for the life of an install: a resume must not switch disks
# or profiles halfway through.
HOST_ARG="${1:-}"
if [[ "$CURRENT_STEP" -gt 0 && -f "$HOST_FILE" ]]; then
    SAVED_HOST="$(cat "$HOST_FILE")"
    if [[ -n "$HOST_ARG" && "$HOST_ARG" != "$SAVED_HOST" ]]; then
        echo "[!] Interrupted run was for '$SAVED_HOST', not '$HOST_ARG'."
        echo "    Re-run with '$SAVED_HOST', or answer 'n' to start fresh."
        exit 1
    fi
    HOST_ARG="$SAVED_HOST"
fi
if [[ -z "$HOST_ARG" ]]; then
    echo "Usage: $0 <cachy-vm|cachygram>"
    exit 1
fi
load_host_profile "$HOST_ARG"
echo "$HOST_ARG" > "$HOST_FILE"

# Kernel command line, built once for both /etc/default/limine writes below.
# ':allow-discards' lets TRIM (discard=async, fstrim.timer) pass through dm-crypt
# to the SSD; without it every discard stops at the LUKS layer. The two
# workqueue options make dm-crypt encrypt/decrypt inline instead of bouncing
# every I/O through kcryptd worker threads -- pure added latency on NVMe.
build_kernel_cmdline() {
    local luks_uuid="$1"
    KERNEL_CMDLINE="cryptdevice=UUID=$luks_uuid:$LUKS_NAME:allow-discards,no-read-workqueue,no-write-workqueue root=/dev/$VG_NAME/lv_root rootflags=subvol=@ rw quiet splash rd.udev.log_priority=3 vt.global_cursor_default=0 zswap.enabled=0${EXTRA_CMDLINE:+ $EXTRA_CMDLINE}"
}

# Detect partition naming scheme (e.g., /dev/vda1 vs /dev/nvme0n1p1)
if [[ "$DISK" =~ [0-9]$ ]]; then
    PART_EFI="${DISK}p1"
    PART_LUKS="${DISK}p2"
else
    PART_EFI="${DISK}1"
    PART_LUKS="${DISK}2"
fi

# CPU Microcode
UCODE_PKG=""
CPU_VENDOR=$(grep -m1 'vendor_id' /proc/cpuinfo | awk '{print $3}')
if [[ "$CPU_VENDOR" == "AuthenticAMD" ]]; then
    UCODE_PKG="amd-ucode"
elif [[ "$CPU_VENDOR" == "GenuineIntel" ]]; then
    UCODE_PKG="intel-ucode"
fi

if [[ "$CURRENT_STEP" -eq 0 ]]; then
    echo "========================================================"
    echo " Host Profile      : $HOST_ARG"
    echo " Target Disk       : $DISK"
    lsblk -dno SIZE,MODEL "$DISK" 2>/dev/null | sed 's/^/                     /'
    echo " EFI Partition     : $EFI_SIZE"
    echo " Root LV Size      : $ROOT_LV_SIZE (Btrfs)"
    echo " Swap LV Size      : ${SWAP_LV_SIZE:-none} (behind zram: $ZRAM_SIZE)"
    echo " Home LV Size      : $HOME_LV_EXTENTS (XFS; rest left unallocated)"
    echo " Bootloader        : Limine (Graphical)"
    echo " Crypt UI / Splash : Plymouth Graphical Prompt"
    echo " Go Repo Clone     : $REPO_URL -> /home/$USERNAME/stay"
    echo " Username          : $USERNAME (Wheel Sudoer)"
    echo " Hostname          : $HOSTNAME"
    echo "========================================================"
    read -rp "Are you sure you want to completely WIPE $DISK and proceed? [y/N]: " CONFIRM
    if [[ ! "$CONFIRM" =~ ^[Yy]$ ]]; then
        echo "Aborting."
        exit 1
    fi

    read -rsp "Enter LUKS Disk Encryption Passphrase: " LUKS_PASS
    echo
    read -rsp "Enter System User/Root Password: " USER_PASS
    echo

    # %q, not '...': a passphrase containing ' (or $, ", `) must round-trip
    # through 'source' verbatim instead of breaking or executing on resume.
    ( umask 077; printf 'LUKS_PASS=%q\nUSER_PASS=%q\n' "$LUKS_PASS" "$USER_PASS" > "$VARS_FILE" )
else
    if [[ -f "$VARS_FILE" ]]; then
        # shellcheck disable=SC1090
        source "$VARS_FILE"
    else
        echo "[!] Variables file missing. Please re-enter credentials."
        read -rsp "Enter LUKS Disk Encryption Passphrase: " LUKS_PASS
        echo
        read -rsp "Enter System User/Root Password: " USER_PASS
        echo
    fi
fi

ensure_storage_ready() {
    if ! cryptsetup status "$LUKS_NAME" >/dev/null 2>&1; then
        echo "[*] Opening LUKS container for resume..."
        printf %s "$LUKS_PASS" | cryptsetup open "$PART_LUKS" "$LUKS_NAME" -
    fi
    vgchange -ay "$VG_NAME" >/dev/null 2>&1 || true
}

# mkfs.fat on the ESP happens in STEP 1. If that step was skipped (resuming on a
# stale checkpoint from an earlier install) or its mkfs raced udev after
# partprobe, the partition exists with no filesystem and mounting it fails with
# "wrong fs type, bad superblock". Format it if it is not already vfat.
ensure_esp_ready() {
    if ! blkid -s TYPE -o value "$PART_EFI" 2>/dev/null | grep -qx vfat; then
        echo "[*] ESP $PART_EFI has no vfat filesystem; formatting..."
        mkfs.fat -F 32 "$PART_EFI"
    fi
}

ensure_mounts_ready() {
    ensure_storage_ready
    if ! mountpoint -q /mnt; then
        echo "[*] Re-mounting filesystems for resume..."
        mount -o noatime,compress=zstd:1,discard=async,subvol=@ "/dev/$VG_NAME/lv_root" /mnt
        mkdir -p /mnt/{.snapshots,var/log,var/cache/pacman/pkg,home,boot}
        mount -o noatime,compress=zstd:1,discard=async,subvol=@snapshots "/dev/$VG_NAME/lv_root" /mnt/.snapshots
        mount -o noatime,compress=zstd:1,discard=async,subvol=@var_log "/dev/$VG_NAME/lv_root" /mnt/var/log
        mount -o noatime,compress=zstd:1,discard=async,subvol=@pkg "/dev/$VG_NAME/lv_root" /mnt/var/cache/pacman/pkg
        mount -o noatime "/dev/$VG_NAME/lv_home" /mnt/home
        ensure_esp_ready
        mount "$PART_EFI" /mnt/boot
    fi
}

# Enable the optimized cachyos repos the CPU supports (znver4 > v4 > v3),
# inserted above [cachyos] in the given pacman.conf. Inlined from the CachyOS
# installer (cachyos-calamares src/scripts/detect-architecture), with a guard
# so a resumed STEP 4 does not insert the sections a second time.
enable_cachyos_arch_repos() {
    local conf="$1"
    grep -q '^\[cachyos\]' "$conf" || return 0
    grep -qE '^\[cachyos-(znver4|v4|v3)\]' "$conf" && return 0

    local check_v3 check_v4
    check_v3="$(/lib/ld-linux-x86-64.so.2 --help 2>/dev/null | grep 'x86-64-v3 (' | awk '{print $1}' || true)"
    check_v4="$(/lib/ld-linux-x86-64.so.2 --help 2>/dev/null | grep 'x86-64-v4 (' | awk '{print $1}' || true)"

    # Zen 4/5 detection, matching the installer's is_zen45(): AVX-512 present
    # and family 0x19 model 0x10-0x1f/0x60-0xaf, or family 0x1a.
    local family model zen45=0
    family="$(grep -Po '^cpu family\s+:\s+\K[0-9]+' /proc/cpuinfo | head -n1 || true)"
    model="$(grep -Po '^model\s+:\s+\K[0-9]+' /proc/cpuinfo | head -n1 || true)"
    if grep -qw avx512f /proc/cpuinfo && [[ -n "$family" && -n "$model" ]]; then
        if (( family == 25 )) && { (( model >= 16 && model <= 31 )) || (( model >= 96 && model <= 175 )); }; then
            zen45=1
        elif (( family == 26 )); then
            zen45=1
        fi
    fi

    if (( zen45 )); then
        echo "[*] CPU supports znver4; enabling cachyos znver4 repos."
        sed -i '/^\[cachyos\]$/i \
[cachyos-znver4] \
Include = /etc/pacman.d/cachyos-v4-mirrorlist \
\
[cachyos-core-znver4] \
Include = /etc/pacman.d/cachyos-v4-mirrorlist \
\
[cachyos-extra-znver4] \
Include = /etc/pacman.d/cachyos-v4-mirrorlist\n' "$conf"
    elif [[ "$check_v4" == "x86-64-v4" ]]; then
        echo "[*] CPU supports x86-64-v4; enabling cachyos v4 repos."
        sed -i '/^\[cachyos\]$/i \
[cachyos-v4] \
Include = /etc/pacman.d/cachyos-v4-mirrorlist \
\
[cachyos-extra-v4] \
Include = /etc/pacman.d/cachyos-v4-mirrorlist \
\
[cachyos-core-v4] \
Include = /etc/pacman.d/cachyos-v4-mirrorlist\n' "$conf"
    elif [[ "$check_v3" == "x86-64-v3" ]]; then
        echo "[*] CPU supports x86-64-v3; enabling cachyos v3 repos."
        sed -i '/^\[cachyos\]$/i \
[cachyos-v3] \
Include = /etc/pacman.d/cachyos-v3-mirrorlist \
\
[cachyos-extra-v3] \
Include = /etc/pacman.d/cachyos-v3-mirrorlist \
\
[cachyos-core-v3] \
Include = /etc/pacman.d/cachyos-v3-mirrorlist\n' "$conf"
    fi
}

# ==========================================
# 1. PARTITIONING & LUKS INITIALIZATION
# ==========================================
if [[ "$CURRENT_STEP" -lt 1 ]]; then
    echo "[*] STEP 1: Wiping and partitioning $DISK..."
    
    umount -R /mnt 2>/dev/null || true
    vgchange -an "$VG_NAME" 2>/dev/null || true
    cryptsetup close "$LUKS_NAME" 2>/dev/null || true

    sgdisk --zap-all "$DISK"
    sgdisk -n 1:0:+"$EFI_SIZE" -t 1:ef00 -c 1:"EFI-SP" "$DISK"
    sgdisk -n 2:0:0            -t 2:8309 -c 2:"cryptsystem" "$DISK"
    partprobe "$DISK"
    # Wait for udev to create the partition nodes; mkfs.fat immediately after
    # partprobe can otherwise race and act on a device that is not there yet.
    udevadm settle
    for _ in $(seq 1 10); do
        [[ -b "$PART_EFI" && -b "$PART_LUKS" ]] && break
        sleep 1
    done
    if [[ ! -b "$PART_EFI" || ! -b "$PART_LUKS" ]]; then
        echo "[!] Partition nodes did not appear after partprobe: $PART_EFI / $PART_LUKS"
        exit 1
    fi

    echo "[*] Formatting EFI System Partition..."
    mkfs.fat -F 32 "$PART_EFI"

    echo "[*] Setting up LUKS container..."
    # 4K encryption sectors: one AES-XTS operation per 4K block instead of
    # eight. Fixed at format time; the NVMe reports 512, so it is not automatic.
    printf %s "$LUKS_PASS" | cryptsetup luksFormat --type luks2 --pbkdf argon2id --sector-size 4096 "$PART_LUKS" -
    printf %s "$LUKS_PASS" | cryptsetup open "$PART_LUKS" "$LUKS_NAME" -

    set_checkpoint 1
fi

# ==========================================
# 2. LVM & FILESYSTEM SETUP
# ==========================================
if [[ "$CURRENT_STEP" -lt 2 ]]; then
    echo "[*] STEP 2: Creating LVM and formatting filesystems..."
    ensure_storage_ready

    pvcreate -f "/dev/mapper/$LUKS_NAME"
    vgcreate -f "$VG_NAME" "/dev/mapper/$LUKS_NAME"
    lvcreate -y -L "$ROOT_LV_SIZE" "$VG_NAME" -n lv_root
    if [[ -n "$SWAP_LV_SIZE" ]]; then
        lvcreate -y -L "$SWAP_LV_SIZE" "$VG_NAME" -n lv_swap
        mkswap -L cachyos-swap "/dev/$VG_NAME/lv_swap"
    fi
    lvcreate -y -l "$HOME_LV_EXTENTS" "$VG_NAME" -n lv_home

    mkfs.btrfs -f -L cachyos-root "/dev/$VG_NAME/lv_root"
    mount "/dev/$VG_NAME/lv_root" /mnt
    btrfs subvolume create /mnt/@
    btrfs subvolume create /mnt/@snapshots
    btrfs subvolume create /mnt/@var_log
    btrfs subvolume create /mnt/@pkg
    umount /mnt

    mkfs.xfs -f -L cachyos-home "/dev/$VG_NAME/lv_home"

    set_checkpoint 2
fi

# ==========================================
# 3. MOUNTING DIRECTORY TREE
# ==========================================
if [[ "$CURRENT_STEP" -lt 3 ]]; then
    echo "[*] STEP 3: Mounting hierarchy..."
    ensure_mounts_ready
    set_checkpoint 3
fi

# ==========================================
# 4. BASE PACKAGES & CACHYOS TWEAKS
# ==========================================
if [[ "$CURRENT_STEP" -lt 4 ]]; then
    echo "[*] STEP 4: Bootstrapping packages (including Plymouth, inetutils, and Go)..."
    ensure_mounts_ready

    # Mirror preparation. pacstrap downloads using the LIVE ISO's mirrorlist and
    # pacman.conf, not the target's, so ranking has to happen here -- installing
    # cachyos-rate-mirrors into /mnt below is too late to help this transaction.
    #
    # Three distinct failures show up without this:
    #   404                      -> stale mirror whose db lists files it no longer has
    #   HTTP/2 PROTOCOL_ERROR    -> CDN resetting multiplexed streams under parallel
    #                               downloads
    #   "does not support byte ranges, cannot resume"
    #                            -> pacman retrying a truncated .part left by the above
    echo "[*] Ranking mirrors..."
    if command -v cachyos-rate-mirrors >/dev/null 2>&1; then
        cachyos-rate-mirrors || echo "[!] Mirror ranking failed; continuing with defaults."
    elif command -v rate-mirrors >/dev/null 2>&1; then
        rate-mirrors --allow-root --save /etc/pacman.d/cachyos-mirrorlist cachyos \
            || echo "[!] Mirror ranking failed; continuing with defaults."
    else
        echo "[!] No rate-mirrors on the ISO; using shipped mirrorlist."
    fi

    # Serialise downloads. The stream resets above are provoked by pacman opening
    # several HTTP/2 streams at once against the same CDN host; one connection at a
    # time trades throughput for a transaction that actually completes.
    sed -i 's/^[[:space:]]*ParallelDownloads.*/ParallelDownloads = 1/' /etc/pacman.conf
    grep -q '^ParallelDownloads' /etc/pacman.conf || echo 'ParallelDownloads = 1' >> /etc/pacman.conf

    # Drop partially-downloaded packages. A truncated .part from a reset connection
    # is what triggers the unresumable byte-range errors on the next attempt.
    rm -f /var/cache/pacman/pkg/*.part
    rm -f /mnt/var/cache/pacman/pkg/*.part 2>/dev/null || true

    # Refresh databases against the newly ranked mirrors so a stale db cannot ask
    # for a package version that no mirror actually carries.
    pacman -Syy --noconfirm || echo "[!] Database refresh failed; continuing."

    # Seed configuration BEFORE pacstrap. Installing the kernel and
    # limine-mkinitcpio-hook in the same transaction fires the mkinitcpio hook
    # immediately, and it reads these files as it builds. Without them the build
    # emits "/etc/vconsole.conf not found" and "Kernel command line is not
    # available", and limine-entry-tool writes boot entries with no cmdline.
    # STEP 5 rebuilds the initramfs anyway, but seeding avoids generating a
    # broken limine.conf that STEP 5 then has to correct.
    LUKS_UUID=$(blkid -s UUID -o value "$PART_LUKS")
    mkdir -p /mnt/etc /mnt/etc/default

    # Target pacman.conf, seeded BEFORE pacstrap -- exactly how the CachyOS
    # installer does it (calamares shellprocess-before-online.conf):
    #     cp /etc/pacman-more.conf ${ROOT}/etc/pacman.conf
    #     detect-architecture ${ROOT}/etc/pacman.conf
    # pacman.conf is a backup= file of the pacman package, so a file already in
    # place when pacstrap installs pacman is kept (the package's copy lands as
    # pacman.conf.pacnew). Without this seed the deployed system gets the stock
    # Arch pacman.conf with NO [cachyos] repos: the chroot's 'pacman -S paru'
    # fails with "target not found", paru never reaches the image, and the
    # installed linux-cachyos kernels have no repo to update from.
    if [[ -f /etc/pacman-more.conf ]]; then
        cp /etc/pacman-more.conf /mnt/etc/pacman.conf
    else
        cp /etc/pacman.conf /mnt/etc/pacman.conf
    fi
    enable_cachyos_arch_repos /mnt/etc/pacman.conf

    cat > /mnt/etc/vconsole.conf <<VCONSOLE
KEYMAP=us
FONT=ter-116n
VCONSOLE

    build_kernel_cmdline "$LUKS_UUID"

    cat > /mnt/etc/default/limine <<LIMINE_DEFAULT
ESP_PATH="/boot"
KERNEL_CMDLINE[default]+="$KERNEL_CMDLINE"
BOOT_ORDER="*, *lts, *fallback, Snapshots"
MKINITCPIO_FALLBACK=linux-cachyos-lts
ENABLE_LIMINE_FALLBACK=yes
LIMINE_DEFAULT

    # snap-pac's pacman hook fires at the end of the pacstrap transaction, before
    # snapper has any config and with no dbus, logging "fatal library error,
    # lookup self". Nothing to snapshot yet, so keep it out of this transaction.
    printf '[root]\nsnapshot = False\n' > /mnt/etc/snap-pac.ini

    # Retry the whole transaction. pacstrap is idempotent -- already-downloaded
    # packages are reused from the cache -- so a retry resumes rather than restarts,
    # and a mirror that reset last time is usually fine on the next pass.
    PACSTRAP_OK=0
    for attempt in 1 2 3; do
        echo "[*] pacstrap attempt $attempt of 3..."
        if pacstrap -K /mnt \
            base base-devel cachyos-keyring cachyos-mirrorlist cachyos-v3-mirrorlist \
            cachyos-v4-mirrorlist paru \
            linux-cachyos linux-cachyos-headers \
            linux-cachyos-lts linux-cachyos-lts-headers \
            linux-firmware $UCODE_PKG \
            lvm2 cryptsetup btrfs-progs xfsprogs systemd \
            limine limine-mkinitcpio-hook limine-snapper-sync plymouth plymouth-kcm \
            snapper snap-pac btrfs-assistant \
            cachyos-settings cachyos-hooks cachyos-rate-mirrors cachyos-fish-config \
            ananicy-cpp cachyos-ananicy-rules scx-scheds \
            zram-generator irqbalance \
            networkmanager sudo git nano curl which bash-completion go inetutils \
            fish terminus-font \
            man-db man-pages pacman-contrib openssh rsync wget unzip htop \
            usbutils pciutils \
            $EXTRA_PKGS
        then
            PACSTRAP_OK=1
            break
        fi
        echo "[!] pacstrap attempt $attempt failed; clearing partials and retrying..."
        rm -f /var/cache/pacman/pkg/*.part /mnt/var/cache/pacman/pkg/*.part 2>/dev/null || true
        sleep 5
    done

    if [[ "$PACSTRAP_OK" -ne 1 ]]; then
        echo "[!] pacstrap failed after 3 attempts. Check network/mirrors and re-run to resume."
        exit 1
    fi

    # Ranked cachyos mirrorlists into the target, as the installer's
    # initialize_pacman job does. pacstrap only copies /etc/pacman.d/mirrorlist;
    # the cachyos-*-mirrorlist packages ship an unranked default, so carry over
    # the lists ranked above where they exist.
    for ml in cachyos-mirrorlist cachyos-v3-mirrorlist cachyos-v4-mirrorlist; do
        if [[ -f "/etc/pacman.d/$ml" ]]; then
            cp "/etc/pacman.d/$ml" "/mnt/etc/pacman.d/$ml"
        fi
    done

    echo "[*] Generating fstab..."
    genfstab -U /mnt > /mnt/etc/fstab
    # genfstab pins btrfs mounts by subvolid= as well as subvol=. A snapper
    # rollback promotes a snapshot with a different id, so a pinned / no longer
    # matches; mount by subvol= alone (as the Arch snapper guide prescribes).
    sed -i -E 's/subvolid=[0-9]+,//g; s/,subvolid=[0-9]+//g' /mnt/etc/fstab
    # Swap is not active in the live environment, so genfstab never sees it.
    # pri=10 keeps it strictly behind zram (100): overflow only.
    if [[ -n "$SWAP_LV_SIZE" ]]; then
        printf '/dev/%s/lv_swap\tnone\tswap\tdefaults,pri=10\t0 0\n' "$VG_NAME" >> /mnt/etc/fstab
    fi

    set_checkpoint 4
fi

# ==========================================
# 5. CHROOT CONFIGURATION & REPO CLONE
# ==========================================
if [[ "$CURRENT_STEP" -lt 5 ]]; then
    echo "[*] STEP 5: Configuring system in chroot..."
    ensure_mounts_ready
    
    LUKS_UUID=$(blkid -s UUID -o value "$PART_LUKS")
    build_kernel_cmdline "$LUKS_UUID"

    # The chroot payload is written to a FILE and executed from it -- never fed
    # to bash on stdin. When it was a stdin heredoc, any command inside that
    # read stdin consumed the remainder of the script text as its input
    # (pacman's ":: Import PGP key? [Y/n]" prompt demonstrably did this), bash
    # then hit EOF and exited 0, and the install "completed" with everything
    # after the reading command -- service enables, user hardening -- silently
    # skipped. A file-backed script cannot lose commands that way, and stdin is
    # pinned to /dev/null so anything that still tries to prompt fails loudly
    # instead of misbehaving.
    cat > /mnt/root/stay-bootstrap-chroot.sh <<CHROOT_SCRIPT
set -euo pipefail

ln -sf "/usr/share/zoneinfo/$TIMEZONE" /etc/localtime
hwclock --systohc
echo "$LOCALE UTF-8" > /etc/locale.gen
locale-gen
echo "LANG=$LOCALE" > /etc/locale.conf
echo "$HOSTNAME" > /etc/hostname

# Console keymap/font for the mkinitcpio keymap+consolefont hooks (they read
# this file); without it the LUKS passphrase prompt uses the built-in default.
cat <<VCONSOLE > /etc/vconsole.conf
KEYMAP=us
FONT=ter-116n
VCONSOLE

# /etc/hosts must exist and resolve the hostname, otherwise sudo's reverse
# lookup stalls for seconds on every invocation.
cat <<HOSTS > /etc/hosts
127.0.0.1   localhost
::1         localhost
127.0.1.1   $HOSTNAME.localdomain $HOSTNAME
HOSTS

# Initramfs configuration with Plymouth for Graphical Decryption
# udev-based hook stack. NOT the systemd stack: 'encrypt' + 'lvm2' handle
# LUKS-on-LVM directly and honour the cryptdevice= cmdline set in
# /etc/default/limine. (sd-encrypt would need rd.luks.uuid= instead, and LVM
# activation via systemd generators.)
#
# There is deliberately no 'plymouth-encrypt' here: that hook was removed from
# the plymouth package and no longer exists, which fails the build outright with
# "Hook 'plymouth-encrypt' cannot be found" -- and a failed build means NO
# initramfs, so the system cannot unlock LUKS or boot at all. Plain 'encrypt'
# after 'plymouth' gives the same graphical passphrase prompt.
sed -i 's/^HOOKS=.*/HOOKS=(base udev plymouth autodetect modconf kms keyboard keymap consolefont block encrypt lvm2 filesystems fsck)/' /etc/mkinitcpio.conf

# Host-pinned modules (the VM's virtio drivers; see load_host_profile).
sed -i 's/^MODULES=.*/MODULES=($INITRAMFS_MODULES)/' /etc/mkinitcpio.conf

# Plymouth Graphical Theme setup. Deliberately without -R: the initramfs is
# rebuilt once below, after the HOOKS/MODULES edits above are all in place.
if plymouth-set-default-theme -l | grep -q "cachyos"; then
    plymouth-set-default-theme cachyos || true
elif plymouth-set-default-theme -l | grep -q "breeze"; then
    plymouth-set-default-theme breeze || true
else
    plymouth-set-default-theme spinner || true
fi

# Snapper config
umount /.snapshots 2>/dev/null || true
rmdir /.snapshots 2>/dev/null || true
if ! snapper -c root list >/dev/null 2>&1; then
    snapper --no-dbus -c root create-config /
fi
btrfs subvolume delete /.snapshots 2>/dev/null || true
mkdir -p /.snapshots
mount -o noatime,compress=zstd:1,discard=async,subvol=@snapshots /dev/$VG_NAME/lv_root /.snapshots
chmod 750 /.snapshots

# Make @ the btrfs default subvolume. snapper-rollback swaps the default
# subvolume to promote a snapshot; that only works if @ is the default to begin
# with, rather than the filesystem root (subvolid=5).
btrfs subvolume set-default "\$(btrfs subvolume list / | awk '\$NF == "@" {print \$2}')" /

sed -i 's/^TIMELINE_MIN_AGE=.*/TIMELINE_MIN_AGE="1800"/' /etc/snapper/configs/root
sed -i 's/^TIMELINE_LIMIT_HOURLY=.*/TIMELINE_LIMIT_HOURLY="5"/' /etc/snapper/configs/root
sed -i 's/^TIMELINE_LIMIT_DAILY=.*/TIMELINE_LIMIT_DAILY="7"/' /etc/snapper/configs/root
sed -i 's/^TIMELINE_LIMIT_WEEKLY=.*/TIMELINE_LIMIT_WEEKLY="0"/' /etc/snapper/configs/root
sed -i 's/^TIMELINE_LIMIT_MONTHLY=.*/TIMELINE_LIMIT_MONTHLY="0"/' /etc/snapper/configs/root
sed -i 's/^TIMELINE_LIMIT_YEARLY=.*/TIMELINE_LIMIT_YEARLY="0"/' /etc/snapper/configs/root
sed -i 's/^ALLOW_GROUPS=.*/ALLOW_GROUPS="wheel"/' /etc/snapper/configs/root

# Re-enable snap-pac. It was disabled before pacstrap so its hook would not fire
# against a system with no snapper config; snapper is configured now, so restore
# the packaged default (pacman kept our file and left its own as .pacnew).
if [[ -f /etc/snap-pac.ini.pacnew ]]; then
    mv /etc/snap-pac.ini.pacnew /etc/snap-pac.ini
elif grep -qs '^snapshot = False' /etc/snap-pac.ini; then
    # Still the seeded pre-pacstrap disable file (no .pacnew was left to
    # restore); drop it so snap-pac falls back to its packaged defaults. A
    # resumed run whose earlier pass already restored the packaged config must
    # NOT hit this branch -- an unconditional rm here used to delete that
    # restored config on every resume.
    rm -f /etc/snap-pac.ini
fi

# Limine Bootloader Setup
#
# /boot/limine.conf is NOT hand-written here. limine-mkinitcpio-hook owns its
# generation: limine-install / limine-update read /etc/default/limine and emit
# the nested entry layout (an OS entry tagged with machine-id, kernels beneath
# it as "//<kernel name>"). limine-snapper-sync requires exactly that nesting to
# graft snapshot entries in; a flat, hand-written config makes it log
# "Your OS entry has no kernel in /boot/limine.conf" on every snapshot.
#
# limine-install also deploys BOOTX64.EFI and registers the UEFI boot entry, so
# no manual cp/efibootmgr is needed.
cat <<LIMINE_DEFAULT > /etc/default/limine
ESP_PATH="/boot"
KERNEL_CMDLINE[default]+="$KERNEL_CMDLINE"
BOOT_ORDER="*, *lts, *fallback, Snapshots"
MKINITCPIO_FALLBACK=linux-cachyos-lts
ENABLE_LIMINE_FALLBACK=yes
LIMINE_DEFAULT

# Rebuild the initramfs with the HOOKS/MODULES set above. pacstrap already built
# one against the seeded config, but with the stock HOOKS line (no plymouth,
# plymouth-encrypt or lvm2) -- that image cannot open the LUKS volume.
# Verify every hook in HOOKS exists before building. A missing hook does not stop
# mkinitcpio -- it warns, skips the kernel, and exits leaving no initramfs, which
# is only discovered at first boot as a LUKS/root failure.
for hook in \$(sed -n 's/^HOOKS=(\(.*\))/\1/p' /etc/mkinitcpio.conf); do
    if [[ ! -e "/usr/lib/initcpio/install/\$hook" ]]; then
        echo "[!] mkinitcpio hook '\$hook' does not exist on this system."
        exit 1
    fi
done

mkinitcpio -P

# mkinitcpio exits 0 even when individual kernels fail ("ERROR: mkinitcpio failed
# for kernel X, skipping"), so confirm an image actually landed for each kernel.
# limine-mkinitcpio-hook installs them under /boot/<machine-id>/<pkgbase>/.
for kdir in /usr/lib/modules/*/; do
    [[ -e "\$kdir/pkgbase" ]] || continue
    pkgbase=\$(cat "\$kdir/pkgbase")
    if ! find /boot -type f -path "*/\$pkgbase/initramfs*" | grep -q .; then
        echo "[!] No initramfs was generated for \$pkgbase. Refusing to continue"
        echo "    -- the system would not be able to unlock LUKS or boot."
        exit 1
    fi
done

limine-install

# ZRAM Setup
cat <<ZRAM_CONF > /etc/systemd/zram-generator.conf
[zram0]
zram-size = $ZRAM_SIZE
compression-algorithm = zstd
swap-priority = 100
ZRAM_CONF

# User & Sudo Setup for rayben. Passwords are NOT set here: this file is written
# to the target disk, so they are piped to chpasswd from outside after it runs.
if ! id "$USERNAME" >/dev/null 2>&1; then
    useradd -m -G wheel,storage,power,network -s /usr/bin/fish "$USERNAME"
fi
# Membership enforced via usermod as well (not only useradd -G) so a resumed
# run repairs an account that already exists without these groups -- a user
# missing 'wheel' is exactly "$USERNAME is not in the sudoers file".
usermod -aG wheel,storage,power,network "$USERNAME"

# Default shell: fish for both root and $USERNAME. Set via chsh (not just
# useradd -s) so a resumed run fixes an account that already exists with bash.
grep -qx '/usr/bin/fish' /etc/shells || echo '/usr/bin/fish' >> /etc/shells
# "chsh: Shell not changed" (and a non-zero exit under set -e) is the expected
# result when the shell already matches -- $USERNAME was just created with
# 'useradd -s /usr/bin/fish'. Only call chsh when there is an actual change.
for u in root "$USERNAME"; do
    if [[ "\$(getent passwd "\$u" | cut -d: -f7)" != /usr/bin/fish ]]; then
        chsh -s /usr/bin/fish "\$u"
    fi
done
# sudo refuses any sudoers drop-in that is group/world-writable, so set 0440
# explicitly rather than inheriting root's umask, and validate before trusting it.
echo "%wheel ALL=(ALL:ALL) ALL" > /etc/sudoers.d/10-wheel
chmod 0440 /etc/sudoers.d/10-wheel
visudo -c -f /etc/sudoers.d/10-wheel

# Enable Services -- before the networked steps below (paru, git clone), so a
# mirror or GitHub hiccup cannot leave a system that boots without networking.
systemctl enable NetworkManager.service
systemctl enable ananicy-cpp.service
systemctl enable irqbalance.service
systemctl enable systemd-resolved.service
systemctl enable fstrim.timer
systemctl enable snapper-timeline.timer
systemctl enable snapper-cleanup.timer
systemctl enable limine-snapper-sync.service
systemctl enable paccache.timer
systemctl enable systemd-timesyncd.service
for unit in $EXTRA_SERVICES; do
    systemctl enable "\$unit"
done

# AUR helper: stay-go's package-manager table prefers paru, and the host configs
# reference AUR packages, so the first stay-go run needs it present.
#
# Prefer the repo package, which is built against this system's pacman. The
# fallback deliberately builds 'paru' from source rather than installing
# 'paru-bin': paru links against libalpm, whose soname tracks the pacman major
# version (pacman 6 -> libalpm.so.15, pacman 7 -> libalpm.so.16). A prebuilt
# paru-bin from the AUR carries whatever soname was current when it was
# uploaded, so on a pacman-7 CachyOS install it dies on every invocation with
#   paru: error while loading shared libraries: libalpm.so.15
# and exit status 127 -- which surfaces later as every single stay-go package
# node failing. Building from source links against the libalpm actually here.
install_paru_from_aur() {
    install -d -o "$USERNAME" -g "$USERNAME" /tmp/paru-build
    su - "$USERNAME" -s /bin/bash -c '
        set -euo pipefail
        rm -rf /tmp/paru-build/paru
        git clone https://aur.archlinux.org/paru.git /tmp/paru-build/paru
        cd /tmp/paru-build/paru
        makepkg -s --noconfirm
    '
    # makepkg can exit 0 having produced nothing useful; do not let a glob that
    # matched no files reach pacman -U as a literal argument.
    shopt -s nullglob
    local pkgs=(/tmp/paru-build/paru/*.pkg.tar.*)
    shopt -u nullglob
    if [[ \${#pkgs[@]} -eq 0 ]]; then
        echo "[!] paru build produced no package."
        return 1
    fi
    pacman -U --noconfirm "\${pkgs[@]}"
    rm -rf /tmp/paru-build
}

# Trust the packagers' keys. 'pacstrap -K' created this keyring EMPTY; the
# archlinux/cachyos keyring packages shipped their keys under
# /usr/share/pacman/keyrings, but nothing in the pacstrap transaction reliably
# lsign-trusts them in the image's keyring, and untrusted keys fail every
# cachyos database and package with 'signature from "CachyOS ..." is unknown
# trust'. The CachyOS installer avoids this by copying the live ISO's populated
# keyring into the target (initialize_pacman job); populating from the shipped
# keyrings here is equivalent without depending on host state. --init is
# idempotent and covers resumed runs whose keyring was never created.
pacman-key --init
pacman-key --populate

# paru is normally already in the image: pacstrap installed it from [cachyos]
# in STEP 4 (the same way the CachyOS installer ships it). --needed makes this
# a no-op then; it is a real install only when resuming from a checkpoint laid
# down by an older bootstrap. -Sy first: the seeded pacman.conf may name repo
# sections (v3/v4/znver4) whose databases pacstrap never synced into this root.
if pacman -Sy --noconfirm --needed paru; then
    echo "[*] paru present (cachyos repo package)."
else
    echo "[!] Repo paru unavailable; building paru from the AUR..."
    pacman -S --noconfirm --needed rust
    install_paru_from_aur
fi

# Verify paru actually runs. A paru linked against the wrong libalpm exits 127
# on every call, and the failure is otherwise invisible until the first stay-go
# run reports every package as failed.
if ! su - "$USERNAME" -s /bin/bash -c 'paru --version' >/dev/null 2>&1; then
    echo "[!] paru is installed but cannot execute (likely a libalpm soname"
    echo "    mismatch against pacman \$(pacman -Q pacman | awk '{print \$2}'))."
    echo "    Rebuilding paru from source against the installed pacman..."
    pacman -Rdd --noconfirm paru paru-bin 2>/dev/null || true
    pacman -S --noconfirm --needed rust
    install_paru_from_aur
    su - "$USERNAME" -s /bin/bash -c 'paru --version' >/dev/null
fi

# Clone stay-go repository into /home/rayben/stay
if [[ -d "/home/$USERNAME/stay" ]]; then
    rm -rf "/home/$USERNAME/stay"
fi
git clone "$REPO_URL" "/home/$USERNAME/stay"
chown -R "$USERNAME:$USERNAME" "/home/$USERNAME/stay"

CHROOT_SCRIPT

    if ! arch-chroot /mnt /bin/bash /root/stay-bootstrap-chroot.sh < /dev/null; then
        rm -f /mnt/root/stay-bootstrap-chroot.sh
        echo "[!] Chroot configuration failed. Fix the cause and re-run to resume."
        exit 1
    fi
    rm -f /mnt/root/stay-bootstrap-chroot.sh

    # Passwords go to chpasswd on stdin only -- never into a file on the target
    # disk, and never through shell quoting. printf %s passes any character.
    {
        printf 'root:%s\n' "$USER_PASS"
        printf '%s:%s\n' "$USERNAME" "$USER_PASS"
    } | arch-chroot /mnt chpasswd

    # Verify the chroot payload ran to completion by checking effects of its
    # TAIL. A truncated payload is precisely the failure that once shipped an
    # image with no networking and no sudo while still printing "complete".
    if [[ ! -e /mnt/etc/systemd/system/multi-user.target.wants/NetworkManager.service ]]; then
        echo "[!] NetworkManager was not enabled in the target -- the chroot"
        echo "    configuration did not run to completion. Refusing to continue."
        exit 1
    fi
    if ! grep '^wheel:' /mnt/etc/group | grep -qw "$USERNAME"; then
        echo "[!] $USERNAME is not in the target's wheel group. Refusing to continue."
        exit 1
    fi
    if [[ ! -f /mnt/etc/sudoers.d/10-wheel ]]; then
        echo "[!] /etc/sudoers.d/10-wheel is missing in the target. Refusing to continue."
        exit 1
    fi

    # systemd-resolved is enabled above; point resolv.conf at its stub resolver.
    # Done OUTSIDE the chroot: arch-chroot bind-mounts the host's resolv.conf over
    # the target's to give the chroot DNS, and a bind-mount target cannot be
    # replaced with a symlink ("Device or resource busy").
    ln -sf /run/systemd/resolve/stub-resolv.conf /mnt/etc/resolv.conf

    set_checkpoint 5
fi

# ==========================================
# 6. TEARDOWN
# ==========================================
echo "[*] STEP 6: Unmounting and finalizing..."
umount -R /mnt || true
vgchange -an "$VG_NAME" || true
cryptsetup close "$LUKS_NAME" || true

# Clean up checkpoint tokens
rm -f "$STATE_FILE" "$VARS_FILE"

echo "[✓] Installation complete! Graphical boot, Plymouth unlock, and /home/$USERNAME/stay configured."