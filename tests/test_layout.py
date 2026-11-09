from gradebot import layout, workbook
from gradebot.llm.client import BadAnswer

from .books import ANNA, IVAN, SHEET_ID
from .books import workbook as book


def quizzes(student=IVAN, **kwargs):
    parsed = workbook.load(book(**kwargs))
    tab = next(t for t in parsed if t.title == "Квизы")
    return tab, [r for r in workbook.find(parsed, student) if r.tab is tab]


async def test_one_layout_for_every_follower(layouts, llm):
    tab, ivan = quizzes()
    _, anna = quizzes(ANNA)
    spec = await layouts.get(SHEET_ID, tab, ivan + anna, 18)
    assert spec["header_rows"] == [1, 2, 3]  # 99 was not shown
    assert (await layouts.get(SHEET_ID, tab, anna, 18)) == spec
    assert len(llm.prompts) == 1


async def test_the_layout_stands_while_the_header_rows_stay(layouts, llm):
    await layouts.get(SHEET_ID, *quizzes(), 18)
    await layouts.get(SHEET_ID, *quizzes(quiz=6), 18)          # the own grade
    await layouts.get(SHEET_ID, *quizzes(classmate=4), 18)     # another student's, in the top rows
    assert len(llm.prompts) == 1
    await layouts.get(SHEET_ID, *quizzes(), 12)                 # another budget: another label
    await layouts.get("2ZyXwVuTsRqPoNmLkJiHgFeDc", *quizzes(), 18)  # another sheet
    assert len(llm.prompts) == 3
    await layouts.get(SHEET_ID, *quizzes(inserted=True), 18)   # the header changed
    assert len(llm.prompts) == 4


async def test_an_answer_the_model_cannot_give_skips_the_tab_until_it_changes(layouts, llm):
    llm.error = BadAnswer("answer was cut at max_tokens")
    spec = await layouts.get(SHEET_ID, *quizzes(), 18)
    assert spec["skip"] and spec["failed"]
    llm.error = None
    assert (await layouts.get(SHEET_ID, *quizzes(), 18))["skip"] and len(llm.prompts) == 1
    assert not (await layouts.get(SHEET_ID, *quizzes(classmate=4), 18))["skip"]  # no header known: any change asks


async def test_forget_drops_the_sheets_layouts_only(layouts, llm, store):
    await layouts.get(SHEET_ID, *quizzes(), 18)
    await layouts.get(SHEET_ID + "_x", *quizzes(), 18)
    assert layouts.forget(SHEET_ID) == 1
    await layouts.get(SHEET_ID, *quizzes(), 18)
    await layouts.get(SHEET_ID + "_x", *quizzes(), 18)
    assert len(llm.prompts) == 3


async def test_a_wide_tab_gets_room_for_its_answer(layouts, llm):
    tab, rows = quizzes()
    await layouts.get(SHEET_ID, tab, rows, 18)
    wide = workbook.Tab("Даты", [], [(1, {workbook.column(i): f"Sep {i}" for i in range(1, 201)})])
    await layouts.get(SHEET_ID, wide, [workbook.Row(wide, 2, "A", {"B": 5})], 18)
    assert llm.max_tokens == [layout.MAX_TOKENS, 200 * layout.TOKENS_PER_WORK]


def test_prompt_spells_out_cyrillic_letter_order():
    assert "в→C" in layout.LAYOUT_SYSTEM and '"HW 1C"' in layout.LAYOUT_SYSTEM


def test_labels_come_in_both_languages():
    assert "label_ru" in layout.LAYOUT_SCHEMA["properties"]["items"]["items"]["required"]
    assert '"ДЗ 1В"' in layout.LAYOUT_SYSTEM
    tab, rows = quizzes()
    spec = {"skip": False, "header_rows": [1, 2, 3], "items": [
        {"column": "E", "label": "Quiz 1", "label_ru": " Квиз  1 ", "max": 10, "bonus_column": None},
        {"column": "G", "label": "Quiz 2", "max": 10, "bonus_column": None}]}   # a layout from before label_ru
    first, second = layout.extract(rows[0], spec)
    assert (first.label, first.label_ru) == ("Quiz 1", "Квиз 1") and second.label_ru == ""
    assert layout.label({"label": "Quiz 1", "label_ru": "Квиз 1"}, "ru") == "Квиз 1"
    assert layout.label({"label": "Quiz 1", "label_ru": "Квиз 1"}, "en") == "Quiz 1"
    assert layout.label({"label": "Quiz 2", "label_ru": None}, "ru") == "Quiz 2"
