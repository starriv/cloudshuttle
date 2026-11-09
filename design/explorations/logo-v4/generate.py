from pathlib import Path
import cairosvg
p=Path(__file__).resolve().parent
cloud='M 329,708 C 245,708 188,653 188,580 C 188,512 236,459 300,451 C 312,366 379,306 461,306 C 542,306 606,355 626,425 C 639,419 654,416 670,416 C 725,416 771,456 780,508 C 820,524 844,559 844,603 C 844,663 796,708 732,708 Z'
right='M 397,690 H 627 M 604,667 L 627,690 L 604,713'
left='M 627,755 H 397 M 420,732 L 397,755 L 420,778'
head='<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">'
shapes={
'01-cloud':f'<path d="{cloud}" transform="translate(0 -65)" fill="#FFFFFF"/>',
'02-transfer-out':f'<path d="{right}" fill="none" stroke="#EAF8FF" stroke-width="22" stroke-linecap="round" stroke-linejoin="round"/>',
'03-transfer-in':f'<path d="{left}" fill="none" stroke="#9DE4FF" stroke-width="22" stroke-linecap="round" stroke-linejoin="round"/>',
}
for name,shape in shapes.items():
    (p/'layers'/f'{name}.svg').write_text(head+shape+'</svg>')
    cairosvg.svg2png(bytestring=(head+shape+'</svg>').encode(),write_to=str(p/'layers'/f'{name}.png'))
defs='''<defs>
<linearGradient id="background" x1="0" y1="0" x2="0.7" y2="1"><stop stop-color="#25B9F2"/><stop offset="0.5" stop-color="#087CEB"/><stop offset="1" stop-color="#1948BE"/></linearGradient>
<linearGradient id="cloud" x1="0" y1="0" x2="0.2" y2="1"><stop stop-color="#FFFFFF"/><stop offset="1" stop-color="#E4F4FF"/></linearGradient>
<linearGradient id="out" x1="0" y1="0" x2="1" y2="0"><stop stop-color="#FFFFFF"/><stop offset="1" stop-color="#EAF8FF"/></linearGradient>
<linearGradient id="in" x1="0" y1="0" x2="1" y2="0"><stop stop-color="#9DE4FF"/><stop offset="1" stop-color="#D5F3FF"/></linearGradient>
<clipPath id="mask"><rect x="80" y="80" width="864" height="864" rx="194"/></clipPath>
</defs>'''
bg='<rect width="1024" height="1024" fill="url(#background)"/>'
cloud_preview=f'<path d="{cloud}" transform="translate(0 -50)" fill="#153D9A" opacity="0.18"/><path d="{cloud}" transform="translate(0 -65)" fill="url(#cloud)"/>'
out=shapes['02-transfer-out'].replace('#EAF8FF','url(#out)')
incoming=shapes['03-transfer-in'].replace('#9DE4FF','url(#in)')
body=bg+'<g id="01-cloud">'+cloud_preview+'</g><g id="02-transfer-out">'+out+'</g><g id="03-transfer-in">'+incoming+'</g>'
master=head+defs+'<title>CloudShuttle — Cloud Shuttle</title>'+body+'</svg>'
(p/'logo.svg').write_text(master)
preview=head+defs+'<g clip-path="url(#mask)">'+body+'</g></svg>'
(p/'preview.svg').write_text(preview)
cairosvg.svg2png(bytestring=preview.encode(),write_to=str(p/'preview.png'),output_width=1024,output_height=1024)
# Monochrome identity without the application tile.
mono=head+shapes['01-cloud'].replace('#FFFFFF','#087CEB')+shapes['02-transfer-out'].replace('#EAF8FF','#087CEB')+shapes['03-transfer-in'].replace('#9DE4FF','#087CEB')+'</svg>'
(p/'mark.svg').write_text(mono)
# Review sheet: same vectors, several sizes, independent source layers.
sheet='<svg xmlns="http://www.w3.org/2000/svg" width="1600" height="1060" viewBox="0 0 1600 1060">'+defs+'<rect width="1600" height="1060" fill="#F3F5F9"/>'
sheet+='<g font-family="Helvetica,Arial,sans-serif" fill="#16263C"><text x="76" y="84" font-size="19" letter-spacing="4">CLOUDSHUTTLE</text><text x="76" y="144" font-size="46" font-weight="600">Cloud Shuttle</text><text x="78" y="188" fill="#607086" font-size="20">A cloud above. A shuttle below.</text></g>'
sheet+='<svg x="32" y="205" width="760" height="760" viewBox="0 0 1024 1024">'+defs+'<g clip-path="url(#mask)">'+body+'</g></svg>'
sheet+='<g font-family="Helvetica,Arial,sans-serif" fill="#607086" font-size="16"><text x="868" y="286" letter-spacing="2">INDEPENDENT VECTOR LAYERS</text></g>'
for i,(name,shape) in enumerate(shapes.items()):
    y=320+i*126
    sheet+=f'<rect x="862" y="{y}" width="124" height="106" rx="18" fill="#DDE7F3"/><svg x="862" y="{y-9}" width="124" height="124" viewBox="0 0 1024 1024">{shape}</svg>'
    desc=['Cloud / pearl white','Outbound / white','Inbound / ice blue'][i]
    sheet+=f'<text x="1012" y="{y+60}" font-family="Helvetica,Arial,sans-serif" font-size="23" fill="#20334D">0{i+1}  {desc}</text>'
sheet+='<text x="868" y="780" font-family="Helvetica,Arial,sans-serif" fill="#607086" font-size="16" letter-spacing="2">SMALL-SIZE PREVIEW</text>'
for x,s in [(850,128),(1025,64),(1140,32)]:
    sheet+=f'<svg x="{x}" y="810" width="{s}" height="{s}" viewBox="0 0 1024 1024">'+defs+'<g clip-path="url(#mask)">'+body+'</g></svg>'
sheet+='</svg>'
(p/'design-sheet.svg').write_text(sheet)
cairosvg.svg2png(bytestring=sheet.encode(),write_to=str(p/'design-sheet.png'))
