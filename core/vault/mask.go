package vault

import (
	"regexp"
	"strings"
)

// What an answer must not carry: a secret that reaches a tool result lands in the conversation's context, and from
// there it never leaves. Masking is by default, not on request.

// Hidden is what a hidden value becomes. Tools that write whole objects back refuse any payload carrying it: a masked
// read edited and saved would otherwise replace the real secret with this text.
const Hidden = "‹hidden — look it up in the service's own panel›"

// secretName matches names that almost certainly hold a secret. auth, dsn, bearer and jwt count as whole words
// (x-auth, SENTRY_DSN), so that author and authority do not.
var secretName = regexp.MustCompile(`(?i)(^|[_-])(pass|passwd|pwd|pin|otp|totp|seed|salt|auth|dsn|bearer|jwt)($|[_-])|password|passphrase|secret|token|apikey|api_key|_key$|^key$|private|credential|authorization|cookie|signing|connection[_-]?string|session[_-]?id|access[_-]?key[_-]?id`)

// secretValue matches secrets by what they look like, wherever they sit: a JWT, a token with a well-known prefix (GitHub,
// OpenAI/Anthropic, Slack, AWS access key, GitLab), a Bearer credential. There is deliberately no rule for «a long hex
// string»: git SHAs and UUIDs would all be covered. Slack's d cookie comes first, to win over the general rule at the
// same position; it is URL-encoded or plain base64, so it runs past \w and -.
var secretValue = regexp.MustCompile(`(?i)` + strings.Join(valueShapes, "|"))

// valueShapes are secretValue's rules one by one, in its order, and shapeKinds their names as ShapeKinds tells them.
var (
	valueShapes = []string{
		`\bxoxd-[\w%+/=.-]{10,}`,
		`\beyJ[\w-]+\.[\w-]+\.[\w-]+`,
		`\b(?:gh[pousr]_|github_pat_|sk-ant-|sk-|xox[a-z]-|AKIA|glpat-)[\w-]{10,}`,
		`\bBearer\s+\S+`,
	}
	shapeKinds = []string{"slack cookie", "jwt", "token of a known prefix", "Bearer-header credential"}
	// (no word may follow «Bearer» with a space in a kind: the answer's mask would hide the kind itself)
	shapeRes = func() []*regexp.Regexp {
		out := make([]*regexp.Regexp, len(valueShapes))
		for i, v := range valueShapes {
			out[i] = regexp.MustCompile(`(?i)` + v)
		}
		return out
	}()
)

// alreadyHidden is a match that holds no value: the placeholder itself («Bearer ‹hidden…›» was masked once), or a
// Bearer that names where the token is (Bearer $TOKEN, Bearer ${N8N_API_KEY}, Bearer <token>, Bearer YOUR_TOKEN).
func alreadyHidden(m string) bool {
	if strings.Contains(m, "‹hidden") {
		return true
	}
	if f := strings.Fields(m); len(f) == 2 && strings.EqualFold(f[0], "bearer") {
		v := strings.Trim(f[1], "\"'`,.;)")
		return strings.HasPrefix(v, "$") || strings.HasPrefix(v, "<") || envName.MatchString(v) || placeholder.MatchString(v) || standIn.MatchString(v)
	}
	return false
}

// credentialsInURL matches an address carrying credentials: postgres://user:password@host/db.
var credentialsInURL = regexp.MustCompile(`://[^/@\s]+:[^/@\s]+@`)

const urlCredentialsHidden = "://‹user›:‹password›@"

// MaskValues is a text with every secret-looking value replaced.
// A match that is already the placeholder is left as it is: masking it again changed a text that holds no secret.
func MaskValues(s string) string {
	return secretValue.ReplaceAllStringFunc(s, func(m string) string {
		if alreadyHidden(m) || bearerWord(m) {
			return m
		}
		return Hidden
	})
}

// bearerWord is a Bearer match followed by a word, not a token: «the Bearer token», «token Bearer in un passo». A word
// is letters only and short; a token is long, or has a digit or a symbol, and a short one of letters is no credential.
// The mask leaves it alone too: a write was refused for «Bearer in» in an Italian sentence.
func bearerWord(m string) bool {
	f := strings.Fields(m)
	return len(f) == 2 && strings.EqualFold(f[0], "bearer") && sentenceWord.MatchString(strings.Trim(f[1], "\"'`,.;:!?)"))
}

