package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"router-toggle/internal/api"
	"router-toggle/internal/geosite"
	"router-toggle/internal/router"
	"router-toggle/internal/store"
)

const openwrtCategoryLimit = 1500

func (s *server) loadGeosite() {
	if err := s.geo.Load(s.cfg.GeositePath); err == nil {
		return
	}
	log.Printf("dlc.dat не найден, скачиваю")
	if err := geosite.Download(s.cfg.GeositePath); err != nil {
		log.Printf("dlc.dat: %v", err)
		return
	}
	if err := s.geo.Load(s.cfg.GeositePath); err != nil {
		log.Printf("dlc.dat: %v", err)
	}
}

func (s *server) expand(name string) []string {
	domains, _ := s.geo.Expand(name)
	return domains
}

func toAPI(entries []router.DomainEntry) []api.DomainEntry {
	out := make([]api.DomainEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, api.DomainEntry{Kind: e.Kind, Name: e.Name})
	}
	return out
}

func (s *server) handleDomains(w http.ResponseWriter, r *http.Request) {
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

	client, ctrl, err := s.connect(rc)
	if err != nil {
		writeError(w, http.StatusBadGateway, errorCode(err), err)
		return
	}
	defer client.Close()

	dc, err := router.AsDomains(ctrl)
	if err != nil {
		writeError(w, http.StatusBadRequest, api.ErrUnknownFormat, err)
		return
	}
	entries, err := dc.ListDomains(client)
	if err != nil {
		writeError(w, http.StatusBadGateway, errorCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, api.DomainsResponse{
		Router:  routerInfo(rc, a),
		Entries: toAPI(entries),
	})
}

func (s *server) handleDomainSearch(w http.ResponseWriter, r *http.Request) {
	var req api.DomainSearchRequest
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

	out := api.DomainSearchResponse{}
	for _, m := range s.geo.Search(req.Query, 8) {
		if rc.Firmware == router.FirmwareOpenWrt && !a.admin {
			if domains, _ := s.geo.Expand(m.Name); len(domains) > openwrtCategoryLimit {
				continue
			}
		}
		out.Matches = append(out.Matches, api.DomainMatch{Name: m.Name, Size: m.Size})
	}
	if d := normalizeDomain(req.Query); router.ValidDomain(d) {
		out.Domain = d
	}
	writeJSON(w, http.StatusOK, out)
}

// преобразование
func normalizeDomain(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	q = strings.TrimPrefix(q, "https://")
	q = strings.TrimPrefix(q, "http://")
	if i := strings.IndexAny(q, "/?#"); i >= 0 {
		q = q[:i]
	}
	return strings.TrimPrefix(q, "www.")
}

func (s *server) handleDomainAdd(w http.ResponseWriter, r *http.Request) {
	s.changeDomains(w, r, true)
}

func (s *server) handleDomainRemove(w http.ResponseWriter, r *http.Request) {
	s.changeDomains(w, r, false)
}

func (s *server) changeDomains(w http.ResponseWriter, r *http.Request, add bool) {
	var req api.DomainChangeRequest
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

	entry := router.DomainEntry{Kind: req.Kind, Name: strings.ToLower(strings.TrimSpace(req.Name))}
	if add {
		if code := s.checkNew(rc, a, &entry); code != "" {
			writeError(w, http.StatusBadRequest, code, nil)
			return
		}
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

	dc, err := router.AsDomains(ctrl)
	if err != nil {
		writeError(w, http.StatusBadRequest, api.ErrUnknownFormat, err)
		return
	}
	current, err := dc.ListDomains(client)
	if err != nil {
		writeError(w, http.StatusBadGateway, errorCode(err), err)
		return
	}

	next := make([]router.DomainEntry, 0, len(current)+1)
	for _, e := range current {
		if e.Kind == entry.Kind && e.Name == entry.Name {
			if add {
				writeError(w, http.StatusConflict, api.ErrDomainExists, nil)
				return
			}
			continue
		}
		next = append(next, e)
	}
	if add {
		if has, err := dc.HasElsewhere(client, entry); err == nil && has {
			writeError(w, http.StatusConflict, api.ErrDomainExists, nil)
			return
		}
		next = append(next, entry)
	}

	op, verb := "domain_remove", "убрать"
	if add {
		op, verb = "domain_add", "добавить"
	}
	if err := router.ApplyDomains(client, dc, next, s.expand); err != nil {
		code := errorCode(err)
		s.st.Log(rc.ID, actorName(a), op, add, code, entry.Name+": "+err.Error())
		s.tg.Send(fmt.Sprintf("%d:%s:%s", rc.ID, op, code),
			fmt.Sprintf("🟠 %s · %s\n%s %s · %s\n\n%v", code, rc.Name, verb, entry.Name, actorName(a), err))
		writeError(w, http.StatusBadGateway, code, err)
		return
	}
	s.st.Log(rc.ID, actorName(a), op, add, "ok", entry.Kind+":"+entry.Name)

	writeJSON(w, http.StatusOK, api.DomainsResponse{
		Router:  routerInfo(rc, a),
		Entries: toAPI(next),
	})
}

// пустая строка
func (s *server) checkNew(rc store.Router, a actor, e *router.DomainEntry) string {
	switch e.Kind {
	case router.KindDomain:
		e.Name = normalizeDomain(e.Name)
		if !router.ValidDomain(e.Name) {
			return api.ErrDomainInvalid
		}
	case router.KindCategory:
		if !router.ValidCategory(e.Name) || !s.geo.Has(e.Name) {
			return api.ErrDomainInvalid
		}
		if rc.Firmware == router.FirmwareOpenWrt && !a.admin {
			if domains, _ := s.geo.Expand(e.Name); len(domains) > openwrtCategoryLimit {
				return api.ErrCategoryTooBig
			}
		}
	default:
		return api.ErrDomainInvalid
	}
	return ""
}

// вс 06:10
func nextRefresh(now time.Time) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), 6, 10, 0, 0, now.Location())
	for t.Weekday() != time.Sunday || !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

