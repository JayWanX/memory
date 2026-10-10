#!/usr/bin/env python3
"""Serve docs/ as rendered Markdown without generating HTML files."""

from __future__ import annotations

import html
import mimetypes
import os
import re
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import quote, unquote, urlparse


ROOT = Path(__file__).resolve().parents[1]
PREFIX = "/docs"
HIDDEN_TOP_LEVEL = {"server"}
PORT = int(os.environ.get("MEMORY_DOCS_RENDERER_PORT", "4180"))


CSS = """
:root {
  color-scheme: light;
  --bg: #f6f8fb;
  --ink: #151922;
  --muted: #667085;
  --line: #d9e0ea;
  --panel: #ffffff;
  --accent: #2f7d55;
}
* { box-sizing: border-box; }
body {
  margin: 0;
  background: var(--bg);
  color: var(--ink);
  font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  line-height: 1.65;
}
a { color: #1d5f91; }
.topbar {
  position: sticky;
  top: 0;
  z-index: 2;
  display: flex;
  justify-content: space-between;
  gap: 18px;
  padding: 14px 24px;
  border-bottom: 1px solid var(--line);
  background: rgba(246, 248, 251, 0.96);
}
.brand {
  color: var(--ink);
  font-weight: 800;
  text-decoration: none;
}
nav {
  display: flex;
  flex-wrap: wrap;
  gap: 14px;
  font-size: 14px;
}
.page {
  width: min(980px, calc(100vw - 32px));
  margin: 0 auto;
  padding: 34px 0 72px;
}
h1, h2, h3, h4, h5, h6 {
  line-height: 1.2;
  letter-spacing: 0;
}
h1 {
  margin: 0 0 20px;
  font-size: clamp(32px, 5vw, 56px);
}
h2 {
  margin-top: 36px;
  padding-top: 10px;
  border-top: 1px solid var(--line);
}
p, li { max-width: 82ch; }
pre {
  overflow-x: auto;
  padding: 16px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: #111827;
  color: #f8fafc;
}
code {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
}
p code, li code, td code {
  padding: 2px 5px;
  border: 1px solid var(--line);
  border-radius: 5px;
  background: var(--panel);
}
blockquote {
  margin-left: 0;
  padding: 10px 16px;
  border-left: 4px solid var(--accent);
  color: var(--muted);
  background: var(--panel);
}
table {
  display: block;
  width: 100%;
  overflow-x: auto;
  border-collapse: collapse;
}
th, td {
  padding: 8px 10px;
  border: 1px solid var(--line);
  text-align: left;
  vertical-align: top;
}
th { background: var(--panel); }
img {
  max-width: 100%;
  height: auto;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--panel);
}
.listing {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 10px;
  padding: 0;
  list-style: none;
}
.listing a {
  display: block;
  padding: 12px 14px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--panel);
  text-decoration: none;
}
@media (max-width: 720px) {
  .topbar { align-items: flex-start; flex-direction: column; }
}
""".strip()


def is_hidden(path: Path) -> bool:
    try:
        rel = path.relative_to(ROOT)
    except ValueError:
        return True
    return bool(rel.parts and rel.parts[0] in HIDDEN_TOP_LEVEL)


def resolve_request(path: str) -> Path | None:
    if path == PREFIX:
        path = PREFIX + "/"
    if not path.startswith(PREFIX + "/"):
        return None
    rel = unquote(path[len(PREFIX) + 1 :])
    target = (ROOT / rel).resolve()
    try:
        target.relative_to(ROOT)
    except ValueError:
        return None
    if not target.exists() and target.suffix == "":
        markdown_target = target.with_suffix(".md")
        try:
            markdown_target.relative_to(ROOT)
        except ValueError:
            return None
        if markdown_target.exists():
            target = markdown_target
    if is_hidden(target):
        return None
    return target


def slugify(text: str) -> str:
    slug = re.sub(r"[^\w\u4e00-\u9fff -]+", "", text.lower()).strip()
    slug = re.sub(r"\s+", "-", slug)
    return slug or "section"


