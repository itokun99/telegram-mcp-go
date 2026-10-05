package kit

// This file ports the rich-message layer of the Python server.
//
// BuildRich mirrors make_rich_input's routing plus Telethon 1.45.0's entity
// parsers (telethon/extensions/markdown.py and html.py — the exact version
// pinned in uv.lock), so a Go tool call with a parse_mode produces the same
// message text and entities the Python server sends. Where Python tests assert
// rendered strings (tests/test_custom_emojis.py,
// tests/test_scheduled_message_parse_mode.py, tests/test_messages semantics)
// rich_test.go pins byte-identical results.
//
// RichTextToStr and RichMessageText port runtime.rich_text_to_str and
// runtime.rich_message_text: a channel posting in the block format leaves
// msg.message empty and carries every word as Instant-View page blocks, so the
// reading tools fall back to rich_message.
//
// Beyond Telethon 1.45.0 (which predates them) the parsers follow the syntax
// the Go stack already speaks in gogram's own parser: <tg-spoiler>/<spoiler>,
// ||spoiler||, <blockquote>/<blockquote expandable> and "> " / ">> " quote
// lines, the <mention> tag, and tg://user / tg://emoji links that map to
// mention and custom-emoji entities.

import (
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	telegram "github.com/amarnathcjd/gogram/telegram"
)

// RichInput is the parsed form of one outgoing message body.
type RichInput struct {
	// Message is the markup-stripped text to send together with Entities.
	Message string
	// Entities are Telegram formatting entities with UTF-16 code-unit offsets,
	// ready for telegram.SendOptions.Entities.
	Entities []telegram.MessageEntity
	// Plain is the plain-text rendering of the same body: every markup
	// construct removed and no entity payloads, i.e. what Telegram displays
	// without formatting. It matches Message in every mode; a caller that
	// cannot attach entities (e.g. an account where custom emoji are
	// restricted) resends Plain without ParseMode.
	Plain string
}

// richParseModes mirrors RICH_PARSE_MODES in runtime.py: the parse modes that
// request server-side rich formatting (tables, headings, formulas — the June
// 2026 "Rich Messages" feature). Sending them requires Telegram Premium and
// routes through make_rich_input / gogram's RichBuilder (SendRich, EditRich),
// not through BuildRich's entity parsing.
var richParseModes = map[string]bool{
	"rich":          true,
	"rich_md":       true,
	"rich_markdown": true,
	"rich_html":     true,
}

// IsRichParseMode reports whether mode is one of RICH_PARSE_MODES, matched
// case-insensitively like the Python checks do with parse_mode.lower().
func IsRichParseMode(mode string) bool {
	return richParseModes[strings.ToLower(mode)]
}

// BuildRich parses text according to parseMode and returns the message and
// entities for SendOptions, mirroring make_rich_input's routing and Telethon's
// parse_mode handling:
//
//   - "md"/"markdown": Telethon's markdown parser.
//   - "html"/"htm": Telethon's HTML parser.
//   - anything else, including "plain" and the rich_* modes: the text
//     verbatim with no entities, like Telethon's explicit parse_mode=None
//     ("plain" maps to None in the Python tools).
//
// The Python server raises ValueError for a non-empty unknown parse mode;
// BuildRich cannot return an error, so unsupported modes degrade to the plain
// behaviour. Callers that must reject rich modes check IsRichParseMode first,
// like the Python tools do against RICH_PARSE_MODES.
//
// Like Telethon, a non-empty input that parses to nothing (for example "```
// ```" with no content) yields Message == "" and no entities; callers that
// replicate Telethon's "Failed to parse message" guard compare Message with
// the input.
func BuildRich(text, parseMode string) RichInput {
	switch strings.ToLower(parseMode) {
	case "md", "markdown":
		message, entities := parseRichMarkdown(text)
		return RichInput{Message: message, Entities: entities, Plain: message}
	case "htm", "html":
		message, entities := parseRichHTML(text)
		return RichInput{Message: message, Entities: entities, Plain: message}
	default:
		return RichInput{Message: text, Entities: nil, Plain: text}
	}
}

// ---------------------------------------------------------------------------
// entity records
// ---------------------------------------------------------------------------

