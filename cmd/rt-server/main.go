// localhost без tls и авторизации
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"strconv"
	"time"

	"router-toggle/internal/api"
	"router-toggle/internal/router"
	"router-toggle/internal/sshconn"
)

type server struct {
	cfg   *Config
	cache *stateCache
	locks *routerLocks
}

func main() {
	configPath := flag.String("config", "/etc/router-toggle/config.json", "путь к конфигурации")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("конфигурация: %v", err)
	}

	s := &server{cfg: cfg, cache: newStateCache(), locks: newRouterLocks()}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": api.Version})
	})
	mux.HandleFunc("/v1/routers", s.handleRouters)
	mux.HandleFunc("/v1/status", s.handleStatus)
	mux.HandleFunc("/v1/apply", s.handleApply)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      3 * time.Minute,
	}
	log.Printf("rt-server слушает %s, роутеров в конфигурации: %d", cfg.Listen, len(cfg.Routers))
	log.Fatal(srv.ListenAndServe())
}

func (s *server) handleRouters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	out := api.RoutersResponse{}
	for _, rc := range s.cfg.Routers {
		out.Routers = append(out.Routers, api.RouterInfo{ID: rc.ID, Name: rc.Name, Firmware: rc.Firmware})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	var req api.StatusRequest
	if !decode(w, r, &req) {
		return
	}
	rc, ok := s.cfg.router(req.RouterID)
	if !ok {
		writeError(w, http.StatusNotFound, api.ErrBadCode, nil)
		return
	}

	state, readAt, stale, err := s.readState(rc, api.OpUDPProxy)
	if err != nil {
		writeError(w, http.StatusBadGateway, errorCode(err), err)
		return
	}

	writeJSON(w, http.StatusOK, api.StateResponse{
		Router: api.RouterInfo{ID: rc.ID, Name: rc.Name, Firmware: rc.Firmware},
		Ops:    []api.OpState{{Op: api.OpUDPProxy, Value: state, ReadAt: readAt, Stale: stale}},
	})
}

func (s *server) handleApply(w http.ResponseWriter, r *http.Request) {
	var req api.ApplyRequest
	if !decode(w, r, &req) {
		return
	}
	// незнакомые операции отвергаются до обращения к роутеру
	if req.Op != api.OpUDPProxy {
		writeError(w, http.StatusBadRequest, api.ErrUnknownFormat, nil)
		return
	}
	rc, ok := s.cfg.router(req.RouterID)
	if !ok {
		writeError(w, http.StatusNotFound, api.ErrBadCode, nil)
		return
	}

	if !s.locks.acquire(rc.ID) {
		writeError(w, http.StatusConflict, api.ErrRouterBusy, nil)
		return
	}
	defer s.locks.release(rc.ID)

	client, ctrl, err := s.connect(rc)
	if err != nil {
		writeError(w, http.StatusBadGateway, errorCode(err), err)
		return
	}
	defer client.Close()

	state, err := router.Apply(client, ctrl, router.Op(req.Op), req.Value)
	if err != nil {
		log.Printf("apply router=%d op=%s value=%v: %v", rc.ID, req.Op, req.Value, err)
		// todo(шаг 6): уведомление в телеграм
		writeError(w, http.StatusBadGateway, errorCode(err), err)
		return
	}
	s.cache.put(rc.ID, req.Op, state)

	writeJSON(w, http.StatusOK, api.StateResponse{
		Router: api.RouterInfo{ID: rc.ID, Name: rc.Name, Firmware: rc.Firmware},
		Ops:    []api.OpState{{Op: req.Op, Value: state, ReadAt: time.Now().UTC(), Stale: false}},
	})
}

// кэш с пометкой stale
func (s *server) readState(rc RouterConfig, op string) (bool, time.Time, bool, error) {
	client, ctrl, err := s.connect(rc)
	if err == nil {
		defer client.Close()
		var state bool
		state, err = ctrl.ReadState(client, router.Op(op))
		if err == nil {
			s.cache.put(rc.ID, op, state)
			return state, time.Now().UTC(), false, nil
		}
	}

	if errors.Is(err, sshconn.ErrOffline) {
		if e, ok := s.cache.get(rc.ID, op); ok {
			return e.value, e.readAt, true, nil
		}
	}
	return false, time.Time{}, false, err
}

func (s *server) connect(rc RouterConfig) (*sshconn.Client, router.Controller, error) {
	ctrl, err := router.For(rc.Firmware)
	if err != nil {
		return nil, nil, err
	}
	client, err := sshconn.Dial(sshconn.Target{
		Addr:                "127.0.0.1:" + strconv.Itoa(rc.TunnelPort),
		User:                rc.SSHUser,
		AuthType:            rc.AuthType,
		Secret:              rc.AuthSecret,
		HostKey:             rc.HostKey,
		AllowUnknownHostKey: rc.AllowUnknownHostKey,
	})
	if err != nil {
		return nil, nil, err
	}
	return client, ctrl, nil
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

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, api.ErrInternal, err)
		return false
	}
	return true
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
