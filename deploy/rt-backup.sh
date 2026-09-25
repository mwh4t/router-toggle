#!/bin/sh
set -eu

CONFIG=/etc/router-toggle/config.json
LOG=/var/log/rt-backup.log

STAMP=$(date +%Y-%m-%d)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/db"
sqlite3 /etc/router-toggle/router-toggle.db ".backup '$WORK/db/router-toggle.sqlite'"
[ -f /opt/vpn-bot/vpn-bot.db ] && sqlite3 /opt/vpn-bot/vpn-bot.db ".backup '$WORK/db/vpn-bot.sqlite'"

ARCHIVE="$WORK/rt-backup-$STAMP.tar.gz"
cd /
tar -czf "$ARCHIVE" \
	--exclude='venv' --exclude='.venv' --exclude='__pycache__' \
	--exclude='*.db' --exclude='*.db-*' --exclude='dlc.dat*' \
	etc/router-toggle \
	opt/rt-bot \
	opt/vpn-bot \
	etc/systemd/system/rt-server.service \
	etc/systemd/system/rt-bot.service \
	etc/systemd/system/vpn-bot.service \
	etc/ssh/sshd_config \
	etc/ssh/ssh_host_* \
	home/tunnel/.ssh \
	-C "$WORK" db

TOKEN=$(python3 -c "import json; print(json.load(open('$CONFIG'))['telegram_token'])")
CHAT=$(python3 -c "import json; print(json.load(open('$CONFIG'))['telegram_chat_id'])")

if ! curl -sf -o /dev/null \
	-F chat_id="$CHAT" \
	-F document=@"$ARCHIVE" \
	-F caption="🗄 Резервная копия router-toggle · $STAMP" \
	"https://api.telegram.org/bot$TOKEN/sendDocument"; then
	echo "$(date) не удалось отправить копию" >> "$LOG"
	exit 1
fi