// sentenceWord is a word as a sentence has it: lowercase, or capitalized, and short
var sentenceWord = regexp.MustCompile(`^\p{Lu}?\p{Ll}{1,11}$`)

// proseBearer is a Bearer match a stored text holds without a token in it: a word (bearerWord), or a value under 16
// characters. The scan reads it so; an answer's mask still hides the short value, as it hides any.
func proseBearer(m string) bool {
	f := strings.Fields(m)
	if len(f) != 2 || !strings.EqualFold(f[0], "bearer") {
		return false
	}
	return bearerWord(m) || len(strings.Trim(f[1], "\"'`,.;)")) < 16
}

// ShapeKinds is what kinds of secret by shape a text holds (a jwt, a bearer credential, the password of an address),
// each once, in a fixed order; nothing when it holds none, or only placeholders. Kinds, never the values: it is for
// telling the owner why a page looks secret.
func ShapeKinds(s string) []string {
	var out []string
	for i, re := range shapeRes {
		for _, m := range re.FindAllString(s, -1) {
			if !alreadyHidden(m) && !proseBearer(m) {
				out = append(out, shapeKinds[i])
				break
			}
		}
	}
	if credentialsInURL.ReplaceAllLiteralString(s, urlCredentialsHidden) != s {
		out = append(out, "credentials in an address")
	}
	return out
}

// MaskField is one value, given the name it is stored under. The name alone is not enough either way: DATABASE_URL
// hides a password and FIVETOOLS_URL is a public address. Of an address with credentials only the user/password pair is
// covered: the rest is what you need to know.
func MaskField(name, value string) string {
	if value == "" {
		return value
	}
	if secretName.MatchString(name) {
		return Hidden
	}
	return MaskValues(credentialsInURL.ReplaceAllLiteralString(value, urlCredentialsHidden))
}

// The three name = value shapes Redact and SecretPairs cover, by the name's kind: group 1 is the name and its separator, group 2 the
// value. A value already hidden, or a reference to the vault (vault:<name>), is not a secret any more.
const kvValue = `("[^"\n]*"|'[^'\n]*'|[^\s,;}]+)`

