import asyncio
import logging
import os

from aiogram import Bot, Dispatcher, F
from aiogram.client.default import DefaultBotProperties
from aiogram.exceptions import TelegramBadRequest
from aiogram.filters import Command, StateFilter
from aiogram.fsm.context import FSMContext
from aiogram.fsm.state import State, StatesGroup
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message
from dotenv import load_dotenv

from api import API, APIError
from ui import check_text, confirm_text, domains_text, entry_title, find_op, match_label, state_text

load_dotenv()

TOKEN = os.environ["BOT_TOKEN"]
API_URL = os.getenv("API_URL", "http://127.0.0.1:8080")
ADMIN_CODE = os.environ["ADMIN_CODE"]
ALLOWED = {int(x) for x in os.environ["ALLOWED_USER_IDS"].split(",") if x.strip()}
PUBLIC_BOT = os.getenv("PUBLIC_BOT", "").lstrip("@")

api = API(API_URL)
dp = Dispatcher()


class DomainForm(StatesGroup):
    query = State()


class RenameForm(StatesGroup):
    name = State()


class NewRouter(StatesGroup):
    name = State()
    display = State()
    firmware = State()
    port = State()
    user = State()
    password = State()


def allowed(user_id: int | None) -> bool:
    return user_id in ALLOWED


def button(text: str, data: str) -> list[InlineKeyboardButton]:
    return [InlineKeyboardButton(text=text, callback_data=data)]


# одна живая панель на чат
panels: dict[int, int] = {}


async def forget_panel(message: Message, keep: int | None = None) -> None:
    old = panels.get(message.chat.id)
    if old and old != keep:
        try:
            await message.bot.delete_message(message.chat.id, old)
        except Exception:
            pass


async def show(message: Message, text: str, reply_markup: InlineKeyboardMarkup | None = None) -> None:
    await forget_panel(message)
    sent = await message.bot.send_message(message.chat.id, text, reply_markup=reply_markup)
    panels[message.chat.id] = sent.message_id


async def drop(message: Message) -> None:
    try:
        await message.delete()
    except Exception:
        pass


async def edit(message: Message, text: str, markup: InlineKeyboardMarkup | None = None) -> None:
    await forget_panel(message, keep=message.message_id)
    panels[message.chat.id] = message.message_id
    try:
        await message.edit_text(text, reply_markup=markup)
    except TelegramBadRequest as e:
        if "message is not modified" not in str(e):
            raise


async def routers_keyboard() -> InlineKeyboardMarkup:
    routers = await api.routers(ADMIN_CODE)
    rows = [button(f"🏠 {r['name']}", f"rt:{r['id']}") for r in routers]
    rows.append(button("➕ Завести роутер", "add"))
    return InlineKeyboardMarkup(inline_keyboard=rows)


def router_keyboard(rid: int, state: dict | None) -> InlineKeyboardMarkup:
    rows = []
    if state:
        udp, vpn = find_op(state, "udp_proxy"), find_op(state, "vpn")
        udp_on = udp.get("value", False)
        rows.append(button(("🎮 Выключить" if udp_on else "🎮 Включить") + " игровые порты",
                           f"ask:udp:{rid}:{int(not udp_on)}"))
        if vpn:
            rows.append(button("🛡 Выключить VPN" if vpn["value"] else "🛡 Включить VPN",
                               f"ask:vpn:{rid}:{int(not vpn['value'])}"))
    rows.append(button("🌐 Сайты через VPN", f"dom:{rid}"))
    rows.append([
        InlineKeyboardButton(text="✏️ Имя", callback_data=f"ren:{rid}"),
        InlineKeyboardButton(text="🔗 Ссылка", callback_data=f"lnk:{rid}"),
    ])
    rows.append([
        InlineKeyboardButton(text="🩺 Проверка", callback_data=f"chk:{rid}"),
        InlineKeyboardButton(text="🔄 Перезагрузка", callback_data=f"ask:rb:{rid}:1"),
    ])
    rows.append([
        InlineKeyboardButton(text="♻️ Обновить", callback_data=f"rt:{rid}"),
        InlineKeyboardButton(text="← Роутеры", callback_data="list"),
    ])
    return InlineKeyboardMarkup(inline_keyboard=rows)


