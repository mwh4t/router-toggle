package router

import (
	"errors"
	"strings"
	"testing"
)

// заглушка роутера
type fakeRunner struct {
	files    map[string]string
	fail     map[string]error
	failOnce map[string]error
	calls    []string
}

func newFakeRunner(files map[string]string) *fakeRunner {
	cp := make(map[string]string, len(files))
	for k, v := range files {
		cp[k] = v
	}
	return &fakeRunner{files: cp, fail: map[string]error{}, failOnce: map[string]error{}}
}

func (f *fakeRunner) Run(cmd string) (string, error) {
	f.calls = append(f.calls, cmd)
	for sub, err := range f.failOnce {
		if strings.Contains(cmd, sub) {
			delete(f.failOnce, sub)
			return "boom", err
		}
	}
	for sub, err := range f.fail {
		if strings.Contains(cmd, sub) {
			return "boom", err
		}
	}

	unq := func(s string) string { return strings.Trim(strings.TrimSpace(s), "'") }

	switch {
	case strings.HasPrefix(cmd, "cat > "):
		head, body, ok := strings.Cut(cmd, "\n")
		if !ok {
			return "", errors.New("bad heredoc")
		}
		path := unq(strings.TrimSpace(strings.TrimPrefix(strings.Split(head, "<<")[0], "cat > ")))
		content := strings.TrimSuffix(body, heredocToken+"\n")
		f.files[path] = content
		return "", nil

	case strings.HasPrefix(cmd, "cat "):
		path := unq(strings.TrimPrefix(cmd, "cat "))
		content, ok := f.files[path]
		if !ok {
			return "", errors.New("no such file")
		}
		return content, nil

	case strings.HasPrefix(cmd, "rm -f "):
		delete(f.files, unq(strings.TrimPrefix(cmd, "rm -f ")))
		return "", nil

	case strings.HasPrefix(cmd, "cp -p "):
		for _, part := range strings.Split(cmd, "&&") {
			part = strings.TrimSpace(part)
			var args string
			switch {
			case strings.HasPrefix(part, "cp -p "):
				args = strings.TrimPrefix(part, "cp -p ")
			case strings.HasPrefix(part, "mv "):
				args = strings.TrimPrefix(part, "mv ")
			default:
				return "", errors.New("unsupported: " + part)
			}
			src, dst, _ := strings.Cut(args, "' '")
			s, d := unq(src), unq(dst)
			content, ok := f.files[s]
			if !ok {
				return "", errors.New("no such file: " + s)
			}
			f.files[d] = content
			if strings.HasPrefix(part, "mv ") {
				delete(f.files, s)
			}
		}
		return "", nil

	case strings.Contains(cmd, "xkeen -restart"),
		strings.Contains(cmd, "nft flush table xray"),
		strings.HasPrefix(cmd, "sh -n "):
		return "", nil
	}
	return "", errors.New("unexpected command: " + cmd)
}

const sampleRouting = `{
  "routing": {
    "rules": [
      { "outboundTag": "vless-reality", "network": "tcp", "port": "443,80" },
      { "outboundTag": "vless-reality", "network": "udp", "port": "443,80" }
    ]
  }
}`

const sampleRCLocal = `#!/bin/sh
nft add table ip xray
nft 'add rule ip xray prerouting ip saddr 192.168.1.0/24 udp dport { 443, 80 } tproxy to :1083 meta mark set 1'
exit 0
`

func keeneticFiles(enabled bool) map[string]string {
	lst := "80:80\n443:443\n"
	routing := sampleRouting
	if enabled {
		lst += keeneticUDPRangeColon + "\n"
		routing = strings.Replace(routing,
			`"network": "udp", "port": "443,80"`,
			`"network": "udp", "port": "443,80,`+keeneticUDPRangeDash+`"`, 1)
	}
	return map[string]string{
		keeneticPortListPath: lst,
		keeneticRoutingPath:  routing,
	}
}

func TestKeeneticReadState(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		r := newFakeRunner(keeneticFiles(enabled))
		got, err := Keenetic{}.ReadState(r, OpUDPProxy)
		if err != nil {
			t.Fatalf("enabled=%v: %v", enabled, err)
		}
		if got != enabled {
			t.Fatalf("enabled=%v: получил %v", enabled, got)
		}
	}
}

func TestKeeneticInconsistent(t *testing.T) {
	files := keeneticFiles(false)
	files[keeneticPortListPath] += keeneticUDPRangeColon + "\n" // есть только в одном файле
	if _, err := (Keenetic{}).ReadState(newFakeRunner(files), OpUDPProxy); !errors.Is(err, ErrInconsistent) {
		t.Fatalf("ожидал ErrInconsistent, получил %v", err)
	}
}

