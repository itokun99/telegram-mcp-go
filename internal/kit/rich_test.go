package kit

// Tests for rich.go. Cases come from two sources:
//
//   - Python test cases under tests/ that assert rendered strings or parse_mode
//     semantics; each such Go test names its Python test.
//   - Telethon 1.45.0 oracle values (the parser the Python server calls, pinned
//     in uv.lock). Those tables are labelled where no Python test asserts them
//     directly.

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	telegram "github.com/amarnathcjd/gogram/telegram"
)

// entShape is a comparable view of a telegram.MessageEntity.
type entShape struct {
	Kind   string
	Offset int32
	Length int32
	Extra  string
}

func richShapeOf(e telegram.MessageEntity) entShape {
	switch v := e.(type) {
	case *telegram.MessageEntityBold:
		return entShape{"bold", v.Offset, v.Length, ""}
	case *telegram.MessageEntityItalic:
		return entShape{"italic", v.Offset, v.Length, ""}
	case *telegram.MessageEntityUnderline:
		return entShape{"underline", v.Offset, v.Length, ""}
	case *telegram.MessageEntityStrike:
		return entShape{"strike", v.Offset, v.Length, ""}
	case *telegram.MessageEntityCode:
		return entShape{"code", v.Offset, v.Length, ""}
	case *telegram.MessageEntityPre:
		return entShape{"pre", v.Offset, v.Length, v.Language}
	case *telegram.MessageEntitySpoiler:
		return entShape{"spoiler", v.Offset, v.Length, ""}
	case *telegram.MessageEntityMention:
		return entShape{"mention", v.Offset, v.Length, ""}
	case *telegram.MessageEntityEmail:
		return entShape{"email", v.Offset, v.Length, ""}
	case *telegram.MessageEntityTextURL:
		return entShape{"text_url", v.Offset, v.Length, v.URL}
	case *telegram.MessageEntityBlockquote:
		return entShape{"blockquote", v.Offset, v.Length, strconv.FormatBool(v.Collapsed)}
	case *telegram.MessageEntityCustomEmoji:
		return entShape{"custom_emoji", v.Offset, v.Length, strconv.FormatInt(v.DocumentID, 10)}
	case *telegram.InputMessageEntityMentionName:
		id := int64(0)
		if user, ok := v.UserID.(*telegram.InputUserObj); ok {
			id = user.UserID
		}
		return entShape{"mention_name", v.Offset, v.Length, strconv.FormatInt(id, 10)}
	default:
		return entShape{fmt.Sprintf("%T", e), 0, 0, ""}
	}
}

func richShapesOf(entities []telegram.MessageEntity) []entShape {
	out := make([]entShape, 0, len(entities))
	for _, e := range entities {
		out = append(out, richShapeOf(e))
	}
	return out
}

func assertRichInput(t *testing.T, got RichInput, wantMessage string, wantEntities []entShape) {
	t.Helper()
	if got.Message != wantMessage {
		t.Errorf("Message = %q, want %q", got.Message, wantMessage)
	}
	if got.Plain != got.Message {
		t.Errorf("Plain = %q, want the rendered message %q", got.Plain, got.Message)
	}
	gotShapes := richShapesOf(got.Entities)
	if len(gotShapes) != len(wantEntities) {
		t.Fatalf("entities = %v, want %v", gotShapes, wantEntities)
	}
	for i := range gotShapes {
		if gotShapes[i] != wantEntities[i] {
			t.Errorf("entity[%d] = %+v, want %+v", i, gotShapes[i], wantEntities[i])
		}
	}
}

// Python: tests/test_custom_emojis.py::test_discovered_emoji_can_be_reused_by_existing_html_path
func TestRichBuildHTMLCustomEmojiReuse(t *testing.T) {
	markup := "New <tg-emoji emoji-id=\"5368324170671202286\">🍷</tg-emoji>"
	got := BuildRich(markup, "html")
	assertRichInput(t, got, "New 🍷", []entShape{
		{"custom_emoji", 4, 2, "5368324170671202286"},
	})
}

// Python: tests/test_scheduled_message_parse_mode.py::test_plain_disables_parsing
// and ::test_rich_modes_rejected.
func TestRichBuildPlainFallback(t *testing.T) {
	cases := []struct {
		text string
		mode string
	}{
		{"2 * 3 = 6", "plain"},
		{"2 * 3 = 6", "PLAIN"},
		{"2 * 3 = 6", ""},
		{"2 * 3 = 6", "plain-marker"},
		{"# title", "rich"},
		{"# title", "rich_md"},
		{"# title", "rich_markdown"},
		{"# title", "RICH_HTML"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			got := BuildRich(tc.text, tc.mode)
			assertRichInput(t, got, tc.text, nil)
		})
	}
}

