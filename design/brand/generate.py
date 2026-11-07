"""Export the approved vector artwork; requires Python 3, CairoSVG and resvg-py."""
from pathlib import Path
import json
import xml.etree.ElementTree as ET
import cairosvg
import resvg_py

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
ASSETS = HERE / 'AppIcon.icon' / 'Assets'

cairosvg.svg2png(url=str(HERE / 'app-icon.svg'), write_to=str(ROOT / 'resources/icon.png'))
# The color tray icon fills more of its small canvas than the app tile.
cairosvg.svg2png(url=str(HERE / 'app-icon.svg'), write_to=str(ROOT / 'cmd/cloudshuttle/tray-color.png'), output_width=64, output_height=64)

ns = {'s': 'http://www.w3.org/2000/svg'}
cloud = ET.parse(ASSETS / '01-cloud.svg').find('s:path', ns).attrib['d']
arrows = [ET.parse(ASSETS / name).find('s:path', ns).attrib['d'] for name in ('02-transfer-out.svg', '03-transfer-in.svg')]
# The monochrome mark uses true transparent arrow cutouts. It remains a
# macOS template, so the system supplies the light/dark menu bar color.
svg = '<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">'
svg += '<defs><mask id="transfer"><rect width="1024" height="1024" fill="white"/>'
for arrow in arrows:
    svg += f'<path d="{arrow}" fill="none" stroke="black" stroke-width="42" stroke-linecap="round" stroke-linejoin="round"/>'
svg += f'</mask></defs><path d="{cloud}" fill="currentColor" mask="url(#transfer)"/></svg>'
(ROOT / 'resources/mark.svg').write_text(svg + '\n')
# Use a close crop for the 22 pt menu bar glyph; the app tile has wider margins.
tray = svg.replace('viewBox="0 0 1024 1024"', 'viewBox="156 170 720 720"').replace('currentColor', '#000000')
(HERE / 'tray-template.svg').write_text(tray + '\n')
(ROOT / 'cmd/cloudshuttle/tray.png').write_bytes(resvg_py.svg_to_bytes(svg_string=tray, width=44, height=44))

for source in ASSETS.glob('*.svg'):
    cairosvg.svg2png(url=str(source), write_to=str(source.with_suffix('.png')))

# Preserve the approved silhouettes and colors in separate native layers.
# Native composition supplies lighting and shadow; source SVGs have none.
icon = {
    'groups': [
        {'layers': [{'image-name': '01-cloud.png', 'name': '01 Cloud', 'glass': False}], 'shadow': {'kind': 'neutral', 'opacity': 0.14}, 'translucency': {'enabled': False, 'value': 0.5}},
        {'layers': [{'image-name': name.replace('.svg', '.png'), 'name': label, 'glass': False} for name, label in [('02-transfer-out.svg', '02 Outbound'), ('03-transfer-in.svg', '03 Inbound')]], 'shadow': {'kind': 'neutral', 'opacity': 0.0}, 'translucency': {'enabled': False, 'value': 0.5}},
    ],
    'supported-platforms': {'squares': 'shared'},
    'fill-specializations': [{'value': {'linear-gradient': ['srgb:0.14510,0.72549,0.94902,1', 'srgb:0.09804,0.28235,0.74510,1'], 'orientation': {'start': {'x': 0, 'y': 0}, 'stop': {'x': 0.7, 'y': 1}}}}],
}
# Icon Composer stores the frontmost group first.
icon['groups'].reverse()
(HERE / 'AppIcon.icon/icon.json').write_text(json.dumps(icon, indent=2) + '\n')
