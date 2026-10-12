package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"router-toggle/internal/api"
	"router-toggle/internal/nodes"
	"router-toggle/internal/router"
	"router-toggle/internal/store"
)

func (s *server) handleRouterSub(w http.ResponseWriter, r *http.Request) {
	var req api.SubRequest
	if !s.decode(w, r, &req) || !s.adminOnly(w, r, req.Code) {
		return
	}
	rc, err := s.st.Router(req.RouterID)
	if err != nil {
		writeError(w, http.StatusNotFound, api.ErrBadCode, err)
		return
	}
	link := strings.TrimSpace(req.URL)
	if _, err := nodes.Fetch(link); err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{
			Code:    api.ErrSubInvalid,
			Message: api.Message(api.ErrSubInvalid) + " " + err.Error(),
		})
		return
	}
	if err := s.st.SetSubURL(rc.ID, link); err != nil {
		writeError(w, http.StatusInternalServerError, api.ErrInternal, err)
		return
	}
	s.st.Log(rc.ID, "admin", "sub_set", true, "ok", maskSub(link))
	rc.SubURL, rc.NodesHash = link, ""
	writeJSON(w, http.StatusOK, api.SyncResponse{Results: []api.SyncResult{s.syncRouter(rc, true, true)}})
}

func (s *server) handleSync(w http.ResponseWriter, r *http.Request) {
	var req api.SyncRequest
	if !s.decode(w, r, &req) || !s.adminOnly(w, r, req.Code) {
		return
	}
	var targets []store.Router
	if req.RouterID > 0 {
		rc, err := s.st.Router(req.RouterID)
		if err != nil {
			writeError(w, http.StatusNotFound, api.ErrBadCode, err)
			return
		}
		targets = []store.Router{rc}
	} else {
		all, err := s.st.Routers()
		if err != nil {
			writeError(w, http.StatusInternalServerError, api.ErrInternal, err)
			return
		}
		targets = all
	}

	if req.DryRun {
		results := make([]api.SyncResult, len(targets))
		var wg sync.WaitGroup
		for i, rc := range targets {
			wg.Add(1)
			go func(i int, rc store.Router) {
				defer wg.Done()
				results[i] = s.syncRouter(rc, true, true)
			}(i, rc)
		}
		wg.Wait()
		writeJSON(w, http.StatusOK, api.SyncResponse{Results: results})
		return
	}
	if len(targets) == 1 {
		writeJSON(w, http.StatusOK, api.SyncResponse{Results: []api.SyncResult{s.syncRouter(targets[0], false, true)}})
		return
	}
	go func() {
		results := make([]api.SyncResult, 0, len(targets))
		for _, rc := range targets {
			results = append(results, s.syncRouter(rc, false, true))
		}
		s.tg.Send("", syncReport(results, false))
	}()
	writeJSON(w, http.StatusOK, api.SyncResponse{Started: true})
}

