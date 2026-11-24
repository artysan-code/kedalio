package db

import "strings"

// NormalizeS3Prefix is the one place that turns what the owner typed as the offsite folder into what the store's client
// wants: "" or a name ending in "/". The core that writes the copies and kedalio-restore that reads them must agree, or
// a prefix given without the slash lists the wrong objects on one side.
func NormalizeS3Prefix(p string) string {
	if p != "" && !strings.HasSuffix(p, "/") {
		return p + "/"
	}
	return p
}