// richEntity is a pending entity while parsing: Telethon mutates offsets and
// lengths as it consumes delimiters, so the parser carries the payload in a
// builder closure and only materialises the telegram.MessageEntity at the end.
type richEntity struct {
	offset int32
	length int32
	build  func(offset, length int32) telegram.MessageEntity
}

// richMaterialize turns parser records into the final entity list. Zero-length
// entities are dropped, exactly like the send path in Telethon (#3884).
func richMaterialize(records []richEntity) []telegram.MessageEntity {
	var out []telegram.MessageEntity
	for _, r := range records {
		if r.length <= 0 {
			continue
		}
		out = append(out, r.build(r.offset, r.length))
	}
	return out
}

func richBoldEnt(o, l int32) telegram.MessageEntity {
	return &telegram.MessageEntityBold{Offset: o, Length: l}
}
func richItalicEnt(o, l int32) telegram.MessageEntity {
	return &telegram.MessageEntityItalic{Offset: o, Length: l}
}
func richUnderlineEnt(o, l int32) telegram.MessageEntity {
	return &telegram.MessageEntityUnderline{Offset: o, Length: l}
}
func richStrikeEnt(o, l int32) telegram.MessageEntity {
	return &telegram.MessageEntityStrike{Offset: o, Length: l}
}
func richCodeEnt(o, l int32) telegram.MessageEntity {
	return &telegram.MessageEntityCode{Offset: o, Length: l}
}
func richSpoilerEnt(o, l int32) telegram.MessageEntity {
	return &telegram.MessageEntitySpoiler{Offset: o, Length: l}
}
func richMentionEnt(o, l int32) telegram.MessageEntity {
	return &telegram.MessageEntityMention{Offset: o, Length: l}
}
func richEmailEnt(o, l int32) telegram.MessageEntity {
	return &telegram.MessageEntityEmail{Offset: o, Length: l}
}

func richPreEnt(language string) func(int32, int32) telegram.MessageEntity {
	return func(o, l int32) telegram.MessageEntity {
		return &telegram.MessageEntityPre{Offset: o, Length: l, Language: language}
	}
}

func richBlockquoteEnt(collapsed bool) func(int32, int32) telegram.MessageEntity {
	return func(o, l int32) telegram.MessageEntity {
		return &telegram.MessageEntityBlockquote{Collapsed: collapsed, Offset: o, Length: l}
	}
}

func richTextURLEnt(url string) func(int32, int32) telegram.MessageEntity {
	return func(o, l int32) telegram.MessageEntity {
		return &telegram.MessageEntityTextURL{Offset: o, Length: l, URL: url}
	}
}

func richEmojiEnt(id int64) func(int32, int32) telegram.MessageEntity {
	return func(o, l int32) telegram.MessageEntity {
		return &telegram.MessageEntityCustomEmoji{Offset: o, Length: l, DocumentID: id}
	}
}

func richMentionNameEnt(id int64) func(int32, int32) telegram.MessageEntity {
	return func(o, l int32) telegram.MessageEntity {
		return &telegram.InputMessageEntityMentionName{
			Offset: o,
			Length: l,
			UserID: &telegram.InputUserObj{UserID: id},
		}
	}
}

// richLinkEnt maps a link target to its entity. Telethon's send pipeline turns
// tg://user?id= links into mentions (after resolving the user; gogram's parser
// builds the same entity straight from the id) and gogram additionally maps
// tg://emoji links to custom emoji, so both forms work offline here.
func richLinkEnt(url string) func(int32, int32) telegram.MessageEntity {
	if rest, ok := strings.CutPrefix(url, "tg://user?id="); ok {
		if id, ok := richLeadingID(rest); ok {
			return richMentionNameEnt(id)
		}
	}
	if rest, ok := strings.CutPrefix(url, "tg://emoji?id="); ok {
		if id, ok := richLeadingID(rest); ok {
			return richEmojiEnt(id)
		}
	}
	return richTextURLEnt(url)
}

