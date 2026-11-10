# gradebot

A Telegram bot that sends a small group of friends their new grades from the Google Sheets of their courses. Each
user sets their full name as the sheets have it and picks the sheets; the bot reads them every 30 minutes (the owner may
change it) and sends new and changed grades to a private chat.

Access is by invite codes from the owner: for one person or several, for a while or for good. There is no sign-up
and no public web part: the bot only talks to the Bot API by long polling.

> Status: early. Everything below is done and tested, but not yet run against live Telegram: deployment and a
> trial with a few friends come next.

## Commands

| Who | Command | What it does |
|---|---|---|
| anyone | `/start <code>` | come in with an invite code (the `t.me/<bot>?start=<code>` link does the same) |
| user | `/grades` | your grades by sheet, the new ones of the last week marked; "Report a mistake" tells the owner of a bad layout |
| user | `/name Surname Name` | your full name exactly as the sheets have it; the owner sees it and may reset it |
| user | `/sheet <link>` | follow a Google Sheet (open to anyone with the link); a link sent alone does the same; a member's sheet the bot does not read yet waits for the owner's approval. The sheet is named by its title in Google until the owner gives it a short name |
| user | `/sheets` | your sheets: when each was read, how many grades, what is wrong; check now or unfollow |
| user | `/group` | your group, with buttons to move to another one: you follow the shared sheets of your group. A group that came with the invite code is changed by the owner only |
| user | `/quiet [23-8\|off]` | quiet hours: messages then come without a sound; 23:00–8:00 by default |
| user | `/lang [en\|ru]` | the bot's language |
| user | `/feedback [text]` | write to the owner: a question, a bug, an idea; the owner's answer comes back in the chat (5 a day) |
| user | `/help` | the commands |
| user | `/stop` | erase everything the bot keeps about you and leave |
| owner | `/invite [group] [x5\|x*] [3d] [+30d\|+31.12] [note]` | an invite code and its link, the words in any order: `x5` lets in up to 5 people and `x*` any number (one by default); `3d` or `12h` is how long the code works (`INVITE_DAYS` by default); `+30d` gives access for 30 days from coming in and `+31.12` until that date (for good by default). With a group the user lands in it, without one they pick it. `/invite help` explains |
| owner | `/codes` | open codes: how many came in, the access they give; a button to revoke each |
| owner | `/users [text]` | users, ten a page, newest first; with text, those whose name, @username, full name, code note or group has it. A user's card trusts them (their new sheets need no approval), moves them to another group, prolongs their access, resets their full name or deletes them |
| owner | `/groups [name [new name]]` | groups: make one, rename one; buttons show a group's sheets or delete it |
| owner | `/groupsheet [group] [link [short name]]` | a group's shared sheets: everyone in the group follows them, newcomers too |
| owner | `/rename <link> [short name]` | a sheet's short name and hashtag (up to 10 characters) for everyone who follows it; without one, its title in Google. Only the owner names sheets: a sheet new to the bot comes with a "Set a short name" button |
| owner | `/relayout <link>` | drop a sheet's cached layout: its tabs are laid out again at once |
| owner | `/status` | sheets and their errors, users, Haiku spending today and this month |
| owner | `/interval [minutes]` | how often the sheets are read: 30 minutes by default, 5 to 240 |

After the invite code the bot asks for the full name at once. A command sent without its arguments, as the command
menu sends it, asks for them one by one: buttons for the usual answers, a button to leave an optional one empty, and
Cancel. A command typed with its arguments runs at once.

A sheet is stored once for everyone who follows it, so it is downloaded and laid out once.

## How grades are read

Every 30 minutes (the owner may change it with `/interval`) each sheet with at least one follower is downloaded as
xlsx and parsed once; every follower's row is looked for by their full name (case, "ё" and punctuation don't
matter, the name matches the start of a cell). A name that matches two rows of one tab is an error: the user makes
it longer.

Claude Haiku lays out each tab once for all followers: which top rows are the header, which columns are graded works,
their maximum points, a bonus column and a short label in English and in Russian (each user sees the one of their
language). The layout is cached while the header rows keep their text.
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
headed by its title and its short name as a hashtag, a row per work with the grade on 0–10, the bonus, the sheet's own
points when its scale is not 0–10, and the grade before for a changed one. Within the user's quiet hours it comes
without a sound. Grades that could not be sent wait for the next poll for up to a week; a user who blocked the bot
is paused until they write to it again, and the owner is told.

## The owner's chat

What needs the owner comes as a message with a sound: a sheet to approve, a request to prolong access, a complaint
about a layout, a `/feedback` message, a sheet failing for 12 hours, the LLM limit. What only tells (a user came in,
set a full name, changed groups, left or blocked the bot) gathers in one silent "News" message, edited as news comes,
until the owner writes to the bot or presses a button; the next news starts a new one below. A button in it answers
in a new message, so the news stays readable.

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

The SQLite database lives in `data/`. The bot copies it once a day after 04:00 local time to
`data/backups/gradebot-<date>.db` and keeps the last 14; copy that folder off the server now and then. To restore,
stop the bot, put a copy in place of `data/gradebot.db` and start it again.

## Privacy

The bot keeps each user's Telegram id and name, language, the full name they set, their sheets and grades. `/stop`
erases all of it, after the access ended too; the owner can erase a user too. Daily copies of the database keep what
was erased for up to 14 days. `/feedback` messages go to the owner's chat and are not stored. Invite codes are stored
as hashes only. When a user's access for a while ends, the bot stops reading their sheets and writing to them, and
keeps their data until the owner prolongs the access or deletes them. The user hears of it a day before and when it
comes, with a button that asks the owner to prolong it; the owner hears of it only then, and sees the rest in
`/status` and `/users`.

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
