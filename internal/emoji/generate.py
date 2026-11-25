"""Regenerate catalog.json from pinned Unicode emoji and English CLDR/gemoji data."""

import hashlib
import json
import re
import urllib.request
import xml.etree.ElementTree as ET
from pathlib import Path

CLDR = "https://raw.githubusercontent.com/unicode-org/cldr/release-49/common/"


def fetch(url, digest):
    with urllib.request.urlopen(url, timeout=30) as response:
        data = response.read()
    if hashlib.sha256(data).hexdigest() != digest:
        raise ValueError(f"Unexpected checksum for {url}")
    return data


names, keywords = {}, {}
for directory, checksum in [
    ("annotations", "14d0e9bb5003c085250df4ac7bd7ba1e26d92e74174d7a3a2f42dc16e07fb155"),
    ("annotationsDerived", "a4a6c1a4c341d94179cb116623c5f44c47400a5bc55da483423ef7430ecf622e"),
]:
    doc = ET.fromstring(fetch(f"{CLDR}{directory}/en.xml", checksum))
    for annotation in doc.findall("./annotations/annotation"):
        target = names if annotation.get("type") == "tts" else keywords
        target[annotation.attrib["cp"]] = annotation.text

# GitHub-style shortcodes are aliases, not Unicode names. Only join aliases
# onto the fully-qualified Unicode rows below; never import custom emoji.
gemoji = json.loads(fetch("https://raw.githubusercontent.com/github/gemoji/v4.1.0/db/emoji.json",
                          "b174ae2aeb321b52f64adb9ff412f966a7f338839d780784dd15dcad702c2dd6"))
aliases = {row["emoji"].replace("\ufe0f", ""): row["aliases"] for row in gemoji}

emoji = fetch("https://www.unicode.org/Public/18.0.0/emoji/emoji-test.txt",
              "8f3735cda1f92a779d78af67cf86066bb1f07143dc22f2ac29394d9bc57ab21a")
rows = []
for line in emoji.decode().splitlines():
    if match := re.match(r"^([0-9A-F ]+); fully-qualified\s+#", line):
        glyph = "".join(chr(int(cp, 16)) for cp in match[1].split())
        key = glyph.replace("\ufe0f", "")  # CLDR omits emoji presentation selectors.
        name = names[key]
        words = set(re.findall(r"[a-z0-9]+", keywords[key].lower()))
        words.difference_update(re.findall(r"[a-z0-9]+", name.lower()))
        row = [glyph, name, " ".join(sorted(words))]
        shortcodes = aliases.get(key, [])
        # The reader derives the same default from the English name.
        if shortcodes and shortcodes != ["_".join(re.findall(r"[a-z0-9]+", name.lower()))]:
            row.append(" ".join(shortcodes))
        rows.append(json.dumps(row, ensure_ascii=False))

Path(__file__).with_name("catalog.json").write_text("[\n" + ",\n".join(rows) + "\n]\n", encoding="utf-8")
