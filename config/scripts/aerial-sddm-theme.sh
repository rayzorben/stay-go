pushd /tmp

git clone https://github.com/3ximus/aerial-sddm-theme.git
mv aerial-sddm-theme /usr/share/sddm/themes
rm -rf aerial-sddm-theme

mkdir -p /etc/sddm.conf.d
tee /etc/sddm.conf.d/10-theme > /dev/null <<'EOF'
[Theme]
Current=aerial-sddm-theme
EOF

popd