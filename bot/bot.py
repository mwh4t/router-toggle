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
from ui import check_text, confirm_text, find_op, state_text

load_dotenv()

TOKEN = os.environ["BOT_TOKEN"]
API_URL = os.getenv("API_URL", "http://127.0.0.1:8080")
ADMIN_CODE = os.environ["ADMIN_CODE"]
ALLOWED = {int(x) for x in os.environ["ALLOWED_USER_IDS"].split(",") if x.strip()}

api = API(API_URL)
dp = Dispatcher()


class NewRouter(StatesGroup):
    name = State()
    firmware = State()
    port = State()
    user = State()
    password = State()


def allowed(user_id: int | None) -> bool:
    return user_id in ALLOWED


def button(text: str, data: str) -> list[InlineKeyboardButton]:
    return [InlineKeyboardButton(text=text, callback_data=data)]


async def edit(message: Message, text: str, markup: InlineKeyboardMarkup | None = None) -> None:
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
    await state.clear()
    try:
        await message.answer("Роутеры:", reply_markup=await routers_keyboard())
    except APIError as e:
        await message.answer(f"⚠️ {e.message}")


@dp.message(Command("log"))
async def cmd_log(message: Message):
    if not allowed(message.from_user.id):
        return
    try:
        entries = await api.log(ADMIN_CODE, 15)
    except APIError as e:
        await message.answer(f"⚠️ {e.message}")
        return
    if not entries:
        await message.answer("Журнал пуст.")
        return

    lines = []
    for e in reversed(entries):
        mark = "✅" if e["result"] == "ok" else "❌"
        lines.append(
            f"{mark} {e['ts'][11:16]} · id={e['router_id']} · {e['actor']} · "
            f"{e['op']}={'вкл' if e['value'] else 'выкл'}"
        )
    await message.answer("\n".join(lines))


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
    await edit(call.message, text, router_keyboard(rid, None))


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
    await state.update_data(name=message.text.strip())
    await state.set_state(NewRouter.firmware)
    await message.answer("Прошивка?", reply_markup=InlineKeyboardMarkup(inline_keyboard=[[
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
    if not message.text.strip().isdigit():
        await message.answer("Нужно число, например 22010.")
        return
    await state.update_data(port=int(message.text.strip()))
    await state.set_state(NewRouter.user)
    await message.answer("Пользователь SSH? Отправьте «-» для root.")


@dp.message(StateFilter(NewRouter.user), F.text)
async def add_user(message: Message, state: FSMContext):
    if not allowed(message.from_user.id):
        return
    user = message.text.strip()
    await state.update_data(user="root" if user == "-" else user)
    await state.set_state(NewRouter.password)
    await message.answer("Пароль? Сообщение удалю сразу.")


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
    await message.answer("Подключаюсь…")

    try:
        res = await api.add_router(ADMIN_CODE, {
            "name": data["name"],
            "firmware": data["firmware"],
            "tunnel_port": data["port"],
            "ssh_user": data["user"],
            "auth_type": "password",
            "auth_secret": password,
        })
    except APIError as e:
        await message.answer(f"⚠️ {e.message}", reply_markup=await routers_keyboard())
        return

    await message.answer(
        f"✅ <b>{res['router']['name']}</b> заведён\n\n"
        f"Код доступа: <code>{res['access_code']}</code>",
        reply_markup=await routers_keyboard(),
    )


async def main():
    logging.basicConfig(level=logging.INFO)
    bot = Bot(TOKEN, default=DefaultBotProperties(parse_mode="HTML"))
    await dp.start_polling(bot)


if __name__ == "__main__":
    asyncio.run(main())
