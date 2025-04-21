package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const BotToken = "7939631781:AAE5RHWADXsXAaRkljwPiyDSeDtMkSjjcP4"
const YtDlpPath = "/app/yt-dlp" // Railway внутренняя директория

const DownloadFolder = "downloads"

var adminID int64 = 5743254515
var allUsers = make(map[int64]bool)
var bannedUsers = make(map[int64]bool)
var awaitingBroadcast = make(map[int64]bool)
var awaitingBan = make(map[int64]bool)
var awaitingUnban = make(map[int64]bool)
var userLang = make(map[int64]string)
var titleCache = make(map[string]string)
var fileCache = make(map[string]string)
var cacheMu sync.Mutex
var inlineProcessing = make(map[string]bool)

type SearchResult struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func main() {
	os.MkdirAll(DownloadFolder, 0755)
	bot, err := tgbotapi.NewBotAPI(BotToken)
	if err != nil {
		log.Fatal(err)
	}

	bot.Debug = true
	log.Printf("Бот запущен как @%s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.InlineQuery != nil {
			query := update.InlineQuery.Query
			if query == "" {
				continue
			}

			results := searchYoutube(query)
			var articles []interface{}
			for i, r := range results {
				cacheMu.Lock()
				titleCache[r.ID] = r.Title
				cacheMu.Unlock()

				if fileID, exists := fileCache[r.ID]; exists {
					audioResult := tgbotapi.NewInlineQueryResultCachedAudio(r.ID, fileID)
					audioResult.Caption = "🎶 " + r.Title
					articles = append(articles, audioResult)
				} else {
					article := tgbotapi.NewInlineQueryResultArticle(r.ID, r.Title, r.Title)
					article.Description = "Нажмите для скачивания"

					keyboard := tgbotapi.NewInlineKeyboardMarkup(
						tgbotapi.NewInlineKeyboardRow(
							tgbotapi.NewInlineKeyboardButtonURL("🎧 Скачать в боте", "https://t.me/"+bot.Self.UserName+"?start="+r.ID),
						),
					)

					article.ReplyMarkup = &keyboard
					article.InputMessageContent = tgbotapi.InputTextMessageContent{
						Text:      "🎵 *" + r.Title + "*\n\nНажмите кнопку ниже, чтобы скачать трек.",
						ParseMode: "Markdown",
					}
					articles = append(articles, article)
				}

				if i == 4 {
					break
				}
			}

			inlineConfig := tgbotapi.InlineConfig{
				InlineQueryID: update.InlineQuery.ID,
				IsPersonal:    true,
				CacheTime:     1,
				Results:       articles,
			}
			bot.Request(inlineConfig)
			continue
		}

		if update.ChosenInlineResult != nil {
			resultID := update.ChosenInlineResult.ResultID
			if _, exists := fileCache[resultID]; !exists {
				go func(videoID string) {
					cacheMu.Lock()
					if inlineProcessing[videoID] {
						cacheMu.Unlock()
						return
					}
					inlineProcessing[videoID] = true
					cacheMu.Unlock()

					title := titleCache[videoID]
					downloadAndCacheAudio(bot, videoID, title)

					cacheMu.Lock()
					inlineProcessing[videoID] = false
					cacheMu.Unlock()
				}(resultID)
			}
			continue
		}

		if update.Message != nil {
			chatID := update.Message.Chat.ID
			allUsers[chatID] = true

			if bannedUsers[chatID] && chatID != adminID {
				bot.Send(tgbotapi.NewMessage(chatID, "🚫 Вы заблокированы."))
				continue
			}

			if chatID == adminID {
				if awaitingBroadcast[chatID] {
					awaitingBroadcast[chatID] = false
					msg := update.Message.Text
					for uid := range allUsers {
						if uid != adminID {
							bot.Send(tgbotapi.NewMessage(uid, "📣 Сообщение от админа:\n\n"+msg))
						}
					}
					bot.Send(tgbotapi.NewMessage(chatID, "✅ Рассылка отправлена."))
					continue
				}

				if awaitingBan[chatID] {
					awaitingBan[chatID] = false
					userID, err := strconv.ParseInt(update.Message.Text, 10, 64)
					if err == nil {
						bannedUsers[userID] = true
						bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Пользователь %d заблокирован.", userID)))
					} else {
						bot.Send(tgbotapi.NewMessage(chatID, "❌ Неверный формат ID пользователя."))
					}
					continue
				}

				if awaitingUnban[chatID] {
					awaitingUnban[chatID] = false
					userID, err := strconv.ParseInt(update.Message.Text, 10, 64)
					if err == nil {
						delete(bannedUsers, userID)
						bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Пользователь %d разблокирован.", userID)))
					} else {
						bot.Send(tgbotapi.NewMessage(chatID, "❌ Неверный формат ID пользователя."))
					}
					continue
				}
			}

			if update.Message.IsCommand() {
				cmd := update.Message.Command()
				args := update.Message.CommandArguments()

				switch cmd {
				case "start":
					if args != "" {
						videoID := args
						cacheMu.Lock()
						title, exists := titleCache[videoID]
						cacheMu.Unlock()

						if !exists {
							title = "Трек"
						}

						go handleDownload(bot, chatID, videoID, title)
						continue
					}

					langButtons := tgbotapi.NewInlineKeyboardMarkup(
						tgbotapi.NewInlineKeyboardRow(
							tgbotapi.NewInlineKeyboardButtonData("🇦🇲 Հայերեն", "lang_hy"),
							tgbotapi.NewInlineKeyboardButtonData("🇷🇺 Русский", "lang_ru"),
						),
					)
					msg := tgbotapi.NewMessage(chatID, "🌍 Выберите язык / Ընտրեք լեզուն")
					msg.ReplyMarkup = langButtons
					bot.Send(msg)
				case "help":
					lang := userLang[chatID]
					var helpText string
					if lang == "hy" {
						helpText = "✨ ━━━━━━━━━━━━━━━━ ✨\n🔊 Ինչպես օգտվել Melody Bot-ից\n✨ ━━━━━━━━━━━━━━━━ ✨\n🎧 Երաժշտություն որոնելու համար:\n1️⃣ Ուղարկեք երգի/արտիստի անունը\n2️⃣ Ընտրեք առաջարկվող արդյունքներից\n3️⃣ Սպասեք MP3 ձևափոխման ավարտին\n4️⃣ Ստացեք երգը և վայելեք այն!\n⚡️ Հուշումներ:\n• Արագ ներբեռնում - եթե երգը արդեն ներբեռնվել է\n• Պահպանում - երգերը պահվում են 24 ժամ\n• Կարիք չկա կրկնակի ներբեռնել նույն երգը\n🔔 Առաջարկներ կամ խնդիրներ?\n📩 Դիմեք: @ldftcer\n✨ ━━━━━━━━━━━━━━━━ ✨"
					} else {
						helpText = "✨ ━━━━━━━━━━━━━━━━ ✨\n🔊 Как пользоваться Melody Bot\n✨ ━━━━━━━━━━━━━━━━ ✨\n🎧 Для поиска музыки:\n1️⃣ Отправьте название песни/исполнителя\n2️⃣ Выберите из предложенных результатов\n3️⃣ Дождитесь конвертации в MP3\n4️⃣ Получите песню и наслаждайтесь!\n⚡️ Подсказки:\n• Быстрая загрузка - если песня уже загружалась\n• Хранение - песни сохраняются 24 часа\n• Не нужно повторно загружать одну и ту же песню\n🔔 Предложения или проблемы?\n📩 Обращайтесь: @ldftcer\n✨ ━━━━━━━━━━━━━━━━ ✨"
					}
					bot.Send(tgbotapi.NewMessage(chatID, helpText))
				case "admin":
					if chatID == adminID {
						menu := tgbotapi.NewInlineKeyboardMarkup(
							tgbotapi.NewInlineKeyboardRow(
								tgbotapi.NewInlineKeyboardButtonData("📊 Статистика", "admin_stats"),
								tgbotapi.NewInlineKeyboardButtonData("📣 Рассылка", "admin_broadcast"),
							),
							tgbotapi.NewInlineKeyboardRow(
								tgbotapi.NewInlineKeyboardButtonData("🚫 Бан", "admin_ban"),
								tgbotapi.NewInlineKeyboardButtonData("✅ Разбан", "admin_unban"),
							),
						)
						msg := tgbotapi.NewMessage(chatID, "🛠 Админ-панель")
						msg.ReplyMarkup = menu
						bot.Send(msg)
					}
				}
				continue
			}

			if userLang[chatID] == "" {
				bot.Send(tgbotapi.NewMessage(chatID, "🌐 Сначала выберите язык через /start"))
				continue
			}

			go handleSearch(bot, chatID, update.Message.Text)
		}

		if update.CallbackQuery != nil {
			chatID := update.CallbackQuery.Message.Chat.ID
			data := update.CallbackQuery.Data

			callback := tgbotapi.NewCallback(update.CallbackQuery.ID, "")
			bot.Request(callback)

			if strings.HasPrefix(data, "dl_") {
				videoID := strings.TrimPrefix(data, "dl_")
				cacheMu.Lock()
				title := titleCache[videoID]
				cacheMu.Unlock()
				go handleDownload(bot, chatID, videoID, title)
				continue
			}

			if chatID == adminID {
				if data == "admin_broadcast" {
					awaitingBroadcast[chatID] = true
					bot.Send(tgbotapi.NewMessage(chatID, "✉️ Введите текст рассылки:"))
					continue
				} else if data == "admin_stats" {
					stats := fmt.Sprintf("👥 Пользователей: %d\n🚫 Забанено: %d", len(allUsers), len(bannedUsers))
					bot.Send(tgbotapi.NewMessage(chatID, stats))
					continue
				} else if data == "admin_ban" {
					awaitingBan[chatID] = true
					bot.Send(tgbotapi.NewMessage(chatID, "👤 Введите ID пользователя для бана:"))
					continue
				} else if data == "admin_unban" {
					awaitingUnban[chatID] = true
					bot.Send(tgbotapi.NewMessage(chatID, "👤 Введите ID пользователя для разбана:"))
					continue
				}
			}

			if data == "lang_hy" {
				userLang[chatID] = "hy"
				welcome := "👋 Բարի գալուստ!\n🎵 Ես կարող եմ գտնել երգեր YouTube-ում և ուղարկել MP3 ֆորմատով։\n🔎 Ուղարկիր ինձ երգի անունը, և ես կսկսեմ որոնումը։\n📌 Օրինակ: Miyagi I Got Love\n💡 Օգնության համար սեղմեք /help։"
				bot.Send(tgbotapi.NewMessage(chatID, welcome))
			} else if data == "lang_ru" {
				userLang[chatID] = "ru"
				welcome := "👋 Добро пожаловать!\n🎵 Я могу найти песни на YouTube и отправить их в формате MP3.\n🔎 Отправь мне название песни, и я начну поиск.\n📌 Пример: Miyagi I Got Love\n💡 Для помощи нажмите /help."
				bot.Send(tgbotapi.NewMessage(chatID, welcome))
			}
		}
	}
}

