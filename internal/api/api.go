// контракт между клиентом и сервером
package api

import "time"

const (
	Version       = "1"
	VersionHeader = "X-Client-Version"
)

// закрытый список операций
const OpUDPProxy = "udp_proxy"

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
