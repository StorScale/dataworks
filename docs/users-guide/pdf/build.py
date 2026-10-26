"""The Users Guide as one PDF: docs/users-guide/*.md, in order, typeset with ReportLab.

    pip install -r docs/users-guide/pdf/requirements.txt
    python docs/users-guide/pdf/build.py <repo> <out.pdf> [<version>]

A cover, the guide's introduction (00-index.md, less its chapter table), a table of
contents with page numbers, then each chapter from a new page. Links between chapters
and to their sections are live, and the PDF's outline lists chapters and sections.
The version on it is <version>, or the repository's VERSION. The fonts are the web
app's, IBM Plex, fetched once from IBM's repository at a fixed commit into FONTS_DIR
(~/.cache/dataworks-guide-fonts). GitLab's pipeline builds it (.gitlab-ci.yml, users-guide).
"""
import datetime
import html
import os
import re
import sys
import urllib.request

from markdown_it import MarkdownIt
from reportlab import rl_config
from reportlab.lib import colors
from reportlab.lib.enums import TA_LEFT
from reportlab.lib.pagesizes import letter
from reportlab.lib.styles import ParagraphStyle
from reportlab.lib.units import inch
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from reportlab.platypus import (
    BaseDocTemplate,
    CondPageBreak,
    Frame,
    ListFlowable,
    ListItem,
    NextPageTemplate,
    PageBreak,
    PageTemplate,
    Paragraph,
    Preformatted,
    Spacer,
    Table,
    TableStyle,
)
from reportlab.platypus.flowables import HRFlowable
from reportlab.platypus.tableofcontents import TableOfContents

REPO, OUT = sys.argv[1], sys.argv[2]
GUIDE = os.path.join(REPO, "docs", "users-guide")
GITHUB = "https://github.com/StorScale/dataworks/blob/main/"
VERSION = sys.argv[3] if len(sys.argv) > 3 else open(os.path.join(REPO, "VERSION")).read().strip()

# The web app's design system (web/src/styles.css): one ink, one accent, IBM Plex.
INK, INK2, MUTED = colors.HexColor("#15191f"), colors.HexColor("#2a323d"), colors.HexColor("#5b6573")
ACCENT, ACCENT_DARK, ACCENT_SOFT = colors.HexColor("#1a56c4"), colors.HexColor("#123f92"), colors.HexColor("#e7effc")
LINE, CODE_BG, HEAD_BG = colors.HexColor("#d9dee6"), colors.HexColor("#f3f5f8"), colors.HexColor("#eef2f8")

# Binary streams (not ASCII85: a quarter smaller), and the same bytes for the same guide (no timestamp or random ID).
rl_config.useA85 = 0
rl_config.invariant = 1

FONTS = os.environ.get("FONTS_DIR") or os.path.expanduser("~/.cache/dataworks-guide-fonts")
PLEX = "https://raw.githubusercontent.com/IBM/plex/71f558992315a12ca1f06a2215d57962a1992162/packages/"
os.makedirs(FONTS, exist_ok=True)
for name, file in [("Plex", "IBMPlexSans-Regular.ttf"), ("Plex-Bold", "IBMPlexSans-Bold.ttf"),
                   ("Plex-Italic", "IBMPlexSans-Italic.ttf"), ("Plex-Semi", "IBMPlexSans-SemiBold.ttf"),
                   ("Mono", "IBMPlexMono-Regular.ttf"), ("Mono-Semi", "IBMPlexMono-SemiBold.ttf")]:
    path = os.path.join(FONTS, file)
    if not os.path.exists(path):
        family = "plex-mono" if "Mono" in file else "plex-sans"
        urllib.request.urlretrieve(f"{PLEX}{family}/fonts/complete/ttf/{file}", path + ".part")
        os.replace(path + ".part", path)
    pdfmetrics.registerFont(TTFont(name, path))
pdfmetrics.registerFontFamily("Plex", normal="Plex", bold="Plex-Bold", italic="Plex-Italic", boldItalic="Plex-Bold")
pdfmetrics.registerFontFamily("Mono", normal="Mono", bold="Mono-Semi", italic="Mono", boldItalic="Mono-Semi")

