package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/oheco/oheco/internal/catalog"
)

// maxIndexBytes bounds an upstream index response. Real project pages are far
// smaller; the limit only protects the ephemeral server from a hostile index.
const maxIndexBytes = 64 << 20

func readAtMost(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxIndexBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxIndexBytes {
		return nil, fmt.Errorf("upstream metadata too large")
	}
	return data, nil
}

// aggregatePip queries every configured index for one project and merges the
// files it publishes. Index order is the tie-breaker: the first index that
// mentions a filename owns it. Any index failure fails the whole request rather
// than silently returning a partial list a client would resolve differently.
func (s *Server) aggregatePip(ctx context.Context, name string) ([]pipFile, error) {
	seen := map[string]bool{}
	var merged []pipFile
	for _, base := range s.PipIndexes {
		files, err := s.fetchIndex(ctx, strings.TrimSuffix(base, "/")+"/"+name+"/")
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.Filename == "" || seen[f.Filename] {
				continue
			}
			seen[f.Filename] = true
			merged = append(merged, f)
		}
	}
	return merged, nil
}

func (s *Server) fetchIndex(ctx context.Context, address string) ([]pipFile, error) {
	if err := catalog.ValidateURL(address); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json, text/html")
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pip index %s: %w", address, err)
	}
	defer resp.Body.Close()
	// A project that an index does not host is reported as 404. With several
	// configured indexes that is the normal case, not a failure, so it must
	// simply contribute no files. Real errors still fail the whole request.
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("pip index %s: HTTP %d", address, resp.StatusCode)
	}
	data, err := readAtMost(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("pip index %s: %w", address, err)
	}
	files := parsePipJSON(data, address)
	if files == nil {
		files, err = parsePipHTML(data, address)
		if err != nil {
			return nil, fmt.Errorf("pip index %s: %w", address, err)
		}
	}
	for _, f := range files {
		if f.URL == "" || f.Filename == "" {
			return nil, fmt.Errorf("pip index %s: link without a target or filename", address)
		}
	}
	return files, nil
}

type pipJSONDoc struct {
	Files []struct {
		Filename       string            `json:"filename"`
		URL            string            `json:"url"`
		Hashes         map[string]string `json:"hashes"`
		RequiresPython string            `json:"requires-python"`
		Yanked         any               `json:"yanked"`
		Size           int64             `json:"size"`
	} `json:"files"`
}

// parsePipJSON decodes a PEP 691 response. It returns nil (without touching the
// input) when the document is not that shape, so the caller can fall back to
// HTML. Content sniffing is intentional: some indexes answer JSON without an
// accurate Content-Type.
func parsePipJSON(data []byte, page string) []pipFile {
	var doc pipJSONDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	files := make([]pipFile, 0, len(doc.Files))
	for _, in := range doc.Files {
		href, sum, ok := resolveIndexURL(page, in.URL)
		if !ok {
			continue
		}
		filename := in.Filename
		if filename == "" {
			filename = filenameFromURL(href)
		}
		if filename == "" {
			continue
		}
		if sum == "" && in.Hashes != nil {
			sum = strings.ToLower(in.Hashes["sha256"])
		}
		files = append(files, pipFile{Filename: filename, URL: href, Hashes: map[string]string{"sha256": sum}, RequiresPython: in.RequiresPython, Yanked: in.Yanked, Size: in.Size})
	}
	return files
}

