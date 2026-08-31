#!/usr/bin/env bash
set -euo pipefail

# ==========================================
# CONFIGURATION VARIABLES
# ==========================================
DISK="/dev/vda"
EFI_SIZE="1G"              # Size for EFI System Partition
ROOT_LV_SIZE="16G"         # Size for Btrfs root LV (rest to XFS /home)
VG_NAME="vg_cachyos"
LUKS_NAME="cryptlvm"
HOSTNAME="cachy-vm"
USERNAME="rayben"
TIMEZONE="America/New_York"
LOCALE="en_US.UTF-8"
REPO_URL="https://github.com/rayzorben/stay-go"

STATE_FILE="/tmp/cachyos_installer_state"
VARS_FILE="/tmp/cachyos_installer_vars"

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
        rm -f "$STATE_FILE" "$VARS_FILE"
        CURRENT_STEP=0
    fi
fi

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
    echo " Target Disk       : $DISK"
    echo " EFI Partition     : $EFI_SIZE"
    echo " Root LV Size      : $ROOT_LV_SIZE (Btrfs)"
    echo " Home LV Size      : Remaining Free Space (XFS)"
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

    cat <<EOF > "$VARS_FILE"
LUKS_PASS='$LUKS_PASS'
USER_PASS='$USER_PASS'
EOF
    chmod 600 "$VARS_FILE"
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
        echo -n "$LUKS_PASS" | cryptsetup open "$PART_LUKS" "$LUKS_NAME" -
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
        mount -o noatime,compress=zstd,discard=async,subvol=@ "/dev/$VG_NAME/lv_root" /mnt
        mkdir -p /mnt/{.snapshots,var/log,var/cache/pacman/pkg,home,boot}
        mount -o noatime,compress=zstd,discard=async,subvol=@snapshots "/dev/$VG_NAME/lv_root" /mnt/.snapshots
        mount -o noatime,compress=zstd,discard=async,subvol=@var_log "/dev/$VG_NAME/lv_root" /mnt/var/log
        mount -o noatime,compress=zstd,discard=async,subvol=@pkg "/dev/$VG_NAME/lv_root" /mnt/var/cache/pacman/pkg
        mount -o noatime "/dev/$VG_NAME/lv_home" /mnt/home
        ensure_esp_ready
        mount "$PART_EFI" /mnt/boot
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
    echo -n "$LUKS_PASS" | cryptsetup luksFormat --type luks2 --pbkdf argon2id "$PART_LUKS" -
    echo -n "$LUKS_PASS" | cryptsetup open "$PART_LUKS" "$LUKS_NAME" -

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
    lvcreate -y -l 100%FREE "$VG_NAME" -n lv_home

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

    pacstrap -K /mnt \
        base base-devel cachyos-keyring cachyos-mirrorlist cachyos-v3-mirrorlist \
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
        qemu-guest-agent

    echo "[*] Generating fstab..."
    genfstab -U /mnt > /mnt/etc/fstab

    set_checkpoint 4
fi

# ==========================================
# 5. CHROOT CONFIGURATION & REPO CLONE
# ==========================================
if [[ "$CURRENT_STEP" -lt 5 ]]; then
    echo "[*] STEP 5: Configuring system in chroot..."
    ensure_mounts_ready
    
    LUKS_UUID=$(blkid -s UUID -o value "$PART_LUKS")

    arch-chroot /mnt /bin/bash <<CHROOT_SCRIPT
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

# systemd-resolved is enabled below; point resolv.conf at its stub resolver.
ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf

# Initramfs configuration with Plymouth for Graphical Decryption
sed -i 's/^HOOKS=.*/HOOKS=(base udev plymouth autodetect modconf kms keyboard keymap consolefont block plymouth-encrypt lvm2 filesystems fsck)/' /etc/mkinitcpio.conf

# Pin the virtio drivers: 'autodetect' probes the *running* installer kernel, so
# a guest whose root lives on virtio can otherwise end up without them.
sed -i 's/^MODULES=.*/MODULES=(virtio virtio_blk virtio_pci virtio_net virtio_scsi)/' /etc/mkinitcpio.conf

