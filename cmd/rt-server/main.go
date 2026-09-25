package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"router-toggle/internal/api"
	"router-toggle/internal/auth"
	"router-toggle/internal/geosite"
	"router-toggle/internal/notify"
	"router-toggle/internal/router"
	"router-toggle/internal/sshconn"
	"router-toggle/internal/store"
)

type server struct {
	cfg     *Config
	key     []byte
	st      *store.Store
	locks   *routerLocks
	limiter *auth.Limiter
	tg      *notify.Telegram
	reboots *cooldown
	geo     *geosite.Index
}

type actor struct {
	admin    bool
	routerID int
}

func main() {
	configPath := flag.String("config", "/etc/router-toggle/config.json", "путь к конфигурации")
	printCodes := flag.Bool("codes", false, "напечатать коды всех роутеров и выйти")
	showLog := flag.Int("log", 0, "напечатать последние N записей журнала и выйти")
	importFrom := flag.String("import", "", "импортировать роутеры из старого json и выйти")
	flag.Parse()

	cfg, key, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("конфигурация: %v", err)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("база: %v", err)
	}
	defer st.Close()

	if *importFrom != "" {
		if err := importRouters(st, *importFrom); err != nil {
			log.Fatalf("импорт: %v", err)
		}
		return
	}

	if *printCodes {
		if err := printRouterCodes(st, key); err != nil {
			log.Fatalf("коды: %v", err)
		}
		return
	}

	if *showLog > 0 {
		if err := printLog(st, *showLog); err != nil {
			log.Fatalf("журнал: %v", err)
		}
		return
	}

	s := &server{
		cfg:     cfg,
		key:     key,
		st:      st,
		locks:   newRouterLocks(),
		limiter: auth.NewLimiter(),
		tg:      notify.New(cfg.TelegramToken, cfg.TelegramChatID),
		reboots: newCooldown(10 * time.Minute),
		geo:     geosite.NewIndex(),
	}
	s.loadGeosite()
	go s.refreshLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": api.Version})
	})
	mux.HandleFunc("/v1/routers", s.handleRouters)
	mux.HandleFunc("/v1/status", s.handleStatus)
	mux.HandleFunc("/v1/apply", s.handleApply)
	mux.HandleFunc("/v1/log", s.handleLog)
	mux.HandleFunc("/v1/reboot", s.handleReboot)
	mux.HandleFunc("/v1/check", s.handleCheck)
	mux.HandleFunc("/v1/routers/add", s.handleAddRouter)
	mux.HandleFunc("/v1/routers/rename", s.handleRename)
	mux.HandleFunc("/v1/domains", s.handleDomains)
	mux.HandleFunc("/v1/domains/search", s.handleDomainSearch)
	mux.HandleFunc("/v1/domains/add", s.handleDomainAdd)
	mux.HandleFunc("/v1/domains/remove", s.handleDomainRemove)

	routers, err := st.Routers()
	if err != nil {
		log.Fatalf("база: %v", err)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      3 * time.Minute,
	}
	log.Printf("rt-server слушает %s, роутеров в базе: %d", cfg.Listen, len(routers))
	log.Fatal(srv.ListenAndServe())
}

func printRouterCodes(st *store.Store, key []byte) error {
	routers, err := st.Routers()
	if err != nil {
		return err
	}
	for _, r := range routers {
		fmt.Printf("%-24s %-9s порт %d   %s\n",
			r.Name, r.Firmware, r.TunnelPort, auth.Format(auth.Code(key, r.TunnelPort)))
	}
	return nil
}

func printLog(st *store.Store, n int) error {
	entries, err := st.RecentLog(n)
	if err != nil {
		return err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		fmt.Printf("%s  id=%-2d %-6s %-10s %-9v %-5s %s\n",
			e.TS, e.RouterID, e.Actor, e.Op, e.Value, e.Result, e.Detail)
	}
	return nil
}

