package mcp

import (
	"bytes"
	"encoding/json"
	"io"
)

// render is what every tool hands back: JSON as text, masked before it is written, so a secret that slipped into a
// page, a task or an error never reaches a conversation (ADR 0002 «The vault»). The JSON stays JSON and keeps the
// order the answer was built in: only string values are ever touched, never a number or a key.
func (s *Server) render(v any) (string, error) {
	var raw bytes.Buffer
	enc := json.NewEncoder(&raw)
	// a page's text has < and & in it; escaping them would only make the answer harder to read
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	data := raw.Bytes()
	if s.cfg.Mask != nil {
		var masked bytes.Buffer
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err := copyMasked(dec, &masked, s.cfg.Mask); err != nil {
			return "", err
		}
		data = masked.Bytes()
	}
	// compact: indentation is context the reader pays for and gets nothing from
	return string(bytes.TrimSpace(data)), nil
}

func writeJSON(w *bytes.Buffer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	w.Truncate(w.Len() - 1) // Encode ends the value with a newline
	return nil
}

// copyMasked writes the next JSON value of dec to w with every string value masked (an object's keys are left alone).
func copyMasked(dec *json.Decoder, w *bytes.Buffer, mask func(string) string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		closing, isObject := byte(']'), t == '{'
		if isObject {
			closing = '}'
		}
		w.WriteByte(byte(t))
		for first := true; dec.More(); first = false {
			if !first {
				w.WriteByte(',')
			}
			if isObject {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				if err := writeJSON(w, key); err != nil {
					return err
				}
				w.WriteByte(':')
			}
			if err := copyMasked(dec, w, mask); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil && err != io.EOF {
			return err
		}
		w.WriteByte(closing)
		return nil
	case string:
		return writeJSON(w, mask(t))
	default:
		return writeJSON(w, t)
	}
}
