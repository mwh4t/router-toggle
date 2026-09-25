package sshconn

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	dialTimeout      = 5 * time.Second
	handshakeTimeout = 10 * time.Second
	keepaliveEvery   = 5 * time.Second
	commandTimeout   = 30 * time.Second
)

var ErrOffline = errors.New("роутер недоступен")        // e-10
var ErrAuth = errors.New("аутентификация или host key") // не вина клиента

type Target struct {
	Addr     string
	User     string
	AuthType string
	Secret   string

	HostKey string

	AllowUnknownHostKey bool // только при заведении роутера
}

type Client struct {
	c       *ssh.Client
	hostKey string
}

func Dial(t Target) (*Client, error) {
	auth, err := authMethod(t)
	if err != nil {
		return nil, err
	}
	hostKey, keyAlgo, err := hostKeyCallback(t)
	if err != nil {
		return nil, err
	}

	var seen string
	capture := func(addr string, remote net.Addr, key ssh.PublicKey) error {
		seen = AuthorizedKey(key)
		return hostKey(addr, remote, key)
	}

	conn, err := net.DialTimeout("tcp", t.Addr, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOffline, err)
	}

	// мёртвый туннель отвечает на tcp
	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		conn.Close()
		return nil, err
	}

	cfg := &ssh.ClientConfig{
		User:            t.User,
		Auth:            auth,
		HostKeyCallback: capture,
		Timeout:         dialTimeout,
	}
	if keyAlgo != "" {
		cfg.HostKeyAlgorithms = []string{keyAlgo}
	}

	sc, chans, reqs, err := ssh.NewClientConn(conn, t.Addr, cfg)
	if err != nil {
		conn.Close()
		return nil, handshakeError(err)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		sc.Close()
		return nil, err
	}

	client := ssh.NewClient(sc, chans, reqs)
	c := &Client{c: client, hostKey: seen}
	go c.keepalive()
	return c, nil
}

func (c *Client) Close() error { return c.c.Close() }

func (c *Client) HostKey() string { return c.hostKey }

func (c *Client) Run(cmd string) (string, error) {
	return c.run(cmd, nil)
}

func (c *Client) RunInput(cmd, input string) (string, error) {
	return c.run(cmd, strings.NewReader(input))
}

func (c *Client) run(cmd string, stdin io.Reader) (string, error) {
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)

	go func() {
		session, err := c.c.NewSession()
		if err != nil {
			done <- result{"", err}
			return
		}
		defer session.Close()
		session.Stdin = stdin
		out, err := session.CombinedOutput(cmd)
		done <- result{string(out), err}
	}()

	select {
	case r := <-done:
		return r.out, r.err
	case <-time.After(commandTimeout):
		// сброс повисшей сессии и порта туннеля
		c.c.Close()
		return "", fmt.Errorf("%w: команда не завершилась за %s", ErrOffline, commandTimeout)
	}
}

func (c *Client) keepalive() {
	t := time.NewTicker(keepaliveEvery)
	defer t.Stop()
	for range t.C {
		if _, _, err := c.c.SendRequest("keepalive@openssh.com", true, nil); err != nil {
			return
		}
	}
}

// ключ роутера до аутентификации
func ScanHostKey(addr string) (line, fingerprint string, err error) {
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrOffline, err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return "", "", err
	}

	cfg := &ssh.ClientConfig{
		User: "probe",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			line, fingerprint = AuthorizedKey(key), ssh.FingerprintSHA256(key)
			return nil
		},
		HostKeyAlgorithms: hostKeyAlgos(""),
		Timeout:           dialTimeout,
	}

	if sc, chans, reqs, cerr := ssh.NewClientConn(conn, addr, cfg); cerr == nil {
		ssh.NewClient(sc, chans, reqs).Close()
	}
	if line == "" {
		return "", "", fmt.Errorf("%w: роутер не показал ключ", ErrOffline)
	}
	return line, fingerprint, nil
}

func authMethod(t Target) ([]ssh.AuthMethod, error) {
	switch t.AuthType {
	case "password":
		ki := ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = t.Secret
			}
			return answers, nil
		})
		return []ssh.AuthMethod{ssh.Password(t.Secret), ki}, nil
	case "key":
		signer, err := ssh.ParsePrivateKey([]byte(t.Secret))
		if err != nil {
			return nil, fmt.Errorf("не смог разобрать приватный ключ: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default:
		return nil, fmt.Errorf("неизвестный auth_type: %q", t.AuthType)
	}
}

func handshakeError(err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Errorf("%w: %v", ErrOffline, err)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("%w: %v", ErrOffline, err)
	}
	return fmt.Errorf("%w: %v", ErrAuth, err)
}

// rsa-ключ подписывается тремя разными алгоритмами
func hostKeyAlgos(keyType string) []string {
	switch keyType {
	case "":
		return []string{ssh.KeyAlgoED25519, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512,
			ssh.KeyAlgoECDSA256, ssh.KeyAlgoRSA}
	case ssh.KeyAlgoRSA:
		return []string{ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSA}
	default:
		return []string{keyType}
	}
}

func hostKeyCallback(t Target) (ssh.HostKeyCallback, string, error) {
	if t.HostKey != "" {
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(t.HostKey))
		if err != nil {
			return nil, "", fmt.Errorf("не смог разобрать host key: %w", err)
		}
		return ssh.FixedHostKey(key), key.Type(), nil
	}
	if t.AllowUnknownHostKey {
		return ssh.InsecureIgnoreHostKey(), "", nil
	}
	return nil, "", fmt.Errorf("для %s не задан host key", t.Addr)
}

func Fingerprint(key ssh.PublicKey) string {
	return ssh.FingerprintSHA256(key)
}

func AuthorizedKey(key ssh.PublicKey) string {
	return key.Type() + " " + base64.StdEncoding.EncodeToString(key.Marshal())
}
