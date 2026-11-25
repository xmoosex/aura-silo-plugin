package main

import "strings"

// languageCodes maps MediUX language names to the codes Silo compares
var languageCodes = map[string]string{
	"english": "en", "spanish": "es", "french": "fr", "german": "de", "italian": "it",
	"portuguese": "pt", "brazilian portuguese": "pt-BR", "japanese": "ja", "korean": "ko",
	"chinese": "zh", "simplified chinese": "zh-CN", "traditional chinese": "zh-TW", "russian": "ru",
	"arabic": "ar", "hindi": "hi", "dutch": "nl", "polish": "pl", "turkish": "tr", "swedish": "sv",
	"norwegian": "no", "danish": "da", "finnish": "fi", "czech": "cs", "greek": "el", "hebrew": "he",
	"hungarian": "hu", "thai": "th", "ukrainian": "uk", "vietnamese": "vi", "indonesian": "id",
}

// languageCode turns a MediUX language name into a code, a name it doesn't know is left as it is
func languageCode(name string) string {
	name = strings.TrimSpace(name)
	if code, ok := languageCodes[strings.ToLower(name)]; ok {
		return code
	}
	return name
}