func TestKeeneticApplyEnableThenDisable(t *testing.T) {
	r := newFakeRunner(keeneticFiles(false))
	c := Keenetic{}

	state, err := Apply(r, c, OpUDPProxy, true)
	if err != nil || !state {
		t.Fatalf("включение: state=%v err=%v", state, err)
	}
	if !lineListHas(r.files[keeneticPortListPath], keeneticUDPRangeColon) {
		t.Fatal("диапазон не попал в port_proxying.lst")
	}
	if !strings.Contains(r.files[keeneticRoutingPath], keeneticUDPRangeDash) {
		t.Fatal("диапазон не попал в 05_routing.json")
	}

	state, err = Apply(r, c, OpUDPProxy, false)
	if err != nil || state {
		t.Fatalf("выключение: state=%v err=%v", state, err)
	}
	if strings.Contains(r.files[keeneticRoutingPath], keeneticUDPRangeDash) {
		t.Fatal("диапазон остался в 05_routing.json")
	}
}

func TestApplyIdempotent(t *testing.T) {
	r := newFakeRunner(keeneticFiles(true))
	before := len(r.calls)
	if _, err := Apply(r, Keenetic{}, OpUDPProxy, true); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls[before:] {
		if strings.Contains(c, "xkeen -restart") {
			t.Fatal("служба перезапущена, хотя менять было нечего")
		}
	}
}

func TestApplyRollbackOnRestartFailure(t *testing.T) {
	r := newFakeRunner(keeneticFiles(false))
	// рестарт упал из-за нового конфига, после отката служба поднялась
	r.failOnce["xkeen -restart"] = errors.New("exit status 1")

	origLst := r.files[keeneticPortListPath]
	origRouting := r.files[keeneticRoutingPath]

	_, err := Apply(r, Keenetic{}, OpUDPProxy, true)
	if !errors.Is(err, ErrRolledBack) {
		t.Fatalf("ожидал ErrRolledBack, получил %v", err)
	}
	if r.files[keeneticPortListPath] != origLst || r.files[keeneticRoutingPath] != origRouting {
		t.Fatal("после отката файлы отличаются от исходных")
	}
}

func TestApplyRollbackFailsWhenServiceDead(t *testing.T) {
	r := newFakeRunner(keeneticFiles(false))
	// служба не поднимается вообще
	r.fail["xkeen -restart"] = errors.New("exit status 1")

	origLst := r.files[keeneticPortListPath]

	_, err := Apply(r, Keenetic{}, OpUDPProxy, true)
	if !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("ожидал ErrRollbackFailed, получил %v", err)
	}
	if r.files[keeneticPortListPath] != origLst {
		t.Fatal("файлы не восстановлены, хотя восстановление было возможно")
	}
}

func TestApplyUnknownFormat(t *testing.T) {
	files := keeneticFiles(false)
	files[keeneticRoutingPath] = `{"routing": {"rules": []}}`
	_, err := Apply(newFakeRunner(files), Keenetic{}, OpUDPProxy, true)
	if !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("ожидал ErrUnknownFormat, получил %v", err)
	}
}

func TestOpenWrtApply(t *testing.T) {
	r := newFakeRunner(map[string]string{openwrtRCLocalPath: sampleRCLocal})
	state, err := Apply(r, OpenWrt{}, OpUDPProxy, true)
	if err != nil || !state {
		t.Fatalf("state=%v err=%v", state, err)
	}
	if !strings.Contains(r.files[openwrtRCLocalPath], openwrtUDPRange) {
		t.Fatal("диапазон не попал в rc.local")
	}
	if _, err := Apply(r, OpenWrt{}, OpUDPProxy, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.files[openwrtRCLocalPath], openwrtUDPRange) {
		t.Fatal("диапазон остался в rc.local")
	}
}

func TestOpenWrtUnknownFormat(t *testing.T) {
	r := newFakeRunner(map[string]string{openwrtRCLocalPath: "#!/bin/sh\nexit 0\n"})
	if _, err := Apply(r, OpenWrt{}, OpUDPProxy, true); !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("ожидал ErrUnknownFormat, получил %v", err)
	}
}

func TestDiffShowsChanges(t *testing.T) {
	r := newFakeRunner(keeneticFiles(false))
	plan, err := Keenetic{}.Plan(r, OpUDPProxy, true)
	if err != nil {
		t.Fatal(err)
	}
	d := Diff(plan)
	if len(d) != 2 {
		t.Fatalf("ожидал изменения в двух файлах, получил %d", len(d))
	}
	found := false
	for _, fd := range d {
		for _, added := range fd.Added {
			if strings.Contains(added, keeneticUDPRangeColon) || strings.Contains(added, keeneticUDPRangeDash) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("дифф не показывает добавленный диапазон")
	}
}
