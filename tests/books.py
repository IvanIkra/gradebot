"""Synthetic grade sheets shaped like real ones, with made-up names, and a fake Haiku that lays them out."""

import io

from openpyxl import Workbook

IVAN, ANNA = "Иванов Иван", "Петрова Анна"     # the names the users set: the quizzes tab has them short
SHEET_ID = "1AbCdEfGhIjKlMnOpQrStUvWx"
URL = f"https://docs.google.com/spreadsheets/d/{SHEET_ID}/edit#gid=0"


def workbook(hw: float = 5.9, quiz: float = 8, her_hw: float = 7, her_quiz: float = 9, inserted: bool = False,
             total: str = "Балл", hidden_tab: bool = True, namesake: bool = False, classmate: float = 10) -> bytes:
    wb = Workbook()
    # Max points in row 1, per-problem points, a total and a bonus column.
    ws = wb.active
    ws.title = "ДЗ-1А"
    ws.append([None, "Max за обыкновенные / сложные:", None, 7, 0])
    ws.append(["Группа", "Студент", "Группа*", "1а", "1б", total, "Балл*"])
    ws.append([101, "Образцов Нагляд Примерович", None, 1, 0, 1, 0])
    ws.append([101, "Петрова Анна Олеговна", None, 1, 1, her_hw, 0])
    ws.append([101, "ИВАНОВ Иван Сергеевич", None, 1, 0.7, hw, 1])
    if namesake:
        ws.append([102, "Иванов Иван Сергеевич", None, 0, 0, 2, 0])
    # A two-row header with a grade after each quiz; `inserted` puts a column before them.
    ws = wb.create_sheet("Квизы")
    extra = [None] if inserted else []
    ws.append(["Первый семестр"])
    ws.append([None, *extra, "Средний балл", "Квиз 1", None, "Оценка", "Квиз 2", "Оценка"])
    ws.append(["ФИО", *extra, None, "№1", "№2", None, None, None])
    ws.append(["сидорова мария", *extra, 9, 1, 1, classmate, None, 0])
    ws.append(["Иванов Иван", *extra, 7, 0.6, 1, quiz, None, 0])
    ws.append(["Петрова Анна", *extra, 8, 1, 1, her_quiz, None, 0])
    if hidden_tab:
        ws = wb.create_sheet("old")
        ws.append(["ФИО", "ДЗ"])
        ws.append(["Иванов Иван", 3])
        ws.sheet_state = "hidden"
    buf = io.BytesIO()
    wb.save(buf)
    return buf.getvalue()


def spec(prompt: str) -> dict:
    """The layout Haiku would give, by the tab and where its columns are in the header."""
    tab = prompt.split('"')[1]
    if tab == "ДЗ-1А":
        return {"skip": False, "reason": "", "header_rows": [1, 2], "items": [
            {"column": "F", "label": "HW 1A", "label_ru": "ДЗ 1А", "max": 7, "bonus_column": "G"}]}
    shift = 1 if "C=Средний балл" in prompt else 0
    quiz1, quiz2 = "EF"[shift], "GH"[shift]
    return {"skip": False, "reason": "", "header_rows": [1, 2, 3, 99], "items": [  # 99: not a row it was shown
        {"column": quiz1, "label": "Quiz 1", "label_ru": "Квиз 1", "max": 10, "bonus_column": None},
        {"column": quiz2, "label": "Quiz 2", "label_ru": "Квиз 2", "max": 10, "bonus_column": None}]}


class FakeLLM:
    available = True

    def __init__(self):
        self.prompts: list[str] = []
        self.max_tokens: list[int] = []
        self.error: Exception | None = None

    async def json(self, task, system, prompt, schema, *, max_tokens):
        assert task == "grades" and "{budget}" not in system
        self.prompts.append(prompt)
        self.max_tokens.append(max_tokens)
        if self.error is not None:
            raise self.error
        return spec(prompt)
