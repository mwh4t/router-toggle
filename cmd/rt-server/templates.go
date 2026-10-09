package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"router-toggle/internal/api"
	"router-toggle/internal/router"
	"router-toggle/internal/store"
)

// эталоны маршрутизации
var templateFirmware = map[string]string{
	router.TemplateServers: router.FirmwareOpenWrt,
	router.TemplateRouting: router.FirmwareKeenetic,
}

func (s *server) templatePath(name string) string {
	return filepath.Join(s.cfg.TemplatesDir, name)
}

// эталоны для всех прошивок
func (s *server) loadTemplates() map[string]string {
	out := map[string]string{}
	for name, fw := range templateFirmware {
		data, err := os.ReadFile(s.templatePath(name))
		if err == nil {
			out[fw] = string(data)
		}
	}
	return out
}

func (s *server) adminOnly(w http.ResponseWriter, r *http.Request, code string) bool {
	a, ok := s.resolve(w, r, code)
	if !ok {
		return false
	}
	if !a.admin {
		writeError(w, http.StatusForbidden, api.ErrBadCode, nil)
		return false
	}
	return true
}

func (s *server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	var req api.StatusRequest
	if !s.decode(w, r, &req) || !s.adminOnly(w, r, req.Code) {
		return
	}
	writeJSON(w, http.StatusOK, s.templatesInfo())
}

func (s *server) templatesInfo() api.TemplatesResponse {
	out := api.TemplatesResponse{Templates: []api.TemplateInfo{}}
	for _, name := range []string{router.TemplateServers, router.TemplateRouting} {
		info := api.TemplateInfo{Name: name, Firmware: templateFirmware[name]}
		if data, err := os.ReadFile(s.templatePath(name)); err == nil {
			info.Size = len(data)
			info.Lines = strings.Count(string(data), "\n")
			if st, err := os.Stat(s.templatePath(name)); err == nil {
				info.UpdatedAt = st.ModTime().UTC()
			}
		}
		out.Templates = append(out.Templates, info)
	}
	return out
}

// прошлая версия остаётся рядом с .prev
func (s *server) handleTemplateUpload(w http.ResponseWriter, r *http.Request) {
	var req api.TemplateUploadRequest
	if !s.decodeLimit(w, r, &req, 4<<20) || !s.adminOnly(w, r, req.Code) {
		return
	}
	content := strings.ReplaceAll(req.Content, "\r\n", "\n")
	if err := router.ValidateTemplate(req.Name, content); err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{
			Code:    api.ErrTemplateInvalid,
			Message: api.Message(api.ErrTemplateInvalid) + " " + err.Error(),
		})
		return
	}
	if err := s.saveTemplate(req.Name, content); err != nil {
		writeError(w, http.StatusInternalServerError, api.ErrInternal, err)
		return
	}
	s.st.Log(0, "admin", "template_upload", true, "ok", req.Name)
	writeJSON(w, http.StatusOK, s.templatesInfo())
}

func (s *server) saveTemplate(name, content string) error {
	if err := os.MkdirAll(s.cfg.TemplatesDir, 0o700); err != nil {
		return err
	}
	path := s.templatePath(name)
	if old, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".prev", old, 0o600); err != nil {
			return err
		}
	}
	// atomic rename
	if err := os.WriteFile(path+".tmp", []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func (s *server) handleTemplateApply(w http.ResponseWriter, r *http.Request) {
	var req api.TemplateApplyRequest
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
	templates := s.loadTemplates()

	// пробный прогон параллельно, роутеры только читаются
	if req.DryRun {
		results := make([]api.TemplateResult, len(targets))
		var wg sync.WaitGroup
		for i, rc := range targets {
			wg.Add(1)
			go func(i int, rc store.Router) {
				defer wg.Done()
				results[i] = s.applyTemplate(rc, templates, true)
			}(i, rc)
		}
		wg.Wait()
		writeJSON(w, http.StatusOK, api.TemplateApplyResponse{Results: results})
		return
	}

	if len(targets) == 1 {
		res := s.applyTemplate(targets[0], templates, false)
		writeJSON(w, http.StatusOK, api.TemplateApplyResponse{Results: []api.TemplateResult{res}})
		return
	}

	go func() {
		results := make([]api.TemplateResult, 0, len(targets))
		for _, rc := range targets {
			results = append(results, s.applyTemplate(rc, templates, false))
		}
		s.tg.Send("", templateReport(results))
	}()
	writeJSON(w, http.StatusOK, api.TemplateApplyResponse{Started: true})
}

func (s *server) applyTemplate(rc store.Router, templates map[string]string, dry bool) api.TemplateResult {
	res := api.TemplateResult{Router: routerInfo(rc, actor{admin: true})}
	template, ok := templates[rc.Firmware]
	if !ok {
		res.Status = "no_template"
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

	tc, err := router.AsTemplate(ctrl)
	if err != nil {
		res.Status, res.Message = "error", err.Error()
		return res
	}

	got, err := router.ApplyTemplate(client, tc, template, s.expand, dry)
	res.Added, res.Removed = got.Added, got.Removed
	switch {
	case err != nil:
		res.Status, res.Message = "error", err.Error()
		if !dry {
			code := errorCode(err)
			s.st.Log(rc.ID, "admin", "routing_template", true, code, err.Error())
			if errors.Is(err, router.ErrRollbackFailed) {
				s.tg.Send(fmt.Sprintf("%d:routing_template", rc.ID),
					fmt.Sprintf("🔴 %s · %s\nмаршрутизация из эталона\n\n%v", code, rc.Name, err))
			}
		}
	case !got.Changed:
		res.Status = "same"
	case dry:
		res.Status = "changes"
	default:
		res.Status = "applied"
		s.st.Log(rc.ID, "admin", "routing_template", true, "ok",
			fmt.Sprintf("+%d -%d", got.Added, got.Removed))
	}
	return res
}

var templateMarks = map[string]string{
	"same": "➖", "changes": "📝", "applied": "✅", "offline": "📴",
	"busy": "⏳", "no_template": "❔", "error": "🔴",
}

func templateReport(results []api.TemplateResult) string {
	lines := []string{"🗺 Маршрутизация из эталона", ""}
	for _, r := range results {
		line := fmt.Sprintf("%s %s", templateMarks[r.Status], r.Router.Name)
		switch r.Status {
		case "applied":
			line += fmt.Sprintf(" · +%d −%d", r.Added, r.Removed)
		case "same":
			line += " · без изменений"
		case "offline":
			line += " · не на связи"
		case "busy":
			line += " · занят"
		case "no_template":
			line += " · нет эталона"
		case "error":
			line += " · " + r.Message
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
