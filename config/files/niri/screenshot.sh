# satty is GTK4, and GDK initialises Vulkan at startup (even with the GL
# renderer), enumerating every GPU: on a hybrid laptop that wakes the
# runtime-suspended NVIDIA card and the window appears ~3s late (measured
# Nov 10 2026: 2.2s inside Vulkan device enumeration). Keep GDK off Vulkan
# and on Mesa's EGL so the dGPU stays asleep.
export GDK_DISABLE=vulkan
export GSK_RENDERER=gl
mesa_egl=/usr/share/glvnd/egl_vendor.d/50_mesa.json
[ -f "$mesa_egl" ] && export __EGL_VENDOR_LIBRARY_FILENAMES=$mesa_egl

wayfreeze \
  --hide-cursor \
  --after-freeze-cmd '
    img=$(mktemp --suffix=.png)

    grim -g "$(slurp -o -c "#ff0000ff")" "$img" &&
    killall wayfreeze &&
    satty \
      --initial-tool rectangle \
      --actions-on-escape "save-to-clipboard,exit" \
      --copy-command wl-copy \
      --annotation-size-factor 0.8 \
      --filename "$img"

    rm -f "$img"
  '
