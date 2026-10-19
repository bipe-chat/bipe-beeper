package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// cloneContent gives a deep copy, so that the conversion does not change the event that the bridge keeps.
func cloneContent(content *event.MessageEventContent) (*event.MessageEventContent, error) {
	data, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	var clone event.MessageEventContent
	if err = json.Unmarshal(data, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

// txnPrefix starts the transaction ID of every event that the bridge sends to the remote homeserver.
const txnPrefix = "beeper-"

// txnID makes the remote transaction ID from the Beeper event ID, so that a retry does not send twice.
func txnID(evt *event.Event) mautrix.ReqSendEvent {
	return mautrix.ReqSendEvent{TransactionID: txnPrefix + evt.ID.String()}
}

func (c *Client) sentMessage(eventID id.EventID) *bridgev2.MatrixMessageResponse {
	return &bridgev2.MatrixMessageResponse{DB: &database.Message{
		ID:        networkid.MessageID(eventID),
		SenderID:  networkid.UserID(c.userID),
		Timestamp: time.Now(),
	}}
}

// relationsToRemote makes the reply and thread relation for the remote event.
func relationsToRemote(threadRoot, replyTo *database.Message) *event.RelatesTo {
	switch {
	case threadRoot != nil:
		fallback := id.EventID(threadRoot.ID)
		if replyTo != nil {
			fallback = id.EventID(replyTo.ID)
		}
		rel := (&event.RelatesTo{}).SetThread(id.EventID(threadRoot.ID), fallback)
		if replyTo != nil {
			rel.IsFallingBack = false
		}
		return rel
	case replyTo != nil:
		return (&event.RelatesTo{}).SetReplyTo(id.EventID(replyTo.ID))
	}
	return nil
}

func (c *Client) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	if err := c.requireLogin(); err != nil {
		return nil, err
	}
	roomID := roomIDOf(msg.Portal)
	content, err := cloneContent(msg.Content)
	if err != nil {
		return nil, err
	}
	evtType := msg.Event.Type
	if content.MsgType == event.CapMsgSticker {
		content.MsgType = ""
		evtType = event.EventSticker
	}
	if err = c.messageToRemote(ctx, roomID, content); err != nil {
		return nil, err
	}
	content.RelatesTo = relationsToRemote(msg.ThreadRoot, msg.ReplyTo)
	resp, err := c.cli.SendMessageEvent(ctx, roomID, evtType, remoteContent(content, msg.Event.Content.Raw), txnID(msg.Event))
	if err != nil {
		return nil, err
	}
	return c.sentMessage(resp.EventID), nil
}

func (c *Client) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	if err := c.requireLogin(); err != nil {
		return err
	}
	roomID := roomIDOf(msg.Portal)
	newContent, err := cloneContent(msg.Content)
	if err != nil {
		return err
	}
	if err = c.messageToRemote(ctx, roomID, newContent); err != nil {
		return err
	}
	content := &event.MessageEventContent{
		MsgType:    newContent.MsgType,
		Body:       "* " + newContent.Body,
		NewContent: newContent,
		Mentions:   &event.Mentions{},
		RelatesTo:  (&event.RelatesTo{}).SetReplace(id.EventID(msg.EditTarget.ID)),
	}
	if newContent.FormattedBody != "" {
		content.Format = event.FormatHTML
		content.FormattedBody = "* " + newContent.FormattedBody
	}
	_, err = c.cli.SendMessageEvent(ctx, roomID, event.EventMessage, content, txnID(msg.Event))
	return err
}

func (c *Client) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	// The emoji ID stays empty here. HandleMatrixReaction sets it to the remote reaction event ID.
	return bridgev2.MatrixReactionPreResponse{
		SenderID: networkid.UserID(c.userID),
		Emoji:    msg.Content.RelatesTo.Key,
	}, nil
}

func (c *Client) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	if err := c.requireLogin(); err != nil {
		return nil, err
	}
	key := msg.Content.RelatesTo.Key
	content := &event.ReactionEventContent{
		RelatesTo: *(&event.RelatesTo{}).SetAnnotation(id.EventID(msg.TargetMessage.ID), key),
	}
	resp, err := c.cli.SendMessageEvent(ctx, roomIDOf(msg.Portal), event.EventReaction, content, txnID(msg.Event))
	if err != nil {
		return nil, err
	}
	return &database.Reaction{EmojiID: networkid.EmojiID(resp.EventID), Emoji: key}, nil
}

func (c *Client) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	if err := c.requireLogin(); err != nil {
		return err
	}
	_, err := c.cli.RedactEvent(ctx, roomIDOf(msg.Portal), id.EventID(msg.TargetReaction.EmojiID), mautrix.ReqRedact{TxnID: txnPrefix + msg.Event.ID.String()})
	return err
}

func (c *Client) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	if err := c.requireLogin(); err != nil {
		return err
	}
	_, err := c.cli.RedactEvent(ctx, roomIDOf(msg.Portal), id.EventID(msg.TargetMessage.ID), mautrix.ReqRedact{
		Reason: msg.Content.Reason,
		TxnID:  txnPrefix + msg.Event.ID.String(),
	})
	return err
}

