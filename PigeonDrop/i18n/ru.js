// Russian texts of the watch app. The keys and the {placeholders} are the same as in en.js (test/i18n.test.mjs checks that).
// Lines are broken by hand to fit the round 480 px display at the size each text is shown in.
export default {
  // Main page (page/index.js)
  'main.settings': 'Настройки',
  'main.list': 'Список записей',
  'main.cancel': 'Отмена',
  'main.idle': 'Клик — записать\nДолгое нажатие —\nвоспроизвести последнюю',
  'main.start_failed': 'Не удалось начать\nзапись',
  'main.sending_wait': 'Отправка...\nВ очереди: {pending}',
  'main.saved_unpaired': 'Записано. Часы не\nпривязаны: Настройки\n→ Привязать часы',
  'main.limit_reached': 'Лимит записи\nдостигнут',
  'main.sent': 'Отправлено',
  'main.cancelled': 'Запись отменена',
  'main.cancel_failed': 'Не удалось отменить:\nзапись сохранена и\nбудет отправлена',
  'main.recording': 'Идёт запись\n{elapsed}\nНажмите ещё раз,\nчтобы остановить',
  'main.connecting': 'Связь с телефоном...\nВ очереди: {pending}',
  'main.sending': 'Отправка {index} из {count}\nчасть {chunk} из {total}',
  'main.queued': 'В очереди: {pending}',
  'main.send_failed': 'Не удалось отправить\n{message}\nВ очереди: {pending}',
  'main.pairing_revoked': 'Привязка отозвана',
  'main.not_paired': 'Часы не привязаны',
  'main.go_pair': 'Настройки →\nПривязать часы',
  'main.no_recordings': 'Нет записей для\nвоспроизведения',
  'main.playing': 'Воспроизведение...',
  'main.play_failed': 'Ошибка чтения\nзаписи',

  // Settings (page/settings.js)
  'settings.pair': 'Привязать часы',
  'settings.check': 'Проверить связь',
  'settings.clear': 'Очистить память',
  'settings.paired': 'Часы привязаны\nк аккаунту',
  'settings.not_paired': 'Часы не привязаны\nк аккаунту',
  'settings.checking': 'Проверяю связь...',
  'settings.check_phone': 'Телефон не отвечает\nПерезапустите\nприложение Zepp',
  'settings.check_unpaired': 'Сервер доступен,\nчасы не привязаны',
  'settings.check_ok': 'Связь в порядке:\nсервер доступен,\nчасы привязаны',
  'settings.check_server_down': 'Сервер недоступен\n({why})',
  'settings.check_revoked': 'Сервер доступен, но\nпривязка часов\nотозвана',
  'settings.check_server_error': 'Ошибка сервера\n({why})',
  'settings.no_answer': 'нет ответа',
  'settings.confirm_clear': 'Удалить все записи: {count}\nНажмите ещё раз',
  'settings.press_again': 'Нажмите ещё раз',
  'settings.busy': 'Идёт отправка.\nПодождите и повторите',
  'settings.cleared': 'Память очищена\nУдалено записей: {removed}',

  // Pairing (page/pair.js)
  'pair.title': 'Привязка часов',
  'pair.connecting': 'Связь с телефоном\nи сервером...',
  'pair.too_many': 'Слишком много попыток.\nПодождите',
  'pair.no_code': 'Не удалось получить\nкод привязки',
  'pair.scan': 'Код: {code}\nОтсканируйте QR\nи подтвердите',
  'pair.done': 'Часы привязаны\nЗаписи отправятся\nавтоматически',
  'pair.expired': 'Код истёк.\nОткройте «Привязать\nчасы» заново',

  // Storage warning (page/storage.js)
  'storage.nearly_full.title': 'Хранилище\nпочти заполнено',
  'storage.nearly_full.text': 'Очистите его на сайте или старые записи начнут удаляться сами',
  'storage.full.title': 'Хранилище\nзаполнено',
  'storage.full.text': 'Старые записи удаляются сами. Чтобы решить, что оставить, очистите хранилище на сайте',

  // List of recordings (page/list.js)
  'list.empty': 'Нет сохранённых\nзаписей',

  // Problems on the way to the server (utils/linkProblem.js)
  'link.phone': 'Нет связи с телефоном\nПерезапустите\nприложение Zepp',
  'link.server': 'Нет связи с сервером\nПроверьте интернет\nна телефоне',
  'link.rate_limited': 'Слишком много загрузок.\nПовторите позже',
}
