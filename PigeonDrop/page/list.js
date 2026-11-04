// Recordings list screen, opened from the "Recordings" button on the
// main page. Shows every saved voice message as its own button, newest
// first, and plays whichever one is tapped.

import { createWidget, widget, align, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { playAudio } from '../utils/player'
import { listRecordings } from '../utils/recordings'
import { t } from '../i18n'

// Layout constants for the list items. The screen is a 480x480 round
// display (radius 240, center at 240,240). A rectangle's top corners sit
// outside the visible circle once they're far enough from center, so
// ITEM_WIDTH and TOP_MARGIN were picked so the first (topmost) button's
// corners stay within a ~230px safe radius instead of being clipped by the
// bezel. If you make items wider, push TOP_MARGIN down to compensate.
const ITEM_HEIGHT = 90
const ITEM_SPACING = 20
const ITEM_WIDTH = 320
const TOP_MARGIN = 70

Page({
  build() {
    const recordings = listRecordings()

    if (recordings.length === 0) {
      createWidget(widget.TEXT, {
        x: px(0),
        y: px(0),
        w: px(480),
        h: px(480),
        text: t('list.empty'),
        text_size: px(30),
        color: 0xffffff,
        align_h: align.CENTER_H,
        align_v: align.CENTER_V,
        text_style: text_style.WRAP,
      })
      return
    }

    // Renders one button per recording, stacked top to bottom. The page
    // scrolls automatically once the buttons no longer fit on screen, so
    // there's no explicit limit on how many can be shown here - the total
    // count is already capped before this list is ever built (at most
    // MAX_RECORDINGS uploaded plus MAX_PENDING_RECORDINGS waiting, both in
    // config.js; see utils/uploadQueue.js#pruneRecordings).
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
        // "№1" is always the newest recording - listRecordings() already
        // sorts newest first, so the index here doubles as the display rank.
        text: '№' + (index + 1) + '  ' + item.label,
        text_size: px(20),
        // The app's one shared Player (utils/player.js): a second one would throw if the
        // main page has played anything before.
        click_func: () => playAudio(item.filePath),
      })
    })
  },
})