func (c *Client) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	if err := c.requireLogin(); err != nil {
		return err
	}
	target := msg.ExactMessage
	if target == nil {
		var err error
		target, err = c.main.br.DB.Message.GetLastPartAtOrBeforeTime(ctx, msg.Portal.PortalKey, msg.ReadUpTo)
		if err != nil {
			return err
		} else if target == nil {
			return nil
		}
	}
	eventID := id.EventID(target.ID)
	return c.cli.SetReadMarkers(ctx, roomIDOf(msg.Portal), &mautrix.ReqSetReadMarkers{Read: eventID, FullyRead: eventID})
}

func (c *Client) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	if err := c.requireLogin(); err != nil {
		return err
	}
	_, err := c.cli.UserTyping(ctx, roomIDOf(msg.Portal), msg.IsTyping, typingTimeout)
	return err
}

func (c *Client) HandleMatrixRoomName(ctx context.Context, msg *bridgev2.MatrixRoomName) (bool, error) {
	if err := c.requireLogin(); err != nil {
		return false, err
	}
	_, err := c.cli.SendStateEvent(ctx, roomIDOf(msg.Portal), event.StateRoomName, "", &event.RoomNameEventContent{Name: msg.Content.Name})
	return err == nil, err
}

func (c *Client) HandleMatrixRoomTopic(ctx context.Context, msg *bridgev2.MatrixRoomTopic) (bool, error) {
	if err := c.requireLogin(); err != nil {
		return false, err
	}
	_, err := c.cli.SendStateEvent(ctx, roomIDOf(msg.Portal), event.StateTopic, "", &event.TopicEventContent{Topic: msg.Content.Topic})
	return err == nil, err
}

func (c *Client) HandleMatrixRoomAvatar(ctx context.Context, msg *bridgev2.MatrixRoomAvatar) (bool, error) {
	if err := c.requireLogin(); err != nil {
		return false, err
	}
	content := &event.RoomAvatarEventContent{}
	if msg.Content.URL != "" {
		data, err := c.main.br.Bot.DownloadMedia(ctx, msg.Content.URL, nil)
		if err != nil {
			return false, fmt.Errorf("failed to download avatar: %w", err)
		}
		resp, err := c.cli.UploadBytes(ctx, data, "")
		if err != nil {
			return false, fmt.Errorf("failed to upload avatar: %w", err)
		}
		content.URL = resp.ContentURI.CUString()
	}
	_, err := c.cli.SendStateEvent(ctx, roomIDOf(msg.Portal), event.StateRoomAvatar, "", content)
	return err == nil, err
}

func (c *Client) HandleMatrixPollStart(ctx context.Context, msg *bridgev2.MatrixPollStart) (*bridgev2.MatrixMessageResponse, error) {
	if err := c.requireLogin(); err != nil {
		return nil, err
	}
	content := maps.Clone(msg.Event.Content.Raw)
	delete(content, "m.relates_to")
	if rel := relationsToRemote(msg.ThreadRoot, msg.ReplyTo); rel != nil {
		content["m.relates_to"] = rel
	}
	resp, err := c.cli.SendMessageEvent(ctx, roomIDOf(msg.Portal), event.EventUnstablePollStart, content, txnID(msg.Event))
	if err != nil {
		return nil, err
	}
	return c.sentMessage(resp.EventID), nil
}

func (c *Client) HandleMatrixPollVote(ctx context.Context, msg *bridgev2.MatrixPollVote) (*bridgev2.MatrixMessageResponse, error) {
	if err := c.requireLogin(); err != nil {
		return nil, err
	}
	content := maps.Clone(msg.Event.Content.Raw)
	content["m.relates_to"] = map[string]any{"rel_type": event.RelReference, "event_id": msg.VoteTo.ID}
	resp, err := c.cli.SendMessageEvent(ctx, roomIDOf(msg.Portal), event.EventUnstablePollResponse, content, txnID(msg.Event))
	if err != nil {
		return nil, err
	}
	return c.sentMessage(resp.EventID), nil
}

// HandleMatrixAcceptMessageRequest joins the remote room of an invite after the user accepts it in Beeper.
func (c *Client) HandleMatrixAcceptMessageRequest(ctx context.Context, msg *bridgev2.MatrixAcceptMessageRequest) error {
	if err := c.requireLogin(); err != nil {
		return err
	}
	_, err := c.cli.JoinRoomByID(ctx, roomIDOf(msg.Portal))
	return err
}

// HandleMatrixDeleteChat declines an invite. For a joined room, it deletes the chat only in Beeper.
func (c *Client) HandleMatrixDeleteChat(ctx context.Context, msg *bridgev2.MatrixDeleteChat) error {
	if err := c.requireLogin(); err != nil {
		return err
	}
	if !msg.Portal.MessageRequest {
		return nil
	}
	_, err := c.cli.LeaveRoom(ctx, roomIDOf(msg.Portal))
	return err
}