def rewrite_link(href: str, source: Path) -> str:
    if re.match(r"^[a-zA-Z][a-zA-Z0-9+.-]*:", href) or href.startswith("#"):
        return html.escape(href, quote=True)
    target, marker, fragment = href.partition("#")
    resolved = (source.parent / unquote(target)).resolve()
    try:
        rel = resolved.relative_to(ROOT)
    except ValueError:
        return "#"

    rewritten = PREFIX + "/" + quote(str(rel))
    return html.escape(rewritten + (marker + fragment if marker else ""), quote=True)


def inline(text: str, source: Path) -> str:
    text = html.escape(text)
    text = re.sub(r"`([^`]+)`", r"<code>\1</code>", text)
    text = re.sub(r"\*\*([^*]+)\*\*", r"<strong>\1</strong>", text)
    text = re.sub(r"__([^_]+)__", r"<strong>\1</strong>", text)
    text = re.sub(r"\*([^*]+)\*", r"<em>\1</em>", text)
    text = re.sub(r"_([^_]+)_", r"<em>\1</em>", text)

    def image(match: re.Match[str]) -> str:
        alt = match.group(1)
        href = rewrite_link(match.group(2), source)
        return f'<img src="{href}" alt="{alt}" loading="lazy">'

    def link(match: re.Match[str]) -> str:
        label = match.group(1)
        href = rewrite_link(match.group(2), source)
        return f'<a href="{href}">{label}</a>'

    text = re.sub(r"!\[([^\]]*)\]\(([^)]+)\)", image, text)
    text = re.sub(r"\[([^\]]+)\]\(([^)]+)\)", link, text)
    return text


def is_table_separator(line: str) -> bool:
    cells = [cell.strip() for cell in line.strip().strip("|").split("|")]
    return bool(cells) and all(re.fullmatch(r":?-{3,}:?", cell or "") for cell in cells)


def render_table(rows: list[str], source: Path) -> str:
    parsed = [[cell.strip() for cell in row.strip().strip("|").split("|")] for row in rows]
    head = parsed[0]
    body = parsed[2:] if len(parsed) > 2 and is_table_separator(rows[1]) else parsed[1:]
    out = ["<table>", "<thead><tr>"]
    out.extend(f"<th>{inline(cell, source)}</th>" for cell in head)
    out.append("</tr></thead>")
    if body:
        out.append("<tbody>")
        for row in body:
            out.append("<tr>")
            out.extend(f"<td>{inline(cell, source)}</td>" for cell in row)
            out.append("</tr>")
        out.append("</tbody>")
    out.append("</table>")
    return "\n".join(out)


def render_markdown(text: str, source: Path) -> tuple[str, str]:
    lines = text.splitlines()
    html_lines: list[str] = []
    title = source.stem
    in_code = False
    paragraph: list[str] = []
    list_type: str | None = None
    blockquote: list[str] = []
    table_rows: list[str] = []

    def flush_paragraph() -> None:
        nonlocal paragraph
        if paragraph:
            html_lines.append(f"<p>{inline(' '.join(paragraph), source)}</p>")
            paragraph = []

    def flush_list() -> None:
        nonlocal list_type
        if list_type:
            html_lines.append(f"</{list_type}>")
            list_type = None

    def flush_blockquote() -> None:
        nonlocal blockquote
        if blockquote:
            html_lines.append(f"<blockquote>{inline(' '.join(blockquote), source)}</blockquote>")
            blockquote = []

    def flush_table() -> None:
        nonlocal table_rows
        if table_rows:
            html_lines.append(render_table(table_rows, source))
            table_rows = []

    for raw in lines:
        line = raw.rstrip()
        fence = re.match(r"^```(\w+)?\s*$", line)
        if fence:
            flush_paragraph()
            flush_list()
            flush_blockquote()
            flush_table()
            if in_code:
                html_lines.append("</code></pre>")
                in_code = False
            else:
                lang = fence.group(1) or ""
                cls = f' class="language-{html.escape(lang)}"' if lang else ""
                html_lines.append(f"<pre><code{cls}>")
                in_code = True
            continue

        if in_code:
            html_lines.append(html.escape(raw))
            continue

        if not line.strip():
            flush_paragraph()
            flush_list()
            flush_blockquote()
            flush_table()
            continue

        if "|" in line and line.strip().startswith("|"):
            flush_paragraph()
            flush_list()
            flush_blockquote()
            table_rows.append(line)
            continue
        flush_table()

        heading = re.match(r"^(#{1,6})\s+(.+)$", line)
        if heading:
            flush_paragraph()
            flush_list()
            flush_blockquote()
            level = len(heading.group(1))
            body = heading.group(2).strip()
            if title == source.stem and level == 1:
                title = re.sub(r"`", "", body)
            anchor = slugify(body)
            html_lines.append(f'<h{level} id="{anchor}">{inline(body, source)}</h{level}>')
            continue

        quote = re.match(r"^>\s?(.*)$", line)
        if quote:
            flush_paragraph()
            flush_list()
            blockquote.append(quote.group(1))
            continue

        item = re.match(r"^[-*]\s+(.+)$", line)
        ordered = re.match(r"^\d+\.\s+(.+)$", line)
        if item or ordered:
            flush_paragraph()
            flush_blockquote()
            wanted = "ol" if ordered else "ul"
            if list_type != wanted:
                flush_list()
                html_lines.append(f"<{wanted}>")
                list_type = wanted
            html_lines.append(f"<li>{inline((ordered or item).group(1), source)}</li>")
            continue

        flush_list()
        flush_blockquote()
        paragraph.append(line.strip())

    flush_paragraph()
    flush_list()
    flush_blockquote()
    flush_table()
    if in_code:
        html_lines.append("</code></pre>")
    return title, "\n".join(html_lines)


