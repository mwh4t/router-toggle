#!/bin/sh
set -eu

CONFIG=/etc/router-toggle/config.json
LOG=/var/log/rt-backup.log
PASS=/etc/router-toggle/backup.pass

[ -s "$PASS" ] || { echo "$(date) нет $PASS, копия не сделана" >> "$LOG"; exit 1; }

STAMP=$(date +%Y-%m-%d)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/db"
sqlite3 /etc/router-toggle/router-toggle.db ".backup '$WORK/db/router-toggle.sqlite'"

ARCHIVE="$WORK/rt-backup-$STAMP.tar.gz"
cd /
tar -czf "$ARCHIVE" \
	--exclude='venv' --exclude='.venv' --exclude='__pycache__' \
	--exclude='*.db' --exclude='*.db-*' --exclude='dlc.dat*' \
	etc/router-toggle \
	etc/systemd/system/rt-server.service \
	etc/ssh/sshd_config \
	etc/ssh/ssh_host_* \
	home/tunnel/.ssh \
	-C "$WORK" db
openssl enc -aes-256-cbc -pbkdf2 -iter 600000 -salt -pass file:"$PASS" -in "$ARCHIVE" -out "$ARCHIVE.enc"

TOKEN=$(python3 -c "import json; print(json.load(open('$CONFIG'))['telegram_token'])")
CHAT=$(python3 -c "import json; print(json.load(open('$CONFIG'))['telegram_chat_id'])")
THREAD=$(python3 -c "import json; c = json.load(open('$CONFIG')); print(c.get('backup_thread_id') or c.get('telegram_thread_id') or '')")

if ! curl -sf -o /dev/null \
	-F chat_id="$CHAT" \
	${THREAD:+-F message_thread_id="$THREAD"} \
	-F document=@"$ARCHIVE.enc" \
	-F caption="🗄 Резервная копия router-toggle · $STAMP · зашифрована" \
	"https://api.telegram.org/bot$TOKEN/sendDocument"; then
	echo "$(date) не удалось отправить копию" >> "$LOG"
	exit 1
fi
