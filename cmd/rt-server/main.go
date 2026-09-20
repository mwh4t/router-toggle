// localhost без tls, авторизация по коду
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
	"time"

	"router-toggle/internal/api"
	"router-toggle/internal/auth"
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
}

// кто пришёл по коду
type actor struct {
	admin    bool
	routerID int
}

func main() {
	configPath := flag.String("config", "/etc/router-toggle/config.json", "путь к конфигурации")
	printCodes := flag.Bool("codes", false, "напечатать коды всех роутеров и выйти")
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

	s := &server{cfg: cfg, key: key, st: st, locks: newRouterLocks(), limiter: auth.NewLimiter()}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": api.Version})
	})
	mux.HandleFunc("/v1/routers", s.handleRouters)
	mux.HandleFunc("/v1/status", s.handleStatus)
	mux.HandleFunc("/v1/apply", s.handleApply)

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
		out.Routers = append(out.Routers, api.RouterInfo{ID: rc.ID, Name: rc.Name, Firmware: rc.Firmware})
	}
	writeJSON(w, http.StatusOK, out)
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

	state, readAt, stale, err := s.readState(rc, api.OpUDPProxy)
	if err != nil {
		writeError(w, http.StatusBadGateway, errorCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, stateResponse(rc, api.OpUDPProxy, state, readAt, stale))
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
	if req.Op != api.OpUDPProxy {
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
		s.st.Log(rc.ID, who, req.Op, req.Value, errorCode(err), err.Error())
		writeError(w, http.StatusBadGateway, errorCode(err), err)
		return
	}
	defer client.Close()

	state, err := router.Apply(client, ctrl, router.Op(req.Op), req.Value)
	if err != nil {
		log.Printf("apply router=%d op=%s value=%v: %v", rc.ID, req.Op, req.Value, err)
		s.st.Log(rc.ID, who, req.Op, req.Value, errorCode(err), err.Error())
		// todo(шаг 6): уведомление в тг
		writeError(w, http.StatusBadGateway, errorCode(err), err)
		return
	}
	_ = s.st.CachePut(rc.ID, req.Op, state)
	s.st.Log(rc.ID, who, req.Op, req.Value, "ok", "")

	writeJSON(w, http.StatusOK, stateResponse(rc, req.Op, state, time.Now().UTC(), false))
}

func (s *server) resolve(w http.ResponseWriter, r *http.Request, code string) (actor, bool) {
	ip := clientIP(r)
	if s.limiter.Blocked(ip) {
		writeError(w, http.StatusTooManyRequests, api.ErrTooManyAttempts, nil)
		return actor{}, false
	}

	got := auth.Normalize(code)
	if got == "" {
		s.limiter.Fail(ip)
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

	s.limiter.Fail(ip)
	writeError(w, http.StatusUnauthorized, api.ErrBadCode, nil)
	return actor{}, false
}

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

// кэш с пометкой stale
func (s *server) readState(rc store.Router, op string) (bool, time.Time, bool, error) {
	client, ctrl, err := s.connect(rc)
	if err == nil {
		defer client.Close()
		var state bool
		state, err = ctrl.ReadState(client, router.Op(op))
		if err == nil {
			_ = s.st.CachePut(rc.ID, op, state)
			return state, time.Now().UTC(), false, nil
		}
	}

	if errors.Is(err, sshconn.ErrOffline) {
		if v, at, ok := s.st.CacheGet(rc.ID, op); ok {
			return v, at, true, nil
		}
	}
	return false, time.Time{}, false, err
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

func stateResponse(rc store.Router, op string, value bool, readAt time.Time, stale bool) api.StateResponse {
	return api.StateResponse{
		Router: api.RouterInfo{ID: rc.ID, Name: rc.Name, Firmware: rc.Firmware},
		Ops:    []api.OpState{{Op: op, Value: value, ReadAt: readAt, Stale: stale}},
	}
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

func clientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
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