var kvShapes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(\b[\w.-]*(?:password|passwd|passphrase|secret|token|apikey|api_key|private_key|credential|totp|_pin)[\w.-]*["']?\s*[=:]\s*)` + kvValue),
	// names that are secret as a whole word, not as a part of one (author: Ada stays)
	regexp.MustCompile(`(?i)(\b(?:[\w.-]*[_-])?(?:dsn|auth|bearer|jwt)(?:[_-][\w.-]*)?["']?\s*[=:]\s*)` + kvValue),
	regexp.MustCompile(`(?i)(\b[\w.-]*(?:connection_string|session_id|access_key_id)[\w.-]*["']?\s*[=:]\s*)` + kvValue),
}

var (
	variableRef = regexp.MustCompile(`^\$\{?\w+\}?$`)
	colonSpace  = regexp.MustCompile(`:\s+$`)
)

// Pair is a name = value pair that looks secret, as SecretPairs finds it. Its value stays unexported, so that what holds
// a Pair can compare it (two Pairs are equal when name and value are) but never print the value.
type Pair struct {
	Name  string
	value string
}

var (
	// envName is a variable's name given as the value, `KEDALIO_CORE_TOKEN`: where the secret is, not the secret
	envName = regexp.MustCompile("^`?\\$?\\{?[A-Z][A-Z0-9_]*\\}?`?$")
	// placeholder is a value that stands for one: <token> (<your key>, of which the pattern sees «<your»), ..., ***, xxx,
	// and a template's slot: a stand-in word in braces, {secret}, or an expression in double ones, {{ $env.N8N_TOKEN }}.
	// Any word in braces is not one: {hunter} may be the value written in braces.
	placeholder = regexp.MustCompile(`^(?:<.*|\.{3}|…|\*+|x{3,}|X{3,}|\{\{\s*\$[\w.]+\s*\}\})$`)
	slot        = regexp.MustCompile(`^\{(\w+)\}$`)
	// keyName is the name of a key in the owner's sops file, where the secret is: <service>.<account>.<kind> in lowercase
	// words (a digit may end one: r2), the last naming the kind of secret (cloudflare.alice.token, r2.backups.secret_key),
	// or one imported from the old vault, old.NAME. correct.horse.battery, or id.secret with random parts, is not one.
	keyName = regexp.MustCompile(`^(?:(?:[a-z]{1,20}[0-9]?(?:[_-][a-z]{1,20})*\.){1,3}(?:[a-z]+[_-])?(?:token|key|secret|refresh|password|pat|cookie|credentials?|dsn)|old\.[A-Z][A-Z0-9_]*)$`)
	// proseValue is a word of prose: after «name: » a word with no digit and no symbol is a sentence going on
	proseValue = regexp.MustCompile(`^\p{L}{1,20}$`)
	// proseName is two words joined by a hyphen (token: kedalio-bot, a name), prose too but for a password, whose words
	// a passphrase joins so
	proseName = regexp.MustCompile(`^\p{L}{1,20}-\p{L}{1,20}$`)
	passName  = regexp.MustCompile(`(?i)pass|secret`)
	// setting is a value that is a switch or a number, never a credential: MAX_THINKING_TOKENS=0, verify_jwt=false
	setting = regexp.MustCompile(`(?i)^(?:-?\d+(?:\.\d+)?|true|false|yes|no|on|off|null|none|nil|enabled|disabled|required|optional)$`)
	// stand-in is a word that stands for a value in an example: SMTP_PASSWORD=changeme, API_KEY=your-key
	// and the same words in Italian (SMTP_PASSWORD=chiave, API_KEY=valore)
	standIn = regexp.MustCompile(`(?i)^(?:changeme|change-me|redacted|secret|password|passwd|example|placeholder|dummy|todo|tbd|hidden|none|empty|unset|key|value|token|chiave|valore|segreto|segreta|esempio|nascosto|vuoto|(?:your|my)[_-]?[a-z_-]*)$`)
	// pinName: a number is the secret itself for these
	pinName  = regexp.MustCompile(`(?i)(?:^|[_-])(?:pin|otp|totp|passcode)(?:$|[_-])`)
	hasAlnum = regexp.MustCompile(`[\p{L}\p{N}]`)
)

// SecretPairs is the name = value pairs that look secret in a note: a page of the memory, a task's text. A note is
// prose, so it is read more leniently than a log: after «name: » a plain word goes on a sentence (auth: OAuth, token: la
// sezione), and a value that names a variable or a key of the secrets file (cloudflare.alice.token, old.NAME), stands
// for one ({secret}, chiave) or has no letter or digit (**Token:** in Markdown) is not a secret. Nor is a switch or a number (MAX_THINKING_TOKENS=0, verify_jwt=false, VITE_GOOGLE_AUTH=on), a stand-in
// (SMTP_PASSWORD=changeme) or a value of five characters or fewer, but for a pin or a one-time code, whose number is
// the secret. Anything longer with a digit or a symbol stays one: TOKEN=abc123.
func SecretPairs(s string) []Pair {
	var out []Pair
	for _, re := range kvShapes {
		for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
			key, val := s[m[2]:m[3]], s[m[4]:m[5]]
			// the value as a sentence ends it: «verify_jwt=false.» is false
			// and Markdown's backticks and stars around it are not part of it: `verify_jwt=false`
			bare := strings.TrimRight(strings.Trim(val, "\"'`*"), ".,:!?)`*")
			switch {
			case variableRef.MatchString(val), strings.HasPrefix(val, "‹hidden"), strings.HasPrefix(val, "vault:"),
				strings.HasPrefix(bare, "$"), // a shell reference or substitution: TOKEN=$(sops -d …), KEY=$KEY
				envName.MatchString(bare), placeholder.MatchString(bare), keyName.MatchString(bare), !hasAlnum.MatchString(bare):
				continue
			}
			name := strings.TrimRight(strings.TrimSpace(key), `=: "'`)
			if colonSpace.MatchString(key) && (proseValue.MatchString(bare) || proseName.MatchString(bare) && !passName.MatchString(name)) {
				continue
			}
			if m := slot.FindStringSubmatch(bare); m != nil && standIn.MatchString(m[1]) {
				continue
			}
			// a switch, a number, a stand-in or a value too short to be a credential; a pin's number is one, though
			if !pinName.MatchString(name) && (setting.MatchString(bare) || standIn.MatchString(bare) || len([]rune(bare)) <= 5) {
				continue
			}
			out = append(out, Pair{Name: name, value: val})
		}
	}
	return out
}
