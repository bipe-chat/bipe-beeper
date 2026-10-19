package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"strings"

	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
)

// passThroughKeys are raw content keys that the bridge copies as they are. They mark voice notes.
var passThroughKeys = []string{"org.matrix.msc3245.voice", "org.matrix.msc1767.audio"}

// matrixToUser finds user links (pills) in formatted bodies.
var matrixToUser = regexp.MustCompile(`https://matrix\.to/#/(@|%40)[^"'<>?/\s]+`)

// beeperMXID is the Beeper user for a remote user: the ghost, or the Beeper user of this login.
func (c *Client) beeperMXID(userID id.UserID) id.UserID {
	if userID == c.userID {
		return c.login.UserMXID
	}
	return c.main.br.Matrix.FormatGhostMXID(networkid.UserID(userID))
}

// remoteMXID is the remote user for a Beeper user. It returns false for Beeper users that are not bridged.
func (c *Client) remoteMXID(userID id.UserID) (id.UserID, bool) {
	if userID == c.login.UserMXID {
		return c.userID, true
	}
	ghostID, ok := c.main.br.Matrix.ParseGhostMXID(userID)
	if !ok {
		return "", false
	}
	return id.UserID(ghostID), true
}

// rewriteMentions changes the user IDs in m.mentions and in pills from one side to the other.
func rewriteMentions(content *event.MessageEventContent, mapUser func(id.UserID) (id.UserID, bool)) {
	if content.Mentions != nil {
		userIDs := make([]id.UserID, 0, len(content.Mentions.UserIDs))
		for _, userID := range content.Mentions.UserIDs {
			if mapped, ok := mapUser(userID); ok {
				userIDs = append(userIDs, mapped)
			}
		}
		content.Mentions.UserIDs = userIDs
	}
	if content.FormattedBody == "" {
		return
	}
	content.FormattedBody = matrixToUser.ReplaceAllStringFunc(content.FormattedBody, func(link string) string {
		raw, err := url.PathUnescape(strings.TrimPrefix(link, "https://matrix.to/#/"))
		if err != nil {
			return link
		}
		mapped, ok := mapUser(id.UserID(raw))
		if !ok {
			return link
		}
		return mapped.URI().MatrixToURL()
	})
}

type mediaDownloader func(ctx context.Context, uri id.ContentURIString, file *event.EncryptedFileInfo) ([]byte, error)
type mediaUploader func(ctx context.Context, data []byte, fileName, mimeType string) (id.ContentURIString, *event.EncryptedFileInfo, error)

// reuploadMedia moves the file and the thumbnail of a media message from one homeserver to the other.
func reuploadMedia(ctx context.Context, content *event.MessageEventContent, download mediaDownloader, upload mediaUploader) error {
	if content.URL == "" && content.File == nil {
		return nil
	}
	mimeType := ""
	if content.Info != nil {
		mimeType = content.Info.MimeType
	}
	data, err := download(ctx, content.URL, content.File)
	if err != nil {
		return fmt.Errorf("failed to download media: %w", err)
	}
	content.URL, content.File, err = upload(ctx, data, content.GetFileName(), mimeType)
	if err != nil {
		return fmt.Errorf("failed to upload media: %w", err)
	}
	if info := content.Info; info != nil && (info.ThumbnailURL != "" || info.ThumbnailFile != nil) {
		thumbMime := ""
		if info.ThumbnailInfo != nil {
			thumbMime = info.ThumbnailInfo.MimeType
		}
		thumb, err := download(ctx, info.ThumbnailURL, info.ThumbnailFile)
		if err == nil {
			info.ThumbnailURL, info.ThumbnailFile, err = upload(ctx, thumb, "thumbnail", thumbMime)
		}
		if err != nil {
			// A missing thumbnail does not stop the message.
			info.ThumbnailURL, info.ThumbnailFile, info.ThumbnailInfo = "", nil, nil
		}
	}
	return nil
}

// downloadRemote downloads a file from the remote homeserver and decrypts it.
func (c *Client) downloadRemote(ctx context.Context, uri id.ContentURIString, file *event.EncryptedFileInfo) ([]byte, error) {
	if file != nil {
		uri = file.URL
	}
	parsed, err := uri.Parse()
	if err != nil {
		return nil, err
	}
	data, err := c.cli.DownloadBytes(ctx, parsed)
	if err != nil {
		return nil, err
	}
	if file != nil {
		if err = file.DecryptInPlace(data); err != nil {
			return nil, err
		}
	}
	return data, nil
}