// richLeadingID returns the leading decimal digits of s, the shape Telethon's
// mention regex (tg://user\?id=(\d+)) accepts.
func richLeadingID(s string) (int64, bool) {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	if n == 0 {
		return 0, false
	}
	id, err := strconv.ParseInt(s[:n], 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// ---------------------------------------------------------------------------
// markdown parser
// ---------------------------------------------------------------------------

// richMarkdownDelimiter is one markdown delimiter; code marks the spans that
// suppress nested entities. Pre always carries language "" because Telethon's
// markdown parser never fills it, even for ```python fences.
type richMarkdownDelimiter struct {
	delim string
	code  bool
	build func(int32, int32) telegram.MessageEntity
}

// Order matters: the largest delimiter is tried first, exactly like Telethon's
// sorted-alternation regex, so "```" is never read as a single "`" span.
var richMarkdownDelimiters = []richMarkdownDelimiter{
	{"```", true, richPreEnt("")},
	{"**", false, richBoldEnt},
	{"__", false, richItalicEnt},
	{"~~", false, richStrikeEnt},
	{"||", false, richSpoilerEnt},
	{"`", true, richCodeEnt},
}

// parseRichMarkdown ports telethon/extensions/markdown.py at 1.45.0, working
// on UTF-16 code units so offsets match Telegram's convention. The delimiters
// are Telethon's (plus "||spoiler||" from the gogram/Bot-API dialect), the
// link syntax is Telethon's [label](url) — where this port also accepts the
// tg://user and tg://emoji targets richLinkEnt maps to entities.
func parseRichMarkdown(text string) (string, []telegram.MessageEntity) {
	msg := utf16.Encode([]rune(text))
	if len(msg) == 0 {
		return "", nil
	}

	msg, records := richStripBlockquotes(msg)

	i := 0
	for i < len(msg) {
		if delim, ok := richDelimiterAt(msg, i); ok {
			end := richIndex(msg, delim.delim, i+len(delim.delim)+1)
			if end != -1 {
				dl := len(delim.delim)
				msg = richSplice(msg, i, dl, end, dl)
				for idx := range records {
					e := &records[idx]
					if e.offset+e.length > int32(i) {
						if e.offset <= int32(i) && e.offset+e.length >= int32(end+dl) {
							e.length -= int32(2 * dl)
						} else {
							e.length -= int32(dl)
						}
					}
				}
				records = append(records, richEntity{
					offset: int32(i),
					length: int32(end - i - dl),
					build:  delim.build,
				})
				// No nested entities inside code blocks.
				if delim.code {
					i = end - dl
					continue
				}
				continue
			}
		} else if m, ok := richURLAt(msg, i); ok {
			msg = richReplaceSpan(msg, i, m.end, msg[m.labelStart:m.labelEnd])
			shrink := int32(m.end - i - (m.labelEnd - m.labelStart))
			for idx := range records {
				e := &records[idx]
				if e.offset+e.length > int32(i) {
					e.length -= shrink
				}
			}
			records = append(records, richEntity{
				offset: int32(i),
				length: int32(m.labelEnd - m.labelStart),
				build:  richLinkEnt(m.url),
			})
			i += m.labelEnd - m.labelStart
			continue
		}
		i++
	}

	msg, records = richStripText(msg, records)
	return richUnitsString(msg), richMaterialize(records)
}

func richDelimiterAt(msg []uint16, i int) (richMarkdownDelimiter, bool) {
	for _, d := range richMarkdownDelimiters {
		if richHasPrefix(msg[i:], d.delim) {
			return d, true
		}
	}
	return richMarkdownDelimiter{}, false
}

// richURLMatch is one [label](url) span (Telethon's DEFAULT_URL_RE).
type richURLMatch struct {
	labelStart int
	labelEnd   int
	end        int // one past the closing ')'
	url        string
}

// richURLAt matches Telethon's \[([^]]*?)\]\(([\s\S]*?)\) at position i: the
// label cannot contain ']', and the url runs to the first ')'.
func richURLAt(msg []uint16, i int) (richURLMatch, bool) {
	if msg[i] != '[' {
		return richURLMatch{}, false
	}
	j := i + 1
	for j < len(msg) && msg[j] != ']' {
		j++
	}
	if j+1 >= len(msg) || msg[j] != ']' || msg[j+1] != '(' {
		return richURLMatch{}, false
	}
	urlStart := j + 2
	k := urlStart
	for k < len(msg) && msg[k] != ')' {
		k++
	}
	if k >= len(msg) {
		return richURLMatch{}, false
	}
	return richURLMatch{
		labelStart: i + 1,
		labelEnd:   j,
		end:        k + 1,
		url:        richUnitsString(msg[urlStart:k]),
	}, true
}

// richStripBlockquotes implements the "> " / ">> " quote lines of the
// gogram/Bot-API markdown dialect (Telethon 1.45.0 has no blockquotes):
// consecutive quoted lines share one blockquote entity over their
// prefix-stripped content — ">> " marks a collapsed quote, "> " a normal one —
// and the prefix itself is removed from the text, like gogram's
// convertBlockquoteSyntax.
func richStripBlockquotes(src []uint16) ([]uint16, []richEntity) {
	var (
		out          []uint16
		records      []richEntity
		runStart     = -1
		runEnd       = -1
		runCollapsed bool
	)
	flush := func() {
		if runStart >= 0 && runEnd > runStart {
			records = append(records, richEntity{
				offset: int32(runStart),
				length: int32(runEnd - runStart),
				build:  richBlockquoteEnt(runCollapsed),
			})
		}
		runStart, runEnd = -1, -1
	}
	i := 0
	for i < len(src) {
		j := i
		for j < len(src) && src[j] != '\n' {
			j++
		}
		line := src[i:j]
		trimStart := 0
		for trimStart < len(line) && richIsPySpace(line[trimStart]) {
			trimStart++
		}
		trimEnd := len(line)
		for trimEnd > trimStart && richIsPySpace(line[trimEnd-1]) {
			trimEnd--
		}
		trimmed := line[trimStart:trimEnd]
		collapsed := false
		prefixLen := 0
		switch {
		case richHasPrefix(trimmed, ">> "):
			collapsed, prefixLen = true, 3
		case richHasPrefix(trimmed, "> "):
			prefixLen = 2
		}
		if prefixLen > 0 {
			if runStart >= 0 && runCollapsed != collapsed {
				flush()
			}
			if runStart < 0 {
				runStart = len(out)
				runCollapsed = collapsed
			}
			out = append(out, trimmed[prefixLen:]...)
			runEnd = len(out)
		} else {
			flush()
			out = append(out, line...)
		}
		if j < len(src) {
			out = append(out, '\n')
			i = j + 1
		} else {
			i = len(src)
		}
	}
	flush()
	return out, records
}

// ---------------------------------------------------------------------------
// HTML parser
// ---------------------------------------------------------------------------

type richToken struct {
	isTag     bool
	isClosing bool
	selfClose bool
	tagName   string
	attrs     map[string]string
	text      []uint16 // data chunk with character references already resolved
}

// parseRichHTML ports telethon/extensions/html.py at 1.45.0: the same tag set
// (plus <tg-spoiler>/<spoiler> and <mention> from the gogram dialect), the
// same bookkeeping for unclosed tags, the same trim, and the same entity order
// (close order reversed, then stable-sorted by offset).
func parseRichHTML(text string) (string, []telegram.MessageEntity) {
	if text == "" {
		return "", nil
	}
	tokens := richTokenizeHTML(utf16.Encode([]rune(text)))

	var (
		out      []uint16
		records  []richEntity
		openTags []string
		openMeta []*string
		building = map[string]*richEntity{}
	)

	// handleStart mirrors HTMLToTelegramParser.handle_starttag.
	handleStart := func(tok richToken) {
		tag := tok.tagName
		openTags = append([]string{tag}, openTags...)
		openMeta = append([]*string{nil}, openMeta...)

		var (
			build func(int32, int32) telegram.MessageEntity
			meta  *string
		)
		switch tag {
		case "strong", "b":
			build = richBoldEnt
		case "em", "i":
			build = richItalicEnt
		case "u":
			build = richUnderlineEnt
		case "del", "s":
			build = richStrikeEnt
		case "blockquote":
			_, expandable := tok.attrs["expandable"]
			collapsed := expandable
			if v, ok := tok.attrs["collapsed"]; ok {
				flag, err := strconv.ParseBool(v)
				if err != nil || flag {
					collapsed = true
				}
			}
			build = richBlockquoteEnt(collapsed)
		case "code":
			if pre, ok := building["pre"]; ok {
				// Syntax highlighting: a <code class="language-x"> inside
				// <pre> only names the language, like Telethon (which slices
				// the class blindly after len("language-")).
				if class, ok := tok.attrs["class"]; ok {
					language := ""
					if len(class) > len("language-") {
						language = class[len("language-"):]
					}
					pre.build = richPreEnt(language)
				}
			} else {
				build = richCodeEnt
			}
		case "pre":
			build = richPreEnt("")
		case "tg-spoiler", "spoiler":
			build = richSpoilerEnt
		case "mention":
			build = richMentionEnt
		case "a":
			href, ok := tok.attrs["href"]
			if !ok {
				return
			}
			if strings.HasPrefix(href, "mailto:") {
				address := strings.TrimPrefix(href, "mailto:")
				build = richEmailEnt
				meta = &address
			} else {
				build = richLinkEnt(href)
			}
			openMeta[0] = meta
		case "tg-emoji":
			id, err := strconv.ParseInt(tok.attrs["emoji-id"], 10, 64)
			if err != nil {
				return
			}
			build = richEmojiEnt(id)
		}
		if build != nil {
			if _, exists := building[tag]; !exists {
				building[tag] = &richEntity{offset: int32(len(out)), build: build}
			}
		}
	}

	// handleEnd mirrors handle_endtag, including its tolerance of mismatched
	// tags: the bookkeeping is popped regardless of the tag name.
	handleEnd := func(tag string) {
		if len(openTags) > 0 {
			openTags = openTags[1:]
		}
		if len(openMeta) > 0 {
			openMeta = openMeta[1:]
		}
		if rec, ok := building[tag]; ok {
			records = append(records, *rec)
			delete(building, tag)
		}
	}

	// handleData mirrors handle_data: an <a> with an email href replaces the
	// link text with the address, and every open entity grows by the chunk.
	handleData := func(data []uint16) {
		if len(openTags) > 0 && openTags[0] == "a" &&
			len(openMeta) > 0 && openMeta[0] != nil && *openMeta[0] != "" {
			data = utf16.Encode([]rune(*openMeta[0]))
		}
		for _, rec := range building {
			rec.length += int32(len(data))
		}
		out = append(out, data...)
	}

	for _, tok := range tokens {
		if !tok.isTag {
			handleData(tok.text)
			continue
		}
		if tok.isClosing {
			handleEnd(tok.tagName)
			continue
		}
		handleStart(tok)
		if tok.selfClose {
			handleEnd(tok.tagName)
		}
	}

	out, records = richStripText(out, records)
	for l, r := 0, len(records)-1; l < r; l, r = l+1, r-1 {
		records[l], records[r] = records[r], records[l]
	}
	sort.SliceStable(records, func(a, b int) bool {
		return records[a].offset < records[b].offset
	})
	return richUnitsString(out), richMaterialize(records)
}

// richTokenizeHTML is a lenient tokenizer with python html.parser's relevant
// behaviour: lowercased tag and attribute names, character references resolved
// in data and attribute values, comments/declarations skipped, and a lone "<"
// treated as data.
func richTokenizeHTML(src []uint16) []richToken {
	var tokens []richToken
	i := 0
	for i < len(src) {
		if src[i] != '<' {
			j := i
			for j < len(src) && src[j] != '<' {
				j++
			}
			tokens = append(tokens, richToken{text: richDecodeCharRefs(src[i:j])})
			i = j
			continue
		}
		if !richTagStart(src, i) {
			tokens = append(tokens, richToken{text: []uint16{'<'}})
			i++
			continue
		}
		switch {
		case richHasPrefix(src[i:], "<!--"):
			end := richIndex(src, "-->", i+4)
			if end == -1 {
				return tokens
			}
			i = end + 3
			continue
		case richHasPrefix(src[i:], "<![CDATA["):
			end := richIndex(src, "]]>", i+9)
			if end == -1 {
				tokens = append(tokens, richToken{text: src[i+9:]})
				return tokens
			}
			tokens = append(tokens, richToken{text: src[i+9 : end]})
			i = end + 3
			continue
		case src[i+1] == '!' || src[i+1] == '?':
			end := richIndex(src, ">", i+2)
			if end == -1 {
				return tokens
			}
			i = end + 1
			continue
		}
		end := richIndex(src, ">", i+1)
		if end == -1 {
			// An unterminated tag: the rest is data, like html.parser.
			tokens = append(tokens, richToken{text: richDecodeCharRefs(src[i:])})
			return tokens
		}
		tokens = append(tokens, richParseTag(src[i+1:end]))
		i = end + 1
	}
	return tokens
}

func richParseTag(body []uint16) richToken {
	tok := richToken{isTag: true}
	j := 0
	if j < len(body) && body[j] == '/' {
		tok.isClosing = true
		j++
	}
	nameStart := j
	for j < len(body) && richIsTagNameUnit(body[j]) {
		j++
	}
	tok.tagName = strings.ToLower(richUnitsString(body[nameStart:j]))
	if tok.isClosing {
		return tok
	}
	tok.attrs = map[string]string{}
	for j < len(body) {
		for j < len(body) && richIsHTMLSpace(body[j]) {
			j++
		}
		if j >= len(body) {
			break
		}
		if body[j] == '/' {
			tok.selfClose = true
			j++
			continue
		}
		attrStart := j
		for j < len(body) && body[j] != '=' && !richIsHTMLSpace(body[j]) && body[j] != '/' {
			j++
		}
		name := strings.ToLower(richUnitsString(body[attrStart:j]))
		if name == "" {
			j++
			continue
		}
		value := ""
		for j < len(body) && richIsHTMLSpace(body[j]) {
			j++
		}
		if j < len(body) && body[j] == '=' {
			j++
			for j < len(body) && richIsHTMLSpace(body[j]) {
				j++
			}
			if j < len(body) && (body[j] == '"' || body[j] == '\'') {
				quote := body[j]
				j++
				valueStart := j
				for j < len(body) && body[j] != quote {
					j++
				}
				value = richUnitsString(richDecodeCharRefs(body[valueStart:j]))
				if j < len(body) {
					j++
				}
			} else {
				valueStart := j
				for j < len(body) && !richIsHTMLSpace(body[j]) {
					j++
				}
				value = richUnitsString(richDecodeCharRefs(body[valueStart:j]))
			}
		}
		tok.attrs[name] = value
	}
	return tok
}

// richDecodeCharRefs resolves semicolon-terminated numeric and the common
// named character references, the shape Telegram markup uses.
func richDecodeCharRefs(units []uint16) []uint16 {
	hasAmp := false
	for _, u := range units {
		if u == '&' {
			hasAmp = true
			break
		}
	}
	if !hasAmp {
		return units
	}
	var out []uint16
	i := 0
	for i < len(units) {
		if units[i] != '&' {
			out = append(out, units[i])
			i++
			continue
		}
		end := -1
		limit := i + 1 + 32
		if limit > len(units) {
			limit = len(units)
		}
		for k := i + 1; k < limit; k++ {
			if units[k] == ';' {
				end = k
				break
			}
		}
		if end == -1 {
			out = append(out, '&')
			i++
			continue
		}
		if r, ok := richCharRefValue(units[i+1 : end]); ok {
			out = append(out, utf16.Encode([]rune{r})...)
		} else {
			out = append(out, units[i:end+1]...)
		}
		i = end + 1
	}
	return out
}

var richNamedCharRefs = map[string]rune{
	"amp":  '&',
	"lt":   '<',
	"gt":   '>',
	"quot": '"',
	"apos": '\'',
	"nbsp": '\u00a0',
}

func richCharRefValue(body []uint16) (rune, bool) {
	if len(body) == 0 {
		return 0, false
	}
	if body[0] == '#' {
		base := 10
		digits := body[1:]
		if len(digits) > 0 && (digits[0] == 'x' || digits[0] == 'X') {
			base = 16
			digits = digits[1:]
		}
		if len(digits) == 0 {
			return 0, false
		}
		value, err := strconv.ParseUint(richUnitsString(digits), base, 32)
		if err != nil || value > 0x10FFFF || (value >= 0xD800 && value <= 0xDFFF) {
			return 0, false
		}
		return rune(value), true
	}
	if r, ok := richNamedCharRefs[richUnitsString(body)]; ok {
		return r, true
	}
	return 0, false
}

func richTagStart(src []uint16, i int) bool {
	if i+1 >= len(src) {
		return false
	}
	c := src[i+1]
	if richIsASCIILetter(c) || c == '!' || c == '?' {
		return true
	}
	return c == '/' && i+2 < len(src) && richIsASCIILetter(src[i+2])
}

func richIsASCIILetter(u uint16) bool {
	return (u >= 'a' && u <= 'z') || (u >= 'A' && u <= 'Z')
}

func richIsTagNameUnit(u uint16) bool {
	return richIsASCIILetter(u) || (u >= '0' && u <= '9') || u == '-'
}

func richIsHTMLSpace(u uint16) bool {
	switch u {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// shared parsing helpers (UTF-16 code-unit view of the message)
// ---------------------------------------------------------------------------

// richStripText ports telethon/helpers.py strip_text: it trims the message and
// shifts, truncates or drops the entities that touch the trimmed region.
func richStripText(text []uint16, entities []richEntity) ([]uint16, []richEntity) {
	if len(entities) == 0 {
		return richTrimRight(richTrimLeft(text)), nil
	}
	lenOri := len(text)
	text = richTrimLeft(text)
	leftOffset := lenOri - len(text)
	text = richTrimRight(text)
	lenFinal := len(text)

	kept := make([]richEntity, 0, len(entities))
	for _, e := range entities {
		if e.length == 0 {
			continue
		}
		if e.offset+e.length > int32(leftOffset) {
			if e.offset >= int32(leftOffset) {
				e.offset -= int32(leftOffset)
			} else {
				e.length = e.offset + e.length - int32(leftOffset)
				e.offset = 0
			}
		} else {
			continue
		}
		if int(e.offset+e.length) <= lenFinal {
			kept = append(kept, e)
			continue
		}
		if int(e.offset) >= lenFinal {
			continue
		}
		e.length = int32(lenFinal) - e.offset
		kept = append(kept, e)
	}
	return text, kept
}

// richSplice removes [first, first+firstLen) and [second, second+secondLen)
// from msg; both ranges are disjoint and in order.
func richSplice(msg []uint16, first, firstLen, second, secondLen int) []uint16 {
	out := make([]uint16, 0, len(msg)-firstLen-secondLen)
	out = append(out, msg[:first]...)
	out = append(out, msg[first+firstLen:second]...)
	out = append(out, msg[second+secondLen:]...)
	return out
}

// richReplaceSpan returns msg with [start, end) replaced by replacement.
func richReplaceSpan(msg []uint16, start, end int, replacement []uint16) []uint16 {
	out := make([]uint16, 0, len(msg)-(end-start)+len(replacement))
	out = append(out, msg[:start]...)
	out = append(out, replacement...)
	out = append(out, msg[end:]...)
	return out
}

// richIndex finds s in units at or after from; s is ASCII.
func richIndex(units []uint16, s string, from int) int {
	if s == "" || from < 0 {
		return -1
	}
	for i := from; i+len(s) <= len(units); i++ {
		if richHasPrefix(units[i:], s) {
			return i
		}
	}
	return -1
}

func richHasPrefix(units []uint16, s string) bool {
	if len(units) < len(s) {
		return false
	}
	for k := 0; k < len(s); k++ {
		if units[k] != uint16(s[k]) {
			return false
		}
	}
	return true
}

func richUnitsString(units []uint16) string {
	return string(utf16.Decode(units))
}

// richIsPySpace matches Python's str whitespace for stripping: unicode
// whitespace plus the U+001C..U+001F separators Go's unicode.IsSpace omits.
func richIsPySpace(u uint16) bool {
	if u >= 0x1C && u <= 0x1F {
		return true
	}
	return unicode.IsSpace(rune(u))
}

func richTrimLeft(units []uint16) []uint16 {
	i := 0
	for i < len(units) && richIsPySpace(units[i]) {
		i++
	}
	return units[i:]
}

func richTrimRight(units []uint16) []uint16 {
	j := len(units)
	for j > 0 && richIsPySpace(units[j-1]) {
		j--
	}
	return units[:j]
}

// ---------------------------------------------------------------------------
// rich message reading
// ---------------------------------------------------------------------------

// richTextWalkFields is the field order of runtime._RICH_TEXT_FIELDS.
var richTextWalkFields = []string{"Texts", "Text", "Alt", "Source"}

// richPageWalkFields is the field order of runtime._PAGE_TEXT_FIELDS.
var richPageWalkFields = []string{
	"Title", "Subtitle", "Author", "Text", "Caption",
	"Credit", "Items", "Blocks", "Rows", "Articles",
}

// RichTextToStr ports runtime.rich_text_to_str: it flattens one RichText node
// into plain text. The walk dispatches on the field rather than on the node
// type, so a node type a future gogram adds still flattens instead of
// vanishing. TextCustomEmoji contributes its alt character — dropping it would
// silently eat the emoji a channel used as a bullet or a heading marker. Text
// nodes that carry no text (TextEmpty, TextImage) yield "".
func RichTextToStr(node any) string {
	if node == nil {
		return ""
	}
	if s, ok := node.(string); ok {
		return s
	}
	rv := reflect.ValueOf(node)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return ""
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		var b strings.Builder
		for i := 0; i < rv.Len(); i++ {
			b.WriteString(RichTextToStr(rv.Index(i).Interface()))
		}
		return b.String()
	case reflect.Struct:
		for _, name := range richTextWalkFields {
			f := rv.FieldByName(name)
			if !f.IsValid() || !f.CanInterface() || richIsNilValue(f) {
				continue
			}
			return RichTextToStr(f.Interface())
		}
	}
	return ""
}

// richPageLines ports runtime._page_lines: the text lines carried by a page
// block, list item, table row or caption. A table row reads as one line, not
// one line per cell.
func richPageLines(node any) []string {
	if node == nil {
		return nil
	}
	if _, ok := node.(telegram.RichText); ok {
		text := strings.TrimSpace(RichTextToStr(node))
		if text != "" {
			return []string{text}
		}
		return nil
	}
	rv := reflect.ValueOf(node)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		var lines []string
		for i := 0; i < rv.Len(); i++ {
			lines = append(lines, richPageLines(rv.Index(i).Interface())...)
		}
		return lines
	case reflect.Struct:
		if cells := rv.FieldByName("Cells"); cells.IsValid() && cells.CanInterface() &&
			(cells.Kind() == reflect.Slice || cells.Kind() == reflect.Array) {
			var rows []string
			for i := 0; i < cells.Len(); i++ {
				rows = append(rows, richPageLines(cells.Index(i).Interface())...)
			}
			row := strings.Join(rows, " | ")
			if row == "" {
				return nil
			}
			return []string{row}
		}
		var lines []string
		for _, name := range richPageWalkFields {
			f := rv.FieldByName(name)
			if !f.IsValid() || !f.CanInterface() || richIsNilValue(f) {
				continue
			}
			lines = append(lines, richPageLines(f.Interface())...)
		}
		return lines
	}
	return nil
}

// RichMessageText ports runtime.rich_message_text: the plain text of a rich
// (block-format) message, "" when there is none. Each block becomes a
// paragraph and the lines within one block stay together, so a list reads as a
// list instead of one run-on line. An unknown block type yields nothing rather
// than breaking the whole message.
//
// msg is anything carrying the rich message — a gogram message with a
// RichMessage field, or the *telegram.RichMessage itself.
func RichMessageText(msg any) string {
	rich := richMessageOf(msg)
	if rich == nil {
		return ""
	}
	blocks := richBlocksOf(rich)
	paragraphs := make([]string, 0, len(blocks))
	for _, block := range blocks {
		paragraph := strings.Join(richPageLines(block), "\n")
		if paragraph != "" {
			paragraphs = append(paragraphs, paragraph)
		}
	}
	return strings.Join(paragraphs, "\n\n")
}

// richMessageOf finds the rich message on a message-shaped value, the
// getattr(msg, "rich_message", None) of the Python walk.
func richMessageOf(msg any) any {
	if msg == nil {
		return nil
	}
	if rich, ok := msg.(*telegram.RichMessage); ok {
		return rich
	}
	rv := reflect.ValueOf(msg)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	f := rv.FieldByName("RichMessage")
	if !f.IsValid() || !f.CanInterface() || richIsNilValue(f) {
		return nil
	}
	return f.Interface()
}

// richBlocksOf reads the Blocks slice off a rich message.
func richBlocksOf(rich any) []any {
	rv := reflect.ValueOf(rich)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	f := rv.FieldByName("Blocks")
	if !f.IsValid() || !f.CanInterface() ||
		(f.Kind() != reflect.Slice && f.Kind() != reflect.Array) {
		return nil
	}
	out := make([]any, 0, f.Len())
	for i := 0; i < f.Len(); i++ {
		out = append(out, f.Index(i).Interface())
	}
	return out
}

func richIsNilValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Interface, reflect.Ptr, reflect.Slice, reflect.Map, reflect.Func, reflect.Chan:
		return v.IsNil()
	}
	return false
}