func handleSearch(bot *tgbotapi.BotAPI, chatID int64, query string) {
	msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("🔍 Ищу: %s", query))
	sent, _ := bot.Send(msg)

	results := searchYoutube(query)
	if len(results) == 0 {
		lang := userLang[chatID]
		var notFoundText string
		if lang == "hy" {
			notFoundText = "❌ Ոչինչ չի գտնվել"
		} else {
			notFoundText = "❌ Ничего не найдено"
		}
		bot.Send(tgbotapi.NewEditMessageText(chatID, sent.MessageID, notFoundText))
		return
	}

	var buttons [][]tgbotapi.InlineKeyboardButton
	for _, r := range results {
		cacheMu.Lock()
		titleCache[r.ID] = r.Title
		cacheMu.Unlock()
		btn := tgbotapi.NewInlineKeyboardButtonData(r.Title, "dl_"+r.ID)
		buttons = append(buttons, tgbotapi.NewInlineKeyboardRow(btn))
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(buttons...)

	lang := userLang[chatID]
	var selectText string
	if lang == "hy" {
		selectText = "🎶 Ընտրեք երգը:"
	} else {
		selectText = "🎶 Выберите трек:"
	}

	edit := tgbotapi.NewEditMessageText(chatID, sent.MessageID, selectText)
	edit.ReplyMarkup = &markup
	bot.Send(edit)
}