def confirm_keyboard(kind: str, rid: int, value: int) -> InlineKeyboardMarkup:
    return InlineKeyboardMarkup(inline_keyboard=[[
        InlineKeyboardButton(text="✅ Да", callback_data=f"do:{kind}:{rid}:{value}"),
        InlineKeyboardButton(text="✖️ Отмена", callback_data=f"rt:{rid}"),
    ]])


async def show_router(call: CallbackQuery, rid: int, note: str = "") -> None:
    try:
        state = await api.status(ADMIN_CODE, rid)
    except APIError as e:
        await edit(call.message, f"{note}⚠️ {e.message}", router_keyboard(rid, None))
        return
    await edit(call.message, note + state_text(state), router_keyboard(rid, state))


@dp.message(Command("start"))
async def cmd_start(message: Message, state: FSMContext):
    if not allowed(message.from_user.id):
        return
    await drop(message)
    await state.clear()
    try:
        await show(message, "Роутеры:", reply_markup=await routers_keyboard())
    except APIError as e:
        await show(message, f"⚠️ {e.message}")


@dp.message(Command("log"))
async def cmd_log(message: Message):
    if not allowed(message.from_user.id):
        return
    await drop(message)
    try:
        entries = await api.log(ADMIN_CODE, 15)
    except APIError as e:
        await show(message, f"⚠️ {e.message}")
        return
    if not entries:
        await show(message, "Журнал пуст.")
        return

    lines = []
    for e in reversed(entries):
        mark = "✅" if e["result"] == "ok" else "❌"
        lines.append(
            f"{mark} {e['ts'][11:16]} · id={e['router_id']} · {e['actor']} · "
            f"{e['op']}={'вкл' if e['value'] else 'выкл'}"
        )
    await show(message, "\n".join(lines),
               reply_markup=InlineKeyboardMarkup(inline_keyboard=[button("← Роутеры", "list")]))


@dp.callback_query(F.data == "list")
async def cb_list(call: CallbackQuery, state: FSMContext):
    if not allowed(call.from_user.id):
        return
    await state.clear()
    await call.answer()
    await edit(call.message, "Роутеры:", await routers_keyboard())


@dp.callback_query(F.data.startswith("rt:"))
async def cb_router(call: CallbackQuery, state: FSMContext):
    if not allowed(call.from_user.id):
        return
    await state.clear()
    await call.answer()
    await show_router(call, int(call.data.split(":")[1]))


@dp.callback_query(F.data.startswith("chk:"))
async def cb_check(call: CallbackQuery):
    if not allowed(call.from_user.id):
        return
    await call.answer("Проверяю…")
    rid = int(call.data.split(":")[1])
    try:
        text = check_text(await api.check(ADMIN_CODE, rid))
    except APIError as e:
        text = f"⚠️ {e.message}"
    # кнопки переключения зависят от состояния
    try:
        state = await api.status(ADMIN_CODE, rid)
    except APIError:
        state = None
    await edit(call.message, text, router_keyboard(rid, state))


@dp.callback_query(F.data.startswith("ask:"))
async def cb_ask(call: CallbackQuery):
    if not allowed(call.from_user.id):
        return
    await call.answer()
    _, kind, rid, value = call.data.split(":")
    await edit(call.message,
               f"{call.message.html_text}\n\n{confirm_text(kind, int(value))}",
               confirm_keyboard(kind, int(rid), int(value)))


