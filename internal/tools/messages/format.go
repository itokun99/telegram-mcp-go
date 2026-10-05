package messages

// Message rendering: the Go port of messages.py's get_media_label,
// get_custom_emoji_metadata, get_reply_quote, message_to_dict,
// format_message_line and their helpers. Every piece of Telegram-provided
// text is sanitized before it can reach a tool result.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	telegram "github.com/amarnathcjd/gogram/telegram"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
)

// linkDomain mirrors LINK_DOMAIN: TELEGRAM_LINK_DOMAIN overrides the default
// because a registry incident once took t.me down.
func linkDomain() string {
	if value := os.Getenv("TELEGRAM_LINK_DOMAIN"); value != "" {
		return value
	}
	return "t.me"
}

// peerMarkedID ports get_marked_id for gogram Peer objects without a client
// (message formatting must not depend on a live connection).
func peerMarkedID(peer telegram.Peer) int64 {
	switch p := peer.(type) {
	case *telegram.PeerUser:
		return p.UserID
	case *telegram.PeerChat:
		return -p.ChatID
	case *telegram.PeerChannel:
		return -1000000000000 - p.ChannelID
	default:
		return 0
	}
}

func unixTime(seconds int32) time.Time {
	return time.Unix(int64(seconds), 0).UTC()
}

// pythonISO renders a datetime exactly like Python's aware isoformat.
func pythonISO(t time.Time) string {
	return t.Format("2006-01-02T15:04:05+00:00")
}

// utf16Slice returns the substring at a Telegram entity offset/length pair
// (UTF-16 code units).
func utf16Slice(text string, offset, length int32) string {
	units := utf16.Encode([]rune(text))
	start := int(offset)
	end := start + int(length)
	if start < 0 || start > len(units) {
		return ""
	}
	if end > len(units) {
		end = len(units)
	}
	return string(utf16.Decode(units[start:end]))
}

// mediaLabel ports get_media_label: a short attachment label, "" when the
// message carries none. Link previews are deliberately not attachments and
// are checked first.
func mediaLabel(m telegram.NewMessage) string {
	if m.Message == nil {
		return ""
	}
	switch media := m.Message.Media.(type) {
	case nil:
		return ""
	case *telegram.MessageMediaWebPage:
		return ""
	case *telegram.MessageMediaPhoto:
		return "photo"
	case *telegram.MessageMediaPoll:
		return "poll"
	case *telegram.MessageMediaGeo, *telegram.MessageMediaGeoLive, *telegram.MessageMediaVenue:
		return "geo"
	case *telegram.MessageMediaContact:
		return "contact"
	case *telegram.MessageMediaDocument:
		return documentLabel(media)
	default:
		return "media"
	}
}

func documentLabel(media *telegram.MessageMediaDocument) string {
	document, ok := media.Document.(*telegram.DocumentObj)
	if !ok {
		return "document"
	}
	var (
		sticker  bool
		voice    bool
		roundMsg bool
		animated bool
		video    bool
		audio    bool
		fileName string
	)
	for _, attribute := range document.Attributes {
		switch a := attribute.(type) {
		case *telegram.DocumentAttributeSticker:
			sticker = true
			if a.Alt != "" {
				return "sticker " + a.Alt
			}
		case *telegram.DocumentAttributeAudio:
			if a.Voice {
				voice = true
			} else {
				audio = true
			}
		case *telegram.DocumentAttributeVideo:
			if a.RoundMessage {
				roundMsg = true
			} else {
				video = true
			}
		case *telegram.DocumentAttributeAnimated:
			animated = true
		case *telegram.DocumentAttributeFilename:
			fileName = a.FileName
		}
	}
	switch {
	case sticker:
		return "sticker"
	case voice:
		return "voice"
	case roundMsg:
		return "video_note"
	case video:
		return "video"
	case audio:
		return "audio"
	case animated:
		return "gif"
	case fileName != "":
		return "document: " + fileName
	default:
		return "document"
	}
}

