OP_TITLE = "Игровые порты"
OP_HINT = "Steam / FACEIT EU"

MARKS = {"ok": "✅", "fail": "❌", "off": "⏸", "skip": "➖"}


def find_op(state: dict, name: str) -> dict:
    for op in state.get("ops") or []:
        if op["op"] == name:
            return op
    return {}


def lamp(value: bool) -> str:
    return "🟢" if value else "⚪️"


def on_off(value: bool) -> str:
    return "вкл" if value else "выкл"


def vpn_text(value: bool) -> str:
    return "вкл" if value else "выкл до перезагрузки"


def state_text(state: dict) -> str:
    udp, vpn = find_op(state, "udp_proxy"), find_op(state, "vpn")
    lines = [f"🏠 <b>{state['router']['name']}</b>"]
    if state["router"].get("display_name"):
        lines.append(f"👤 для клиента: {state['router']['display_name']}")
    lines.append("")
    if udp.get("stale"):
        lines.append(f"⚠️ роутер не отвечает, данные от {udp['read_at'][11:16]}")
        lines.append("")
    lines.append(f"🎮 {OP_TITLE} · {lamp(udp.get('value', False))} {on_off(udp.get('value', False))}")
    if vpn:
        lines.append(f"🛡 VPN · {lamp(vpn['value'])} {vpn_text(vpn['value'])}")
    return "\n".join(lines)


def check_text(res: dict) -> str:
    lines = [f"🩺 <b>{res['router']['name']}</b>", ""]
    for c in res.get("checks") or []:
        line = f"{MARKS.get(c['state'], '❔')} {c['name']}"
        if c.get("hint"):
            line += f" — {c['hint']}"
        lines.append(line)
    return "\n".join(lines)


def confirm_text(kind: str, value: int) -> str:
    if kind == "rb":
        return "🔄 Перезагрузить роутер? Интернет пропадёт на 1-2 минуты."
    if kind == "vpn":
        return "🛡 Включить VPN?" if value else "🛡 Выключить VPN до перезагрузки роутера?"
    action = "Включить" if value else "Выключить"
    return f"🎮 {action} игровые порты? На пару секунд порвутся соединения."


def entry_title(e: dict) -> str:
    return f"{e['name']} (сервис)" if e["kind"] == "category" else e["name"]


def domains_text(res: dict) -> str:
    lines = [f"🌐 <b>{res['router']['name']}</b> · сайты через VPN", ""]
    entries = res.get("entries") or []
    if not entries:
        lines.append("Своих сайтов пока нет")
    for e in entries:
        lines.append(f"• {entry_title(e)}")
    return "\n".join(lines)


def match_label(m: dict) -> str:
    return f"🧩 {m['name']} · доменов: {m['size']}"
