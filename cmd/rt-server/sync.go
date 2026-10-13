package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
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
	if _, err := s.fetch(rc, link); err != nil {
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
	writeJSON(w, http.StatusOK, api.SyncResponse{Results: []api.SyncResult{s.syncRouter(rc, true, true, false)}})
}

var panelIDRe = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

func (s *server) handleRouterOwner(w http.ResponseWriter, r *http.Request) {
	var req api.OwnerRequest
	if !s.decode(w, r, &req) || !s.adminOnly(w, r, req.Code) {
		return
	}
	owner := strings.TrimSpace(req.Owner)
	if owner != "" && !panelIDRe.MatchString(owner) || len(req.Access) > 20 {
		writeError(w, http.StatusBadRequest, api.ErrUnknownFormat, nil)
		return
	}
	seen := map[int64]bool{}
	var access []int64
	for _, id := range req.Access {
		if id > 0 && !seen[id] {
			seen[id] = true
			access = append(access, id)
		}
	}
	if err := s.st.SetOwner(req.RouterID, owner, access); err != nil {
		writeError(w, http.StatusNotFound, api.ErrBadCode, err)
		return
	}
	rc, err := s.st.Router(req.RouterID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, api.ErrInternal, err)
		return
	}
	s.st.Log(rc.ID, "admin", "owner", true, "ok", fmt.Sprintf("%s +%d", owner, len(access)))
	writeJSON(w, http.StatusOK, routerInfo(rc, actor{admin: true}))
}

// роутер в панели виден как отдельное устройство владельца
func device(rc store.Router) nodes.Device {
	return nodes.Device{
		HWID:  "router-toggle-" + strconv.Itoa(rc.TunnelPort),
		OS:    rc.Firmware,
		Model: "Router " + strconv.Itoa(rc.TunnelPort),
	}
}

func (s *server) fetch(rc store.Router, link string) (nodes.Set, error) {
	set, err := nodes.Fetch(link, device(rc))
	if err != nil {
		return set, err
	}
	set = set.Only(s.cfg.HostsMark)
	set.Primary = rc.Primary
	return set, nil
}

const (
	measureEvery = 7*24*time.Hour - time.Hour
	switchMargin = 1.3
)

// замер только нод, уже стоящих на роутере
func measureNodes(r router.Runner, ctrl router.Controller, set nodes.Set) (map[string]float64, error) {
	nc, err := router.AsNodes(ctrl)
	if err != nil {
		return nil, err
	}
	have, err := nc.NodeTags(r)
	if err != nil {
		return nil, err
	}
	if kb, err := router.MemAvailableKB(r); err == nil && kb < router.MinMemForProbeKB {
		return nil, &lowMemError{kb}
	}
	speeds := map[string]float64{}
	for _, tag := range set.Tags() {
		if !contains(have, tag) {
			continue
		}
		v, err := nc.NodeSpeed(r, tag)
		if err != nil {
			return nil, err
		}
		speeds[tag] = v
	}
	return speeds, nil
}

// замер подождёт неделю, чтобы не задеть рабочий xray
type lowMemError struct{ kb int }

func (e *lowMemError) Error() string {
	return fmt.Sprintf("замер пропущен, у роутера свободно %d МБ", e.kb>>10)
}

