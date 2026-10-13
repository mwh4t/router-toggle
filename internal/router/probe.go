package router

import (
	"fmt"
	"strconv"
	"strings"
)

// пробный запрос через туннель роутера
const (
	probePort = 10891
	probeFile = "/tmp/rt-probe.json"
	probeOut  = "/tmp/rt-probe.out"
	probeHost = "cp.cloudflare.com"
)

func probeInbound(tag string) string { return probeInboundTo(tag, probeHost) }

func probeInboundTo(tag, host string) string {
	return fmt.Sprintf(`{"tag":%q,"listen":"127.0.0.1","port":%d,"protocol":"dokodemo-door",`+
		`"settings":{"address":%q,"port":80,"network":"tcp"}}`, tag, probePort, host)
}

func probeConfig(inbounds []string, in, out string) string {
	cfg := `{"log":{"loglevel":"none"},"inbounds":[` + strings.Join(inbounds, ",") + `]`
	if out != "" {
		cfg += fmt.Sprintf(`,"routing":{"rules":[{"type":"field","inboundTag":[%q],"outboundTag":%q}]}`, in, out)
	}
	return cfg + "}"
}

// временный xray с исходящим роутера
func probeScript(env string, configs []string, cfg string) string {
	return strings.Join(append(probeStart(env, configs, cfg),
		"if command -v curl >/dev/null 2>&1; then"+
			" c=$(curl -s -o /dev/null -m 8 -w '%{http_code}' -H "+shq("Host: "+probeHost)+" "+
			fmt.Sprintf("http://127.0.0.1:%d/generate_204", probePort)+" 2>/dev/null);"+
			" else printf "+shq(fmt.Sprintf(`GET /generate_204 HTTP/1.0\r\nHost: %s\r\n\r\n`, probeHost))+
			fmt.Sprintf(" | nc 127.0.0.1 %d > %s 2>/dev/null & N=$!;", probePort, probeOut)+
			" i=0; while [ $i -lt 8 ] && kill -0 $N 2>/dev/null; do sleep 1; i=$((i+1)); done;"+
			" kill $N 2>/dev/null; c=$(head -1 "+probeOut+" | cut -d' ' -f2); fi",
		"kill $P 2>/dev/null; rm -f "+probeFile+" "+probeOut,
		`case "$c" in [1-5][0-9][0-9]) echo yes ;; *) echo no ;; esac`,
	), "\n")
}

func probeStart(env string, configs []string, cfg string) []string {
	var args strings.Builder
	for _, c := range configs {
		args.WriteString(" -c " + shq(c))
	}
	listen := fmt.Sprintf("0100007F:%04X 00000000:0000 0A", probePort)
	return []string{
		`for p in $(pidof xray); do grep -q rt-probe /proc/$p/cmdline 2>/dev/null && kill $p; done`,
		"printf '%s' " + shq(cfg) + " > " + probeFile,
		env + "xray run" + args.String() + " -c " + probeFile + " >/dev/null 2>&1 &",
		"P=$!",
		"i=0; while [ $i -lt 6 ] && ! grep -q " + shq(listen) + " /proc/net/tcp; do sleep 1; i=$((i+1)); done",
	}
}

// замер скорости через ноду
const (
	speedHost  = "speedtest.tele2.net"
	speedPath  = "/10MB.zip"
	speedLimit = 15
)

func speedScript(env string, configs []string, cfg string) string {
	req := fmt.Sprintf(`GET %s HTTP/1.0\r\nHost: %s\r\n\r\n`, speedPath, speedHost)
	return strings.Join(append(probeStart(env, configs, cfg),
		"t0=$(cut -d' ' -f1 /proc/uptime)",
		"printf "+shq(req)+fmt.Sprintf(" | nc 127.0.0.1 %d > %s 2>/dev/null & N=$!", probePort, probeOut),
		fmt.Sprintf("(sleep %d; kill $N 2>/dev/null) >/dev/null 2>&1 & W=$!", speedLimit),
		"wait $N 2>/dev/null; kill $W 2>/dev/null",
		"t1=$(cut -d' ' -f1 /proc/uptime)",
		"b=$(wc -c < "+probeOut+" 2>/dev/null)",
		"kill $P 2>/dev/null; rm -f "+probeFile+" "+probeOut,
		`echo "${b:-0} $t0 $t1"`,
	), "\n")
}

// временному xray нужно около 20 мб
const MinMemForProbeKB = 48 << 10

func MemAvailableKB(r Runner) (int, error) {
	out, err := r.Run(`awk '/^MemAvailable:/ {print $2}' /proc/meminfo`)
	if err != nil {
		return 0, fmt.Errorf("память роутера: %w", err)
	}
	kb, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("память роутера: неожиданный ответ %q", strings.TrimSpace(out))
	}
	return kb, nil
}

func speedOf(r Runner, cmd string) (float64, error) {
	out, err := r.Run(cmd)
	if err != nil {
		return 0, fmt.Errorf("замер: %w (%s)", err, strings.TrimSpace(out))
	}
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) != 3 {
		return 0, fmt.Errorf("замер: неожиданный ответ %q", strings.TrimSpace(out))
	}
	b, err1 := strconv.ParseFloat(f[0], 64)
	t0, err2 := strconv.ParseFloat(f[1], 64)
	t1, err3 := strconv.ParseFloat(f[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, fmt.Errorf("замер: неожиданный ответ %q", strings.TrimSpace(out))
	}
	if b < 100<<10 || t1 <= t0 {
		return 0, nil
	}
	return b * 8 / (t1 - t0) / 1e6, nil
}