def page(title: str, body: str) -> bytes:
    document = f"""<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{html.escape(title)} - Memory Docs</title>
  <style>{CSS}</style>
</head>
<body>
  <header class="topbar">
    <a class="brand" href="/">Memory Docs</a>
    <nav>
      <a href="{PREFIX}/">Docs Index</a>
    </nav>
  </header>
  <main class="page">
{body}
  </main>
</body>
</html>
"""
    return document.encode("utf-8")


def render_directory(path: Path) -> bytes:
    rel = path.relative_to(ROOT)
    title = "Documentation Files" if rel == Path(".") else str(rel)
    items = []
    if rel != Path("."):
        parent = PREFIX + "/" + quote(str(rel.parent)) + "/"
        items.append(f'<li><a href="{parent}">../</a></li>')
    for child in sorted(path.iterdir(), key=lambda p: (not p.is_dir(), p.name.lower())):
        if is_hidden(child):
            continue
        child_rel = child.relative_to(ROOT)
        suffix = "/" if child.is_dir() else ""
        label = html.escape(child.name + suffix)
        href = PREFIX + "/" + quote(str(child_rel)) + suffix
        items.append(f'<li><a href="{href}">{label}</a></li>')
    body = f"<h1>{html.escape(title)}</h1>\n<ul class=\"listing\">{''.join(items)}</ul>"
    return page(title, body)


class Handler(BaseHTTPRequestHandler):
    server_version = "MemoryDocsRenderer/1.0"

    def do_HEAD(self) -> None:
        self.handle_request(send_body=False)

    def do_GET(self) -> None:
        self.handle_request(send_body=True)

    def handle_request(self, send_body: bool) -> None:
        parsed = urlparse(self.path)
        target = resolve_request(parsed.path)
        if target is None or not target.exists():
            self.send_error(HTTPStatus.NOT_FOUND)
            return

        if target.is_dir():
            body = render_directory(target)
            self.write_response(body, "text/html; charset=utf-8", send_body)
            return

        if target.suffix.lower() == ".md":
            title, body_html = render_markdown(target.read_text(encoding="utf-8"), target)
            self.write_response(page(title, body_html), "text/html; charset=utf-8", send_body)
            return

        content_type = mimetypes.guess_type(target.name)[0] or "application/octet-stream"
        data = target.read_bytes()
        self.write_response(data, content_type, send_body)

    def write_response(self, body: bytes, content_type: str, send_body: bool) -> None:
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if send_body:
            self.wfile.write(body)

    def log_message(self, fmt: str, *args: object) -> None:
        print(f"{self.address_string()} - {fmt % args}")


def main() -> None:
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    print(f"Serving rendered docs on http://127.0.0.1:{PORT}{PREFIX}/")
    server.serve_forever()


if __name__ == "__main__":
    main()