// Python: tests/test_scheduled_message_parse_mode.py::test_parse_mode_is_forwarded
// ("**hi**" under md/markdown/html) and
// tests/test_rich_messages.py::test_edit_message_omits_parse_mode_when_not_given
// ("**bold**" reaches the default markdown parser).
func TestRichBuildParseModeForwarding(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		mode     string
		want     string
		entities []entShape
	}{
		{"md", "**hi**", "md", "hi", []entShape{{"bold", 0, 2, ""}}},
		{"markdown", "**hi**", "markdown", "hi", []entShape{{"bold", 0, 2, ""}}},
		{"markdown-uppercase", "**hi**", "Markdown", "hi", []entShape{{"bold", 0, 2, ""}}},
		{"html-keeps-markdown-letters", "**hi**", "html", "**hi**", nil},
		{"edit-default-parser", "**bold**", "markdown", "bold", []entShape{{"bold", 0, 4, ""}}},
		{"html", "<b>hi</b>", "html", "hi", []entShape{{"bold", 0, 2, ""}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRichInput(t, BuildRich(tc.text, tc.mode), tc.want, tc.entities)
		})
	}
}

// Python: tests/test_custom_emojis.py::test_original_utf16_offsets_and_compound_fallback_are_preserved
// and tests/test_date_chip.py::test_offsets_are_utf16_code_units.
func TestRichBuildUTF16Offsets(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		mode     string
		want     string
		entities []entShape
	}{
		{"astral-char-counts-two", "<b>😀 x</b>", "html", "😀 x", []entShape{{"bold", 0, 4, ""}}},
		{"markdown-after-emoji", "😀 **жирный**", "markdown", "😀 жирный", []entShape{{"bold", 3, 6, ""}}},
		{"compound-emoji-sequence", "<tg-emoji emoji-id=\"1\">👩🏽‍💻</tg-emoji>", "html", "👩🏽‍💻", []entShape{{"custom_emoji", 0, 7, "1"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRichInput(t, BuildRich(tc.text, tc.mode), tc.want, tc.entities)
		})
	}
}

// Telethon-oracle parity for markdown: values generated with telethon 1.45.0
// (telethon/extensions/markdown.py, the parser the Python server calls). No
// Python test asserts these renderings directly.
func TestRichBuildMarkdownTelethonParity(t *testing.T) {
	cases := []struct {
		text     string
		want     string
		entities []entShape
	}{
		{"**bold**", "bold", []entShape{{"bold", 0, 4, ""}}},
		{"__it__", "it", []entShape{{"italic", 0, 2, ""}}},
		{"~~st~~", "st", []entShape{{"strike", 0, 2, ""}}},
		{"`code`", "code", []entShape{{"code", 0, 4, ""}}},
		{"```\npre\n```", "pre", []entShape{{"pre", 0, 3, ""}}},
		{"```python\nprint(1)\n```", "python\nprint(1)", []entShape{{"pre", 0, 15, ""}}},
		{"[text](https://example.com)", "text", []entShape{{"text_url", 0, 4, "https://example.com"}}},
		{"a **b** c **d** e", "a b c d e", []entShape{{"bold", 2, 1, ""}, {"bold", 6, 1, ""}}},
		{"**a **b** c**", "a b c", []entShape{{"bold", 0, 2, ""}, {"bold", 3, 2, ""}}},
		{"`a` and `b`", "a and b", []entShape{{"code", 0, 1, ""}, {"code", 6, 1, ""}}},
		{"**__x__**", "x", []entShape{{"bold", 0, 1, ""}, {"italic", 0, 1, ""}}},
		{"****", "****", nil},
		{"**x", "**x", nil},
		{"x**", "x**", nil},
		{"[a](b) [c](d)", "a c", []entShape{{"text_url", 0, 1, "b"}, {"text_url", 2, 1, "d"}}},
		{"[x]()", "x", []entShape{{"text_url", 0, 1, ""}}},
		{"[[x]](y)", "[[x]](y)", nil},
		{"[a]b](c)", "[a]b](c)", nil},
		{"` `` `", "", nil},
		{"<b>x</b>y", "<b>x</b>y", nil},
		{"plain", "plain", nil},
		{"", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			assertRichInput(t, BuildRich(tc.text, "markdown"), tc.want, tc.entities)
		})
	}
}