// без force только при новых нодах
func (s *server) syncRouter(rc store.Router, dry, force bool) api.SyncResult {
	res := api.SyncResult{Router: routerInfo(rc, actor{admin: true}), Sub: maskSub(rc.SubURL)}
	if rc.SubURL == "" {
		res.Status = "no_sub"
		return res
	}
	set, err := nodes.Fetch(rc.SubURL)
	if err != nil {
		res.Status, res.Message = "sub_error", err.Error()
		return res
	}
	res.Nodes = set.Names()
	hash := set.Hash()
	if !force && hash == rc.NodesHash {
		res.Status = "same"
		return res
	}

	if !dry {
		if !s.locks.acquire(rc.ID) {
			res.Status = "busy"
			return res
		}
		defer s.locks.release(rc.ID)
	}
	client, ctrl, err := s.connect(rc)
	if err != nil {
		res.Status, res.Message = "offline", err.Error()
		if errorCode(err) != api.ErrRouterOffline {
			res.Status = "error"
		}
		return res
	}
	defer client.Close()

	got, err := router.ApplyNodes(client, ctrl, set, dry)
	res.First = len(got.Before) == 0
	res.Added, res.Removed = tagDiff(got.Before, got.After)
	switch {
	case err != nil:
		res.Status, res.Message = "error", err.Error()
		if !dry {
			code := errorCode(err)
			s.st.Log(rc.ID, "admin", "nodes", true, code, err.Error())
			if errors.Is(err, router.ErrRollbackFailed) {
				s.tg.Send(fmt.Sprintf("%d:nodes", rc.ID),
					fmt.Sprintf("🔴 %s · %s\nноды из подписки\n\n%v", code, rc.Name, err))
			}
		}
	case !got.Changed:
		res.Status = "same"
		_ = s.st.SetNodesHash(rc.ID, hash)
	case dry:
		res.Status = "changes"
	default:
		res.Status = "applied"
		_ = s.st.SetNodesHash(rc.ID, hash)
		s.st.Log(rc.ID, "admin", "nodes", true, "ok", strings.Join(got.After, ","))
	}
	return res
}

func tagDiff(before, after []string) (added, removed []string) {
	in := func(list []string, v string) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}
	for _, t := range after {
		if !in(before, t) {
			added = append(added, strings.TrimPrefix(t, nodes.TagPrefix))
		}
	}
	for _, t := range before {
		if !in(after, t) {
			removed = append(removed, strings.TrimPrefix(t, nodes.TagPrefix))
		}
	}
	return added, removed
}

// ссылка без секретной части
func maskSub(link string) string {
	if link == "" {
		return ""
	}
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return "…"
	}
	tail := strings.Trim(u.Path, "/")
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	return u.Scheme + "://" + u.Host + "/…" + tail
}

// ежедневно после кронов роутеров
func nextSync(now time.Time) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), 6, 30, 0, 0, now.Location())
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

func (s *server) syncLoop() {
	for {
		time.Sleep(time.Until(nextSync(time.Now())))
		s.syncAll()
	}
}

func (s *server) syncAll() {
	routers, err := s.st.Routers()
	if err != nil {
		return
	}
	var results []api.SyncResult
	for _, rc := range routers {
		if rc.SubURL == "" {
			continue
		}
		res := s.syncRouter(rc, false, false)
		if res.Status != "same" {
			results = append(results, res)
		}
	}
	if len(results) > 0 {
		s.tg.Send("", syncReport(results, true))
	}
}

var syncMarks = map[string]string{
	"same": "➖", "changes": "📝", "applied": "✅", "offline": "📴",
	"busy": "⏳", "no_sub": "❔", "sub_error": "🟠", "error": "🔴",
}

func syncReport(results []api.SyncResult, nightly bool) string {
	title := "🛰 Ноды из подписок"
	if nightly {
		title += " · ночная сверка"
	}
	lines := []string{title, ""}
	for _, r := range results {
		lines = append(lines, syncMarks[r.Status]+" "+r.Router.Name+syncDetail(r))
	}
	return strings.Join(lines, "\n")
}

func syncDetail(r api.SyncResult) string {
	switch r.Status {
	case "applied":
		if r.First {
			return " · переведён на ноды: " + strings.Join(r.Added, ", ")
		}
		return changesText(r)
	case "same":
		return " · без изменений"
	case "offline":
		return " · не на связи"
	case "busy":
		return " · занят"
	case "no_sub":
		return " · нет подписки"
	case "sub_error", "error":
		return " · " + r.Message
	}
	return ""
}

func changesText(r api.SyncResult) string {
	var parts []string
	if len(r.Added) > 0 {
		parts = append(parts, "+"+strings.Join(r.Added, ", +"))
	}
	if len(r.Removed) > 0 {
		parts = append(parts, "−"+strings.Join(r.Removed, ", −"))
	}
	if len(parts) == 0 {
		return " · обновлены параметры нод"
	}
	return " · " + strings.Join(parts, " ")
}