BODY = ParagraphStyle("body", fontName="Plex", fontSize=9.5, leading=13.5, textColor=INK, spaceAfter=6, alignment=TA_LEFT)
SMALL = ParagraphStyle("small", parent=BODY, fontSize=8, leading=10.5, spaceAfter=0)
CELL_HEAD = ParagraphStyle("cellhead", parent=SMALL, fontName="Plex-Semi", textColor=INK2)
H1 = ParagraphStyle("h1", fontName="Plex-Bold", fontSize=24, leading=29, textColor=INK, spaceAfter=14)
H2 = ParagraphStyle("h2", fontName="Plex-Semi", fontSize=15, leading=19, textColor=ACCENT_DARK, spaceBefore=14, spaceAfter=6)
H3 = ParagraphStyle("h3", fontName="Plex-Semi", fontSize=11.5, leading=15, textColor=INK, spaceBefore=10, spaceAfter=4)
H4 = ParagraphStyle("h4", fontName="Plex-Semi", fontSize=10, leading=13, textColor=INK2, spaceBefore=8, spaceAfter=3)
CODE = ParagraphStyle("code", fontName="Mono", fontSize=7.6, leading=10, textColor=INK)
QUOTE = ParagraphStyle("quote", parent=BODY, textColor=INK2, leftIndent=10)
TOC1 = ParagraphStyle("toc1", fontName="Plex-Semi", fontSize=11, leading=15, spaceBefore=7, textColor=INK)
TOC2 = ParagraphStyle("toc2", fontName="Plex", fontSize=9, leading=12, leftIndent=16, textColor=INK2)

PAGE_W, PAGE_H = letter
MARGIN = 0.85 * inch
TEXT_W = PAGE_W - 2 * MARGIN


def slug(text, seen):
    """GitHub's anchor for a heading: lower case, punctuation out, spaces to dashes; repeats numbered."""
    s = re.sub(r"[^\w\- ]", "", text.lower().strip()).replace(" ", "-")
    n = seen.get(s, 0)
    seen[s] = n + 1
    return s if n == 0 else f"{s}-{n}"


def plain(inline):
    """A heading's text, without markup."""
    return "".join(t.content for t in (inline.children or []) if t.type in ("text", "code_inline"))


def wrap_code(text, width):
    """Long code lines broken at a space where there is one, the rest indented under them."""
    out = []
    for line in text.split("\n"):
        indent = "  " + line[:len(line) - len(line.lstrip())]
        while len(line) > width:
            cut = line.rfind(" ", len(indent) + 8, width)
            if cut <= 0:
                cut = width
            out.append(line[:cut].rstrip())
            line = indent + line[cut:].lstrip()
        out.append(line)
    return "\n".join(out)


