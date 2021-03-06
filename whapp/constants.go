package whapp

const url = "https://web.whatsapp.com"
const userAgent = "Mozilla/5.0 (Windows NT 5.1) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/61.0.3163.100 Safari/537.36"

var cryptKeys = map[string]string{
	"image":    "576861747341707020496d616765204b657973",
	"sticker":  "576861747341707020496d616765204b657973",
	"video":    "576861747341707020566964656f204b657973",
	"audio":    "576861747341707020417564696f204b657973",
	"ptt":      "576861747341707020417564696f204b657973",
	"document": "576861747341707020446f63756d656e74204b657973",
}

func getCryptKey(typ string) string {
	if res, found := cryptKeys[typ]; found {
		return res
	}

	return cryptKeys["document"]
}
