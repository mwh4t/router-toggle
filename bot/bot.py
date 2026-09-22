import asyncio
import logging
import os

from aiogram import Bot, Dispatcher, F
from aiogram.filters import Command
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


def allowed(user_id: int | None) -> bool:
    return user_id in ALLOWED


def button(text: str, data: str) -> list[InlineKeyboardButton]:
    return [InlineKeyboardButton(text=text, callback_data=data)]


async def routers_keyboard() -> InlineKeyboardMarkup:
    routers = await api.routers(ADMIN_CODE)
    rows = [button(f"{r['name']} ({r['firmware']})", f"rt:{r['id']}") for r in routers]
    return InlineKeyboardMarkup(inline_keyboard=rows)


def router_keyboard(rid: int, state: dict | None) -> InlineKeyboardMarkup:
    rows = []
    if state:
        udp, vpn = find_op(state, "udp_proxy"), find_op(state, "vpn")
        udp_on = udp.get("value", False)
        rows.append(button(("Выключить" if udp_on else "Включить") + " проксирование портов",
                           f"ask:udp:{rid}:{int(not udp_on)}"))
        if vpn:
            rows.append(button("Выключить VPN" if vpn["value"] else "Включить VPN",
                               f"ask:vpn:{rid}:{int(not vpn['value'])}"))
    rows.append(button("Проверить, всё ли в порядке", f"chk:{rid}"))
    rows.append(button("Перезагрузить роутер", f"ask:rb:{rid}:1"))
    rows.append(button("Обновить", f"rt:{rid}"))
    rows.append(button("К списку", "list"))
    return InlineKeyboardMarkup(inline_keyboard=rows)


def confirm_keyboard(kind: str, rid: int, value: int) -> InlineKeyboardMarkup:
    return InlineKeyboardMarkup(inline_keyboard=[
        button("Да", f"do:{kind}:{rid}:{value}"),
        button("Отмена", f"rt:{rid}"),
    ])


async def show_router(call: CallbackQuery, rid: int, note: str = "") -> None:
    try:
        state = await api.status(ADMIN_CODE, rid)
    except APIError as e:
        await call.message.edit_text(f"{note}{e.message}\n\nid={rid}", reply_markup=router_keyboard(rid, None))
        return
    await call.message.edit_text(note + state_text(state), reply_markup=router_keyboard(rid, state))


@dp.message(Command("start"))
async def cmd_start(message: Message):
    if not allowed(message.from_user.id):
        return
    try:
        await message.answer("Роутеры:", reply_markup=await routers_keyboard())
    except APIError as e:
        await message.answer(e.message)


@dp.message(Command("log"))
async def cmd_log(message: Message):
    if not allowed(message.from_user.id):
        return
    try:
        entries = await api.log(ADMIN_CODE, 15)
    except APIError as e:
        await message.answer(e.message)
        return
    if not entries:
        await message.answer("Журнал пуст.")
        return
    lines = [
        f"{e['ts'][:19].replace('T', ' ')}  id={e['router_id']}  {e['actor']}  {e['op']}={e['value']}  {e['result']}"
        for e in reversed(entries)
    ]
    await message.answer("\n".join(lines))


@dp.callback_query(F.data == "list")
async def cb_list(call: CallbackQuery):
    if not allowed(call.from_user.id):
        return
    await call.answer()
    await call.message.edit_text("Роутеры:", reply_markup=await routers_keyboard())


@dp.callback_query(F.data.startswith("rt:"))
async def cb_router(call: CallbackQuery):
    if not allowed(call.from_user.id):
        return
    await call.answer()
    await show_router(call, int(call.data.split(":")[1]))


@dp.callback_query(F.data.startswith("chk:"))
async def cb_check(call: CallbackQuery):
    if not allowed(call.from_user.id):
        return
    await call.answer("Проверяю...")
    rid = int(call.data.split(":")[1])
    try:
        res = await api.check(ADMIN_CODE, rid)
    except APIError as e:
        await call.message.edit_text(e.message, reply_markup=router_keyboard(rid, None))
        return
    await call.message.edit_text(check_text(res), reply_markup=router_keyboard(rid, None))


@dp.callback_query(F.data.startswith("ask:"))
async def cb_ask(call: CallbackQuery):
    if not allowed(call.from_user.id):
        return
    await call.answer()
    _, kind, rid, value = call.data.split(":")
    await call.message.edit_text(
        f"{call.message.text}\n\n{confirm_text(kind, int(value))}",
        reply_markup=confirm_keyboard(kind, int(rid), int(value)),
    )


@dp.callback_query(F.data.startswith("do:"))
async def cb_do(call: CallbackQuery):
    if not allowed(call.from_user.id):
        return
    await call.answer("Применяю...")
    _, kind, rid, value = call.data.split(":")
    rid, on = int(rid), value == "1"

    try:
        if kind == "rb":
            await api.reboot(ADMIN_CODE, rid)
            await call.message.edit_text(
                "Роутер перезагружается. Обновите через пару минут.",
                reply_markup=router_keyboard(rid, None),
            )
            return
        op = "vpn" if kind == "vpn" else "udp_proxy"
        await api.apply(ADMIN_CODE, op, on, rid)
    except APIError as e:
        await show_router(call, rid, f"{e.message}\n\n")
        return
    await show_router(call, rid, "Готово.\n\n")


async def main():
    logging.basicConfig(level=logging.INFO)
    await dp.start_polling(Bot(TOKEN))


if __name__ == "__main__":
    asyncio.run(main())