class Guide:
    def __init__(self):
        self.md = MarkdownIt("commonmark").enable("table")
        self.files = sorted(f for f in os.listdir(GUIDE) if re.match(r"\d\d-.*\.md$", f))
        # Every heading's anchor, by file, so a link to one is checked before it's made.
        self.anchors = {}
        for f in self.files:
            seen, names = {}, set()
            tokens = self.md.parse(open(os.path.join(GUIDE, f), encoding="utf-8").read())
            for i, t in enumerate(tokens):
                if t.type == "heading_open":
                    names.add(slug(plain(tokens[i + 1]), seen))
            self.anchors[f] = names

    # --- links ---------------------------------------------------------------------
    def href(self, target, current):
        if re.match(r"^[a-z]+:", target):
            return target
        path, _, frag = target.partition("#")
        if not path:
            path = current
        if path in self.anchors:
            if frag and frag in self.anchors[path]:
                return f"#{path}:{frag}"
            return f"#{path}:"
        # Elsewhere in the repository (the lab guide, the handoff): on GitHub.
        full = os.path.normpath(os.path.join("docs", "users-guide", path))
        return GITHUB + full + (f"#{frag}" if frag else "")

    # --- inline markup -------------------------------------------------------------
    def inline(self, tok, current, size=None):
        out = []
        for c in tok.children or []:
            if c.type == "text":
                out.append(html.escape(c.content, quote=False))
            elif c.type == "code_inline":
                out.append(f'<font name="Mono" size="{size or BODY.fontSize - 1}" color="#123f92">{html.escape(c.content, quote=False)}</font>')
            elif c.type == "strong_open":
                out.append("<b>")
            elif c.type == "strong_close":
                out.append("</b>")
            elif c.type == "em_open":
                out.append("<i>")
            elif c.type == "em_close":
                out.append("</i>")
            elif c.type == "link_open":
                out.append(f'<a href="{html.escape(self.href(c.attrs["href"], current))}" color="#1a56c4">')
            elif c.type == "link_close":
                out.append("</a>")
            elif c.type == "softbreak":
                out.append(" ")
            elif c.type == "hardbreak":
                out.append("<br/>")
            elif c.type == "html_inline":
                out.append(html.escape(c.content, quote=False))
            elif c.type == "image":
                out.append(html.escape(c.content, quote=False))
        return "".join(out)

    # --- blocks --------------------------------------------------------------------
    def blocks(self, tokens, current, seen, chapter_title=None, width=TEXT_W, top=False):
        """Flowables for a run of block tokens (a file, a list item, a quote)."""
        out, i = [], 0
        while i < len(tokens):
            t = tokens[i]
            if t.type == "heading_open":
                level = int(t.tag[1])
                inline = tokens[i + 1]
                text = self.inline(inline, current)
                anchor = slug(plain(inline), seen)
                style = {1: H1, 2: H2, 3: H3}.get(level, H4)
                p = Paragraph(f'<a name="{current}:{anchor}"/>{text}', style)
                p._outline = (level, plain(inline), f"{current}:{anchor}")
                if level == 1:
                    p = Paragraph(f'<a name="{current}:"/><a name="{current}:{anchor}"/>{text}', style)
                    p._outline = (1, chapter_title or plain(inline), f"{current}:")
                    out.append(p)
                    out.append(HRFlowable(width="100%", thickness=2, color=ACCENT, spaceBefore=0, spaceAfter=12))
                else:
                    if top:   # a section heading starts no lower than an inch from the page's foot
                        out.append(CondPageBreak(1.1 * inch))
                    out.append(p)
                i += 3
            elif t.type == "paragraph_open":
                out.append(Paragraph(self.inline(tokens[i + 1], current), BODY))
                i += 3
            elif t.type in ("bullet_list_open", "ordered_list_open"):
                j = self.close(tokens, i)
                out.append(self.list(tokens[i:j + 1], current, seen, width))
                out.append(Spacer(1, 3))
                i = j + 1
            elif t.type == "table_open":
                j = self.close(tokens, i)
                out.append(self.table(tokens[i:j + 1], current, width))
                out.append(Spacer(1, 8))
                i = j + 1
            elif t.type in ("fence", "code_block"):
                out.append(self.code(t.content.rstrip("\n"), width))
                out.append(Spacer(1, 7))
                i += 1
            elif t.type == "blockquote_open":
                j = self.close(tokens, i)
                inner = self.blocks(tokens[i + 1:j], current, seen, width=width - 14)
                box = Table([[inner]], colWidths=[width])
                box.setStyle(TableStyle([("LINEBEFORE", (0, 0), (0, 0), 2.5, ACCENT_SOFT), ("LEFTPADDING", (0, 0), (-1, -1), 10),
                                         ("TOPPADDING", (0, 0), (-1, -1), 0), ("BOTTOMPADDING", (0, 0), (-1, -1), 0)]))
                out.append(box)
                i = j + 1
            elif t.type == "hr":
                out.append(HRFlowable(width="100%", thickness=0.6, color=LINE, spaceBefore=6, spaceAfter=10))
                i += 1
            elif t.type == "html_block":
                if t.content.strip():
                    out.append(Paragraph(html.escape(t.content.strip()), BODY))
                i += 1
            else:
                i += 1
        return out

    @staticmethod
    def close(tokens, i):
        """The index of the token closing the block opened at i."""
        depth = 0
        for j in range(i, len(tokens)):
            depth += tokens[j].nesting
            if depth == 0:
                return j
        return len(tokens) - 1

    def list(self, tokens, current, seen, width):
        ordered = tokens[0].type == "ordered_list_open"
        items, i = [], 1
        while i < len(tokens) - 1:
            if tokens[i].type == "list_item_open":
                j = self.close(tokens, i)
                inner = tokens[i + 1:j]
                # A tight list's paragraphs are hidden: still paragraphs, no gap after.
                flows = self.blocks(inner, current, seen, width=width - 18)
                for f in flows:
                    if isinstance(f, Paragraph) and f.style is BODY:
                        f.style = ParagraphStyle("li", parent=BODY, spaceAfter=2.5)
                items.append(ListItem(flows, leftIndent=16))
                i = j + 1
            else:
                i += 1
        if ordered:
            start = int(tokens[0].attrs.get("start", 1)) if tokens[0].attrs else 1
            return ListFlowable(items, bulletType="1", start=start, bulletFontName="Plex-Semi", bulletFontSize=9,
                                bulletColor=ACCENT_DARK, leftIndent=16, bulletDedent=14)
        return ListFlowable(items, bulletType="bullet", start="•", bulletFontName="Plex", bulletFontSize=9,
                            bulletColor=ACCENT, leftIndent=16, bulletDedent=11)

    def table(self, tokens, current, width):
        rows, row, head_rows = [], None, 0
        in_head = False
        for t in tokens:
            if t.type == "thead_open":
                in_head = True
            elif t.type == "thead_close":
                in_head = False
            elif t.type == "tr_open":
                row = []
            elif t.type == "tr_close":
                rows.append(row)
                head_rows += in_head
            elif t.type == "inline":
                row.append((self.inline(t, current, size=SMALL.fontSize - 0.6), plain(t)))
        n = max(len(r) for r in rows)
        rows = [r + [("", "")] * (n - len(r)) for r in rows]
        # Column widths by how much each holds, within bounds, filling the width.
        lengths = [max(min(len(r[c][1]), 90) for r in rows) + 4 for c in range(n)]
        total = sum(lengths)
        widths = [max(width * L / total, 0.45 * inch) for L in lengths]
        scale = width / sum(widths)
        widths = [w * scale for w in widths]
        data = [[Paragraph(cell[0], CELL_HEAD if r < head_rows else SMALL) for cell in row] for r, row in enumerate(rows)]
        tbl = Table(data, colWidths=widths, repeatRows=head_rows, hAlign="LEFT")
        style = [("VALIGN", (0, 0), (-1, -1), "TOP"), ("LINEBELOW", (0, 0), (-1, -1), 0.4, LINE),
                 ("LEFTPADDING", (0, 0), (-1, -1), 4), ("RIGHTPADDING", (0, 0), (-1, -1), 4),
                 ("TOPPADDING", (0, 0), (-1, -1), 3), ("BOTTOMPADDING", (0, 0), (-1, -1), 3)]
        if head_rows:
            style += [("BACKGROUND", (0, 0), (-1, head_rows - 1), HEAD_BG), ("LINEBELOW", (0, head_rows - 1), (-1, head_rows - 1), 0.8, ACCENT)]
        tbl.setStyle(TableStyle(style))
        return tbl

    def code(self, text, width):
        chars = int((width - 14 - 6) / pdfmetrics.stringWidth("m", "Mono", CODE.fontSize))   # the box's padding, and a margin
        pre = Preformatted(wrap_code(text, chars), CODE)
        if text.count("\n") > 55:   # longer than a page: no box (a table cell can't split)
            return pre
        box = Table([[pre]], colWidths=[width])
        box.setStyle(TableStyle([("BACKGROUND", (0, 0), (-1, -1), CODE_BG), ("BOX", (0, 0), (-1, -1), 0.4, LINE),
                                 ("LEFTPADDING", (0, 0), (-1, -1), 7), ("RIGHTPADDING", (0, 0), (-1, -1), 7),
                                 ("TOPPADDING", (0, 0), (-1, -1), 5), ("BOTTOMPADDING", (0, 0), (-1, -1), 5)]))
        return box   # one cell: it moves to the next page whole rather than splitting

    def chapter(self, f):
        src = open(os.path.join(GUIDE, f), encoding="utf-8").read()
        title = re.search(r"^#\s+(.*)$", src, re.M).group(1)
        return self.blocks(self.md.parse(src), f, {}, chapter_title=title, top=True)