// Telethon-oracle parity for HTML: same source and caveat as the markdown
// table above (telethon/extensions/html.py).
func TestRichBuildHTMLTelethonParity(t *testing.T) {
	cases := []struct {
		text     string
		want     string
		entities []entShape
	}{
		{"<b>bold</b>", "bold", []entShape{{"bold", 0, 4, ""}}},
		{"<strong>s</strong>", "s", []entShape{{"bold", 0, 1, ""}}},
		{"<i>i</i>", "i", []entShape{{"italic", 0, 1, ""}}},
		{"<em>e</em>", "e", []entShape{{"italic", 0, 1, ""}}},
		{"<u>u</u>", "u", []entShape{{"underline", 0, 1, ""}}},
		{"<s>s</s>", "s", []entShape{{"strike", 0, 1, ""}}},
		{"<del>d</del>", "d", []entShape{{"strike", 0, 1, ""}}},
		{"<code>c</code>", "c", []entShape{{"code", 0, 1, ""}}},
		{"<pre>p</pre>", "p", []entShape{{"pre", 0, 1, ""}}},
		{"<pre><code class=\"language-go\">x</code></pre>", "x", []entShape{{"pre", 0, 1, "go"}}},
		{"<a href=\"https://x\">link</a>", "link", []entShape{{"text_url", 0, 4, "https://x"}}},
		{"<a href=\"mailto:a@b.c\">mail</a>", "a@b.c", []entShape{{"email", 0, 5, ""}}},
		{"<blockquote>q</blockquote>", "q", []entShape{{"blockquote", 0, 1, "false"}}},
		{"<blockquote expandable>q</blockquote>", "q", []entShape{{"blockquote", 0, 1, "true"}}},
		{"<b>a<i>b</i>c</b>", "abc", []entShape{{"bold", 0, 3, ""}, {"italic", 1, 1, ""}}},
		{"<b>unclosed", "unclosed", nil},
		{" x ", "x", nil},
		{"<b>x</b> y", "x y", []entShape{{"bold", 0, 1, ""}}},
		{"<b>😀</b>", "😀", []entShape{{"bold", 0, 2, ""}}},
		{"<b></b>", "", nil},
		{"<b>  </b>", "", nil},
		{"a<br>b", "ab", nil},
		{"<b>a</b><b>b</b>", "ab", []entShape{{"bold", 0, 1, ""}, {"bold", 1, 1, ""}}},
		{"<b>a<b>b</b>c</b>", "abc", []entShape{{"bold", 0, 2, ""}}},
		{"a &amp; b &lt;c&gt;", "a & b <c>", nil},
		{"<b>bold</b> ", "bold", []entShape{{"bold", 0, 4, ""}}},
		{"  <b>x</b>  ", "x", []entShape{{"bold", 0, 1, ""}}},
		{"plain", "plain", nil},
		{"", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			assertRichInput(t, BuildRich(tc.text, "html"), tc.want, tc.entities)
		})
	}
}