@dp.callback_query(F.data.startswith("do:"))
async def cb_do(call: CallbackQuery):
    if not allowed(call.from_user.id):
        return
    await call.answer("Применяю…")
    _, kind, rid, value = call.data.split(":")
    rid, on = int(rid), value == "1"

    try:
        if kind == "rb":
            await api.reboot(ADMIN_CODE, rid)
            await edit(call.message,
                       "🔄 Роутер перезагружается, обновите через пару минут.",
                       router_keyboard(rid, None))
            return
        await api.apply(ADMIN_CODE, "vpn" if kind == "vpn" else "udp_proxy", on, rid)
    except APIError as e:
        await show_router(call, rid, f"⚠️ {e.message}\n\n")
        return
    await show_router(call, rid, "✅ Готово\n\n")


def domains_keyboard(rid: int, entries: list[dict]) -> InlineKeyboardMarkup:
    rows = [button("➕ Добавить сайт или сервис", f"dadd:{rid}")]
    for i, e in enumerate(entries):
        rows.append(button(f"🗑 {entry_title(e)}", f"drm:{rid}:{i}"))
    rows.append(button("← Назад", f"rt:{rid}"))
    return InlineKeyboardMarkup(inline_keyboard=rows)


async def show_domains(call: CallbackQuery, rid: int, note: str = "") -> None:
    try:
        res = await api.domains(ADMIN_CODE, rid)
    except APIError as e:
        await edit(call.message, f"{note}⚠️ {e.message}",
                   InlineKeyboardMarkup(inline_keyboard=[button("← Назад", f"rt:{rid}")]))
        return
    await edit(call.message, note + domains_text(res), domains_keyboard(rid, res.get("entries") or []))


@dp.callback_query(F.data.startswith("dom:"))
async def cb_domains(call: CallbackQuery, state: FSMContext):
    if not allowed(call.from_user.id):
        return
    await state.clear()
    await call.answer()
    await show_domains(call, int(call.data.split(":")[1]))


@dp.callback_query(F.data.startswith("drm:"))
async def cb_domain_ask(call: CallbackQuery):
    if not allowed(call.from_user.id):
        return
    await call.answer()
    _, rid, i = call.data.split(":")
    rid, i = int(rid), int(i)
    entries = (await api.domains(ADMIN_CODE, rid)).get("entries") or []
    if i >= len(entries):
        await show_domains(call, rid)
        return
    await edit(call.message,
               f"{call.message.html_text}\n\n🗑 Убрать {entry_title(entries[i])} из VPN?",
               InlineKeyboardMarkup(inline_keyboard=[[
                   InlineKeyboardButton(text="✅ Да", callback_data=f"drmy:{rid}:{i}"),
                   InlineKeyboardButton(text="✖️ Отмена", callback_data=f"dom:{rid}"),
               ]]))


@dp.callback_query(F.data.startswith("drmy:"))
async def cb_domain_remove(call: CallbackQuery):
    if not allowed(call.from_user.id):
        return
    await call.answer("Применяю…")
    _, rid, i = call.data.split(":")
    rid, i = int(rid), int(i)
    entries = (await api.domains(ADMIN_CODE, rid)).get("entries") or []
    if i >= len(entries):
        await show_domains(call, rid)
        return
    e = entries[i]
    try:
        await api.remove_domain(ADMIN_CODE, e["kind"], e["name"], rid)
    except APIError as err:
        await show_domains(call, rid, f"⚠️ {err.message}\n\n")
        return
    await show_domains(call, rid, "✅ Готово\n\n")


@dp.callback_query(F.data.startswith("dadd:"))
async def cb_domain_add(call: CallbackQuery, state: FSMContext):
    if not allowed(call.from_user.id):
        return
    await call.answer()
    rid = int(call.data.split(":")[1])
    await state.set_state(DomainForm.query)
    await state.update_data(rid=rid)
    await edit(call.message, "🔎 Напишите название сервиса или адрес сайта",
               InlineKeyboardMarkup(inline_keyboard=[button("✖️ Отмена", f"dom:{rid}")]))


