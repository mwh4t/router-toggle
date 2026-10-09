package router

import (
	"fmt"
	"strings"
)

// пробный запрос через туннель роутера
const (
	probePort = 10891
	probeFile = "/tmp/rt-probe.json"
	probeHost = "cp.cloudflare.com"
)

func probeInbound(tag string) string {
	return fmt.Sprintf(`{"tag":%q,"listen":"127.0.0.1","port":%d,"protocol":"dokodemo-door",`+
		`"settings":{"address":%q,"port":80,"network":"tcp"}}`, tag, probePort, probeHost)
}

// временный xray с исходящим роутера
func probeScript(env string, configs []string, inbounds ...string) string {
	cfg := `{"log":{"loglevel":"none"},"inbounds":[` + strings.Join(inbounds, ",") + `]}`
	var args strings.Builder
	for _, c := range configs {
		args.WriteString(" -c " + shq(c))
	}
	listen := fmt.Sprintf("0100007F:%04X 00000000:0000 0A", probePort)
	url := fmt.Sprintf("http://127.0.0.1:%d/generate_204", probePort)
	req := fmt.Sprintf(`GET /generate_204 HTTP/1.0\r\nHost: %s\r\n\r\n`, probeHost)

	return strings.Join([]string{
		`for p in $(pidof xray); do grep -q rt-probe /proc/$p/cmdline 2>/dev/null && kill $p; done`,
		"printf '%s' " + shq(cfg) + " > " + probeFile,
		env + "xray run" + args.String() + " -c " + probeFile + " >/dev/null 2>&1 &",
		"P=$!",
		"i=0; while [ $i -lt 6 ] && ! grep -q " + shq(listen) + " /proc/net/tcp; do sleep 1; i=$((i+1)); done",
		"if command -v curl >/dev/null 2>&1; then" +
			" c=$(curl -s -o /dev/null -m 8 -w '%{http_code}' -H " + shq("Host: "+probeHost) + " " + url + " 2>/dev/null);" +
			" else c=$(printf " + shq(req) + fmt.Sprintf(" | nc -w 8 127.0.0.1 %d 2>/dev/null | head -1 | cut -d' ' -f2); fi", probePort),
		"kill $P 2>/dev/null; rm -f " + probeFile,
		`case "$c" in [1-5][0-9][0-9]) echo yes ;; *) echo no ;; esac`,
	}, "\n")
}
