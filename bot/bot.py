import asyncio
import logging
import os

from aiogram import Bot, Dispatcher, F
from aiogram.filters import Command
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message
from dotenv import load_dotenv

from api import API, APIError

load_dotenv()

TOKEN = os.environ["BOT_TOKEN"]
API_URL = os.getenv("API_URL", "http://127.0.0.1:8080")
ADMIN_CODE = os.environ["ADMIN_CODE"]
ALLOWED = {int(x) for x in os.environ["ALLOWED_USER_IDS"].split(",") if x.strip()}

OP_TITLE = "Проксирование портов Steam / FACEIT EU"

api = API(API_URL, ADMIN_CODE)
dp = Dispatcher()


def allowed(user_id: int | None) -> bool:
    return user_id in ALLOWED


def on_off(value: bool) -> str:
    return "включено" if value else "выключено"


async def routers_keyboard() -> InlineKeyboardMarkup:
    routers = await api.routers()
    rows = [
        [InlineKeyboardButton(text=f"{r['name']} ({r['firmware']})", callback_data=f"rt:{r['id']}")]
        for r in routers
    ]
    return InlineKeyboardMarkup(inline_keyboard=rows)


def router_keyboard(router_id: int, value: bool) -> InlineKeyboardMarkup:
    action = "Выключить" if value else "Включить"
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [InlineKeyboardButton(text=action, callback_data=f"ask:{router_id}:{int(not value)}")],
            [InlineKeyboardButton(text="Обновить", callback_data=f"rt:{router_id}")],
            [InlineKeyboardButton(text="К списку", callback_data="list")],
        ]
    )


def confirm_keyboard(router_id: int, value: int) -> InlineKeyboardMarkup:
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [InlineKeyboardButton(text="Да", callback_data=f"do:{router_id}:{value}")],
            [InlineKeyboardButton(text="Отмена", callback_data=f"rt:{router_id}")],
        ]
    )


def status_text(state: dict) -> str:
    router = state["router"]
    op = state["ops"][0]
    lines = [f"{router['name']} ({router['firmware']}, id={router['id']})"]
    if op.get("stale"):
        lines.append(f"Роутер не отвечает. Последнее известное: {on_off(op['value'])}")
        lines.append(f"Прочитано: {op['read_at']}")
    else:
        lines.append(f"{OP_TITLE}: {on_off(op['value'])}")
    return "\n".join(lines)


@dp.message(Command("start"))
async def cmd_start(message: Message):
    if not allowed(message.from_user.id):
        return
    await message.answer("Роутеры:", reply_markup=await routers_keyboard())


@dp.message(Command("log"))
async def cmd_log(message: Message):
    if not allowed(message.from_user.id):
        return
    entries = await api.log(15)
    if not entries:
        await message.answer("Журнал пуст.")
        return

    lines = []
    for e in reversed(entries):
        lines.append(
            f"{e['ts'][:19]}  id={e['router_id']}  {e['actor']}  "
            f"{e['op']}={on_off(e['value'])}  {e['result']}"
        )
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

    router_id = int(call.data.split(":")[1])
    try:
        state = await api.status(router_id)
    except APIError as e:
        await call.message.edit_text(
            f"{e.message}\n\nid={router_id}",
            reply_markup=router_keyboard(router_id, False),
        )
        return

    await call.message.edit_text(
        status_text(state),
        reply_markup=router_keyboard(router_id, state["ops"][0]["value"]),
    )


@dp.callback_query(F.data.startswith("ask:"))
async def cb_ask(call: CallbackQuery):
    if not allowed(call.from_user.id):
        return
    await call.answer()

    _, router_id, value = call.data.split(":")
    action = "включить" if value == "1" else "выключить"
    # перезапуск службы рвёт активные соединения
    await call.message.edit_text(
        f"{call.message.text}\n\nТочно {action}? Это на несколько секунд разорвёт соединения.",
        reply_markup=confirm_keyboard(int(router_id), int(value)),
    )


@dp.callback_query(F.data.startswith("do:"))
async def cb_do(call: CallbackQuery):
    if not allowed(call.from_user.id):
        return
    await call.answer("Применяю...")

    _, router_id, value = call.data.split(":")
    router_id, value = int(router_id), value == "1"

    try:
        state = await api.apply(router_id, value)
    except APIError as e:
        await call.message.edit_text(
            f"{e.message}\n\nid={router_id}",
            reply_markup=router_keyboard(router_id, not value),
        )
        return

    await call.message.edit_text(
        status_text(state),
        reply_markup=router_keyboard(router_id, state["ops"][0]["value"]),
    )


async def main():
    logging.basicConfig(level=logging.INFO)
    bot = Bot(TOKEN)
    await dp.start_polling(bot)


if __name__ == "__main__":
    asyncio.run(main())
