from __future__ import annotations

import os
import sys
from pathlib import Path

import click
from loguru import logger

DEFAULT_NOTES_FILE = Path(".cc-notes/notes.md")

notes_file_option = click.option(
    "--file",
    "notes_file",
    type=click.Path(path_type=Path),
    default=DEFAULT_NOTES_FILE,
    show_default=True,
    help="Notes file to use.",
)


@click.group()
@click.version_option(package_name="cc-notes")
def main() -> None:
    """Notes and tasks layer for agents."""
    logger.remove()
    logger.add(sys.stderr, level="DEBUG" if os.getenv("DEBUG") else "INFO")


@main.command()
@click.argument("text")
@notes_file_option
def add(text: str, notes_file: Path) -> None:
    """Append a note to the notes file."""
    notes_file.parent.mkdir(parents=True, exist_ok=True)
    with notes_file.open("a", encoding="utf-8") as f:
        f.write(f"- {text}\n")
    logger.debug("added note to {}", notes_file)
    click.echo(f"Added note to {notes_file}")


@main.command(name="list")
@notes_file_option
def list_notes(notes_file: Path) -> None:
    """Print every note in the notes file."""
    if not notes_file.exists():
        click.echo("No notes yet.")
        return
    click.echo(notes_file.read_text(encoding="utf-8"), nl=False)
