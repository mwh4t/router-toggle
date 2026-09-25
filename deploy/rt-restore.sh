#!/bin/sh
set -eu

ARCHIVE=${1:?укажите архив резервной копии}
[ "$(id -u)" = 0 ] || { echo "запускать от root"; exit 1; }
[ -x /usr/local/bin/rt-server ] || { echo "сначала положите /usr/local/bin/rt-server"; exit 1; }

apt-get update -q
apt-get install -y -q sqlite3 python3-venv curl certbot

SRC=$(mktemp -d)
trap 'rm -rf "$SRC"' EXIT
tar -xzf "$ARCHIVE" -C "$SRC"

# сервер
install -d -m 700 /etc/router-toggle
cp -a "$SRC/etc/router-toggle/." /etc/router-toggle/
install -m 600 "$SRC/db/router-toggle.sqlite" /etc/router-toggle/router-toggle.db
chmod 600 /etc/router-toggle/config.json

# боты, окружения собираются заново
for bot in rt-bot vpn-bot; do
	[ -d "$SRC/opt/$bot" ] || continue
	install -d "/opt/$bot"
	cp -a "$SRC/opt/$bot/." "/opt/$bot/"
	chmod 600 "/opt/$bot/.env"
	python3 -m venv "/opt/$bot/venv"
	"/opt/$bot/venv/bin/pip" install -q -r "/opt/$bot/requirements.txt"
done
if [ -f "$SRC/db/vpn-bot.sqlite" ]; then
	install -m 600 "$SRC/db/vpn-bot.sqlite" /opt/vpn-bot/vpn-bot.db
fi

cp "$SRC"/etc/systemd/system/*.service /etc/systemd/system/

# ключи сервера
for key in "$SRC"/etc/ssh/ssh_host_*; do
	name=$(basename "$key")
	case "$name" in
	*.pub) install -m 644 "$key" "/etc/ssh/$name" ;;
	*) install -m 600 "$key" "/etc/ssh/$name" ;;
	esac
done

# пользователь туннелей
id tunnel >/dev/null 2>&1 || useradd -m -s /bin/sh tunnel
install -d -m 700 -o tunnel -g tunnel /home/tunnel/.ssh
install -m 600 -o tunnel -g tunnel "$SRC/home/tunnel/.ssh/authorized_keys" /home/tunnel/.ssh/authorized_keys

cp /etc/ssh/sshd_config /etc/ssh/sshd_config.before-restore
cp "$SRC/etc/ssh/sshd_config" /etc/ssh/sshd_config
if ! sshd -t; then
	cp /etc/ssh/sshd_config.before-restore /etc/ssh/sshd_config
	echo "sshd_config из копии не прошёл проверку, оставлен прежний"
	exit 1
fi
systemctl restart ssh

if [ -x /usr/local/bin/rt-backup ] && ! crontab -l 2>/dev/null | grep -q rt-backup; then
	(crontab -l 2>/dev/null; echo "30 6 * * 0 /usr/local/bin/rt-backup") | crontab -
fi

systemctl daemon-reload
systemctl enable rt-server rt-bot vpn-bot

echo
echo "готово, службы включены, но не запущены"
echo "ssh теперь на порту: $(grep -E '^\s*Port' /etc/ssh/sshd_config | awk '{print $2}')"
echo "дальше по deploy/MIGRATION.md"
