# Recording script for the speech recognition benchmark

Read the phrases **in order** on the watch, each as a separate recording. Read the way you talk in real life, not like a newsreader. The overall idea: check Russian and English, numbers, names, English words inside Russian speech, noise, fast and quiet voice, a long recording and a pause without speech. (The Russian phrases are test data and stay in Russian; the last column translates them.)

**Order matters:** the script `import_recordings.py` matches the downloaded files to the phrases by recording order. If a recording went wrong, record it again and then delete the failed one on the `/pair` page, so that there are no extra files. In total there are 21 recordings, about 15 minutes with the pauses.

The watch records no longer than 60 seconds and can send at most 60 recordings an hour: the script fits this limit.

| № | id | Condition | What to read | English gloss |
|---|---|---|---|---|
| 1 | `ru01` | Quiet room, normal pace | Не забудь купить молоко, хлеб и яйца по дороге домой. | Don't forget to buy milk, bread and eggs on the way home. |
| 2 | `ru02` | Quiet room, as in conversation, with pauses and "um" | Слушай, я немного задержусь на работе, так что ужинайте без меня, я приду позже обычного. | Listen, I'll be a bit late at work, so have dinner without me, I'll be later than usual. |
| 3 | `ru03` | Quiet room, names and cities | Позвони Ивану Петровичу Сидорову и скажи, что договор подпишем в Новосибирске. | Call Ivan Petrovich Sidorov and tell him we'll sign the contract in Novosibirsk. |
| 4 | `ru04` | Quiet room, English words in Russian speech (read the way you say them) | Надо обновить репозиторий на гитхабе, сделать пулл-реквест и написать в слаке команде. | We need to update the repository on GitHub, make a pull request and write to the team in Slack. |
| 5 | `ru05` | Quiet room, numbers | Закажи двенадцать стульев и пять столов. | Order twelve chairs and five tables. |
| 6 | `ru06` | Quiet room, digits one by one | Код доступа четыре семь один ноль девять. | The access code is four seven one zero nine. |
| 7 | `ru07` | Quiet room, fast (as if in a hurry) | Так, значит, завтра с утра созвон с клиентом, потом обед, потом ревью кода и вечером тренировка. | So, tomorrow morning a call with the client, then lunch, then a code review and a workout in the evening. |
| 8 | `ru08` | Quiet room, half-voice | Тихо, ребёнок только что уснул, давай поговорим позже. | Quiet, the child has just fallen asleep, let's talk later. |
| 9 | `ru09` | Outside by a road, traffic noise | Я сейчас иду по улице, плохо слышно, перезвоню через пять минут. | I'm walking down the street now, it's hard to hear, I'll call back in five minutes. |
| 10 | `ru10` | On the move, wind or footsteps | Напомни мне вечером забрать посылку из пункта выдачи. | Remind me in the evening to pick up the parcel from the pickup point. |
| 11 | `ru11` | Running water, an extractor fan or a TV nearby | Нужно записать рецепт: полстакана муки, два яйца и щепотка соли. | I need to write down a recipe: half a glass of flour, two eggs and a pinch of salt. |
| 12 | `ru12` | Quiet room, normal pace | Записаться к терапевту на среду и сдать анализ крови натощак. | Book a therapist for Wednesday and get a fasting blood test. |
| 13 | `en01` | Quiet room, normal pace | Please remind me to call the dentist tomorrow morning. | - |
| 14 | `en02` | Quiet room, numbers | The flight leaves at six forty five from gate twelve. | - |
| 15 | `en03` | Quiet room, names | Tell Sarah Thompson that the meeting with Microsoft moved to Thursday. | - |
| 16 | `en04` | Outside, on the move | I am walking to the station, I will call you back in a few minutes. | - |
| 17 | `en05` | Quiet room, fast | Okay so first we deploy the backend, then we update the app, and finally we send the release notes to everyone. | - |
| 18 | `mix01` | Quiet room: a Russian phrase with an English name | Скинь мне ссылку на документ в Google Docs, я сегодня до вечера отредактирую презентацию. | Send me the link to the document in Google Docs, I'll edit the presentation by this evening. |
| 19 | `long01` | Quiet room, normal pace, without stopping: it should come to 50-60 seconds (if you do not fit in a minute, read a little faster, the watch will cut the recording at 60 s) | Сегодня был длинный день, и я хочу записать несколько мыслей, пока они не забылись. Утром мы обсудили планы на следующий месяц с командой, и стало понятно, что основную работу придётся отдать подрядчикам, потому что своих людей не хватает. Потом я съездил в офис к клиенту, показал ему черновой вариант презентации, и он попросил добавить больше примеров из реальных проектов. После обеда я разбирал почту, ответил на самые срочные письма и договорился о встрече на следующей неделе. Вечером нужно заехать в магазин, купить продукты на выходные и не забыть забрать заказ из пункта выдачи. Ещё надо не забыть позвонить родителям и узнать, как у них дела, а также обсудить, когда мы сможем приехать к ним в гости. Если останется время, хочется пройтись пешком и просто немного подумать о том, что делать дальше. | Summary: a long, ordinary day recounted in one go: planning meetings with the team, a client visit, email, errands, calls to parents. |
| 20 | `sil01` | Quiet room: press record and say nothing for 10 seconds | *(ничего не говорить)* | (say nothing) |
| 21 | `sil02` | Noise without speech: 10 seconds of recording outside or by running water, say nothing | *(ничего не говорить)* | (say nothing) |

Where a phrase contains an English name (`GitHub`, `pull request`, `Slack`, `Google Docs`), pronounce it the way you usually do: in English or in Russian. The scoring accepts both spellings.