// Markup the Go stack speaks in gogram's own parser but Telethon 1.45.0
// predates: spoiler tags, ||spoiler||, quote lines, the <mention> tag, and
// tg:// links mapping straight to mention / custom-emoji entities.
func TestRichBuildMarkupExtensions(t *testing.T) {
	cases := []struct {
		text     string
		mode     string
		want     string
		entities []entShape
	}{
		{"<tg-spoiler>s</tg-spoiler>", "html", "s", []entShape{{"spoiler", 0, 1, ""}}},
		{"<spoiler>s</spoiler>", "html", "s", []entShape{{"spoiler", 0, 1, ""}}},
		{"||s||", "markdown", "s", []entShape{{"spoiler", 0, 1, ""}}},
		{"<a href=\"tg://user?id=42\">u</a>", "html", "u", []entShape{{"mention_name", 0, 1, "42"}}},
		{"[u](tg://user?id=42)", "markdown", "u", []entShape{{"mention_name", 0, 1, "42"}}},
		{"<a href=\"tg://emoji?id=7\">e</a>", "html", "e", []entShape{{"custom_emoji", 0, 1, "7"}}},
		{"[e](tg://emoji?id=7)", "markdown", "e", []entShape{{"custom_emoji", 0, 1, "7"}}},
		{"<mention>@x</mention>", "html", "@x", []entShape{{"mention", 0, 2, ""}}},
		{"> q", "markdown", "q", []entShape{{"blockquote", 0, 1, "false"}}},
		{">> q", "markdown", "q", []entShape{{"blockquote", 0, 1, "true"}}},
		{"> a\n> b", "markdown", "a\nb", []entShape{{"blockquote", 0, 3, "false"}}},
		{"x\n> a\n> b\ny", "markdown", "x\na\nb\ny", []entShape{{"blockquote", 2, 3, "false"}}},
		{">> a\n> b", "markdown", "a\nb", []entShape{
			{"blockquote", 0, 1, "true"},
			{"blockquote", 2, 1, "false"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.mode+"/"+tc.text, func(t *testing.T) {
			assertRichInput(t, BuildRich(tc.text, tc.mode), tc.want, tc.entities)
		})
	}
}

// Python: tests/test_scheduled_message_parse_mode.py::test_rich_modes_rejected
// (rich/rich_md/rich_markdown/RICH_HTML are RICH_PARSE_MODES in runtime.py).
func TestIsRichParseMode(t *testing.T) {
	for _, mode := range []string{"rich", "rich_md", "rich_markdown", "rich_html", "RICH_HTML"} {
		if !IsRichParseMode(mode) {
			t.Errorf("IsRichParseMode(%q) = false, want true", mode)
		}
	}
	for _, mode := range []string{"", "plain", "md", "markdown", "html", "htm"} {
		if IsRichParseMode(mode) {
			t.Errorf("IsRichParseMode(%q) = true, want false", mode)
		}
	}
}

// No Python test; hostile inputs must not panic and must keep the
// Plain == Message invariant.
func TestRichBuildRobustness(t *testing.T) {
	inputs := []string{
		"[", "<", "</", "**", "||", ">>>>", "<a href=", "```", "<b", "\x00",
		"<tg-emoji emoji-id=\"x\">e</tg-emoji>", "<blockquote collapsed=\"maybe\">q</blockquote>",
		strings.Repeat("*", 64), strings.Repeat("> ", 32),
	}
	for _, mode := range []string{"markdown", "html", "plain"} {
		for _, text := range inputs {
			got := BuildRich(text, mode)
			if got.Plain != got.Message {
				t.Errorf("BuildRich(%q, %q): Plain = %q, Message = %q", text, mode, got.Plain, got.Message)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// reading rich messages
// ---------------------------------------------------------------------------

// richCarrier mimics the Python tests' SimpleNamespace(rich_message=...).
type richCarrier struct {
	RichMessage *telegram.RichMessage
}

// unknownRichBlock stands for a page-block type a future Telegram adds, the
// SimpleNamespace() of the Python tests.
type unknownRichBlock struct {
	telegram.PageBlockDivider
}

func richText(text string) telegram.RichText {
	return &telegram.TextPlain{Text: text}
}

// Python: tests/test_rich_messages.py::test_rich_text_flattens_every_nesting_shape
func TestRichTextToStrFlattensEveryNestingShape(t *testing.T) {
	node := &telegram.TextConcat{Texts: []telegram.RichText{
		richText("plain "),
		&telegram.TextBold{Text: richText("bold ")},
		&telegram.TextURL{Text: richText("link"), URL: "https://example.com"},
		&telegram.TextCustomEmoji{DocumentID: 1, Alt: "🧠"},
		&telegram.TextEmpty{},
	}}
	if got := RichTextToStr(node); got != "plain bold link🧠" {
		t.Errorf("RichTextToStr = %q, want %q", got, "plain bold link🧠")
	}
}

// Python: tests/test_rich_messages.py::test_rich_text_ignores_nodes_that_carry_no_text
func TestRichTextToStrIgnoresNodesWithoutText(t *testing.T) {
	for _, node := range []any{
		&telegram.TextEmpty{},
		&telegram.TextImage{DocumentID: 1, W: 10, H: 10},
		nil,
	} {
		if got := RichTextToStr(node); got != "" {
			t.Errorf("RichTextToStr(%T) = %q, want %q", node, got, "")
		}
	}
}

// rich node kinds that carry words in their Text field (mention and hashtag
// among them).
func TestRichTextToStrTextCarryingKinds(t *testing.T) {
	cases := []struct {
		node any
		want string
	}{
		{&telegram.TextPlain{Text: "plain"}, "plain"},
		{&telegram.TextMention{Text: richText("@user")}, "@user"},
		{&telegram.TextHashtag{Text: richText("#tag")}, "#tag"},
		{&telegram.TextURL{Text: richText("site"), URL: "https://x"}, "site"},
		{&telegram.TextCustomEmoji{DocumentID: 7, Alt: "🍷"}, "🍷"},
	}
	for _, tc := range cases {
		if got := RichTextToStr(tc.node); got != tc.want {
			t.Errorf("RichTextToStr(%T) = %q, want %q", tc.node, got, tc.want)
		}
	}
}

// Python: tests/test_rich_messages.py::test_rich_message_text_walks_blocks
// (the shape a real block-format post arrives in).
func TestRichMessageTextWalksBlocks(t *testing.T) {
	rich := &telegram.RichMessage{Blocks: []telegram.PageBlock{
		&telegram.PageBlockHeading1{Text: &telegram.TextConcat{Texts: []telegram.RichText{
			richText("Личная CRM "),
			&telegram.TextCustomEmoji{DocumentID: 5927026418616636353, Alt: "🧠"},
		}}},
		&telegram.PageBlockPhoto{
			PhotoID: 5197653938998550234,
			Caption: &telegram.PageCaption{Text: &telegram.TextEmpty{}, Credit: &telegram.TextEmpty{}},
		},
		&telegram.PageBlockParagraph{Text: richText("Первый абзац.")},
		&telegram.PageBlockParagraph{Text: richText("Второй абзац.")},
	}}
	want := "Личная CRM 🧠\n\nПервый абзац.\n\nВторой абзац."
	if got := RichMessageText(richCarrier{RichMessage: rich}); got != want {
		t.Errorf("RichMessageText = %q, want %q", got, want)
	}
}

// Python: tests/test_rich_messages.py::test_rich_message_text_reaches_captions_lists_tables_and_details
func TestRichMessageTextReachesCaptionsListsTablesDetails(t *testing.T) {
	rich := &telegram.RichMessage{Blocks: []telegram.PageBlock{
		&telegram.PageBlockVideo{
			VideoID: 1,
			Caption: &telegram.PageCaption{Text: richText("под видео"), Credit: &telegram.TextEmpty{}},
		},
		&telegram.PageBlockList{Items: []telegram.PageListItem{
			&telegram.PageListItemText{Text: richText("первый")},
			&telegram.PageListItemBlocks{Blocks: []telegram.PageBlock{
				&telegram.PageBlockParagraph{Text: richText("второй")},
			}},
		}},
		&telegram.PageBlockTable{Title: &telegram.TextEmpty{}, Rows: []*telegram.PageTableRow{
			{Cells: []*telegram.PageTableCell{
				{Text: richText("A")},
				{Text: richText("B")},
			}},
		}},
		&telegram.PageBlockDetails{Title: richText("подробнее"), Blocks: []telegram.PageBlock{
			&telegram.PageBlockParagraph{Text: richText("скрытое")},
		}},
	}}
	want := "под видео\n\nпервый\nвторой\n\nA | B\n\nподробнее\nскрытое"
	if got := RichMessageText(richCarrier{RichMessage: rich}); got != want {
		t.Errorf("RichMessageText = %q, want %q", got, want)
	}
}

// Python: tests/test_rich_messages.py::test_rich_message_text_skips_unknown_blocks_instead_of_failing
func TestRichMessageTextSkipsUnknownBlocks(t *testing.T) {
	rich := &telegram.RichMessage{Blocks: []telegram.PageBlock{
		&unknownRichBlock{},
		&telegram.PageBlockParagraph{Text: richText("уцелевший абзац")},
	}}
	if got := RichMessageText(richCarrier{RichMessage: rich}); got != "уцелевший абзац" {
		t.Errorf("RichMessageText = %q, want %q", got, "уцелевший абзац")
	}
}

// Python: tests/test_rich_messages.py::test_rich_message_text_is_empty_without_a_rich_message
func TestRichMessageTextIsEmptyWithoutRichMessage(t *testing.T) {
	if got := RichMessageText(richCarrier{}); got != "" {
		t.Errorf("RichMessageText(no rich message) = %q, want %q", got, "")
	}
	if got := RichMessageText(struct{}{}); got != "" {
		t.Errorf("RichMessageText(plain struct) = %q, want %q", got, "")
	}
}

// Python: tests/test_rich_messages.py::test_message_to_dict_falls_back_to_rich_message
// and ::test_format_message_line_falls_back_to_rich_message assert the same
// flattening through the messages tools; this pins the kit-level entry point
// those tools call.
func TestRichMessageTextAcceptsRichMessageDirectly(t *testing.T) {
	rich := &telegram.RichMessage{Blocks: []telegram.PageBlock{
		&telegram.PageBlockParagraph{Text: richText("direct")},
	}}
	if got := RichMessageText(rich); got != "direct" {
		t.Errorf("RichMessageText(*RichMessage) = %q, want %q", got, "direct")
	}
}
