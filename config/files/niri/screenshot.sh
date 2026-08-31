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
