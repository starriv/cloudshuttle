from pathlib import Path
import cairosvg
p=Path(__file__).resolve().parent
cloud='M 345,664 C 271,664 218,615 218,548 C 218,484 263,436 323,429 C 334,355 392,302 464,302 C 535,302 593,347 611,409 C 624,404 637,402 650,402 C 701,402 742,437 752,485 C 791,499 814,532 814,573 C 814,625 772,664 716,664 Z'
right='M 474,615 H 708 M 681,588 L 708,615 L 681,642'
left='M 730,685 H 496 M 523,658 L 496,685 L 523,712'

head='<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">'
shapes={
'01-cloud':f'<path d="{cloud}" fill="#FFFFFF"/>',
'02-transfer-out':f'<path d="{right}" fill="none" stroke="#1745A0" stroke-width="30" stroke-linecap="round" stroke-linejoin="round"/>',
'03-transfer-in':f'<path d="{left}" fill="none" stroke="#1745A0" stroke-width="30" stroke-linecap="round" stroke-linejoin="round"/>',
}
for name in ('02-transfer-out','03-transfer-in'):
    foreground=shapes[name]
    rim=foreground.replace('#1745A0','#F1F9FF').replace('stroke-width="30"','stroke-width="52"')
    shapes[name]='<g transform="translate(615 650) scale(0.82) translate(-615 -650)">'+rim+foreground+'</g>'
for name,shape in shapes.items():
    (p/'layers'/f'{name}.svg').write_text(head+shape+'</svg>')
    cairosvg.svg2png(bytestring=(head+shape+'</svg>').encode(),write_to=str(p/'layers'/f'{name}.png'))
defs='''<defs>
<linearGradient id="background" x1="0" y1="0" x2="0.7" y2="1"><stop stop-color="#25B9F2"/><stop offset="0.5" stop-color="#087CEB"/><stop offset="1" stop-color="#1948BE"/></linearGradient>
<linearGradient id="cloud" x1="0" y1="0" x2="0.2" y2="1"><stop stop-color="#FFFFFF"/><stop offset="1" stop-color="#E4F4FF"/></linearGradient>
<linearGradient id="out" x1="0" y1="0" x2="1" y2="0"><stop stop-color="#169DE9"/><stop offset="1" stop-color="#087CEB"/></linearGradient>
<linearGradient id="in" x1="0" y1="0" x2="1" y2="0"><stop stop-color="#9DE4FF"/><stop offset="1" stop-color="#D5F3FF"/></linearGradient>
<clipPath id="mask"><rect x="80" y="80" width="864" height="864" rx="194"/></clipPath>
</defs>'''
bg='<rect width="1024" height="1024" fill="url(#background)"/>'
cloud_preview=f'<path d="{cloud}" transform="translate(0 10)" fill="#153D9A" opacity="0.18"/><path d="{cloud}" fill="url(#cloud)"/>'
out=shapes['02-transfer-out']
incoming=shapes['03-transfer-in']
body=bg+'<g id="01-cloud">'+cloud_preview+'</g><g id="02-transfer-out">'+out+'</g><g id="03-transfer-in">'+incoming+'</g>'
master=head+defs+'<title>CloudShuttle — Cloud Shuttle</title>'+body+'</svg>'
(p/'logo.svg').write_text(master)
preview=head+defs+'<g clip-path="url(#mask)">'+body+'</g></svg>'
(p/'preview.svg').write_text(preview)
cairosvg.svg2png(bytestring=preview.encode(),write_to=str(p/'preview.png'),output_width=1024,output_height=1024)
# Monochrome identity without the application tile.
mono=head+shapes['01-cloud'].replace('#FFFFFF','#1745A0')+shapes['02-transfer-out'].replace('#F1F9FF','#1745A0').replace('stroke="#1745A0" stroke-width="30"','stroke="#FFFFFF" stroke-width="30"')+shapes['03-transfer-in'].replace('#F1F9FF','#1745A0').replace('stroke="#1745A0" stroke-width="30"','stroke="#FFFFFF" stroke-width="30"')+'</svg>'
(p/'mark.svg').write_text(mono)
# Review sheet: same vectors, several sizes, independent source layers.
sheet='<svg xmlns="http://www.w3.org/2000/svg" width="1600" height="1060" viewBox="0 0 1600 1060">'+defs+'<rect width="1600" height="1060" fill="#F3F5F9"/>'
sheet+='<g font-family="Helvetica,Arial,sans-serif" fill="#16263C"><text x="76" y="84" font-size="19" letter-spacing="4">CLOUDSHUTTLE</text><text x="76" y="144" font-size="46" font-weight="600">Cloud Shuttle</text><text x="78" y="188" fill="#607086" font-size="20">Cloud and transfer, one silhouette.</text></g>'
sheet+='<svg x="32" y="205" width="760" height="760" viewBox="0 0 1024 1024">'+defs+'<g clip-path="url(#mask)">'+body+'</g></svg>'
sheet+='<g font-family="Helvetica,Arial,sans-serif" fill="#607086" font-size="16"><text x="868" y="286" letter-spacing="2">INDEPENDENT VECTOR LAYERS</text></g>'
for i,(name,shape) in enumerate(shapes.items()):
    y=320+i*126
    sheet+=f'<rect x="862" y="{y}" width="124" height="106" rx="18" fill="#DDE7F3"/><svg x="862" y="{y-9}" width="124" height="124" viewBox="0 0 1024 1024">{shape}</svg>'
    desc=['Cloud / pearl white','Outbound / cobalt','Inbound / cobalt'][i]
    sheet+=f'<text x="1012" y="{y+60}" font-family="Helvetica,Arial,sans-serif" font-size="23" fill="#20334D">0{i+1}  {desc}</text>'
sheet+='<text x="868" y="780" font-family="Helvetica,Arial,sans-serif" fill="#607086" font-size="16" letter-spacing="2">SMALL-SIZE PREVIEW</text>'
for x,s in [(850,128),(1025,64),(1140,32)]:
    sheet+=f'<svg x="{x}" y="810" width="{s}" height="{s}" viewBox="0 0 1024 1024">'+defs+'<g clip-path="url(#mask)">'+body+'</g></svg>'
sheet+='</svg>'
(p/'design-sheet.svg').write_text(sheet)
cairosvg.svg2png(bytestring=sheet.encode(),write_to=str(p/'design-sheet.png'))
