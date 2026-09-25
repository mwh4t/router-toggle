# переезд на новый vps

❗переносится только router-toggle (сервер, оба бота, туннели роутеров)❗

## что понадобится

- свежий архив `rt-backup-*.tar.gz` из админского бота
- доступ к dns домена
- пк с репозиторием

## 1. подготовка нового сервера

на пк:

```
make server
scp rt-server deploy/rt-restore.sh deploy/rt-backup.sh rt-backup-*.tar.gz root@НОВЫЙ_IP:/root/
```

на новом сервере:

```
install -m 755 /root/rt-server /usr/local/bin/rt-server
install -m 700 /root/rt-backup.sh /usr/local/bin/rt-backup
sh /root/rt-restore.sh /root/rt-backup-*.tar.gz
```

скрипт в конце напечатает порт ssh - дальше подключаться к новому серверу уже на него

## 2. настройки нового сервера

в `/etc/router-toggle/config.json` поменять `public_ip` на адрес нового сервера - иначе проверка связи с vpn будет искать соединение со старым

в `/etc/nginx/nginx.conf` добавить блок для api - имя в карту `stream`, `upstream rtapi` на `127.0.0.1:8444` и `server` на `127.0.0.1:8444` с `proxy_pass` на `127.0.0.1:8080`

## 3. переключение

1. на старом сервере: `rt-backup`, если с момента последней копии что-то менялось, и `systemctl stop rt-server rt-bot vpn-bot`. Два экземпляра ботов с одним токеном одновременно не работают
2. в dns перевести старые домены на новые
3. дождаться, пока записи обновятся: `dig +short example.com` должен показывать новый адрес
4. на новом сервере выпустить сертификат и запустить всё:

```
certbot certonly --standalone -d rt.example.com
nginx -t && systemctl reload nginx
systemctl start rt-server rt-bot vpn-bot
```

## 4. проверка

туннели роутеров переподключаются сами, обычно за пару минут:

```
ss -tlnp | grep 127.0.0.1:220
```

должны быть все порты от 22001, потом в админском боте - «проверка» на паре роутеров и `/log`

если какой-то openwrt так и не появился - значит, на новый сервер не попали ключи ssh-сервера, и роутер отказывается подключаться к чужому vps: `ls /etc/ssh/ssh_host_*` должен совпадать с содержимым архива
