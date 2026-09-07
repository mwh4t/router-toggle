package main

import "strings"

func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
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
			list = append(list, value)
		}
		return list
	}
	if idx != -1 {
		list = append(list[:idx], list[idx+1:]...)
	}
	return list
}

func toggleLineList(content, line string, enable bool) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	idx := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == line {
			idx = i
			break
		}
	}
	if enable {
		if idx == -1 {
			lines = append(lines, line)
		}
	} else if idx != -1 {
		lines = append(lines[:idx], lines[idx+1:]...)
	}
	return strings.Join(lines, "\n") + "\n"
}
