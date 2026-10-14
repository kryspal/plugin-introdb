package main

import "github.com/kryspal/plugin-sdk/i18n"

// messages is the declared string table (change request 0057): en, zh-TW and zh-CN; other languages follow the i18n fallback chain, ending in English.
// The attribution (attribution.text) is the English original, the same in every language, and is not here.
var messages = i18n.New(messageTables)

var messageTables = map[string]map[string]string{
	"en": {
		"desc":           "Looks up intro, recap and outro times for episodes and movies from the IntroDB community database, using the IMDb ID, so skipping can work without reading the audio of every file. The server asks IntroDB directly for the episodes it needs, one request at a time, and keeps the answers only on this server. Each result is checked against the file before it is used. Choose this plugin in a library's settings to turn it on.",
		"groupAdvanced":  "Advanced",
		"keyLabel":       "IntroDB API key",
		"keyHelp":        "Only needed to upload your own manual markings. Create the key in your own IntroDB account (it starts with idb_). Lookups do not need a key.",
		"submitLabel":    "Upload my manual markings to IntroDB",
		"submitHelp":     "Off by default. When on, intros, recaps and outros you mark by hand are sent to IntroDB with the IMDb ID, season and episode. File names and paths are never sent. Times found by the server itself or taken from other databases are not uploaded.",
		"baseURLLabel":   "API base URL",
		"baseURLHelp":    "Leave empty to use the official API. Only for a trusted HTTPS proxy.",
		"errKeyFormat":   "An IntroDB API key starts with idb_.",
		"errHTTPS":       "Must be an https:// address.",
		"errSubmitNoKey": "Enter your IntroDB API key to turn uploads on.",
	},
	"zh-TW": {
		"desc":           "用 IMDb ID 向 IntroDB 社群資料庫查電影與劇集的片頭、前情提要與片尾時間，這樣不必讀取每個檔案的音訊也能跳過。伺服器只向 IntroDB 直接查需要的集數，一次一個請求，答案只存在這台伺服器上。每筆結果都會和檔案核對過才採用。要使用，請在媒體庫設定裡勾選這個外掛。",
		"groupAdvanced":  "進階",
		"keyLabel":       "IntroDB API 金鑰",
		"keyHelp":        "只有要上傳自己手動標記的片段時才需要。請在自己的 IntroDB 帳號建立金鑰（以 idb_ 開頭）。查詢不需要金鑰。",
		"submitLabel":    "把我手動標記的片段上傳到 IntroDB",
		"submitHelp":     "預設關閉。開啟後，你手動標記的片頭、前情提要與片尾會連同 IMDb ID、季與集送給 IntroDB；不會送出檔名與路徑。伺服器自己偵測的時間與取自其他資料庫的時間不會上傳。",
		"baseURLLabel":   "API 網址",
		"baseURLHelp":    "留空表示使用官方 API。只用於可信任的 HTTPS 代理。",
		"errKeyFormat":   "IntroDB 的 API 金鑰以 idb_ 開頭。",
		"errHTTPS":       "必須是 https:// 網址。",
		"errSubmitNoKey": "要開啟上傳，請先填入你的 IntroDB API 金鑰。",
	},
	"zh-CN": {
		"desc":           "用 IMDb ID 向 IntroDB 社区数据库查电影和剧集的片头、前情提要与片尾时间，这样不必读取每个文件的音频也能跳过。服务器只向 IntroDB 直接查需要的集数，一次一个请求，答案只保存在这台服务器上。每条结果都会和文件核对过才采用。要使用，请在媒体库设置里勾选这个插件。",
		"groupAdvanced":  "高级",
		"keyLabel":       "IntroDB API 密钥",
		"keyHelp":        "只有要上传自己手动标记的片段时才需要。请在自己的 IntroDB 账号创建密钥（以 idb_ 开头）。查询不需要密钥。",
		"submitLabel":    "把我手动标记的片段上传到 IntroDB",
		"submitHelp":     "默认关闭。开启后，你手动标记的片头、前情提要与片尾会连同 IMDb ID、季与集发送给 IntroDB；不会发送文件名和路径。服务器自己检测的时间与取自其他数据库的时间不会上传。",
		"baseURLLabel":   "API 网址",
		"baseURLHelp":    "留空表示使用官方 API。只用于可信任的 HTTPS 代理。",
		"errKeyFormat":   "IntroDB 的 API 密钥以 idb_ 开头。",
		"errHTTPS":       "必须是 https:// 网址。",
		"errSubmitNoKey": "要开启上传，请先填入你的 IntroDB API 密钥。",
	},
}
