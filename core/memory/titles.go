package memory

import (
	"regexp"
	"strings"
)

var foldAccents = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ã", "a", "ä", "a", "å", "a", "ā", "a", "ă", "a", "ą", "a",
	"ç", "c", "ć", "c", "č", "c", "ď", "d", "đ", "d",
	"è", "e", "é", "e", "ê", "e", "ë", "e", "ē", "e", "ė", "e", "ę", "e", "ě", "e",
	"ì", "i", "í", "i", "î", "i", "ï", "i", "ī", "i", "į", "i",
	"ł", "l", "ľ", "l", "ñ", "n", "ń", "n", "ň", "n",
	"ò", "o", "ó", "o", "ô", "o", "õ", "o", "ö", "o", "ø", "o", "ō", "o", "ő", "o",
	"ŕ", "r", "ř", "r", "ś", "s", "š", "s", "ş", "s", "ș", "s", "ť", "t", "ț", "t", "ţ", "t",
	"ù", "u", "ú", "u", "û", "u", "ü", "u", "ū", "u", "ů", "u", "ű", "u", "ų", "u",
	"ý", "y", "ÿ", "y", "ź", "z", "ž", "z", "ż", "z",
)

// Fold is a text in lower case without the accents of the Latin alphabets (what NFD with the marks dropped gave).
func Fold(s string) string { return foldAccents.Replace(strings.ToLower(s)) }

var alnumRun = regexp.MustCompile(`[a-z0-9]+`)

// TitleWords is the set of words of a title, folded, those of one or two letters left out.
func TitleWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range alnumRun.FindAllString(Fold(s), -1) {
		if len(w) > 2 {
			out[w] = true
		}
	}
	return out
}

// TitleSimilarity is how alike two titles are, by their words (Jaccard), 1 when one contains the other: a title of two words
// or more. A one-word title shares its word with any long title that has it (that is not a duplicate), so it is
// judged by the Jaccard alone.
func TitleSimilarity(a, b string) float64 { return wordsSimilarity(TitleWords(a), TitleWords(b)) }

func wordsSimilarity(x, y map[string]bool) float64 {
	if len(x) == 0 || len(y) == 0 {
		return 0
	}
	inter := 0
	for w := range x {
		if y[w] {
			inter++
		}
	}
	if m := min(len(x), len(y)); m >= 2 && inter == m {
		return 1
	}
	return float64(inter) / float64(len(x)+len(y)-inter)
}
