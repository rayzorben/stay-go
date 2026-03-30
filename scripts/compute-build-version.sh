#!/bin/sh
# Writes the next daily build version to VERSION_FILE (two fields: YYYYMMDD seq)
# and prints it as YYYYMMDD.seq (seq zero-padded to 2 digits, e.g. .00 .01).
# Resets seq to 0 when the calendar date changes.
set -eu
VERSION_FILE="${1:-.build-version}"
DATE=$(date +%Y%m%d)

if [ ! -f "$VERSION_FILE" ]; then
	SEQ=0
else
	read -r LAST_DATE LAST_SEQ < "$VERSION_FILE" || true
	LAST_DATE=${LAST_DATE:-}
	LAST_SEQ=${LAST_SEQ:-0}
	if [ "$LAST_DATE" = "$DATE" ]; then
		SEQ=$((LAST_SEQ + 1))
	else
		SEQ=0
	fi
fi

echo "$DATE $SEQ" > "$VERSION_FILE"
printf '%s.%02d\n' "$DATE" "$SEQ"
