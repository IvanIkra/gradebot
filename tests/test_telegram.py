import pytest
from aiohttp import web

from gradebot.rich import Doc
from gradebot.telegram import BotApi, TelegramError

from .conftest import FakeApi


@pytest.fixture
async def server(aiohttp_server):
    """A fake Bot API: answers queued per method, every request recorded."""
    state = {"answers": {}, "requests": []}

    async def handler(request: web.Request) -> web.Response:
        method = request.match_info["method"]
        state["requests"].append((method, await request.json()))
        queue = state["answers"].get(method) or [{"ok": True, "result": {"message_id": 7}}]
        answer = queue.pop(0) if len(queue) > 1 else queue[0]
        return web.json_response(answer, status=200 if answer.get("ok") else answer.get("error_code", 400))

    app = web.Application()
    app.router.add_post("/botTOKEN/{method}", handler)
    srv = await aiohttp_server(app)
    state["url"] = str(srv.make_url("")).rstrip("/")
    return state


async def test_call_retries_429_then_raises_errors(server):
    server["answers"]["getMe"] = [{"ok": False, "error_code": 429, "parameters": {"retry_after": 0}},
                                  {"ok": True, "result": {"username": "b"}}]
    server["answers"]["sendMessage"] = [{"ok": False, "error_code": 403, "description": "Forbidden: bot was blocked"}]
    async with BotApi("TOKEN", server["url"]) as api:
        assert await api.call("getMe") == {"username": "b"}
        with pytest.raises(TelegramError) as e:
            await api.send(1, "hi")
    assert e.value.blocked
    assert [m for m, _ in server["requests"]] == ["getMe", "getMe", "sendMessage"]
    assert server["requests"][-1][1] == {"chat_id": 1, "text": "hi", "parse_mode": "HTML",
                                         "link_preview_options": {"is_disabled": True}}


async def test_rich_message_and_silent(server):
    async with BotApi("TOKEN", server["url"]) as api:
        assert await api.send(5, Doc().h("x"), silent=True) == 7
    assert server["requests"] == [("sendRichMessage", {"chat_id": 5, "rich_message": {"html": "<h3>x</h3>"},
                                                       "disable_notification": True})]


async def test_falls_back_to_classic_html_for_good():
    api = FakeApi(rich=False)
    await api.send(5, Doc().h("x").p("y"))
    await api.send(5, Doc().h("z"))
    await api.edit(5, 101, Doc().h("w"))
    assert [m for m, _ in api.calls] == ["sendRichMessage", "sendMessage", "sendMessage", "editMessageText"]
    assert api.calls[1][1]["text"] == "<b>x</b>\n\ny"
    assert api.calls[3][1]["text"] == "<b>w</b>"
    assert not api.rich


async def test_other_rich_errors_are_not_hidden():
    api = FakeApi()
    api.fail["sendRichMessage"] = TelegramError("sendRichMessage", 400, "Bad Request: can't parse")
    with pytest.raises(TelegramError):
        await api.send(5, Doc().h("x"))
    assert api.rich


async def test_unchanged_edit_is_fine():
    api = FakeApi()
    api.fail["editMessageText"] = TelegramError("editMessageText", 400, "Bad Request: message is not modified")
    await api.edit(5, 1, Doc().h("x"))
    assert api.calls[0][1]["rich_message"] == {"html": "<h3>x</h3>"}
