# 云梭 Logo v6

重新调整整体比例：云朵收紧，双箭头缩小并统一为深蓝色，成组叠放于右下方，穿过云朵底边。浅色轮廓保证箭头离开白云后仍与蓝色背景分离。

`logo.svg` 是完整分层矢量源稿；`layers/` 是云朵和两支箭头的独立 SVG / 透明 PNG。`preview.png` 是静态展示，`design-sheet.png` 包含分层和小尺寸预览。`generate.py` 使用 CairoSVG 重新生成。

Icon Composer 背景设置渐变 #25B9F2 → #087CEB → #1948BE，按编号导入图层。保留箭头浅色轮廓作为设计形状；光照、阴影和系统材质在 Composer 中处理。尚未制作 `.icon` 或替换应用图标。