// remoteUploader uploads files to the remote homeserver. It encrypts them when the room is encrypted.
func (c *Client) remoteUploader(roomID id.RoomID) mediaUploader {
	return func(ctx context.Context, data []byte, fileName, mimeType string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
		encrypted, err := c.cli.StateStore.IsEncrypted(ctx, roomID)
		if err != nil {
			return "", nil, err
		}
		req := mautrix.ReqUploadMedia{ContentBytes: data, ContentType: mimeType, FileName: fileName}
		var file *attachment.EncryptedFile
		if encrypted {
			file = attachment.NewEncryptedFile()
			file.EncryptInPlace(data)
			req.ContentType = "application/octet-stream"
			req.FileName = ""
		}
		resp, err := c.cli.UploadMedia(ctx, req)
		if err != nil {
			return "", nil, err
		}
		if file != nil {
			return "", &event.EncryptedFileInfo{EncryptedFile: *file, URL: resp.ContentURI.CUString()}, nil
		}
		return resp.ContentURI.CUString(), nil, nil
	}
}

func passThrough(raw map[string]any) map[string]any {
	var extra map[string]any
	for _, key := range passThroughKeys {
		if value, ok := raw[key]; ok {
			if extra == nil {
				extra = make(map[string]any)
			}
			extra[key] = value
		}
	}
	return extra
}

// messageToMatrix converts the content of a remote m.room.message or m.sticker for Beeper.
// It changes content in place.
func (c *Client) messageToMatrix(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, evtType event.Type, content *event.MessageEventContent, raw map[string]any) (*bridgev2.ConvertedMessagePart, error) {
	content.RemoveReplyFallback()
	content.RelatesTo = nil
	content.NewContent = nil
	rewriteMentions(content, func(userID id.UserID) (id.UserID, bool) {
		return c.beeperMXID(userID), true
	})
	err := reuploadMedia(ctx, content, c.downloadRemote, func(ctx context.Context, data []byte, fileName, mimeType string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
		return intent.UploadMedia(ctx, portal.MXID, data, fileName, mimeType)
	})
	if err != nil {
		return nil, err
	}
	return &bridgev2.ConvertedMessagePart{Type: evtType, Content: content, Extra: passThrough(raw)}, nil
}

// noticePart is a notice that replaces an event that Beeper cannot show.
func noticePart(text string) *bridgev2.ConvertedMessagePart {
	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: &event.MessageEventContent{MsgType: event.MsgNotice, Body: text},
	}
}

// relationsToMatrix gives the reply and thread targets of a remote event.
func relationsToMatrix(rel *event.RelatesTo, converted *bridgev2.ConvertedMessage) {
	if rel == nil {
		return
	}
	if root := rel.GetThreadParent(); root != "" {
		converted.ThreadRoot = ptr.Ptr(networkid.MessageID(root))
		if replyTo := rel.GetNonFallbackReplyTo(); replyTo != "" {
			converted.ReplyTo = &networkid.MessageOptionalPartID{MessageID: networkid.MessageID(replyTo)}
		}
	} else if replyTo := rel.GetReplyTo(); replyTo != "" {
		converted.ReplyTo = &networkid.MessageOptionalPartID{MessageID: networkid.MessageID(replyTo)}
	}
}

// convertToMatrix converts a remote timeline event to a Beeper message.
func (c *Client) convertToMatrix(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, evt *event.Event) (*bridgev2.ConvertedMessage, error) {
	converted := &bridgev2.ConvertedMessage{}
	switch evt.Type {
	case event.EventEncrypted:
		converted.Parts = []*bridgev2.ConvertedMessagePart{noticePart(bipe.UndecryptableText)}
	case event.EventMessage, event.EventSticker:
		content, ok := evt.Content.Parsed.(*event.MessageEventContent)
		if !ok {
			converted.Parts = []*bridgev2.ConvertedMessagePart{noticePart(bipe.UnknownEventText)}
			break
		}
		relationsToMatrix(content.RelatesTo, converted)
		if !isKnownMsgType(evt.Type, content.MsgType) {
			converted.Parts = []*bridgev2.ConvertedMessagePart{noticePart(bipe.UnknownEventText)}
			break
		}
		part, err := c.messageToMatrix(ctx, portal, intent, evt.Type, content, evt.Content.Raw)
		if err != nil {
			return nil, err
		}
		converted.Parts = []*bridgev2.ConvertedMessagePart{part}
	case event.EventUnstablePollStart:
		pollStart, ok := evt.Content.Parsed.(*event.PollStartEventContent)
		if !ok {
			converted.Parts = []*bridgev2.ConvertedMessagePart{noticePart(bipe.UnknownEventText)}
			break
		}
		relationsToMatrix(pollStart.RelatesTo, converted)
		extra := maps.Clone(evt.Content.Raw)
		delete(extra, "m.relates_to")
		converted.Parts = []*bridgev2.ConvertedMessagePart{{
			Type:    event.EventUnstablePollStart,
			Content: &event.MessageEventContent{Body: pollStart.PollStart.Question.GetText()},
			Extra:   extra,
		}}
	case event.EventUnstablePollResponse, event.EventUnstablePollEnd:
		part, err := c.pollReferenceToMatrix(ctx, portal, evt)
		if err != nil {
			return nil, err
		}
		converted.Parts = []*bridgev2.ConvertedMessagePart{part}
	default:
		converted.Parts = []*bridgev2.ConvertedMessagePart{noticePart(bipe.UnknownEventText)}
	}
	return converted, nil
}