// parsePipHTML decodes a PEP 503 response. Relative hrefs (Alibaba Cloud serves
// "../../packages/...") are resolved against the index page URL.
func parsePipHTML(data []byte, page string) ([]pipFile, error) {
	var files []pipFile
	var attrs map[string]string
	var text strings.Builder
	inAnchor := false
	for offset := 0; ; {
		kind, tag, body, next := scanHTMLToken(data, offset)
		if kind == tokenNone {
			return files, nil
		}
		offset = next
		switch kind {
		case tokenStartTag:
			if tagName(tag) != "a" || inAnchor {
				continue
			}
			inAnchor, attrs = true, parseHTMLAttrs(tag)
			text.Reset()
		case tokenText:
			if inAnchor {
				text.Write(body)
			}
		case tokenEndTag:
			if tagName(tag) != "a" || !inAnchor {
				continue
			}
			inAnchor = false
			address, sum, ok := resolveIndexURL(page, attrs["href"])
			if !ok {
				continue
			}
			filename := strings.TrimSpace(decodeHTMLEntities(text.String()))
			if filename == "" {
				filename = filenameFromURL(address)
			}
			if filename == "" {
				continue
			}
			var mark any
			if yanked, ok := attrs["data-yanked"]; ok && yanked != "" {
				mark = yanked
			}
			files = append(files, pipFile{Filename: filename, URL: address, Hashes: map[string]string{"sha256": sum}, RequiresPython: attrs["data-requires-python"], Yanked: mark})
		}
	}
}

// Only the anchor elements PEP 503 requires are interpreted. <script> and
// <style> content is skipped so a page's JavaScript cannot be mistaken for a
// link.
const (
	tokenNone = iota
	tokenText
	tokenStartTag
	tokenEndTag
)

// scanHTMLToken splits the input at offset into the next tag or text run. The
// returned slice for a tag is its whole body, "<a href=...>" included, so the
// caller can reuse it as the final token.
func scanHTMLToken(data []byte, offset int) (kind int, tag, body []byte, next int) {
	if offset >= len(data) {
		return tokenNone, nil, nil, len(data)
	}
	if data[offset] != '<' {
		end := bytes.IndexByte(data[offset:], '<')
		if end < 0 {
			end = len(data) - offset
		}
		return tokenText, nil, data[offset : offset+end], offset + end
	}
	end := bytes.IndexByte(data[offset:], '>')
	if end < 0 {
		end = len(data) - offset - 1
	}
	tag = data[offset : offset+end+1]
	lower := strings.ToLower(string(tag))
	if strings.HasPrefix(lower, "<script") || strings.HasPrefix(lower, "<style") {
		if close := bytes.Index(bytes.ToLower(data[offset:]), []byte("</")); close >= 0 {
			return tokenText, nil, nil, offset + close
		}
		return tokenNone, nil, nil, len(data)
	}
	if strings.HasPrefix(lower, "</") {
		return tokenEndTag, tag, nil, offset + len(tag)
	}
	if strings.HasPrefix(lower, "<!") || strings.HasPrefix(lower, "<?") {
		return tokenText, nil, nil, offset + len(tag)
	}
	return tokenStartTag, tag, nil, offset + len(tag)
}

// parseHTMLAttrs reads the attributes of one tag. Quoted values keep their
// original case; names are lower-cased. The tag name may be followed by any
// whitespace, not only a space.
func parseHTMLAttrs(tag []byte) map[string]string {
	attrs := map[string]string{}
	body := tag
	if len(body) > 0 && body[0] == '<' {
		body = body[1:]
	}
	if len(body) > 0 && body[0] == '/' {
		body = body[1:]
	}
	end := 0
	for end < len(body) && !bytes.ContainsRune(htmlTagDelimiters, rune(body[end])) {
		end++
	}
	body = body[end:]
	i := 0
	for i < len(body) {
		for i < len(body) && isHTMLSpace(body[i]) {
			i++
		}
		start := i
		for i < len(body) && body[i] != '=' && !isHTMLSpace(body[i]) && body[i] != '>' && body[i] != '/' {
			i++
		}
		if start == i {
			break
		}
		name := strings.ToLower(string(body[start:i]))
		for i < len(body) && isHTMLSpace(body[i]) {
			i++
		}
		value := ""
		if i < len(body) && body[i] == '=' {
			i++
			for i < len(body) && isHTMLSpace(body[i]) {
				i++
			}
			if i < len(body) && (body[i] == '"' || body[i] == '\'') {
				quote := body[i]
				i++
				start = i
				for i < len(body) && body[i] != quote && body[i] != '>' {
					i++
				}
				value = string(body[start:i])
				if i < len(body) && body[i] == quote {
					i++
				}
			} else {
				start = i
				// An unquoted value ends only at whitespace or the tag end; a
				// "/" is part of the value (as in https://...).
				for i < len(body) && !isHTMLSpace(body[i]) && body[i] != '>' {
					i++
				}
				value = string(body[start:i])
			}
		}
		attrs[name] = decodeHTMLEntities(value)
	}
	return attrs
}

func isHTMLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f'
}

func tagName(tag []byte) string {
	body := tag
	for len(body) > 0 && (body[0] == '<' || body[0] == '/') {
		body = body[1:]
	}
	end := 0
	for end < len(body) && !bytes.ContainsRune(htmlTagDelimiters, rune(body[end])) {
		end++
	}
	return strings.ToLower(string(body[:end]))
}

var htmlTagDelimiters = []byte(" \t\r\n>/")

// decodeHTMLEntities handles the references that appear in distribution
// filenames and link attributes.
func decodeHTMLEntities(s string) string {
	if !strings.ContainsRune(s, '&') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '&' {
			b.WriteByte(s[i])
			i++
			continue
		}
		semi := strings.IndexByte(s[i:], ';')
		if semi < 0 {
			b.WriteString(s[i:])
			break
		}
		entity := s[i+1 : i+semi]
		if r, ok := htmlEntity(entity); ok {
			b.WriteString(r)
		} else {
			b.WriteString(s[i : i+semi+1])
		}
		i += semi + 1
	}
	return b.String()
}

var namedEntities = map[string]string{
	"amp": "&", "lt": "<", "gt": ">", "quot": `"`, "apos": "'", "nbsp": "\u00a0",
	"plus": "+", "num": "#", "sol": "/", "equals": "=", "colon": ":", "period": ".",
	"commat": "@", "lpar": "(", "rpar": ")", "percnt": "%", "ast": "*", "excl": "!",
}

func htmlEntity(entity string) (string, bool) {
	if value, ok := namedEntities[entity]; ok {
		return value, true
	}
	if entity == "" {
		return "", false
	}
	base := 10
	digits := entity
	if entity[0] == '#' {
		base, digits = 10, entity[1:]
		if strings.HasPrefix(digits, "x") || strings.HasPrefix(digits, "X") {
			base, digits = 16, digits[1:]
		}
	} else {
		return "", false
	}
	var r rune
	for _, c := range digits {
		digit := -1
		switch {
		case c >= '0' && c <= '9':
			digit = int(c - '0')
		case base == 16 && c >= 'a' && c <= 'f':
			digit = int(c-'a') + 10
		case base == 16 && c >= 'A' && c <= 'F':
			digit = int(c-'A') + 10
		}
		if digit < 0 {
			return "", false
		}
		r = r*rune(base) + rune(digit)
	}
	if r == 0 || r > 0x10FFFF {
		return "", false
	}
	return string(r), true
}

// resolveIndexURL turns a possibly relative href into an absolute URL without a
// fragment, reporting the SHA-256 carried by a "#sha256=" fragment. A URL with
// no fragment is passed through unchanged.
func resolveIndexURL(page, raw string) (string, string, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", "", false
	}
	base, err := url.Parse(page)
	if err != nil {
		return "", "", false
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	abs := base.ResolveReference(ref)
	sum := ""
	for _, part := range strings.Split(abs.Fragment, "&") {
		if value, ok := strings.CutPrefix(part, "sha256="); ok {
			sum = strings.ToLower(strings.TrimSpace(value))
		}
	}
	abs.Fragment = ""
	address := abs.String()
	if catalog.ValidateURL(address) != nil {
		return "", "", false
	}
	return address, sum, true
}

func filenameFromURL(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}
	name, err := url.PathUnescape(u.Path[strings.LastIndex(u.Path, "/")+1:])
	if err != nil {
		return ""
	}
	return strings.TrimSpace(name)
}
