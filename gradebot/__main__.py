"""Run the bot: python -m gradebot [--data data]."""

import argparse
import asyncio
import logging
import sys
from pathlib import Path

from .app import Service
from .config import ConfigError, Env, load_env


def main() -> None:
    parser = argparse.ArgumentParser(prog="gradebot")
    parser.add_argument("--data", type=Path, default=Path("data"), help="directory for the database")
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()
    load_env(Path(".env"))
    logging.basicConfig(level=args.log_level, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
    try:
        env = Env.from_environ()
    except ConfigError as e:
        sys.exit(f"gradebot: {e}")
    try:
        asyncio.run(Service(env, args.data).run())
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
