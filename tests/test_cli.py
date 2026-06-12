from __future__ import annotations

from pathlib import Path

from click.testing import CliRunner

from cc_notes.cli import main


def test_help_exits_cleanly() -> None:
    result = CliRunner().invoke(main, ["--help"])
    assert result.exit_code == 0
    assert result.output.startswith("Usage: main")


def test_add_then_list_round_trips() -> None:
    runner = CliRunner()
    with runner.isolated_filesystem():
        result = runner.invoke(main, ["add", "Refactor the auth module"])
        assert result.exit_code == 0
        assert result.output == "Added note to .cc-notes/notes.md\n"
        assert Path(".cc-notes/notes.md").read_text() == "- Refactor the auth module\n"

        result = runner.invoke(main, ["list"])
        assert result.exit_code == 0
        assert result.output == "- Refactor the auth module\n"


def test_add_appends_to_existing_notes() -> None:
    runner = CliRunner()
    with runner.isolated_filesystem():
        runner.invoke(main, ["add", "first"])
        runner.invoke(main, ["add", "second"])
        result = runner.invoke(main, ["list"])
        assert result.exit_code == 0
        assert result.output == "- first\n- second\n"


def test_list_without_notes_file() -> None:
    runner = CliRunner()
    with runner.isolated_filesystem():
        result = runner.invoke(main, ["list"])
        assert result.exit_code == 0
        assert result.output == "No notes yet.\n"
