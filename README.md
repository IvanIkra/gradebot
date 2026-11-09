# gradebot

A Telegram bot that sends a small group of friends their new grades from the Google Sheets of their courses. Each
user sets their full name as the sheets have it and picks the sheets; the bot reads them every 30 minutes and sends
new and changed grades to a private chat.

Access is by one-time invite codes from the owner. There is no sign-up and no public web part: the bot only talks
to the Bot API by long polling.

> Status: early. The skeleton is done: invite codes, users, owner's commands. Reading sheets and sending grades come
> next.

## Commands

| Who | Command | What it does |
|---|---|---|
| anyone | `/start <code>` | come in with an invite code (the `t.me/<bot>?start=<code>` link does the same) |
| user | `/help` | the commands |
| user | `/stop` | erase everything the bot keeps about you and leave |
| owner | `/invite [note]` | a one-time invite code and its link |
| owner | `/codes` | unused codes, with a button to revoke each |
| owner | `/users` | users, with a button to delete each |

## Messages

Lists and notices are rich messages (`sendRichMessage`, Bot API 10.1+): headings and tables. When the Bot API
server has no rich messages, the bot falls back to classic HTML messages with the same content.

## Running

You need [uv](https://docs.astral.sh/uv/) and a bot token from [@BotFather](https://t.me/BotFather).

```sh
cp .env.example .env    # fill in BOT_TOKEN and OWNER_ID
uv sync
.venv/bin/python -m gradebot --data data
```

With Docker:

```sh
docker compose up -d --build
```

The SQLite database lives in `data/`.

## Privacy

The bot keeps each user's Telegram id and name, language, the full name they set, their sheets and grades. `/stop`
erases all of it; the owner can erase a user too. Invite codes are stored as hashes only.

## Development

```sh
uv sync
uv run pytest -q
uv run ruff check .
```

## License

MIT
