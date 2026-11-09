# 云梭 Logo v5

在 v4 基础上，两条对向箭头整体上移 55 px。上箭头压住云朵底边，下箭头留在云外；上箭头使用蓝色以保证与白云重叠处的对比。

云朵、向右箭头、向左箭头分别位于 `layers/`，提供统一 1024 × 1024 画布的 SVG 和透明 PNG。`logo.svg` 为完整分层合成，`preview.png` 为静态应用图标预览，`design-sheet.png` 展示分层与小尺寸效果。`generate.py` 使用 CairoSVG 重新导出素材。

Icon Composer 背景渐变：#25B9F2 → #087CEB → #1948BE。独立源图层不含阴影、裁切或玻璃效果；在 Composer 中调整材质。尚未替换应用图标或生成 `.icon` 文件。
