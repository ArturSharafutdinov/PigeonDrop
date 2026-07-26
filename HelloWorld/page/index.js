import { createWidget, widget, align } from '@zos/ui'
import { px } from '@zos/utils'

Page({
  build() {
    createWidget(widget.TEXT, {
      x: px(0),
      y: px(0),
      w: px(480),
      h: px(480),
      text: 'Hello World',
      text_size: px(36),
      color: 0xffffff,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
    })
  },
})
