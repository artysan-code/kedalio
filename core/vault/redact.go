package vault

import "strings"

// Redact is a stored text with what the secret scan finds in it replaced by Hidden, and how many values it replaced:
// the name = value pairs SecretPairs reads as secrets (the name stays), the shapes ShapeKinds names and the credentials
// of an address. Everything else stays byte for byte, and a redacted text redacts to itself with nothing replaced. It
// is for cleaning what the memory already stores (an old revision), so it follows the scan, which reads a note more
// leniently than a log would be read: what the scan does not flag is not touched.
func Redact(s string) (string, int) {
	n := 0
	secret := map[string]bool{}
	for _, p := range SecretPairs(s) {
		secret[p.value] = true
	}
	if len(secret) > 0 {
		for _, re := range kvShapes {
			var b strings.Builder
			last := 0
			for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
				key, val := s[m[2]:m[3]], s[m[4]:m[5]]
				b.WriteString(s[last:m[0]])
				last = m[1]
				if secret[val] {
					b.WriteString(key + Hidden)
					n++
				} else {
					b.WriteString(s[m[0]:m[1]])
				}
			}
			b.WriteString(s[last:])
			s = b.String()
		}
	}
	s = secretValue.ReplaceAllStringFunc(s, func(m string) string {
		if alreadyHidden(m) || proseBearer(m) {
			return m
		}
		n++
		// «Bearer \S+» runs to the next space: the quote or comma that closes it is not the token and stays
		rest := strings.TrimRight(m, "\"'`,.;)")
		return Hidden + m[len(rest):]
	})
	s = credentialsInURL.ReplaceAllStringFunc(s, func(m string) string {
		if m == urlCredentialsHidden {
			return m
		}
		n++
		return urlCredentialsHidden
	})
	return s, n
}
