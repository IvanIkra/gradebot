from gradebot.access import WRONG_LIMIT, Access, NewUser, code_hash, new_code, normalise

PETR = NewUser(2001, "Пётр", "petr_example", "ru")
ANNA = NewUser(2002, "Анна", None, "ru")


def test_code_shape_and_normalising():
    code = new_code()
    assert len(code) == 7 and code[3] == "-"
    assert not set(code.replace("-", "")) & set("01ILO")
    assert normalise(" k7q f-29 ") == "K7QF29"
    assert code_hash("k7q f29") == code_hash("K7Q-F29")


def test_redeem_once(access: Access, store):
    invite_id, code = access.invite("Петя")
    row = dict(store.db.execute("select * from invites").fetchone())
    assert not any(normalise(code) in normalise(str(v)) for v in row.values())  # only the hash is kept
    invite = access.redeem(code.lower().replace("-", " "), PETR)
    assert invite["id"] == invite_id and invite["note"] == "Петя"
    assert access.user(PETR.tg_id)["invite_id"] == invite_id
    assert access.redeem(code, ANNA) is None
    assert access.user(ANNA.tg_id) is None
    assert access.open_invites() == []


def test_expired_and_revoked_codes(access: Access, clock):
    _, old = access.invite()
    clock.advance(days=7, seconds=1)
    assert access.redeem(old, PETR) is None
    revoked_id, revoked = access.invite()
    assert access.revoke(revoked_id)
    assert not access.revoke(revoked_id)
    assert access.redeem(revoked, PETR) is None
    live_id, _ = access.invite("Аня")
    assert [inv["id"] for inv in access.open_invites()] == [live_id]


def test_wrong_codes_lock_for_an_hour(access: Access, clock):
    for _ in range(WRONG_LIMIT - 1):
        access.redeem("AAAA-AAAA", PETR)
    assert not access.locked(PETR.tg_id)
    access.redeem("AAAA-AAAA", PETR)
    assert access.locked(PETR.tg_id)
    assert not access.locked(ANNA.tg_id)
    clock.advance(hours=1, seconds=1)
    assert not access.locked(PETR.tg_id)


def test_users_and_delete(access: Access):
    _, code = access.invite("Петя")
    access.redeem(code, PETR)
    access.update_profile(PETR.tg_id, "Пётр Петров", None)
    [row] = access.users()
    assert (row["tg_name"], row["username"], row["note"]) == ("Пётр Петров", None, "Петя")
    assert access.delete(PETR.tg_id)
    assert not access.delete(PETR.tg_id)
    assert access.users() == []


def test_long_note_is_cut(access: Access):
    access.invite("x" * 100 + "   y")
    assert len(access.open_invites()[0]["note"]) == 40