func (s *server) handleRouters(w http.ResponseWriter, r *http.Request) {
	var req api.StatusRequest
	if !s.decode(w, r, &req) {
		return
	}
	a, ok := s.resolve(w, r, req.Code)
	if !ok {
		return
	}
	if !a.admin {
		writeError(w, http.StatusForbidden, api.ErrBadCode, nil)
		return
	}

	routers, err := s.st.Routers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, api.ErrInternal, err)
		return
	}
	out := api.RoutersResponse{}
	for _, rc := range routers {
		out.Routers = append(out.Routers, routerInfo(rc, actor{admin: true}))
	}
	writeJSON(w, http.StatusOK, out)
}

// журнал только для админа
func (s *server) handleLog(w http.ResponseWriter, r *http.Request) {
	var req api.LogRequest
	if !s.decode(w, r, &req) {
		return
	}
	a, ok := s.resolve(w, r, req.Code)
	if !ok {
		return
	}
	if !a.admin {
		writeError(w, http.StatusForbidden, api.ErrBadCode, nil)
		return
	}
	if req.Limit <= 0 || req.Limit > 100 {
		req.Limit = 20
	}

	entries, err := s.st.RecentLog(req.Limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, api.ErrInternal, err)
		return
	}
	out := api.LogResponse{}
	for _, e := range entries {
		out.Entries = append(out.Entries, api.LogEntry{
			TS: e.TS, RouterID: e.RouterID, Actor: e.Actor,
			Op: e.Op, Value: e.Value, Result: e.Result, Detail: e.Detail,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleAddRouter(w http.ResponseWriter, r *http.Request) {
	var req api.AddRouterRequest
	if !s.decode(w, r, &req) {
		return
	}
	a, ok := s.resolve(w, r, req.Code)
	if !ok {
		return
	}
	if !a.admin {
		writeError(w, http.StatusForbidden, api.ErrBadCode, nil)
		return
	}

	ctrl, err := router.For(req.Firmware)
	if err != nil {
		writeError(w, http.StatusBadRequest, api.ErrUnknownFormat, err)
		return
	}
	if req.TunnelPort <= 0 || req.Name == "" || req.SSHUser == "" || req.AuthSecret == "" {
		writeError(w, http.StatusBadRequest, api.ErrUnknownFormat, fmt.Errorf("не заполнены поля"))
		return
	}
	if req.AuthType == "" {
		req.AuthType = "password"
	}

	existing, err := s.st.Routers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, api.ErrInternal, err)
		return
	}
	for _, rc := range existing {
		if rc.TunnelPort == req.TunnelPort {
			writeError(w, http.StatusConflict, api.ErrRouterExists, nil)
			return
		}
	}

	client, err := sshconn.Dial(sshconn.Target{
		Addr:                "127.0.0.1:" + strconv.Itoa(req.TunnelPort),
		User:                req.SSHUser,
		AuthType:            req.AuthType,
		Secret:              req.AuthSecret,
		AllowUnknownHostKey: true,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, api.ErrRouterRefused, err)
		return
	}
	defer client.Close()

	if _, err := ctrl.ReadState(client, router.OpUDPProxy); err != nil {
		writeError(w, http.StatusBadGateway, errorCode(err), err)
		return
	}

	id, err := s.st.AddRouter(store.Router{
		Name: req.Name, DisplayName: strings.TrimSpace(req.DisplayName),
		Firmware: req.Firmware, TunnelPort: req.TunnelPort,
		SSHUser: req.SSHUser, AuthType: req.AuthType, AuthSecret: req.AuthSecret,
		HostKey: client.HostKey(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, api.ErrInternal, err)
		return
	}
	s.st.Log(id, "admin", "add_router", true, "ok", req.Name)

	writeJSON(w, http.StatusOK, api.AddRouterResponse{
		Router:     api.RouterInfo{ID: id, Name: req.Name, Firmware: req.Firmware, DisplayName: req.DisplayName},
		AccessCode: auth.Format(auth.Code(s.key, req.TunnelPort)),
	})
}

func (s *server) handleRename(w http.ResponseWriter, r *http.Request) {
	var req api.RenameRequest
	if !s.decode(w, r, &req) {
		return
	}
	a, ok := s.resolve(w, r, req.Code)
	if !ok {
		return
	}
	if !a.admin {
		writeError(w, http.StatusForbidden, api.ErrBadCode, nil)
		return
	}
	name := strings.TrimSpace(req.DisplayName)
	if len([]rune(name)) > 40 {
		writeError(w, http.StatusBadRequest, api.ErrUnknownFormat, nil)
		return
	}
	if err := s.st.SetDisplayName(req.RouterID, name); err != nil {
		writeError(w, http.StatusNotFound, api.ErrBadCode, err)
		return
	}
	rc, err := s.st.Router(req.RouterID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, api.ErrInternal, err)
		return
	}
	writeJSON(w, http.StatusOK, routerInfo(rc, a))
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	var req api.StatusRequest
	if !s.decode(w, r, &req) {
		return
	}
	a, ok := s.resolve(w, r, req.Code)
	if !ok {
		return
	}
	rc, ok := s.pick(w, a, req.RouterID)
	if !ok {
		return
	}

	ops, err := s.readStates(rc)
	if err != nil {
		writeError(w, http.StatusBadGateway, errorCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, api.StateResponse{
		Router: routerInfo(rc, a),
		Ops:    ops,
	})
}

func (s *server) handleApply(w http.ResponseWriter, r *http.Request) {
	var req api.ApplyRequest
	if !s.decode(w, r, &req) {
		return
	}
	a, ok := s.resolve(w, r, req.Code)
	if !ok {
		return
	}
	if req.Op != api.OpUDPProxy && req.Op != api.OpVPN {
		// закрытый список
		writeError(w, http.StatusBadRequest, api.ErrUnknownFormat, nil)
		return
	}
	rc, ok := s.pick(w, a, req.RouterID)
	if !ok {
		return
	}

	if !s.locks.acquire(rc.ID) {
		writeError(w, http.StatusConflict, api.ErrRouterBusy, nil)
		return
	}
	defer s.locks.release(rc.ID)

	who := "client"
	if a.admin {
		who = "admin"
	}

	client, ctrl, err := s.connect(rc)
	if err != nil {
		s.report(rc, who, req, err)
		writeError(w, http.StatusBadGateway, errorCode(err), err)
		return
	}
	defer client.Close()

	var state bool
	if req.Op == api.OpVPN {
		state, err = router.ApplyVPN(client, ctrl, req.Value)
	} else {
		state, err = router.Apply(client, ctrl, router.Op(req.Op), req.Value)
	}
	if err != nil {
		log.Printf("apply router=%d op=%s value=%v: %v", rc.ID, req.Op, req.Value, err)
		s.report(rc, who, req, err)
		writeError(w, http.StatusBadGateway, errorCode(err), err)
		return
	}
	_ = s.st.CachePut(rc.ID, req.Op, state)
	s.st.Log(rc.ID, who, req.Op, req.Value, "ok", "")

	writeJSON(w, http.StatusOK, stateResponse(routerInfo(rc, a), req.Op, state, time.Now().UTC(), false))
}

// не чаще раза в 10 минут на роутер
func (s *server) handleReboot(w http.ResponseWriter, r *http.Request) {
	var req api.ActionRequest
	if !s.decode(w, r, &req) {
		return
	}
	a, ok := s.resolve(w, r, req.Code)
	if !ok {
		return
	}
	rc, ok := s.pick(w, a, req.RouterID)
	if !ok {
		return
	}
	if !s.reboots.allow(rc.ID) {
		writeError(w, http.StatusTooManyRequests, api.ErrRebootCooldown, nil)
		return
	}

	if !s.locks.acquire(rc.ID) {
		writeError(w, http.StatusConflict, api.ErrRouterBusy, nil)
		return
	}
	defer s.locks.release(rc.ID)

	who := actorName(a)
	client, ctrl, err := s.connect(rc)
	if err != nil {
		s.st.Log(rc.ID, who, "reboot", true, errorCode(err), err.Error())
		writeError(w, http.StatusBadGateway, errorCode(err), err)
		return
	}
	defer client.Close()

	if err := ctrl.Reboot(client); err != nil {
		s.st.Log(rc.ID, who, "reboot", true, api.ErrInternal, err.Error())
		writeError(w, http.StatusBadGateway, api.ErrInternal, err)
		return
	}
	s.reboots.mark(rc.ID)
	s.st.Log(rc.ID, who, "reboot", true, "ok", "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "rebooting"})
}

func (s *server) handleCheck(w http.ResponseWriter, r *http.Request) {
	var req api.ActionRequest
	if !s.decode(w, r, &req) {
		return
	}
	a, ok := s.resolve(w, r, req.Code)
	if !ok {
		return
	}
	rc, ok := s.pick(w, a, req.RouterID)
	if !ok {
		return
	}

	info := routerInfo(rc, a)
	resp := api.HealthResponse{Router: info}

	client, ctrl, err := s.connect(rc)
	if err != nil {
		resp.Checks = []api.Check{{Name: "Роутер на связи", State: "fail",
			Hint: "роутер выключен или без интернета"}}
		s.notifyCheck(rc, actorName(a), resp.Checks, err)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	defer client.Close()

	h, err := router.CheckHealth(client, ctrl, s.cfg.PublicIP)
	if err != nil {
		writeError(w, http.StatusBadGateway, api.ErrInternal, err)
		return
	}
	resp.Checks = healthChecks(h)
	s.notifyCheck(rc, actorName(a), resp.Checks, nil)
	writeJSON(w, http.StatusOK, resp)
}

func healthChecks(h router.Health) []api.Check {
	checks := []api.Check{{Name: "Роутер на связи", State: "ok"}}

	internet := api.Check{Name: "Интернет", State: "ok"}
	if !h.Internet {
		internet.State, internet.Hint = "fail", "проблема у провайдера"
	}
	checks = append(checks, internet)

	proxy := api.Check{Name: "Служба прокси", State: "ok"}
	switch {
	case !h.VPNOn:
		proxy.State, proxy.Hint = "off", "vpn выключен до перезагрузки роутера"
	case !h.Proxy:
		proxy.State, proxy.Hint = "fail", "попробуйте перезагрузить роутер"
	}
	checks = append(checks, proxy)

	vps := api.Check{Name: "Связь с VPN", State: "ok"}
	switch {
	case !h.VPSKnown:
		vps.State = "skip"
	case !h.VPNOn:
		vps.State = "off"
	case !h.VPS:
		vps.State, vps.Hint = "fail", "попробуйте перезагрузить роутер"
	}
	checks = append(checks, vps)
	return checks
}

func (s *server) notifyCheck(rc store.Router, who string, checks []api.Check, err error) {
	var failed []string
	for _, c := range checks {
		if c.State == "fail" {
			failed = append(failed, c.Name)
		}
	}
	if len(failed) == 0 {
		return
	}
	text := fmt.Sprintf("🩺 Проверка · %s\nКто: %s\n\n%s",
		rc.Name, who, strings.Join(failed, "\n"))
	if err != nil {
		text += fmt.Sprintf("\n\n%v", err)
	}
	s.tg.Send(fmt.Sprintf("%d:check:%s", rc.ID, strings.Join(failed, ",")), text)
}

func actorName(a actor) string {
	if a.admin {
		return "admin"
	}
	return "client"
}

// журнал и уведомление об одной неудаче
func (s *server) report(rc store.Router, who string, req api.ApplyRequest, err error) {
	code := errorCode(err)
	s.st.Log(rc.ID, who, req.Op, req.Value, code, err.Error())

	action := "включить"
	if !req.Value {
		action = "выключить"
	}
	mark := "🟠"
	if code == api.ErrRollbackFailed || code == api.ErrServiceFailed {
		mark = "🔴"
	}
	text := fmt.Sprintf("%s %s · %s\n%s %s · %s\n\n%v",
		mark, code, rc.Name, action, req.Op, who, err)

	key := fmt.Sprintf("%d:%s", rc.ID, code)
	if code == api.ErrRollbackFailed {
		text = "🔴 ТРЕБУЕТСЯ ВМЕШАТЕЛЬСТВО\n\n" + text
		key = ""
	}
	s.tg.Send(key, text)
}

func (s *server) resolve(w http.ResponseWriter, r *http.Request, code string) (actor, bool) {
	ip, known := clientIP(r)
	if known && s.limiter.Blocked(ip) {
		writeError(w, http.StatusTooManyRequests, api.ErrTooManyAttempts, nil)
		return actor{}, false
	}

	got := auth.Normalize(code)
	if got == "" {
		s.failed(ip, known)
		writeError(w, http.StatusUnauthorized, api.ErrBadCode, nil)
		return actor{}, false
	}

	if auth.Equal(got, auth.Normalize(s.cfg.AdminCode)) {
		s.limiter.Reset(ip)
		return actor{admin: true}, true
	}

	routers, err := s.st.Routers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, api.ErrInternal, err)
		return actor{}, false
	}
	for _, rc := range routers {
		if auth.Equal(got, auth.Code(s.key, rc.TunnelPort)) {
			s.limiter.Reset(ip)
			return actor{routerID: rc.ID}, true
		}
	}

	s.failed(ip, known)
	writeError(w, http.StatusUnauthorized, api.ErrBadCode, nil)
	return actor{}, false
}

// вместо блокировки задержка
func (s *server) failed(ip string, known bool) {
	if known {
		s.limiter.Fail(ip)
		return
	}
	time.Sleep(time.Second)
}

// клиенту свой роутер, админу запрошенный
func (s *server) pick(w http.ResponseWriter, a actor, requested int) (store.Router, bool) {
	id := a.routerID
	if a.admin {
		id = requested
	}
	rc, err := s.st.Router(id)
	if err != nil {
		writeError(w, http.StatusNotFound, api.ErrBadCode, err)
		return store.Router{}, false
	}
	return rc, true
}

var statusOps = []string{api.OpUDPProxy, api.OpVPN}

func (s *server) readStates(rc store.Router) ([]api.OpState, error) {
	client, ctrl, err := s.connect(rc)
	if err == nil {
		defer client.Close()
		now := time.Now().UTC()
		out := make([]api.OpState, 0, len(statusOps))
		for _, op := range statusOps {
			var v bool
			v, err = readOp(client, ctrl, op)
			if err != nil {
				break
			}
			_ = s.st.CachePut(rc.ID, op, v)
			out = append(out, api.OpState{Op: op, Value: v, ReadAt: now})
		}
		if err == nil {
			return out, nil
		}
	}

	if !errors.Is(err, sshconn.ErrOffline) {
		return nil, err
	}
	out := make([]api.OpState, 0, len(statusOps))
	for _, op := range statusOps {
		v, at, ok := s.st.CacheGet(rc.ID, op)
		if !ok {
			return nil, err
		}
		out = append(out, api.OpState{Op: op, Value: v, ReadAt: at, Stale: true})
	}
	return out, nil
}

func readOp(r router.Runner, c router.Controller, op string) (bool, error) {
	if op == api.OpVPN {
		return c.VPNState(r)
	}
	return c.ReadState(r, router.Op(op))
}

func (s *server) connect(rc store.Router) (*sshconn.Client, router.Controller, error) {
	ctrl, err := router.For(rc.Firmware)
	if err != nil {
		return nil, nil, err
	}
	client, err := sshconn.Dial(sshconn.Target{
		Addr:     "127.0.0.1:" + strconv.Itoa(rc.TunnelPort),
		User:     rc.SSHUser,
		AuthType: rc.AuthType,
		Secret:   rc.AuthSecret,
		HostKey:  rc.HostKey,
	})
	if err != nil {
		return nil, nil, err
	}
	return client, ctrl, nil
}

func stateResponse(info api.RouterInfo, op string, value bool, readAt time.Time, stale bool) api.StateResponse {
	return api.StateResponse{
		Router: info,
		Ops:    []api.OpState{{Op: op, Value: value, ReadAt: readAt, Stale: stale}},
	}
}

// клиенту не видно внутреннее имя
func routerInfo(rc store.Router, a actor) api.RouterInfo {
	if a.admin {
		return api.RouterInfo{ID: rc.ID, Name: rc.Name, Firmware: rc.Firmware, DisplayName: rc.DisplayName}
	}
	name := rc.DisplayName
	if name == "" {
		name = "Роутер"
	}
	return api.RouterInfo{ID: rc.ID, Name: name, Firmware: rc.Firmware}
}

// подробности остаются в логе сервера
func errorCode(err error) string {
	switch {
	case errors.Is(err, sshconn.ErrOffline):
		return api.ErrRouterOffline
	case errors.Is(err, sshconn.ErrAuth):
		return api.ErrInternal
	case errors.Is(err, router.ErrUnknownFormat), errors.Is(err, router.ErrInconsistent):
		return api.ErrUnknownFormat
	case errors.Is(err, router.ErrRollbackFailed):
		return api.ErrRollbackFailed
	case errors.Is(err, router.ErrRolledBack):
		return api.ErrApplyRolledBack
	case errors.Is(err, router.ErrServiceFailed):
		return api.ErrServiceFailed
	default:
		return api.ErrInternal
	}
}

func (s *server) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return false
	}
	if v := r.Header.Get(api.VersionHeader); v != "" && v != api.Version {
		writeError(w, http.StatusUpgradeRequired, api.ErrClientOutdated, nil)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, api.ErrInternal, err)
		return false
	}
	return true
}