func handleDownload(bot *tgbotapi.BotAPI, chatID int64, videoID, title string) {
	if fileID, ok := fileCache[videoID]; ok {
		audio := tgbotapi.NewAudio(chatID, tgbotapi.FileID(fileID))
		audio.Caption = "🎶 " + title
		audio.Title = title
		bot.Send(audio)
		return
	}

	lang := userLang[chatID]
	var downloadText string
	if lang == "hy" {
		downloadText = "🎧 Ներբեռնում եմ: " + title
	} else {
		downloadText = "🎧 Скачиваю: " + title
	}

	msg := tgbotapi.NewMessage(chatID, downloadText)
	statusMsg, _ := bot.Send(msg)

	resultFileID := downloadAndCacheAudio(bot, videoID, title)

	if resultFileID != "" {
		audio := tgbotapi.NewAudio(chatID, tgbotapi.FileID(resultFileID))
		audio.Caption = "🎶 " + title
		audio.Title = title
		bot.Send(audio)
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, statusMsg.MessageID)
		bot.Request(deleteMsg)
	} else {
		var errorText string
		if lang == "hy" {
			errorText = "❌ Ներբեռնման սխալ"
		} else {
			errorText = "❌ Ошибка скачивания"
		}
		edit := tgbotapi.NewEditMessageText(chatID, statusMsg.MessageID, errorText)
		bot.Send(edit)
	}
}

