import pytest

from gradebot.app import OFFSET, poll
from gradebot.config import ConfigError, Env

from .conftest import OWNER, message


def test_env():
    env = Env.from_environ({"BOT_TOKEN": "SECRET", "OWNER_ID": " 42 ", "INVITE_DAYS": "3", "TZ": "Asia/Yekaterinburg"})
    assert (env.owner_id, env.invite_days, env.tz, env.api_url) == (42, 3, "Asia/Yekaterinburg",
                                                                   "https://api.telegram.org")
    assert "SECRET" not in repr(env)


@pytest.mark.parametrize("environ, error", [
    ({}, "BOT_TOKEN, OWNER_ID"),
    ({"BOT_TOKEN": "t", "OWNER_ID": "me"}, "OWNER_ID must be an integer"),
    ({"BOT_TOKEN": "t", "OWNER_ID": "1", "INVITE_DAYS": "0"}, "INVITE_DAYS"),
    ({"BOT_TOKEN": "t", "OWNER_ID": "1", "TZ": "Mars/Olympus"}, "TZ is not a time zone"),
    ({"BOT_TOKEN": "t", "OWNER_ID": "1", "BOT_API_URL": "ftp://x"}, "BOT_API_URL"),
])
def test_env_errors(environ, error):
    with pytest.raises(ConfigError, match=error):
        Env.from_environ(environ)


async def test_poll_handles_updates_and_keeps_the_offset(api, bot, store):
    first, second = message(OWNER, "/help"), message(OWNER, "/codes")
    first["update_id"], second["update_id"] = 10, 11
    updates = [[first, second]]

    async def get_updates(offset, timeout=50):
        api.calls.append(("getUpdates", {"offset": offset}))
        return updates.pop(0)

    api.get_updates = get_updates
    api.fail["sendRichMessage"] = RuntimeError("boom")   # the first update fails, the second is still handled
    await poll(api, bot, store, once=True)
    assert store.get_json(OFFSET) == 12
    assert [m for m, _ in api.calls].count("sendRichMessage") == 2