class Doc(BaseDocTemplate):
    def __init__(self, path):
        super().__init__(path, pagesize=letter, leftMargin=MARGIN, rightMargin=MARGIN, topMargin=MARGIN, bottomMargin=MARGIN,
                         title="DataWorks Users Guide", author="StorScale", subject=f"DataWorks {VERSION}",
                         creator="docs/users-guide")
        frame = Frame(MARGIN, MARGIN, TEXT_W, PAGE_H - 2 * MARGIN, id="body", leftPadding=0, rightPadding=0, topPadding=0, bottomPadding=0)
        self.addPageTemplates([PageTemplate("cover", [frame], onPage=self.cover),
                               PageTemplate("front", [frame], onPage=self.footer),
                               PageTemplate("body", [frame], onPage=self.footer, onPageEnd=self.header)])
        self.chapter_title, self.chapter_page = "", 0
        self.seen_outline = set()

    def beforeDocument(self):
        # multiBuild lays the story out more than once (for the contents' page numbers): each pass starts afresh.
        self.chapter_title, self.chapter_page, self.seen_outline = "", 0, set()
        self.canv.showOutline()

    def afterFlowable(self, f):
        o = getattr(f, "_outline", None)
        if not o:
            return
        level, text, key = o
        if level == 1:
            self.chapter_title, self.chapter_page = text, self.page
        if level <= 2:
            self.notify("TOCEntry", (level - 1, html.escape(text), self.page, key))
        if level <= 3 and key not in self.seen_outline:
            self.seen_outline.add(key)
            self.canv.bookmarkPage("o-" + key)   # the outline's own destination (the heading's anchor is the text's)
            self.canv.addOutlineEntry(text, "o-" + key, level=level - 1, closed=level == 1)

    def cover(self, canv, doc):
        canv.saveState()
        canv.setFillColor(ACCENT)
        canv.rect(0, PAGE_H - 3.4 * inch, PAGE_W, 3.4 * inch, stroke=0, fill=1)
        canv.setFillColor(colors.white)
        canv.setFont("Plex-Semi", 13)
        canv.drawString(MARGIN, PAGE_H - 1.3 * inch, "DATAWORKS")
        canv.setFont("Plex-Bold", 40)
        canv.drawString(MARGIN, PAGE_H - 2.25 * inch, "Users Guide")
        canv.setFont("Plex", 12.5)
        canv.drawString(MARGIN, PAGE_H - 2.75 * inch, "How to set up and use each of the platform's features")
        canv.setFillColor(INK2)
        canv.setFont("Plex", 11)
        canv.drawString(MARGIN, PAGE_H - 4.2 * inch, f"Release {VERSION}")
        canv.drawString(MARGIN, PAGE_H - 4.5 * inch, datetime.date.today().strftime("%B %-d, %Y"))
        canv.setFillColor(MUTED)
        canv.setFont("Plex", 8.5)
        canv.drawString(MARGIN, MARGIN, "StorScale DataWorks · github.com/StorScale/dataworks · AGPL-3.0-or-later")
        canv.restoreState()

    def footer(self, canv, doc):
        canv.saveState()
        canv.setFont("Plex", 8)
        canv.setFillColor(MUTED)
        canv.drawString(MARGIN, 0.5 * inch, f"DataWorks Users Guide · release {VERSION}")
        canv.drawRightString(PAGE_W - MARGIN, 0.5 * inch, str(doc.page))
        canv.restoreState()

    def header(self, canv, doc):
        # Drawn once the page is filled, so it names the chapter on it; none on a chapter's first page.
        if doc.page == self.chapter_page:
            return
        canv.saveState()
        canv.setFont("Plex", 8)
        canv.setFillColor(MUTED)
        canv.drawString(MARGIN, PAGE_H - 0.55 * inch, self.chapter_title)
        canv.setStrokeColor(LINE)
        canv.setLineWidth(0.5)
        canv.line(MARGIN, PAGE_H - 0.62 * inch, PAGE_W - MARGIN, PAGE_H - 0.62 * inch)
        canv.restoreState()


def main():
    g = Guide()
    story = [NextPageTemplate("front"), PageBreak()]
    # The introduction: 00-index.md, less its chapter table (the contents follow).
    intro = open(os.path.join(GUIDE, "00-index.md"), encoding="utf-8").read()
    intro = re.sub(r"\n## Chapters\n.*?(?=\n## )", "\n", intro, flags=re.S)
    intro = re.sub(r"^# .*\n", "# About this guide\n", intro, count=1)
    g.anchors["00-index.md"] = set()
    story += g.blocks(g.md.parse(intro), "00-index.md", {}, chapter_title="About this guide", top=True)
    story += [PageBreak(), Paragraph("Contents", H1), HRFlowable(width="100%", thickness=2, color=ACCENT, spaceAfter=10)]
    toc = TableOfContents(dotsMinLevel=0)
    toc.levelStyles = [TOC1, TOC2]
    story.append(toc)
    story.append(NextPageTemplate("body"))
    for f in g.files:
        if f == "00-index.md":
            continue
        story.append(PageBreak())
        story += g.chapter(f)
    doc = Doc(OUT)
    doc.multiBuild(story)
    print(OUT, doc.page, "pages")


main()
