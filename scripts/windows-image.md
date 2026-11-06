# Imaging Windows before bootstrap.sh

Run this on a new laptop (rayzorbot) before `bootstrap.sh` wipes its disk. It
saves a full image of the factory Windows install, so Windows can be restored
later for a warranty claim, a BIOS or EC update, or a resale. The image goes to
cachygram first and then to orohost (`/storage/backup/archive/`).

`scripts/windows-image.sh` does the work. It runs on cachygram and drives the
laptop over ssh while the laptop is booted from the CachyOS live USB. The
NTFS partitions are copied with partclone, which copies only the blocks in
use. They are compressed with zstd on the laptop, so a factory install comes
out at about 15–30 GB instead of the disk's 2 TB.

## 1. In Windows

Open an admin terminal (Win+X, then Terminal (Admin)):

1. Save the BitLocker recovery key somewhere off the laptop. Windows 11 turns
   on Device Encryption by itself, so check even if you never enabled it:
   ```
   manage-bde -protectors -get C:
   ```
2. Turn encryption off, and wait until `manage-bde -status` shows
   `Percentage Encrypted: 0.0%`. partclone can't read an encrypted partition,
   and the script stops if it finds one.
   ```
   manage-bde -off C:
   ```
3. Turn off hibernation and Fast Startup, so the NTFS partitions are cleanly
   unmounted when you shut down:
   ```
   powercfg /h off
   ```
4. Set the fan mode, keyboard RGB and charge limit in Control Center now. They
   have no Linux driver, and the EC keeps them after the wipe.
5. Shut down. Use a full shutdown, not a restart.

## 2. On the laptop: boot the live USB

Boot the same CachyOS USB you'll use for `bootstrap.sh`, and connect to Wi-Fi.
Then, in a terminal:

```bash
passwd
```
```bash
sudo systemctl start sshd
```
```bash
ip -4 -br addr
```

`passwd` sets the live user's password, which you'll type once in the next
step. Note the address that `ip` prints (`192.168.8.x`).

## 3. On cachygram: capture

```bash
cd ~ && ~/Dropbox/Ray/Code/stay-go/scripts/windows-image.sh capture 192.168.8.x
```

The script asks for the live user's password once. It then installs partclone
on the live USB if it's missing, prints the disk layout and pulls each
partition into `~/rayzorbot-windows-<date>/`. It refuses to run if `DISK`
(default `/dev/nvme0n1`) is a USB disk, which would mean it found the live
stick. If the internal disk has another name, run it as
`DISK=/dev/... windows-image.sh capture ...`.

The folder holds:

| File | What it is |
|---|---|
| `ptable.sfdisk` | the GPT, including the disk's and the partitions' GUIDs |
| `p<N>.img.zst` | partition N: partclone for NTFS, dd for everything else |
| `partitions.tsv` | the partition numbers and types, read by restore |
| `lsblk.txt` | the layout, for people |
| `SHA256SUMS` | checksums of every file |

## 4. On cachygram: copy to orohost

```bash
~/Dropbox/Ray/Code/stay-go/scripts/windows-image.sh push ~/rayzorbot-windows-<date>
```

This copies the folder to `orohost:/storage/backup/archive/rayzorbot-windows-<date>/`
with rsync and checks every checksum there. After it prints `Verified on orohost`,
delete the copy on cachygram:

```bash
rm -r ~/rayzorbot-windows-<date>
```

Now `bootstrap.sh rayzorbot` can wipe the disk.

## Restoring Windows later

This erases the laptop's disk. Back up Linux first (`kopia-backup`).

1. Boot the laptop from the live USB and do step 2 again.
2. On any machine that has the image folder, run restore. orohost has it, and
   its copy of the repo is at `~/code/stay-go`:
   ```bash
   ~/code/stay-go/scripts/windows-image.sh restore 192.168.8.x /storage/backup/archive/rayzorbot-windows-<date>
   ```
   The script checks the checksums, shows the laptop's disk and asks you to
   type its name (`nvme0n1`) before it writes anything. It then rewrites the
   GPT and writes each partition back.
3. Remove the USB and boot. The firmware's boot entry for Linux is gone with
   the disk, so it falls back to Windows' `\EFI\Boot\bootx64.efi`. If it
   doesn't, choose "Windows Boot Manager" in the boot menu (F7 or F12 on
   Thunderobot). Turn Device Encryption back on in Windows if you want it.

## Troubleshooting

- **`can't ssh to liveuser@…`**: sshd isn't running on the live USB, or
  `passwd` wasn't set. Repeat step 2. To use another user, pass `user@address`.
- **`… is BitLocker-encrypted`**: decryption isn't finished, or never started.
  Boot Windows and finish step 1.
- **partclone refuses the NTFS volume** (dirty or hibernated): boot Windows, run
  `powercfg /h off` and `chkdsk C: /f`, then shut down fully.
- **Skipping cachygram:** run `capture` on orohost instead (from
  `/storage/backup/archive`, using `~/code/stay-go/scripts/windows-image.sh`).
  The image then lands there directly, and no `push` is needed.