func downloadAndCacheAudio(bot *tgbotapi.BotAPI, videoID, title string) string {
	cacheMu.Lock()
	if fileID, ok := fileCache[videoID]; ok {
		cacheMu.Unlock()
		return fileID
	}
	cacheMu.Unlock()

	safeTitle := sanitizeFileName(title)
	output := fmt.Sprintf("%s/%s.%%(ext)s", DownloadFolder, safeTitle)
	cmd := exec.Command(YtDlpPath, "-x", "--audio-format", "mp3", "-o", output, "https://www.youtube.com/watch?v="+videoID)
	err := cmd.Run()
	if err != nil {
		return ""
	}

	mp3 := fmt.Sprintf("%s/%s.mp3", DownloadFolder, safeTitle)
	if _, err := os.Stat(mp3); err == nil {
		audioToCache := tgbotapi.NewAudio(-1002617453139, tgbotapi.FilePath(mp3))
		audioToCache.Caption = "🎶 " + title
		audioToCache.Title = title
		result, err := bot.Send(audioToCache)
		if err == nil {
			cacheMu.Lock()
			fileCache[videoID] = result.Audio.FileID
			cacheMu.Unlock()
			os.Remove(mp3)
			return result.Audio.FileID
		}
		os.Remove(mp3)
	}
	return ""
}

func searchYoutube(query string) []SearchResult {
	cmd := exec.Command(YtDlpPath, "-j", "--flat-playlist", "ytsearch5:"+query)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var results []SearchResult
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		var r SearchResult
		json.Unmarshal([]byte(line), &r)
		results = append(results, r)
	}
	return results
}

func sanitizeFileName(name string) string {
	forbidden := []string{"/", "\\", ":", "*", "?", "'", "<", ">", "|"}
	for _, ch := range forbidden {
		name = strings.ReplaceAll(name, ch, "_")
	}
	return strings.TrimSpace(name)
}
