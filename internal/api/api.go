package api

import "time"

const (
	Version       = "1"
	VersionHeader = "X-Client-Version"
)

// закрытый список операций
const (
	OpUDPProxy = "udp_proxy"
	OpVPN      = "vpn" // до перезагрузки роутера
)

type StatusRequest struct {
	Code     string `json:"code,omitempty"`
	RouterID int    `json:"router_id,omitempty"`
}

type ApplyRequest struct {
	Code     string `json:"code,omitempty"`
	RouterID int    `json:"router_id,omitempty"`
	Op       string `json:"op"`
	Value    bool   `json:"value"`
}

type AddRouterRequest struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	Firmware   string `json:"firmware"`
	TunnelPort int    `json:"tunnel_port"`
	SSHUser    string `json:"ssh_user"`
	AuthType   string `json:"auth_type"`
	AuthSecret string `json:"auth_secret"`
}

type AddRouterResponse struct {
	Router     RouterInfo `json:"router"`
	AccessCode string     `json:"access_code"`
}

type ActionRequest struct {
	Code     string `json:"code,omitempty"`
	RouterID int    `json:"router_id,omitempty"`
}

// ok, fail, off или skip
type Check struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Hint  string `json:"hint,omitempty"`
}

type HealthResponse struct {
	Router RouterInfo `json:"router"`
	Checks []Check    `json:"checks"`
}

type RouterInfo struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Firmware string `json:"firmware"`
}

type OpState struct {
	Op     string    `json:"op"`
	Value  bool      `json:"value"`
	ReadAt time.Time `json:"read_at"`
	Stale  bool      `json:"stale"`
}

type StateResponse struct {
	Router RouterInfo `json:"router"`
	Ops    []OpState  `json:"ops"`
}

type LogRequest struct {
	Code  string `json:"code"`
	Limit int    `json:"limit,omitempty"`
}

type LogEntry struct {
	TS       string `json:"ts"`
	RouterID int    `json:"router_id"`
	Actor    string `json:"actor"`
	Op       string `json:"op"`
	Value    bool   `json:"value"`
	Result   string `json:"result"`
	Detail   string `json:"detail,omitempty"`
}

type LogResponse struct {
	Entries []LogEntry `json:"entries"`
}

type RoutersResponse struct {
	Routers []RouterInfo `json:"routers"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const (
	// коды ошибок
	ErrBadCode         = "E-01"
	ErrTooManyAttempts = "E-02"
	ErrClientOutdated  = "E-03"
	ErrRouterOffline   = "E-10"
	ErrUnknownFormat   = "E-11"
	ErrApplyRolledBack = "E-12"
	ErrRollbackFailed  = "E-13"
	ErrRouterBusy      = "E-14"
	ErrRebootCooldown  = "E-15"
	ErrServiceFailed   = "E-16"
	ErrRouterExists    = "E-17"
	ErrRouterRefused   = "E-18"
	ErrInternal        = "E-20"
)

var messages = map[string]string{
	ErrBadCode:         "Код не подошёл. Проверьте, что ввели его целиком.",
	ErrTooManyAttempts: "Слишком много попыток, подождите минуту.",
	ErrClientOutdated:  "Версия приложения устарела, скачайте новую.",
	ErrRouterOffline:   "Роутер не отвечает. Проверьте, что он включён и подключён к интернету.",
	ErrUnknownFormat:   "Конфигурация роутера отличается от ожидаемой, ничего не изменено.",
	ErrApplyRolledBack: "Не получилось применить настройку. Всё вернул как было. Администратор уведомлён.",
	ErrRollbackFailed:  "Роутер в неопределённом состоянии. Администратор уже уведомлён, свяжитесь с ним.",
	ErrRouterBusy:      "Роутер сейчас занят, попробуйте через минуту.",
	ErrRebootCooldown:  "Роутер недавно перезагружался, подождите несколько минут.",
	ErrServiceFailed:   "Не удалось переключить VPN. Администратор уведомлён.",
	ErrRouterExists:    "Роутер с таким портом туннеля уже заведён.",
	ErrRouterRefused:   "Не удалось подключиться к роутеру: проверьте порт, пользователя и пароль.",
	ErrInternal:        "Внутренняя ошибка. Администратор уведомлён.",
}

func Message(code string) string {
	if m, ok := messages[code]; ok {
		return m
	}
	return messages[ErrInternal]
}

func NewError(code string) ErrorResponse {
	return ErrorResponse{Code: code, Message: Message(code)}
}