// смена только при заметном выигрыше
func pickPrimary(set nodes.Set, speeds map[string]float64) string {
	cur, best := set.Main(), set.Main()
	for _, tag := range set.Tags() {
		if speeds[tag] > speeds[best] {
			best = tag
		}
	}
	if speeds[cur] == 0 && speeds[best] > 0 || speeds[best] >= speeds[cur]*switchMargin {
		return best
	}
	return cur
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func shortSpeeds(speeds map[string]float64) map[string]float64 {
	if len(speeds) == 0 {
		return nil
	}
	out := map[string]float64{}
	for tag, v := range speeds {
		out[strings.TrimPrefix(tag, nodes.TagPrefix)] = float64(int(v*10+0.5)) / 10
	}
	return out
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

	if req.DryRun && req.Measure && len(targets) == 1 {
		writeJSON(w, http.StatusOK, api.SyncResponse{Results: []api.SyncResult{s.syncRouter(targets[0], true, true, true)}})
		return
	}
	if req.DryRun {
		results := make([]api.SyncResult, len(targets))
		var wg sync.WaitGroup
		for i, rc := range targets {
			wg.Add(1)
			go func(i int, rc store.Router) {
				defer wg.Done()
				results[i] = s.syncRouter(rc, true, true, false)
			}(i, rc)
		}
		wg.Wait()
		writeJSON(w, http.StatusOK, api.SyncResponse{Results: results})
		return
	}
	if len(targets) == 1 {
		writeJSON(w, http.StatusOK, api.SyncResponse{Results: []api.SyncResult{s.syncRouter(targets[0], false, true, req.Measure)}})
		return
	}
	go func() {
		results := make([]api.SyncResult, 0, len(targets))
		for _, rc := range targets {
			results = append(results, s.syncRouter(rc, false, true, req.Measure))
		}
		s.tg.Send("", syncReport(results, false))
	}()
	writeJSON(w, http.StatusOK, api.SyncResponse{Started: true})
}

// без force только при новых нодах или пора замерять
func (s *server) syncRouter(rc store.Router, dry, force, measure bool) api.SyncResult {
	res := api.SyncResult{Router: routerInfo(rc, actor{admin: true}), Sub: maskSub(rc.SubURL)}
	if rc.SubURL == "" {
		res.Status = "no_sub"
		return res
	}
	set, err := s.fetch(rc, rc.SubURL)
	if err != nil {
		res.Status, res.Message = "sub_error", err.Error()
		return res
	}
	res.Nodes = set.Names()
	res.Speeds = shortSpeeds(rc.Speeds)
	measure = measure || !dry && time.Since(rc.MeasuredAt) > measureEvery
	if !force && !measure && set.Hash() == rc.NodesHash {
		res.Status = "same"
		res.Primary = strings.TrimPrefix(set.Main(), nodes.TagPrefix)
		return res
	}

	if !dry || measure {
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

	if measure {
		speeds, err := measureNodes(client, ctrl, set)
		var low *lowMemError
		switch {
		case errors.As(err, &low):
			_ = s.st.SetSpeeds(rc.ID, nil)
			res.Message = err.Error()
		case err != nil:
			res.Message = "замер не удался: " + err.Error()
		case len(speeds) > 1:
			_ = s.st.SetSpeeds(rc.ID, speeds)
			res.Speeds = shortSpeeds(speeds)
			if best := pickPrimary(set, speeds); best != set.Main() {
				res.Switched = true
				set.Primary = best
			} else {
				set.Primary = set.Main()
			}
			// выбор хранится сразу, применится этой или следующей сверкой
			_ = s.st.SetPrimary(rc.ID, set.Primary)
		}
	}
	res.Primary = strings.TrimPrefix(set.Main(), nodes.TagPrefix)
	hash := set.Hash()

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
		s.st.Log(rc.ID, "admin", "nodes", true, "ok", strings.Join(got.After, ",")+" main "+set.Main())
	}
	return res
}

func tagDiff(before, after []string) (added, removed []string) {
	for _, t := range after {
		if !contains(before, t) {
			added = append(added, strings.TrimPrefix(t, nodes.TagPrefix))
		}
	}
	for _, t := range before {
		if !contains(after, t) {
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
		res := s.syncRouter(rc, false, false, false)
		if res.Status != "same" || res.Switched || res.Message != "" {
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
		line := syncMarks[r.Status] + " " + r.Router.Name + syncDetail(r)
		if r.Switched {
			line += " · основная теперь " + r.Primary + speedsText(r.Speeds)
		}
		if r.Message != "" && r.Status != "sub_error" && r.Status != "error" {
			line += " · " + r.Message
		}
		lines = append(lines, line)
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

func speedsText(speeds map[string]float64) string {
	if len(speeds) == 0 {
		return ""
	}
	var parts []string
	for tag, v := range speeds {
		parts = append(parts, fmt.Sprintf("%s %.0f", tag, v))
	}
	sort.Strings(parts)
	return " (" + strings.Join(parts, ", ") + " Мбит/с)"
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