// customEmojiMetadata ports get_custom_emoji_metadata: unique {emoji, id}
// pairs, deduplicated by document ID, ready for parse_mode='html' reuse.
func customEmojiMetadata(m telegram.NewMessage) map[string]any {
	if m.Message == nil {
		return nil
	}
	text := m.MessageText()
	seen := map[string]bool{}
	emojis := []any{}
	for _, entity := range m.Message.Entities {
		emoji, ok := entity.(*telegram.MessageEntityCustomEmoji)
		if !ok {
			continue
		}
		id := strconv.FormatInt(emoji.DocumentID, 10)
		if seen[id] {
			continue
		}
		seen[id] = true
		emojis = append(emojis, map[string]any{
			"emoji": kit.SanitizeUserContent(
				utf16Slice(text, emoji.Offset, emoji.Length),
				kit.WithMaxLength(64),
				kit.WithPreserveEmoji(),
			),
			"id": id,
		})
	}
	if len(emojis) == 0 {
		return nil
	}
	return map[string]any{"custom_emojis": emojis}
}

// replyQuote ports get_reply_quote: a reply that targets only a span of the
// original message carries quote_text (and its offset).
func replyQuote(m telegram.NewMessage) map[string]any {
	if m.Message == nil {
		return nil
	}
	header, ok := m.Message.ReplyTo.(*telegram.MessageReplyHeaderObj)
	if !ok || header.QuoteText == "" {
		return nil
	}
	quote := map[string]any{"text": kit.SanitizeUserContent(header.QuoteText)}
	if header.QuoteOffset != 0 {
		quote["offset"] = int64(header.QuoteOffset)
	}
	return quote
}

// replyToID reads reply_to_msg_id off a reply header.
func replyToID(m telegram.NewMessage) int64 {
	if m.Message == nil {
		return 0
	}
	if header, ok := m.Message.ReplyTo.(*telegram.MessageReplyHeaderObj); ok {
		return int64(header.ReplyToMsgID)
	}
	return 0
}

