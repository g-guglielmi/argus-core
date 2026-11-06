# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi
"""Rebuild figtree-one.woff2: Figtree's digit 1 alone, for theme.css.

Montserrat's 1 reads like a lowercase l ("site1"), so the app draws only that one character
with Figtree (its 1 has a sloping flag and the same height as Montserrat's digits); every other
character stays Montserrat. theme.css loads this file with unicode-range U+0031.

Source: the npm package @fontsource-variable/figtree (SIL Open Font License, OFL-Figtree.txt),
file files/figtree-latin-wght-normal.woff2. Needs fontTools and brotli (pip install fonttools brotli).

    python make-figtree-one.py <figtree-latin-wght-normal.woff2> figtree-one.woff2
"""
import sys

from fontTools import subset
from fontTools.ttLib import TTFont


def main(src: str, out: str) -> None:
    font = TTFont(src)
    opts = subset.Options()
    opts.flavor = 'woff2'
    opts.layout_features = []  # one glyph, nothing to substitute or kern
    opts.name_IDs = ['*']      # keep the copyright and licence names
    opts.notdef_outline = False
    sub = subset.Subsetter(opts)
    sub.populate(unicodes=[0x31])
    sub.subset(font)
    font.flavor = 'woff2'
    font.save(out)
    print(out, 'glyphs:', font.getGlyphOrder(), 'axes:', [a.axisTag for a in font['fvar'].axes] if 'fvar' in font else [])


if __name__ == '__main__':
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    main(sys.argv[1], sys.argv[2])
