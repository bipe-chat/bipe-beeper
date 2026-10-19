package connector

import (
	"context"
	"slices"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// FetchMessages reads room history from the remote homeserver with /messages.
// The first backfill of a portal takes the newest messages. Later calls page back from the cursor
// or from the oldest bridged message.
func (c *Client) FetchMessages(ctx context.Context, params bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	if err := c.requireLogin(); err != nil {
		return nil, err
	} else if c.crypto == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	if params.ThreadRoot != "" {
		// Thread messages come with the main timeline.
		return &bridgev2.FetchMessagesResponse{HasMore: false, Forward: params.Forward}, nil
	}
	roomID := roomIDOf(params.Portal)
	dir := mautrix.DirectionBackward
	var from string
	switch {
	case params.Forward && params.AnchorMessage != nil:
		evtCtx, err := c.cli.Context(ctx, roomID, id.EventID(params.AnchorMessage.ID), nil, 0)
		if err != nil {
			return nil, err
		}
		from, dir = evtCtx.End, mautrix.DirectionForward
	case params.Forward:
		// No bridged messages yet: take the newest messages.
	case params.Cursor != "":
		from = string(params.Cursor)
	case params.AnchorMessage != nil:
		evtCtx, err := c.cli.Context(ctx, roomID, id.EventID(params.AnchorMessage.ID), nil, 0)
		if err != nil {
			return nil, err
		}
		from = evtCtx.Start
	}
	resp, err := c.cli.Messages(ctx, roomID, from, "", dir, nil, params.Count)
	if err != nil {
		return nil, err
	}
	events := resp.Chunk
	if dir == mautrix.DirectionBackward {
		slices.Reverse(events)
	}
	return &bridgev2.FetchMessagesResponse{
		Messages: c.backfillMessages(ctx, params.Portal, roomID, events),
		Cursor:   networkid.PaginationCursor(resp.End),
		HasMore:  resp.End != "" && len(resp.Chunk) > 0,
		Forward:  params.Forward,
	}, nil
}

// backfillMessages converts a chronological batch of remote events.
// Reactions and the latest edit go onto their target when the target is in the same batch.
func (c *Client) backfillMessages(ctx context.Context, portal *bridgev2.Portal, roomID id.RoomID, events []*event.Event) []*bridgev2.BackfillMessage {
	log := zerolog.Ctx(ctx)
	var messages []*event.Event
	edits := make(map[id.EventID]*event.Event)
	reactions := make(map[id.EventID][]*event.Event)
	for _, evt := range events {
		evt.RoomID = roomID
		if evt.Type == event.EventEncrypted {
			_ = evt.Content.ParseRaw(evt.Type)
			decrypted, err := c.crypto.Decrypt(ctx, evt)
			if err != nil {
				log.Debug().Err(err).Stringer("event_id", evt.ID).Msg("Failed to decrypt a backfill event")
			} else {
				evt = decrypted
			}
		} else {
			_ = evt.Content.ParseRaw(evt.Type)
		}
		if evt.StateKey != nil || evt.Unsigned.RedactedBecause != nil || isIgnoredType(evt.Type) {
			continue
		}
		switch evt.Type {
		case event.EventRedaction:
			continue
		case event.EventReaction:
			if content, ok := evt.Content.Parsed.(*event.ReactionEventContent); ok && content.RelatesTo.Type == event.RelAnnotation {
				reactions[content.RelatesTo.EventID] = append(reactions[content.RelatesTo.EventID], evt)
			}
			continue
		}
		if content, ok := evt.Content.Parsed.(*event.MessageEventContent); ok {
			if target := content.RelatesTo.GetReplaceID(); target != "" {
				edits[target] = evt
				continue
			}
		}
		messages = append(messages, evt)
	}

	out := make([]*bridgev2.BackfillMessage, 0, len(messages))
	for _, evt := range messages {
		sender := c.sender(evt.Sender)
		intent, ok := portal.GetIntentFor(ctx, sender, c.login, bridgev2.RemoteEventMessage)
		if !ok {
			continue
		}
		converted, err := c.convertToMatrix(ctx, portal, intent, applyEdit(evt, edits[evt.ID]))
		if err != nil {
			log.Err(err).Stringer("event_id", evt.ID).Msg("Failed to convert a backfill event")
			continue
		}
		msg := &bridgev2.BackfillMessage{
			ConvertedMessage: converted,
			Sender:           sender,
			ID:               networkid.MessageID(evt.ID),
			Timestamp:        time.UnixMilli(evt.Timestamp),
		}
		for _, reaction := range reactions[evt.ID] {
			msg.Reactions = append(msg.Reactions, &bridgev2.BackfillReaction{
				Timestamp: time.UnixMilli(reaction.Timestamp),
				Sender:    c.sender(reaction.Sender),
				EmojiID:   networkid.EmojiID(reaction.ID),
				Emoji:     reaction.Content.AsReaction().RelatesTo.Key,
			})
		}
		out = append(out, msg)
	}
	return out
}

// applyEdit gives the event with the content of its latest edit. The relations of the original stay.
func applyEdit(evt, edit *event.Event) *event.Event {
	if edit == nil {
		return evt
	}
	original, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		return evt
	}
	newContent := edit.Content.AsMessage().NewContent
	if newContent == nil {
		return evt
	}
	newContent.RelatesTo = original.RelatesTo
	newRaw, _ := edit.Content.Raw["m.new_content"].(map[string]any)
	edited := *evt
	edited.Content = event.Content{Parsed: newContent, Raw: newRaw}
	return &edited
}