// forwardInfo ports the fwd_from block of message_to_dict. ok is false when
// the message is not forwarded at all.
func forwardInfo(m telegram.NewMessage) (map[string]any, bool) {
	if m.Message == nil || m.Message.FwdFrom == nil {
		return nil, false
	}
	fwd := m.Message.FwdFrom
	info := map[string]any{}
	if fwd.Date != 0 {
		info["date"] = unixTime(fwd.Date)
	}
	if fwd.FromName != "" {
		info["from_name"] = kit.SanitizeName(fwd.FromName)
	}
	if fwd.FromID != nil {
		info["from_chat_id"] = peerMarkedID(fwd.FromID)
	}
	if fwd.ChannelPost != 0 {
		info["channel_post"] = int64(fwd.ChannelPost)
	}
	if fwd.PostAuthor != "" {
		info["post_author"] = kit.SanitizeName(fwd.PostAuthor)
	}
	if fwd.ChannelPost != 0 {
		if info["from_chat_id"] != nil {
			info["post_link"] = fmt.Sprintf(
				"https://%s/c/%d/%d",
				linkDomain(),
				abs64(info["from_chat_id"].(int64))%10000000000,
				fwd.ChannelPost,
			)
		}
	}
	if len(info) == 0 {
		return map[string]any{}, true
	}
	return info, true
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// engagementDict ports get_engagement_dict: views, forwards and the summed
// reaction count, present only when the message carries them.
func engagementDict(m telegram.NewMessage) map[string]any {
	if m.Message == nil {
		return nil
	}
	result := map[string]any{}
	if m.Message.Views != 0 || m.Message.Post {
		result["views"] = int64(m.Message.Views)
	}
	if m.Message.Forwards != 0 || m.Message.Post {
		result["forwards"] = int64(m.Message.Forwards)
	}
	if m.Message.Reactions != nil {
		total := int64(0)
		for _, count := range m.Message.Reactions.Results {
			total += int64(count.Count)
		}
		result["reactions"] = total
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// engagementInfo ports get_engagement_info: the " | views:…, forwards:…" tail
// of a message line.
func engagementInfo(m telegram.NewMessage) string {
	if m.Message == nil {
		return ""
	}
	parts := []string{}
	if m.Message.Views != 0 || m.Message.Post {
		parts = append(parts, fmt.Sprintf("views:%d", m.Message.Views))
	}
	if m.Message.Forwards != 0 || m.Message.Post {
		parts = append(parts, fmt.Sprintf("forwards:%d", m.Message.Forwards))
	}
	if m.Message.Reactions != nil {
		total := int32(0)
		for _, count := range m.Message.Reactions.Results {
			total += count.Count
		}
		parts = append(parts, fmt.Sprintf("reactions:%d", total))
	}
	if len(parts) == 0 {
		return ""
	}
	return " | " + strings.Join(parts, ", ")
}

// inlineButtons ports the msg.buttons property: flat rows of KeyboardButton.
func inlineButtons(m telegram.NewMessage) []telegram.KeyboardButton {
	if m.Message == nil {
		return nil
	}
	markup, ok := m.Message.ReplyMarkup.(*telegram.ReplyInlineMarkup)
	if !ok {
		return nil
	}
	buttons := []telegram.KeyboardButton{}
	for _, row := range markup.Rows {
		if row == nil {
			continue
		}
		buttons = append(buttons, row.Buttons...)
	}
	return buttons
}

func buttonText(button telegram.KeyboardButton) string {
	if url, ok := button.(*telegram.KeyboardButtonURL); ok {
		return url.Text
	}
	if callback, ok := button.(*telegram.KeyboardButtonCallback); ok {
		return callback.Text
	}
	if urlAuth, ok := button.(*telegram.KeyboardButtonURLAuth); ok {
		return urlAuth.Text
	}
	if base, ok := button.(*telegram.KeyboardButtonObj); ok {
		return base.Text
	}
	return ""
}

func buttonURL(button telegram.KeyboardButton) string {
	switch b := button.(type) {
	case *telegram.KeyboardButtonURL:
		return b.URL
	case *telegram.KeyboardButtonURLAuth:
		return b.URL
	default:
		return ""
	}
}

func buttonData(button telegram.KeyboardButton) []byte {
	if callback, ok := button.(*telegram.KeyboardButtonCallback); ok {
		return callback.Data
	}
	return nil
}

func inlineButtonTexts(m telegram.NewMessage) []string {
	buttons := inlineButtons(m)
	if len(buttons) == 0 {
		return nil
	}
	out := make([]string, 0, len(buttons))
	for _, button := range buttons {
		if text := buttonText(button); text != "" {
			out = append(out, kit.SanitizeUserContent(text))
		}
	}
	return out
}

func linkURLs(m telegram.NewMessage) []string {
	if m.Message == nil {
		return nil
	}
	out := []string{}
	for _, entity := range m.Message.Entities {
		if link, ok := entity.(*telegram.MessageEntityTextURL); ok && link.URL != "" {
			out = append(out, link.URL)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// senderName ports get_sender_name over gogram's sender/chat/channel fields.
func senderName(m telegram.NewMessage) string {
	if m.Sender != nil {
		if m.Sender.FirstName != "" || m.Sender.LastName != "" {
			full := strings.TrimSpace(m.Sender.FirstName + " " + m.Sender.LastName)
			return kit.SanitizeName(full)
		}
	}
	if m.SenderChat != nil && m.SenderChat.Title != "" {
		return kit.SanitizeName(m.SenderChat.Title)
	}
	if m.Chat != nil && m.Chat.Title != "" {
		return kit.SanitizeName(m.Chat.Title)
	}
	if m.Channel != nil && m.Channel.Title != "" {
		return kit.SanitizeName(m.Channel.Title)
	}
	return "Unknown"
}

func senderUsername(m telegram.NewMessage) string {
	if m.Sender != nil && m.Sender.Username != "" {
		return kit.SanitizeName(m.Sender.Username)
	}
	if m.Channel != nil && m.Channel.Username != "" {
		return kit.SanitizeName(m.Channel.Username)
	}
	return ""
}

// senderID is the marked ID of from_id, or 0 when absent.
func senderID(m telegram.NewMessage) int64 {
	if m.Message == nil || m.Message.FromID == nil {
		return 0
	}
	return peerMarkedID(m.Message.FromID)
}

// senderInfo ports get_sender_info: "name (@username) [id=NNN]".
func senderInfo(m telegram.NewMessage) string {
	name := senderName(m)
	username := senderUsername(m)
	sid := senderID(m)
	suffix := ""
	if username != "" {
		suffix += " (@" + username + ")"
	}
	if sid != 0 {
		suffix += fmt.Sprintf(" [id=%d]", sid)
	}
	return name + suffix
}

func actionName(m telegram.NewMessage) string {
	if m.Action == nil {
		return ""
	}
	return strings.TrimPrefix(fmt.Sprintf("%T", m.Action), "*telegram.")
}

func pollDict(m telegram.NewMessage) map[string]any {
	media, ok := m.Message.Media.(*telegram.MessageMediaPoll)
	if !ok || media.Poll == nil {
		return nil
	}
	answers := []any{}
	for _, answer := range media.Poll.Answers {
		if obj, ok := answer.(*telegram.PollAnswerObj); ok && obj.Text != nil {
			answers = append(answers, kit.SanitizeUserContent(obj.Text.Text))
		}
	}
	question := ""
	if media.Poll.Question != nil {
		question = kit.SanitizeUserContent(media.Poll.Question.Text)
	}
	return map[string]any{"question": question, "answers": answers}
}

func webPreviewDict(m telegram.NewMessage) map[string]any {
	media, ok := m.Message.Media.(*telegram.MessageMediaWebPage)
	if !ok {
		return nil
	}
	page, ok := media.Webpage.(*telegram.WebPageObj)
	if !ok || page.URL == "" {
		return nil
	}
	result := map[string]any{"url": page.URL}
	if page.SiteName != "" {
		result["site_name"] = kit.SanitizeUserContent(page.SiteName)
	}
	if page.Title != "" {
		result["title"] = kit.SanitizeUserContent(page.Title)
	}
	if page.Description != "" {
		result["description"] = kit.SanitizeUserContent(page.Description)
	}
	return result
}

// messageToDict ports messages.py's message_to_dict: the compact but
// API-complete Telethon message view (empty fields omitted). chatID enables
// voice/video-note transcript enrichment via the cache.
func messageToDict(runtime Runtime, m telegram.NewMessage, chatID *int64) map[string]any {
	d := map[string]any{
		"id":     int64(m.ID),
		"sender": senderName(m),
		"date":   unixTime(m.Message.Date),
	}
	if sid := senderID(m); sid != 0 {
		d["sender_id"] = sid
	}
	if username := senderUsername(m); username != "" {
		d["username"] = username
	}
	if m.Message.Out {
		d["out"] = true
	}

	text := ""
	rich := false
	if m.Message.Message != "" {
		text = kit.SanitizeUserContent(m.Message.Message)
	}
	if text == "" {
		if richText := kit.RichMessageText(m.Message); richText != "" {
			text = kit.SanitizeUserContent(richText)
			rich = true
		}
	}
	if text != "" {
		d["text"] = text
	}
	if rich {
		d["rich"] = true
	}
	for key, value := range customEmojiMetadata(m) {
		d[key] = value
	}

	if label := mediaLabel(m); label != "" {
		d["media"] = label
	}
	if preview := webPreviewDict(m); preview != nil {
		d["web_preview"] = preview
	}
	if poll := pollDict(m); poll != nil {
		d["poll"] = poll
	}
	if text == "" {
		if info, ok := voiceAttachmentInfo(runtime, m, chatID); ok {
			if info.DurationSeconds != nil {
				d["duration"] = int64(*info.DurationSeconds)
			}
			switch info.TranscriptStatus {
			case "ready":
				d["transcript"] = info.Transcript
				d["transcript_source"] = info.TranscriptSource
				d["transcript_note"] = "Machine transcript, not a verbatim quote."
			case "pending":
				d["transcript_status"] = "pending"
			}
		}
	}
	if m.Message.GroupedID != 0 {
		d["grouped_id"] = m.Message.GroupedID
	}
	if replyTo := replyToID(m); replyTo != 0 {
		d["reply_to"] = replyTo
	}
	if quote := replyQuote(m); quote != nil {
		d["reply_quote"] = quote
	}
	if info, ok := forwardInfo(m); ok {
		d["forwarded"] = info
	}
	if m.Message.ViaBotID != 0 {
		d["via_bot_id"] = m.Message.ViaBotID
	}
	if m.Message.EditDate != 0 {
		d["edited"] = unixTime(m.Message.EditDate)
	}
	if m.Message.Pinned {
		d["pinned"] = true
	}
	if engagement := engagementDict(m); engagement != nil {
		d["engagement"] = engagement
	}
	if m.Message.Replies != nil {
		d["comments"] = int64(m.Message.Replies.Replies)
	}
	if buttons := inlineButtonTexts(m); len(buttons) > 0 {
		d["buttons"] = buttons
	}
	if urls := linkURLs(m); len(urls) > 0 {
		d["link_urls"] = urls
	}
	if action := actionName(m); action != "" {
		d["action"] = action
	}
	if m.Message.TtlPeriod != 0 {
		d["ttl_period"] = int64(m.Message.TtlPeriod)
	}
	return d
}

// formatMessageLine ports format_message_line: one human-readable line per
// message with every key flag.
func formatMessageLine(runtime Runtime, m telegram.NewMessage, chatID *int64) string {
	parts := []string{
		fmt.Sprintf("ID: %d", m.ID),
		senderInfo(m),
		"Date: " + pythonISO(unixTime(m.Message.Date)),
	}
	if replyTo := replyToID(m); replyTo != 0 {
		parts = append(parts, fmt.Sprintf("reply to %d", replyTo))
	}
	if quote := replyQuote(m); quote != nil {
		preview := strings.ReplaceAll(quote["text"].(string), "\n", " ")
		if len([]rune(preview)) > 60 {
			preview = string([]rune(preview)[:60]) + "…"
		}
		parts = append(parts, fmt.Sprintf("quoting %q", preview))
	}

	flags := []string{}
	if label := mediaLabel(m); label != "" {
		flags = append(flags, "📎 "+label)
	}
	if m.Message.GroupedID != 0 {
		flags = append(flags, fmt.Sprintf("album:%d", m.Message.GroupedID))
	}
	if m.Message.FwdFrom != nil {
		flags = append(flags, "forwarded")
	}
	if m.Message.EditDate != 0 {
		flags = append(flags, "edited")
	}
	if m.Message.ViaBotID != 0 {
		flags = append(flags, "via_bot")
	}
	if m.Message.Pinned {
		flags = append(flags, "pinned")
	}
	if buttons := inlineButtonTexts(m); len(buttons) > 0 {
		flags = append(flags, fmt.Sprintf("buttons:%d", len(buttons)))
	}
	if action := actionName(m); action != "" {
		flags = append(flags, "service:"+action)
	}
	if len(flags) > 0 {
		parts = append(parts, strings.Join(flags, ", "))
	}

	if engagement := strings.TrimSpace(strings.TrimPrefix(engagementInfo(m), " |")); engagement != "" {
		parts = append(parts, engagement)
	}
	if custom := customEmojiMetadata(m); custom != nil {
		encoded := jsonMarshal(custom["custom_emojis"])
		parts = append(parts, "custom_emojis: "+encoded)
	}

	raw := ""
	if m.Message.Message != "" {
		raw = kit.SanitizeUserContent(m.Message.Message)
	}
	if raw == "" {
		if richText := kit.RichMessageText(m.Message); richText != "" {
			raw = kit.SanitizeUserContent(richText)
			parts = append(parts, "rich")
		}
	}
	safeText := ""
	switch {
	case raw != "":
		safeText = strings.ReplaceAll(raw, "\n", `\n`)
	default:
		if info, ok := voiceAttachmentInfo(runtime, m, chatID); ok {
			safeText = kit.RenderVoiceText(info)
		} else {
			safeText = "[empty]"
		}
	}
	return strings.Join(parts, " | ") + " | Message: " + safeText
}
