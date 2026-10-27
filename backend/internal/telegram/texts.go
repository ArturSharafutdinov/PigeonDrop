package telegram

import (
	"fmt"
	"strings"
)

// The bot's answers. Two languages, like the rest of the service: a person who is linked gets their account's
// language, anyone else the one their Telegram app is set to (Russian for ru, English for the rest).

type language string

const (
	langRU language = "ru"
	langEN language = "en"
)

// languageOf maps a language code of Telegram (or of an account) to one of ours.
func languageOf(code string) language {
	if strings.HasPrefix(strings.ToLower(code), "ru") {
		return langRU
	}
	return langEN
}

type phrase int

const (
	phraseLinked phrase = iota
	phraseInvalidLink
	phraseHowTo
	phraseStopped
	phraseNotLinked
	phrasePrivateOnly
	phraseTryAgain
	phraseHelp
	phraseTooOld
)

// say returns the text of a phrase; page is the address of the page where a person signs in to link their chat.
func say(l language, p phrase, page string) string {
	if l == langRU {
		switch p {
		case phraseLinked:
			return "Готово: этот чат привязан к вашему аккаунту PigeonDrop. Новые записи с часов будут приходить сюда голосовым сообщением с расшифровкой. Отключить: /stop."
		case phraseInvalidLink:
			return fmt.Sprintf("Ссылка недействительна или устарела. Откройте %s, нажмите «Привязать Telegram» и перейдите по новой ссылке.", page)
		case phraseHowTo:
			return fmt.Sprintf("Чтобы получать записи с часов в этот чат, откройте %s, войдите и нажмите «Привязать Telegram».", page)
		case phraseStopped:
			return fmt.Sprintf("Отключено: записи сюда больше не приходят. Привязать снова можно на странице %s.", page)
		case phraseNotLinked:
			return fmt.Sprintf("Этот чат не привязан ни к какому аккаунту. Привязать можно на странице %s.", page)
		case phrasePrivateOnly:
			return "Я работаю только в личных сообщениях."
		case phraseTryAgain:
			return "Не получилось, попробуйте ещё раз через минуту."
		case phraseTooOld:
			return "Telegram не позволяет боту удалять сообщения старше 48 часов. Удалите это сообщение сами."
		default:
			return fmt.Sprintf("Я присылаю записи с ваших часов PigeonDrop: голосовое сообщение и его расшифровку.\n\nПривязать чат: откройте %s, войдите и нажмите «Привязать Telegram».\n/stop: отключить отправку в этот чат.", page)
		}
	}
	switch p {
	case phraseLinked:
		return "Done: this chat is linked to your PigeonDrop account. New recordings from your watch will arrive here as a voice message with its transcript. To stop: /stop."
	case phraseInvalidLink:
		return fmt.Sprintf("This link is not valid or has expired. Open %s, press “Link Telegram” and follow the new link.", page)
	case phraseHowTo:
		return fmt.Sprintf("To get your watch recordings in this chat, open %s, sign in and press “Link Telegram”.", page)
	case phraseStopped:
		return fmt.Sprintf("Stopped: recordings will no longer come here. You can link this chat again on %s.", page)
	case phraseNotLinked:
		return fmt.Sprintf("This chat is not linked to any account. You can link it on %s.", page)
	case phrasePrivateOnly:
		return "I only work in private messages."
	case phraseTryAgain:
		return "That did not work, please try again in a minute."
	case phraseTooOld:
		return "Telegram does not let a bot delete messages older than 48 hours. Please delete this message yourself."
	default:
		return fmt.Sprintf("I send you the recordings from your PigeonDrop watch: a voice message and its transcript.\n\nTo link this chat, open %s, sign in and press “Link Telegram”.\n/stop: stop sending recordings to this chat.", page)
	}
}

// seenLabel is the text of the button under a recording that deletes it from the chat.
func seenLabel(l language) string {
	if l == langRU {
		return "Просмотрено"
	}
	return "Seen"
}

// menu is the list of commands Telegram shows in the bot's menu, in each language.
func menu(l language) []Command {
	if l == langRU {
		return []Command{{Command: "stop", Description: "Отключить отправку записей"}, {Command: "help", Description: "Как это работает"}}
	}
	return []Command{{Command: "stop", Description: "Stop sending recordings"}, {Command: "help", Description: "How it works"}}
}
