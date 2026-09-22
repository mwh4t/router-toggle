OP_TITLE = "Проксирование портов Steam / FACEIT EU"

MARKS = {"ok": "✓", "fail": "✗", "off": "—", "skip": "·"}


def find_op(state: dict, name: str) -> dict:
    for op in state.get("ops") or []:
        if op["op"] == name:
            return op
    return {}


def on_off(value: bool) -> str:
    return "включено" if value else "выключено"


def vpn_text(value: bool) -> str:
    return "включён" if value else "выключен до перезагрузки роутера"


def state_text(state: dict) -> str:
    router = state["router"]
    udp, vpn = find_op(state, "udp_proxy"), find_op(state, "vpn")
    lines = [router["name"]]
    if udp.get("stale"):
        lines.append(f"Роутер не отвечает, последнее известное ({udp['read_at'][:16].replace('T', ' ')}):")
    lines.append(f"{OP_TITLE}: {on_off(udp.get('value', False))}")
    if vpn:
        lines.append(f"VPN: {vpn_text(vpn['value'])}")
    return "\n".join(lines)


def check_text(res: dict) -> str:
    lines = [res["router"]["name"], ""]
    for c in res.get("checks") or []:
        line = f"{MARKS.get(c['state'], '?')} {c['name']}"
        if c.get("hint"):
            line += f" — {c['hint']}"
        lines.append(line)
    return "\n".join(lines)


# что спросить перед действием
def confirm_text(kind: str, value: int) -> str:
    if kind == "rb":
        return "Перезагрузить роутер? Интернет пропадёт на 1-2 минуты."
    if kind == "vpn":
        return "Включить VPN?" if value else "Выключить VPN до перезагрузки роутера?"
    action = "Включить" if value else "Выключить"
    return f"{action} проксирование портов? На несколько секунд порвутся соединения."
