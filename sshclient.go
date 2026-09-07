package main

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"golang.org/x/crypto/ssh"
)

const jumpUser = "root"

func jumpHost() string {
	return os.Getenv("JUMP_HOST")
}

var jumpPassword string

func jumpConfig() (*ssh.ClientConfig, error) {
	if jumpPassword == "" {
		if err := survey.AskOne(&survey.Password{
			Message: "Пароль root на VPS:",
		}, &jumpPassword); err != nil {
			return nil, err
		}
	}
	return &ssh.ClientConfig{
		User:            jumpUser,
		Auth:            []ssh.AuthMethod{ssh.Password(jumpPassword)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: заменить на ssh.FixedHostKey(...)
		Timeout:         5 * time.Second,
	}, nil
}

func routerConfig(p Preset) (*ssh.ClientConfig, error) {
	var auth ssh.AuthMethod
	switch p.AuthType {
	case "key":
		signer, err := ssh.ParsePrivateKey([]byte(p.AuthSecret))
		if err != nil {
			return nil, fmt.Errorf("не смог разобрать приватный ключ: %w", err)
		}
		auth = ssh.PublicKeys(signer)
	case "password":
		auth = ssh.Password(p.AuthSecret)
	default:
		return nil, fmt.Errorf("неизвестный auth_type: %q", p.AuthType)
	}
	return &ssh.ClientConfig{
		User:            p.SSHUser,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: захардкодить host key роутера
		Timeout:         5 * time.Second,
	}, nil
}

func Connect(p Preset) (*ssh.Client, error) {
	direct := fmt.Sprintf("%s:%d", p.LocalIP, p.LocalPort)

	conn, err := net.DialTimeout("tcp", direct, 1500*time.Millisecond)
	if err == nil {
		conn.Close()
		cfg, err := routerConfig(p)
		if err != nil {
			return nil, err
		}
		return ssh.Dial("tcp", direct, cfg)
	}

	jc, err := jumpConfig()
	if err != nil {
		return nil, err
	}
	jumpAddr := jumpHost()
	if jumpAddr == "" {
		return nil, fmt.Errorf("переменная окружения JUMP_HOST не задана")
	}
	jumpClient, err := ssh.Dial("tcp", jumpAddr, jc)
	if err != nil {
		return nil, fmt.Errorf("VPS недоступен: %w", err)
	}

	tunnelAddr := fmt.Sprintf("localhost:%d", p.TunnelPort)
	remoteConn, err := jumpClient.Dial("tcp", tunnelAddr)
	if err != nil {
		return nil, fmt.Errorf("не достучался до роутера через туннель (порт %d на VPS): %w", p.TunnelPort, err)
	}

	cfg, err := routerConfig(p)
	if err != nil {
		return nil, err
	}
	ncc, chans, reqs, err := ssh.NewClientConn(remoteConn, tunnelAddr, cfg)
	if err != nil {
		return nil, err
	}
	return ssh.NewClient(ncc, chans, reqs), nil
}

func Exec(client *ssh.Client, cmd string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	out, err := session.CombinedOutput(cmd)
	return string(out), err
}

func writeRemoteFile(client *ssh.Client, path, content string) error {
	cmd := fmt.Sprintf("cat > %s <<'EOF'\n%sEOF", path, content)
	_, err := Exec(client, cmd)
	return err
}