func clientIP(r *http.Request) (string, bool) {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if !isLoopback(ip) {
		return ip, true
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		last := strings.TrimSpace(parts[len(parts)-1])
		if last != "" && !isLoopback(last) {
			return last, true
		}
	}
	return ip, false
}

func isLoopback(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.IsLoopback()
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code string, err error) {
	if err != nil {
		log.Printf("%s: %v", code, err)
	}
	writeJSON(w, status, api.NewError(code))
}

// разовый перенос роутеров из конфига в базу
func importRouters(st *store.Store, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var old struct {
		Routers []struct {
			Name       string `json:"name"`
			Firmware   string `json:"firmware"`
			TunnelPort int    `json:"tunnel_port"`
			SSHUser    string `json:"ssh_user"`
			AuthType   string `json:"auth_type"`
			AuthSecret string `json:"auth_secret"`
			HostKey    string `json:"host_key"`
		} `json:"routers"`
	}
	if err := json.Unmarshal(data, &old); err != nil {
		return err
	}

	existing, err := st.Routers()
	if err != nil {
		return err
	}
	known := map[int]bool{}
	for _, r := range existing {
		known[r.TunnelPort] = true
	}

	for _, r := range old.Routers {
		if known[r.TunnelPort] {
			fmt.Printf("пропуск %s: порт %d уже в базе\n", r.Name, r.TunnelPort)
			continue
		}
		id, err := st.AddRouter(store.Router{
			Name: r.Name, Firmware: r.Firmware, TunnelPort: r.TunnelPort,
			SSHUser: r.SSHUser, AuthType: r.AuthType, AuthSecret: r.AuthSecret, HostKey: r.HostKey,
		})
		if err != nil {
			return fmt.Errorf("%s: %w", r.Name, err)
		}
		fmt.Printf("добавлен id=%d %s (порт %d)\n", id, r.Name, r.TunnelPort)
	}
	return nil
}
