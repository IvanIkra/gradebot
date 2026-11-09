FROM python:3.12-slim
COPY --from=ghcr.io/astral-sh/uv:0.13.7 /uv /bin/

# The environment lives outside /app, so a bind mount over the app directory cannot hide it.
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 \
    UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never UV_PROJECT_ENVIRONMENT=/opt/venv \
    PATH=/opt/venv/bin:$PATH
WORKDIR /app

# Dependencies first, so code changes don't reinstall them. Exact versions come from uv.lock.
COPY pyproject.toml uv.lock .python-version ./
RUN uv sync --locked --no-dev --no-install-project --no-cache

COPY gradebot gradebot

# Long polling only: no port to expose.
CMD ["python", "-m", "gradebot", "--data", "data"]
