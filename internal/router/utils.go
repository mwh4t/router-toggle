package router

import "strings"

func trimSpace(s string) string { return strings.TrimSpace(s) }

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

func toggleInList(list []string, value string, enable bool) []string {
	idx := -1
	for i, v := range list {
		if v == value {
			idx = i
			break
		}
	}
	if enable {
		if idx == -1 {
			return append(append([]string{}, list...), value)
		}
		return list
	}
	if idx == -1 {
		return list
	}
	out := append([]string{}, list[:idx]...)
	return append(out, list[idx+1:]...)
}

func toggleLineList(content, line string, enable bool) string {
	lines := splitLines(content)
	idx := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == line {
			idx = i
			break
		}
	}
	switch {
	case enable && idx == -1:
		lines = append(lines, line)
	case !enable && idx != -1:
		lines = append(lines[:idx], lines[idx+1:]...)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func lineListHas(content, line string) bool {
	for _, l := range splitLines(content) {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}
