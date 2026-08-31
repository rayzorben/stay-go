# When making changes, bring up any pending items in this TODO as a reminder to implement.
# If the item has already been implemented, remove it from the TODO

Item 1
Make sure all executed output goes through the same filter and status system as it does when applying changes. For example
 bin/stay-go

  level                   resource                                                             type       action
  ----------------------  -------------------------------------------------------------------  ---------  ---------
  /hosts/cachygram.yaml   /home/rayben/.config/solaar/config.yaml                              file       ~ update
                          │ symlink /home/rayben/.config/solaar/config.yaml → /home/rayben/Dropbox/PC/developm…
  /users/rayben-cachygr…  motion (in-box)                                                      distrobox  ~ update
                          │ packages: -dotnet-sdk -visual-studio-code-bin
                          │ exports: -code

  /users/rayben-cachygr…  motion                                                               distrobox  = sync
  /users/rayben-cachygr…  sedanos                                                              distrobox  = sync
  /users/rayben-cachygr…  sedanos (in-box)                                                     distrobox  = sync
  /users/rayben-cachygr…  stoneridge                                                           distrobox  = sync
  /users/rayben-cachygr…  stoneridge (in-box)                                                  distrobox  = sync

  ─────────────────────────────────────────────────────────────────────────────────────────────────────────────────

  +0 added  ~2 updated  =0 moved  -0 removed  !2 skipped  ·  123 managed
  ·  2 items skipped. Pass --skipped or -S to view.

  Proceed? [Y/n] (or 's' to show skipped):
  ✓  /home/rayben/.config/solaar/config.yaml             file       0.0s
── applying in [motion] ────────────────────────────────────────────────
  ✓  visual-studio-code-bin                    package    2.4s
  ✓  dotnet-sdk                                package    0.5s
────────────────────────────────────────────────────────────────────────────
  ✓  motion (in-box)                                     distrobox  3.5s

  ─────────────────────────────────────────────────────────────────────────────────────────────────────────────────

  A working and success or fail indicator for each item is displayed instead of raw output. Not shown here is while it is running, it is showing the output on a single line. If the command errors, 1 or more lines are captured and printed so that the error can be easily diagnosed.

  However, right now --upgrade is just raw output and should go through this path as well.

  Make sure there is a general mechanism for output to go through in a status/message/error style output and make sure both code paths use this general shared code. Document this for future use in case we add on any functionality that is executing commands.