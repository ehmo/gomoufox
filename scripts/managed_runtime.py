#!/usr/bin/env python3
"""Resolve managed Python environments for explicit benchmark tooling."""

import os
import pathlib
import stat
import sys


MARKER = ".gomoufox-python-environment"


def default_cache():
    if sys.platform == "darwin":
        return pathlib.Path.home() / "Library/Caches/gomoufox/venv"
    if sys.platform == "win32":
        return pathlib.Path(os.environ["LOCALAPPDATA"]) / "gomoufox/venv"
    return pathlib.Path(os.environ.get("XDG_CACHE_HOME", pathlib.Path.home() / ".cache")) / "gomoufox/venv"


def active_environment(cache):
    cache = pathlib.Path(cache)
    marker = cache / MARKER
    try:
        info = marker.lstat()
    except FileNotFoundError:
        return cache
    if not stat.S_ISREG(info.st_mode):
        raise ValueError("active Python environment marker must be a regular file")
    with marker.open("rb") as source:
        raw = source.read(1025)
    name = raw.decode("utf-8").strip()
    if len(raw) > 1024 or not name.startswith("env-") or any(c in name for c in "/\\\x00"):
        raise ValueError("invalid active Python environment marker")
    parent = cache / "python-envs"
    environment = parent / name
    for path in (parent, environment):
        if not stat.S_ISDIR(path.lstat().st_mode):
            raise ValueError("Python environments must be managed directories")
    return environment


def selected_python(override="", fallback="python3"):
    if override:
        return override
    environment = active_environment(default_cache())
    python = environment / ("Scripts/python.exe" if sys.platform == "win32" else "bin/python")
    if python.is_file() and os.access(python, os.X_OK):
        return str(python)
    return os.environ.get("PYTHON", fallback)


def cache_from_environment(prefix):
    prefix = pathlib.Path(prefix)
    if prefix.name.startswith("env-") and prefix.parent.name == "python-envs":
        cache = prefix.parent.parent
        if active_environment(cache) == cache:
            raise ValueError("managed Python environment has no active marker")
        return cache
    return prefix


if __name__ == "__main__":
    print(selected_python())