// pollReferenceToMatrix converts a poll response or poll end. Both refer to the poll start event.
func (c *Client) pollReferenceToMatrix(ctx context.Context, portal *bridgev2.Portal, evt *event.Event) (*bridgev2.ConvertedMessagePart, error) {
	var rel struct {
		RelatesTo event.RelatesTo `json:"m.relates_to"`
	}
	_ = json.Unmarshal(evt.Content.VeryRaw, &rel)
	target, err := c.main.br.DB.Message.GetFirstPartByID(ctx, portal.Receiver, networkid.MessageID(rel.RelatesTo.GetReferenceID()))
	if err != nil {
		return nil, err
	}
	extra := maps.Clone(evt.Content.Raw)
	if target == nil {
		// The poll is not in Beeper, so the vote or the end has nothing to refer to.
		return &bridgev2.ConvertedMessagePart{Type: evt.Type, Content: &event.MessageEventContent{}, Extra: extra, DontBridge: true}, nil
	}
	extra["m.relates_to"] = map[string]any{"rel_type": event.RelReference, "event_id": target.MXID}
	content := &event.MessageEventContent{}
	if text, ok := extra["org.matrix.msc1767.text"].(string); ok {
		content.Body = text
	}
	return &bridgev2.ConvertedMessagePart{Type: evt.Type, Content: content, Extra: extra}, nil
}

// isKnownMsgType reports whether Beeper can show a msgtype.
func isKnownMsgType(evtType event.Type, msgType event.MessageType) bool {
	if evtType == event.EventSticker {
		return true
	}
	switch msgType {
	case event.MsgText, event.MsgNotice, event.MsgEmote, event.MsgImage, event.MsgVideo,
		event.MsgAudio, event.MsgFile, event.MsgLocation:
		return true
	}
	return false
}

// convertEditToMatrix converts a remote m.replace event.
func (c *Client) convertEditToMatrix(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, evt *event.Event) (*bridgev2.ConvertedEdit, error) {
	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok || len(existing) == 0 {
		return nil, fmt.Errorf("%w: unexpected edit content", bridgev2.ErrIgnoringRemoteEvent)
	}
	newContent := content.NewContent
	if newContent == nil {
		newContent = content
		newContent.Body = strings.TrimPrefix(newContent.Body, "* ")
	}
	var newRaw map[string]any
	if rawNew, ok := evt.Content.Raw["m.new_content"].(map[string]any); ok {
		newRaw = rawNew
	}
	part, err := c.messageToMatrix(ctx, portal, intent, evt.Type, newContent, newRaw)
	if err != nil {
		return nil, err
	}
	return &bridgev2.ConvertedEdit{
		ModifiedParts: []*bridgev2.ConvertedEditPart{part.ToEditPart(existing[0])},
	}, nil
}

// messageToRemote converts the content of a Beeper message for the remote homeserver. It changes content in place.
func (c *Client) messageToRemote(ctx context.Context, roomID id.RoomID, content *event.MessageEventContent) error {
	content.RemoveReplyFallback()
	content.RelatesTo = nil
	content.NewContent = nil
	rewriteMentions(content, c.remoteMXID)
	return reuploadMedia(ctx, content, c.main.br.Bot.DownloadMedia, c.remoteUploader(roomID))
}

// remoteContent merges the converted content with the pass-through keys of the raw Beeper content.
func remoteContent(content *event.MessageEventContent, raw map[string]any) *event.Content {
	return &event.Content{Parsed: content, Raw: passThrough(raw)}
}
