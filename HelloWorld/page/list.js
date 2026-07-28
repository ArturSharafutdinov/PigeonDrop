import { createWidget, widget, align, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { create, id } from '@zos/media'
import { listRecordings } from '../utils/recordings'

const ITEM_HEIGHT = 90
const ITEM_SPACING = 20
const ITEM_WIDTH = 320
const TOP_MARGIN = 70

Page({
  state: {
    player: null,
  },

  build() {
    const recordings = listRecordings()

    if (recordings.length === 0) {
      createWidget(widget.TEXT, {
        x: px(0),
        y: px(0),
        w: px(480),
        h: px(480),
        text: 'Нет сохранённых\nзаписей',
        text_size: px(30),
        color: 0xffffff,
        align_h: align.CENTER_H,
        align_v: align.CENTER_V,
        text_style: text_style.WRAP,
      })
      return
    }

    const player = create(id.PLAYER)
    this.state.player = player

    player.addEventListener(player.event.PREPARE, (result) => {
      if (result) {
        player.start()
      }
    })

    player.addEventListener(player.event.COMPLETE, () => {
      player.stop()
    })

    recordings.forEach((item, index) => {
      const y = TOP_MARGIN + index * (ITEM_HEIGHT + ITEM_SPACING)

      createWidget(widget.BUTTON, {
        x: px((480 - ITEM_WIDTH) / 2),
        y: px(y),
        w: px(ITEM_WIDTH),
        h: px(ITEM_HEIGHT),
        radius: px(16),
        normal_color: 0x2f2f2f,
        press_color: 0x4a4a4a,
        text: '№' + (index + 1) + '  ' + item.label,
        text_size: px(20),
        click_func: () => {
          player.setSource(player.source.FILE, { file: item.filePath })
          player.prepare()
        },
      })
    })
  },
})
