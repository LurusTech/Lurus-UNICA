#!/usr/bin/env python3
"""Check that delivery-handbook source-line citations resolve in this tree.

A source-line citation is one semicolon-separated item inside an HTML comment
of the form ``<!-- 依据: ... -->`` under ``docs/delivery/``. The item names a
repository path and one or more line numbers::

    path/to/file.ext:12
    path/to/file.ext:12-40
    path/to/file.ext:12-40,80
    path/to/file.ext:12（optional note）

A leading label (``收件箱命名 path/file.go:12``) is still a citation: the path
does not resolve until the label is removed. Prose items with no line numbers
(search notes, bare paths, contract clauses) are not source-line citations.
The format example in ``docs/delivery/README.md`` (``路径:行号``) is not one
either.

The check is existence only: the path is a file in the repo, and every cited
line is inside that file. It does not compare the cited text to an older
commit.

Exit status is 0 when every citation resolves, 1 otherwise.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DOCS = ROOT / "docs" / "delivery"

COMMENT_RE = re.compile(r"<!--\s*依据:\s*(.*?)\s*-->", re.DOTALL)
# path has no spaces; optional fullwidth note after the line spec.
CITATION_RE = re.compile(
    r"^(?P<path>[^\s:]+):(?P<spec>\d+(?:-\d+)?(?:,\d+(?:-\d+)?)*)"
    r"(?P<note>（[^）]*）)?$"
)
# Same shape, but with a leading label. The path itself may still be real;
# the written token does not resolve until the label is removed.
PREFIXED_RE = re.compile(
    r"^(?P<label>\S.*?\s)(?P<path>[^\s:]+\.[A-Za-z0-9]+):"
    r"(?P<spec>\d+(?:-\d+)?(?:,\d+(?:-\d+)?)*)(?P<note>（[^）]*）)?$"
)


def line_count(path: Path) -> int | None:
    if not path.is_file():
        return None
    data = path.read_bytes()
    if not data:
        return 0
    return data.count(b"\n") + (0 if data.endswith(b"\n") else 1)


def parse_spec(spec: str) -> list[tuple[int, int]] | None:
    ranges: list[tuple[int, int]] = []
    for piece in spec.split(","):
        if "-" in piece:
            start_s, end_s = piece.split("-", 1)
            start, end = int(start_s), int(end_s)
        else:
            start = end = int(piece)
        if start < 1 or end < start:
            return None
        ranges.append((start, end))
    return ranges


def iter_comments() -> list[tuple[str, int, str]]:
    found: list[tuple[str, int, str]] = []
    for path in sorted(DOCS.rglob("*.md")):
        text = path.read_text(encoding="utf-8")
        rel = path.relative_to(ROOT).as_posix()
        for match in COMMENT_RE.finditer(text):
            line = text[: match.start()].count("\n") + 1
            found.append((rel, line, match.group(1).strip()))
    return found


def main() -> int:
    citations: list[tuple[str, int, str, str, str]] = []
    for rel, line, body in iter_comments():
        for raw in re.split(r"[;；]", body):
            item = raw.strip()
            if not item or item == "路径:行号":
                continue
            match = CITATION_RE.match(item)
            prefixed = None if match else PREFIXED_RE.match(item)
            if match is None and prefixed is None:
                continue
            chosen = match or prefixed
            citations.append(
                (rel, line, item, chosen.group("path"), chosen.group("spec"))
            )

    mismatches: list[str] = []
    for rel, line, item, path, spec in citations:
        ranges = parse_spec(spec)
        target = ROOT / path
        count = line_count(target)
        reasons: list[str] = []
        if not CITATION_RE.match(item):
            reasons.append("token has a leading label, so the written path does not resolve")
        if count is None:
            reasons.append(f"path not found: {path}")
        elif ranges is None:
            reasons.append(f"bad line spec: {spec}")
        else:
            for start, end in ranges:
                if end > count:
                    reasons.append(
                        f"{path}:{start}-{end} exceeds {count} lines"
                        if start != end
                        else f"{path}:{start} exceeds {count} lines"
                    )
        if reasons:
            shown = item if len(item) < 180 else item[:177] + "..."
            mismatches.append(f"{rel}:{line}: {shown} :: {'; '.join(reasons)}")

    print(f"source-line citations: {len(citations)}")
    print(f"mismatches: {len(mismatches)}")
    for row in mismatches:
        print(row)
    return 1 if mismatches else 0


if __name__ == "__main__":
    sys.exit(main())
