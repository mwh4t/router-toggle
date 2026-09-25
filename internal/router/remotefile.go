package router

import (
	"fmt"
	"strings"
)

const (
	tmpSuffix    = ".rt-new"
	bakSuffix    = ".rt-bak"
	rollbackTmp  = ".rt-rb"
	heredocToken = "RT_EOF_4f19c2a7"
)

func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func readFile(r Runner, path string) (string, error) {
	out, err := r.Run("cat " + shq(path))
	if err != nil {
		return "", fmt.Errorf("не смог прочитать %s: %w", path, err)
	}
	return out, nil
}

// только для временных файлов
type inputRunner interface {
	RunInput(cmd, input string) (string, error)
}

func writeFile(r Runner, path, content string) error {
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if ir, ok := r.(inputRunner); ok {
		if out, err := ir.RunInput("cat > "+shq(path), content); err != nil {
			return fmt.Errorf("не смог записать %s: %w (%s)", path, err, strings.TrimSpace(out))
		}
		return nil
	}

	if strings.Contains(content, heredocToken) {
		return fmt.Errorf("содержимое файла %s содержит служебный маркер", path)
	}
	cmd := fmt.Sprintf("cat > %s <<'%s'\n%s%s\n", shq(path), heredocToken, content, heredocToken)
	if out, err := r.Run(cmd); err != nil {
		return fmt.Errorf("не смог записать %s: %w (%s)", path, err, strings.TrimSpace(out))
	}
	return nil
}

func removeFile(r Runner, path string) {
	_, _ = r.Run("rm -f " + shq(path))
}

// atomic rename
func replaceFile(r Runner, path string) error {
	cmd := fmt.Sprintf("cp -p %s %s && mv %s %s",
		shq(path), shq(path+bakSuffix),
		shq(path+tmpSuffix), shq(path))
	if out, err := r.Run(cmd); err != nil {
		return fmt.Errorf("не смог заменить %s: %w (%s)", path, err, strings.TrimSpace(out))
	}
	return nil
}

// бэкап после отката
func restoreFile(r Runner, path string) error {
	cmd := fmt.Sprintf("cp -p %s %s && mv %s %s",
		shq(path+bakSuffix), shq(path+rollbackTmp),
		shq(path+rollbackTmp), shq(path))
	if out, err := r.Run(cmd); err != nil {
		return fmt.Errorf("не смог восстановить %s из бэкапа: %w (%s)", path, err, strings.TrimSpace(out))
	}
	return nil
}
