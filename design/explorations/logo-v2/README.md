# Cloud Exchange / 云梭新标志

以白色云朵为识别主体，用上下相向、略有错位的两条箭头表达文件往返传输。蓝色延续现有图标的识别，浅蓝与靛蓝区分两个方向；无文字、无细线，保持小尺寸识别。

## 文件

- `logo.svg`：1024 × 1024 完整矢量合成，保留命名图层，背景满幅、不裁圆角。
- `preview.svg`、`preview.png`：模拟应用图标外框与静态材质的展示稿。
- `design-sheet.svg`、`design-sheet.png`：完整图标、分层结构、小尺寸对照。
- `mark.svg`：透明背景的双色标志，可独立用于品牌展示。
- `layers/01-cloud.svg`、`02-transfer-out.svg`、`03-transfer-in.svg`：独立源图层，统一 1024 × 1024 画布及坐标；对应 PNG 为透明背景备选。

## Icon Composer 组装

这是一套矢量设计素材，尚未制作或验证 `.icon` 文件，也未替换应用当前的 `resources/icon.png`。

1. 新建面向 macOS 的 1024 × 1024 图标。在 Icon Composer 内设置背景色或渐变：顶部 `#25B9F2`、中部 `#087CEB`、底部 `#1948BE`。背景由 Composer 配置，不导入预览的圆角外框。
2. 按编号顺序导入 `layers/` 内三个 SVG；它们使用同一画布，无需重新对齐。云朵作为一组，两条箭头合为前景组。
3. 云朵保持接近不透明的白色；箭头保留蓝色和靛蓝，优先保证对比。将轻微材质、阴影交给 Composer；源图层没有预先烘焙阴影、模糊或半透明。
4. 检查默认、深色、单色等外观与 16、32、64、128 px 下的可读性，再导出。展示稿只检查了静态尺寸；动态玻璃效果与系统外观仍需在 Composer 中验证。

预览的圆角、渐变和轻微投影仅用于展示，不作为系统外框或真实 Liquid Glass 渲染结果。

## 设计参考

- [Apple Human Interface Guidelines — App icons](https://developer.apple.com/design/human-interface-guidelines/app-icons)
- [Creating your app icon using Icon Composer](https://developer.apple.com/documentation/xcode/creating-your-app-icon-using-icon-composer)

遵循主体清晰、分层组织、源素材可缩放、外框由系统裁切的方向。
