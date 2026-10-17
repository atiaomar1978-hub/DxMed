#!/usr/bin/env bash
# Builds docs/<name>.docx and docs/<name>.pdf from the Markdown sources: each Mermaid diagram is rendered to
# PNG with mermaid-cli (installed under ~/med-runtime/tools/mmdc on first use) and sized to fit an A4 page,
# Word is produced by pandoc with docs/reference.docx, PDF by headless Chrome from pandoc HTML + docs/pdf.css.
set -euo pipefail

SRC="$(cd "$(dirname "$0")/.." && pwd)"
DOCS="$SRC/docs"
TOOLS="${MED_RUNTIME:-$HOME/med-runtime}/tools/mmdc"
CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
[ -d /opt/homebrew/opt/node@22/bin ] && PATH="/opt/homebrew/opt/node@22/bin:$PATH"
for t in pandoc python3; do command -v "$t" >/dev/null || { echo "error: $t not found" >&2; exit 1; }; done
[ -x "$CHROME" ] || { echo "error: Chrome not found at $CHROME (set CHROME)" >&2; exit 1; }

if [ ! -x "$TOOLS/node_modules/.bin/mmdc" ]; then
  mkdir -p "$TOOLS"
  ( cd "$TOOLS" && { [ -f package.json ] || npm init -y >/dev/null; } &&
    PUPPETEER_SKIP_DOWNLOAD=1 npm install --no-audit --no-fund @mermaid-js/mermaid-cli@11 >/dev/null )
fi

BUILD="$(mktemp -d)"
trap 'rm -rf "$BUILD"' EXIT
printf '{"executablePath":"%s","args":["--no-sandbox"],"headless":"new"}' "$CHROME" >"$BUILD/puppeteer.json"

for name in DxMed_HLD DxMed_LLD DxMed_Operations_Guide; do
  # 1. extract diagrams to <name>-N.mmd, leaving a placeholder line per diagram
  python3 - "$DOCS/$name.md" "$BUILD/$name" <<'PY'
import re, sys
src, out = sys.argv[1], sys.argv[2]
n = 0
def take(m):
    global n
    n += 1
    open(f"{out}-{n}.mmd", "w", encoding="utf-8").write(m.group(1))
    return f"@@DIAGRAM {n}@@"
text = re.sub(r"^```mermaid\n(.*?)^```$", take, open(src, encoding="utf-8").read(), flags=re.S | re.M)
open(f"{out}.pre.md", "w", encoding="utf-8").write(text)
PY
  # 2. render each diagram
  for mmd in "$BUILD/$name"-*.mmd; do
    [ -e "$mmd" ] || continue
    "$TOOLS/node_modules/.bin/mmdc" -q -p "$BUILD/puppeteer.json" -i "$mmd" -o "${mmd%.mmd}.png" -w 1200 -s 2 -b white
  done
  # 3. replace placeholders with images that fit 16 cm x 22 cm
  python3 - "$BUILD/$name" <<'PY'
import re, struct, sys
out = sys.argv[1]
def size(path):
    with open(path, "rb") as f:
        head = f.read(24)
    return struct.unpack(">II", head[16:24])
def img(m):
    n = m.group(1)
    w, h = size(f"{out}-{n}.png")
    attr = "width=16cm" if h / w <= 22 / 16 else "height=22cm"
    return f"![]({out.rsplit('/', 1)[1]}-{n}.png){{{attr}}}\\"
text = open(f"{out}.pre.md", encoding="utf-8").read()
open(f"{out}.md", "w", encoding="utf-8").write(re.sub(r"@@DIAGRAM (\d+)@@", img, text))
PY
  # 4. Word and PDF
  pandoc "$BUILD/$name.md" --resource-path="$BUILD:$DOCS" --toc --toc-depth=2 \
    --reference-doc="$DOCS/reference.docx" -o "$DOCS/$name.docx"
  pandoc "$BUILD/$name.md" --resource-path="$BUILD:$DOCS" --standalone --embed-resources --toc --toc-depth=2 \
    --css="$DOCS/pdf.css" --metadata lang=en -o "$BUILD/$name.html"
  "$CHROME" --headless=new --disable-gpu --no-pdf-header-footer --print-to-pdf="$DOCS/$name.pdf" \
    "file://$BUILD/$name.html" >/dev/null 2>&1
  echo "docs: $name.docx, $name.pdf"
done
