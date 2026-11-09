# gradebot

A Telegram bot that sends a small group of friends their new grades from the Google Sheets of their courses. Each
user sets their full name as the sheets have it and picks the sheets; the bot reads them every 30 minutes (the owner may
change it) and sends new and changed grades to a private chat.

Access is by one-time invite codes from the owner. There is no sign-up and no public web part: the bot only talks
to the Bot API by long polling.

> Status: early. Everything below is done and tested, but not yet run against live Telegram: deployment and a
> trial with a few friends come next.

## Commands

| Who | Command | What it does |
|---|---|---|
| anyone | `/start <code>` | come in with an invite code (the `t.me/<bot>?start=<code>` link does the same) |
| user | `/grades` | your grades by sheet, the new ones of the last week marked; "Wrong" reports a bad layout to the owner |
| user | `/name Surname Name` | your full name exactly as the sheets have it; the owner sees it and may reset it |
| user | `/sheet <link>` | follow a Google Sheet (open to anyone with the link) |
| user | `/sheets` | your sheets: when each was read, how many grades, what is wrong; check now or unfollow |
| user | `/quiet [23-8\|off]` | quiet hours: messages then come without a sound; 23:00–8:00 by default |
| user | `/lang [en\|ru]` | the bot's language |
| user | `/help` | the commands |
| user | `/stop` | erase everything the bot keeps about you and leave |
| owner | `/invite [note]` | a one-time invite code and its link |
| owner | `/codes` | unused codes, with a button to revoke each |
| owner | `/users` | users, with a button to delete each |
| owner | `/groupsheet [link [course]]` | the group's sheets: every user follows them, new users too |
| owner | `/course <link> <course>` | a sheet's course, the short name in front of its works (up to 10 characters) |
| owner | `/relayout <link>` | drop a sheet's cached layout: its tabs are laid out again at once |
| owner | `/status` | sheets and their errors, users, Haiku spending today and this month |
| owner | `/interval [minutes]` | how often the sheets are read: 30 minutes by default, 5 to 240 |

A sheet is stored once for everyone who follows it, so it is downloaded and laid out once.

## How grades are read

Every 30 minutes (the owner may change it with `/interval`) each sheet with at least one follower is downloaded as
xlsx and parsed once; every follower's row is looked for by their full name (case, "ё" and punctuation don't
matter, the name matches the start of a cell). A name that matches two rows of one tab is an error: the user makes
it longer.

Claude Haiku lays out each tab once for all followers: which top rows are the header, which columns are graded works,
their maximum points, a bonus column and a short label. The layout is cached while the header rows keep their text.
Grades are put on a 0–10 scale. The first read of a sheet for a user is a snapshot; after it, a new or changed grade
is news. A zero before any real grade is a placeholder, a grade that leaves the sheet and comes back unchanged is no
news, and a column shifted by an insertion is the same work.

A new sheet or a new full name is read at once, and the user is told what was found. A sheet that cannot be read
for 12 hours is told to the owner and its followers once, and once more when it reads again. Haiku spending has a
monthly limit (`LLM_MONTHLY_USD`): beyond it new tabs wait for the next month, cached layouts keep working.

## Messages

Lists and notices are rich messages (`sendRichMessage`, Bot API 10.3+): headings, tables, collapsible sections and
buttons inside the message. When the Bot API server has no rich messages, the bot falls back to classic HTML
messages with the same content and the buttons as an inline keyboard.

New grades come once a poll, in one message for all sheets (more only when it would not fit): a section per sheet
headed by its title and the course's hashtag, a row per work with the grade on 0–10, the bonus, the sheet's own
points when its scale is not 0–10, and the grade before for a changed one. Within the user's quiet hours it comes
without a sound. Grades that could not be sent wait for the next poll for up to a week; a user who blocked the bot
is paused until they write to it again, and the owner is told.

## Running

You need [uv](https://docs.astral.sh/uv/) and a bot token from [@BotFather](https://t.me/BotFather).

```sh
cp .env.example .env    # fill in BOT_TOKEN, OWNER_ID and ANTHROPIC_API_KEY
uv sync
.venv/bin/python -m gradebot --data data
```

With Docker:

```sh
mkdir -p data           # before the first start, or Docker creates it owned by root
docker compose up -d --build
docker compose logs -f
```

The SQLite database lives in `data/`; back it up while the bot is stopped (`docker compose stop`).

## Privacy

The bot keeps each user's Telegram id and name, language, the full name they set, their sheets and grades. `/stop`
erases all of it; the owner can erase a user too. Invite codes are stored as hashes only.

To lay out a tab, Claude Haiku sees the tab's top rows (other students' rows may be among them) and one follower's
row without their name. Sheets are open to anyone with the link anyway; who may use the bot is up to the owner,
who also sees every full name users set.

## Development

```sh
uv sync
uv run pytest -q
uv run ruff check .
```

## License

MIT