func (s *server) refreshLoop() {
	for {
		time.Sleep(time.Until(nextRefresh(time.Now())))
		s.refreshDomains()
	}
}

// пересборка категорий на openwrt
func (s *server) refreshDomains() {
	if err := geosite.Download(s.cfg.GeositePath); err != nil {
		s.tg.Send("geosite", fmt.Sprintf("🟠 Обновление dlc.dat не удалось\n\n%v", err))
		return
	}
	if err := s.geo.Load(s.cfg.GeositePath); err != nil {
		s.tg.Send("geosite", fmt.Sprintf("🟠 Новый dlc.dat не читается\n\n%v", err))
		return
	}

	routers, err := s.st.Routers()
	if err != nil {
		return
	}
	var failed []string
	updated := 0
	for _, rc := range routers {
		if rc.Firmware != router.FirmwareOpenWrt {
			continue
		}
		changed, err := s.refreshRouter(rc)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", rc.Name, err))
			continue
		}
		if changed {
			updated++
		}
	}

	text := fmt.Sprintf("🌐 Домены обновлены · роутеров с изменениями: %d", updated)
	if len(failed) > 0 {
		text = fmt.Sprintf("🟠 Обновление доменов\n\n%s", strings.Join(failed, "\n"))
	}
	s.tg.Send("", text)
}

func (s *server) refreshRouter(rc store.Router) (bool, error) {
	if !s.locks.acquire(rc.ID) {
		return false, errors.New("роутер занят")
	}
	defer s.locks.release(rc.ID)

	client, ctrl, err := s.connect(rc)
	if err != nil {
		return false, err
	}
	defer client.Close()

	dc, err := router.AsDomains(ctrl)
	if err != nil {
		return false, err
	}
	entries, err := dc.ListDomains(client)
	if err != nil || len(entries) == 0 {
		return false, err
	}
	plan, err := dc.PlanDomains(client, entries, s.expand)
	if err != nil || plan.Empty() {
		return false, err
	}
	if err := router.ApplyDomains(client, dc, entries, s.expand); err != nil {
		return false, err
	}
	s.st.Log(rc.ID, "server", "domain_refresh", true, "ok", "")
	return true, nil
}