# Plymouth Graphical Theme setup. Deliberately without -R: the initramfs is
# built once later, after /etc/default/limine exists.
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
mount -o noatime,compress=zstd,discard=async,subvol=@snapshots /dev/$VG_NAME/lv_root /.snapshots
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
KERNEL_CMDLINE[default]+="cryptdevice=UUID=$LUKS_UUID:$LUKS_NAME root=/dev/$VG_NAME/lv_root rootflags=subvol=@ rw quiet splash rd.udev.log_priority=3 vt.global_cursor_default=0 zswap.enabled=0"
BOOT_ORDER="*, *lts, *fallback, Snapshots"
MKINITCPIO_FALLBACK=linux-cachyos-lts
ENABLE_LIMINE_FALLBACK=yes
LIMINE_DEFAULT

# Build initramfs only now: /etc/default/limine must exist first so that
# limine-entry-tool can generate boot entries as part of this build.
mkinitcpio -P

limine-install

# ZRAM Setup
cat <<ZRAM_CONF > /etc/systemd/zram-generator.conf
[zram0]
zram-size = min(ram / 2, 4096)
compression-algorithm = zstd
ZRAM_CONF

# User & Sudo Setup for rayben
echo "root:$USER_PASS" | chpasswd
if ! id "$USERNAME" >/dev/null 2>&1; then
    useradd -m -G wheel,storage,power,network -s /usr/bin/fish "$USERNAME"
fi
echo "$USERNAME:$USER_PASS" | chpasswd

# Default shell: fish for both root and $USERNAME. Set via chsh (not just
# useradd -s) so a resumed run fixes an account that already exists with bash.
grep -qx '/usr/bin/fish' /etc/shells || echo '/usr/bin/fish' >> /etc/shells
chsh -s /usr/bin/fish root
chsh -s /usr/bin/fish "$USERNAME"
# sudo refuses any sudoers drop-in that is group/world-writable, so set 0440
# explicitly rather than inheriting root's umask, and validate before trusting it.
echo "%wheel ALL=(ALL:ALL) ALL" > /etc/sudoers.d/10-wheel
chmod 0440 /etc/sudoers.d/10-wheel
visudo -c -f /etc/sudoers.d/10-wheel

# AUR helper: stay-go's package-manager table prefers paru, and the host configs
# reference AUR packages, so the first stay-go run needs it present. Try the
# binary package from the CachyOS repos first; fall back to building from the
# AUR as $USERNAME (makepkg refuses to run as root).
if ! pacman -S --noconfirm --needed paru 2>/dev/null; then
    install -d -o "$USERNAME" -g "$USERNAME" /tmp/paru-build
    su - "$USERNAME" -s /bin/bash -c '
        set -euo pipefail
        git clone https://aur.archlinux.org/paru-bin.git /tmp/paru-build/paru-bin
        cd /tmp/paru-build/paru-bin
        makepkg -s --noconfirm
    '
    pacman -U --noconfirm /tmp/paru-build/paru-bin/*.pkg.tar.zst
    rm -rf /tmp/paru-build
fi

# Clone stay-go repository into /home/rayben/stay
if [[ -d "/home/$USERNAME/stay" ]]; then
    rm -rf "/home/$USERNAME/stay"
fi
git clone "$REPO_URL" "/home/$USERNAME/stay"
chown -R "$USERNAME:$USERNAME" "/home/$USERNAME/stay"

# Enable Services
systemctl enable NetworkManager.service
systemctl enable ananicy-cpp.service
systemctl enable irqbalance.service
systemctl enable systemd-resolved.service
systemctl enable qemu-guest-agent.service
systemctl enable fstrim.timer
systemctl enable snapper-timeline.timer
systemctl enable snapper-cleanup.timer
systemctl enable limine-snapper-sync.service
systemctl enable paccache.timer
systemctl enable systemd-timesyncd.service

CHROOT_SCRIPT

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