@dp.message(StateFilter(DomainForm.query), F.text)
async def on_domain_query(message: Message, state: FSMContext):
    if not allowed(message.from_user.id):
        return
    await drop(message)
    rid = (await state.get_data())["rid"]
    try:
        res = await api.search_domains(ADMIN_CODE, message.text.strip(), rid)
    except APIError as e:
        await show(message, f"⚠️ {e.message}")
        return

    options, rows = [], []
    for m in res.get("matches") or []:
        rows.append(button(match_label(m), f"dpick:{rid}:{len(options)}"))
        options.append({"kind": "category", "name": m["name"]})
    if res.get("domain"):
        rows.append(button(f"🔗 Только сайт {res['domain']}", f"dpick:{rid}:{len(options)}"))
        options.append({"kind": "domain", "name": res["domain"]})
    if not options:
        await show(message, "🤷 Ничего не нашёл. Попробуйте другое название или адрес сайта, например example.com")
        return

    await state.set_state(None)
    await state.update_data(options=options)
    rows.append(button("✖️ Отмена", f"dom:{rid}"))
    await show(message, "Что добавить?", reply_markup=InlineKeyboardMarkup(inline_keyboard=rows))


@dp.callback_query(F.data.startswith("dpick:"))
async def cb_domain_pick(call: CallbackQuery, state: FSMContext):
    if not allowed(call.from_user.id):
        return
    _, rid, i = call.data.split(":")
    rid, i = int(rid), int(i)
    options = (await state.get_data()).get("options") or []
    await state.clear()
    if i >= len(options):
        await call.answer("Список устарел, поищите заново")
        return
    await call.answer("Применяю…")
    o = options[i]
    try:
        await api.add_domain(ADMIN_CODE, o["kind"], o["name"], rid)
    except APIError as e:
        await show_domains(call, rid, f"⚠️ {e.message}\n\n")
        return
    await show_domains(call, rid, f"✅ {o['name']} идёт через VPN\n\n")


@dp.callback_query(F.data.startswith("lnk:"))
async def cb_link(call: CallbackQuery):
    if not allowed(call.from_user.id):
        return
    await call.answer()
    rid = int(call.data.split(":")[1])
    back = InlineKeyboardMarkup(inline_keyboard=[button("← Назад", f"rt:{rid}")])
    try:
        code = await api.router_code(ADMIN_CODE, rid)
    except APIError as e:
        await edit(call.message, f"⚠️ {e.message}", back)
        return
    text = f"🔑 Код: <code>{code}</code>"
    if PUBLIC_BOT:
        text += f"\n🔗 https://t.me/{PUBLIC_BOT}?start={code}"
    await edit(call.message, text, back)


@dp.callback_query(F.data.startswith("ren:"))
async def cb_rename(call: CallbackQuery, state: FSMContext):
    if not allowed(call.from_user.id):
        return
    await call.answer()
    rid = int(call.data.split(":")[1])
    await state.set_state(RenameForm.name)
    await state.update_data(rid=rid)
    await edit(call.message, "✏️ Как этот роутер будут видеть клиенты? Отправьте «-», чтобы убрать имя.",
               InlineKeyboardMarkup(inline_keyboard=[button("✖️ Отмена", f"rt:{rid}")]))


@dp.message(StateFilter(RenameForm.name), F.text)
async def on_rename(message: Message, state: FSMContext):
    if not allowed(message.from_user.id):
        return
    await drop(message)
    rid = (await state.get_data())["rid"]
    await state.clear()
    name = message.text.strip()
    try:
        await api.rename_router(ADMIN_CODE, rid, "" if name == "-" else name)
    except APIError as e:
        await show(message, f"⚠️ {e.message}")
        return
    try:
        st = await api.status(ADMIN_CODE, rid)
    except APIError:
        await show(message, "✅ Готово", reply_markup=router_keyboard(rid, None))
        return
    await show(message, "✅ Готово\n\n" + state_text(st), reply_markup=router_keyboard(rid, st))


@dp.callback_query(F.data == "add")
async def cb_add(call: CallbackQuery, state: FSMContext):
    if not allowed(call.from_user.id):
        return
    await call.answer()
    await state.set_state(NewRouter.name)
    await edit(call.message, "➕ Название роутера?")


@dp.message(StateFilter(NewRouter.name), F.text)
async def add_name(message: Message, state: FSMContext):
    if not allowed(message.from_user.id):
        return
    await drop(message)
    await state.update_data(name=message.text.strip())
    await state.set_state(NewRouter.display)
    await show(message, "Название для клиента (Дом, Дача…)? Отправьте «-», чтобы пропустить.")


@dp.message(StateFilter(NewRouter.display), F.text)
async def add_display(message: Message, state: FSMContext):
    if not allowed(message.from_user.id):
        return
    await drop(message)
    display = message.text.strip()
    await state.update_data(display="" if display == "-" else display)
    await state.set_state(NewRouter.firmware)
    await show(message, "Прошивка?", reply_markup=InlineKeyboardMarkup(inline_keyboard=[[
        InlineKeyboardButton(text="keenetic", callback_data="fw:keenetic"),
        InlineKeyboardButton(text="openwrt", callback_data="fw:openwrt"),
    ]]))


@dp.callback_query(StateFilter(NewRouter.firmware), F.data.startswith("fw:"))
async def add_firmware(call: CallbackQuery, state: FSMContext):
    if not allowed(call.from_user.id):
        return
    await call.answer()
    await state.update_data(firmware=call.data.split(":")[1])
    await state.set_state(NewRouter.port)
    await edit(call.message, "Порт туннеля на VPS?")


@dp.message(StateFilter(NewRouter.port), F.text)
async def add_port(message: Message, state: FSMContext):
    if not allowed(message.from_user.id):
        return
    await drop(message)
    if not message.text.strip().isdigit():
        await show(message, "Нужно число, например 22010.")
        return
    await state.update_data(port=int(message.text.strip()))
    await state.set_state(NewRouter.user)
    await show(message, "Пользователь SSH? Отправьте «-» для root.")


@dp.message(StateFilter(NewRouter.user), F.text)
async def add_user(message: Message, state: FSMContext):
    if not allowed(message.from_user.id):
        return
    await drop(message)
    user = message.text.strip()
    await state.update_data(user="root" if user == "-" else user)
    await state.set_state(NewRouter.password)
    await show(message, "Пароль? Сообщение удалю сразу.")


@dp.message(StateFilter(NewRouter.password), F.text)
async def add_password(message: Message, state: FSMContext, bot: Bot):
    if not allowed(message.from_user.id):
        return
    password = message.text
    # пароль не должен остаться в переписке
    try:
        await bot.delete_message(message.chat.id, message.message_id)
    except Exception:
        pass

    data = await state.get_data()
    await state.clear()
    await show(message, "Подключаюсь…")

    try:
        res = await api.add_router(ADMIN_CODE, {
            "name": data["name"],
            "display_name": data.get("display", ""),
            "firmware": data["firmware"],
            "tunnel_port": data["port"],
            "ssh_user": data["user"],
            "auth_type": "password",
            "auth_secret": password,
        })
    except APIError as e:
        await show(message, f"⚠️ {e.message}", reply_markup=await routers_keyboard())
        return

    await show(message, 
        f"✅ <b>{res['router']['name']}</b> заведён\n\n"
        f"Код доступа: <code>{res['access_code']}</code>",
        reply_markup=await routers_keyboard(),
    )


async def main():
    logging.basicConfig(level=logging.INFO)
    bot = Bot(TOKEN, default=DefaultBotProperties(parse_mode="HTML", link_preview_is_disabled=True))
    await dp.start_polling(bot)


if __name__ == "__main__":
    asyncio.run(main())
