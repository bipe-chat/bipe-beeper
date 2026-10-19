package connector

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// typingTimeout is how long Beeper shows a remote user as typing without a new typing event.
const typingTimeout = 30 * time.Second

// ignoredPrefixes are remote event types that have no meaning in Beeper. The bridge drops them
// and does not show the unknown event fallback.
var ignoredPrefixes = []string{
	"m.call.", "org.matrix.call.", "org.matrix.msc3401.call", "m.rtc.", "org.matrix.msc4143.",
	"m.key.verification.", "org.matrix.msc3672.beacon", "m.beacon",
	"org.matrix.msc4075.", "io.element.", "im.vector.",
}

func isInitialSync(ctx context.Context) bool {
	since, _ := ctx.Value(mautrix.SyncTokenContextKey).(string)
	return since == ""
}

// handleSync runs once for each sync response, before the events of the response.
func (c *Client) handleSync(ctx context.Context, resp *mautrix.RespSync, since string) bool {
	initial := since == ""
	c.initialSync.Store(initial)
	for roomID, room := range resp.Rooms.Join {
		if initial || room.Timeline.Limited {
			// On the first sync, every joined room gets a portal and a backfill.
			// After a gap (limited timeline), the backfill fills the gap.
			c.queueChatResync(roomID, true, latestTimestamp(room.Timeline.Events))
		}
	}
	for roomID, room := range resp.Rooms.Invite {
		c.queueInvite(ctx, roomID, room.State.Events)
	}
	for roomID := range resp.Rooms.Leave {
		// The user left, declined the invite, or was removed. The portal goes away in Beeper too.
		c.login.QueueRemoteEvent(&simplevent.ChatDelete{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventChatDelete,
				PortalKey: c.portalKey(roomID),
				Sender:    c.sender(c.userID),
			},
			OnlyForMe: true,
		})
	}
	for _, evt := range resp.AccountData.Events {
		if evt.Type.Type == event.AccountDataDirectChats.Type {
			c.setDirectRooms(parseDirect(evt.Content.VeryRaw))
		}
	}
	if !c.connected.Swap(true) {
		c.login.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	}
	return true
}

func latestTimestamp(events []*event.Event) time.Time {
	if len(events) == 0 {
		return time.Time{}
	}
	return time.UnixMilli(events[len(events)-1].Timestamp)
}

func (c *Client) queueChatResync(roomID id.RoomID, backfill bool, latest time.Time) {
	evt := &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    c.portalKey(roomID),
			CreatePortal: true,
		},
		GetChatInfoFunc: c.GetChatInfo,
	}
	if backfill {
		evt.LatestMessageTS = latest
		evt.CheckNeedsBackfillFunc = func(ctx context.Context, latestMessage *database.Message) (bool, error) {
			return latestMessage == nil || latest.IsZero() || latestMessage.Timestamp.Before(latest), nil
		}
	}
	c.login.QueueRemoteEvent(evt)
}

// queueInvite shows a remote invite in Beeper as a message request. The bridge does not join the room.
func (c *Client) queueInvite(ctx context.Context, roomID id.RoomID, state []*event.Event) {
	for _, evt := range state {
		evt.RoomID = roomID
	}
	info := c.chatInfoFromState(ctx, roomID, state, true)
	c.login.QueueRemoteEvent(&simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    c.portalKey(roomID),
			CreatePortal: true,
		},
		ChatInfo: info,
	})
}

// handleEvent runs for each event of a sync response, and again for each decrypted event.
func (c *Client) handleEvent(ctx context.Context, evt *event.Event) {
	source := evt.Mautrix.EventSource
	if source&event.SourceJoin == 0 || evt.Type == event.EventEncrypted {
		return
	}
	if source&event.SourceEphemeral != 0 {
		if !isInitialSync(ctx) {
			c.handleEphemeral(ctx, evt)
		}
		return
	}
	if source&event.SourceTimeline == 0 || isInitialSync(ctx) {
		// State outside the timeline and the first sync come in through the chat resync and the backfill.
		return
	}
	if evt.Unsigned.RedactedBecause != nil || isBridgeEcho(evt) {
		return
	}
	log := zerolog.Ctx(ctx).With().Stringer("room_id", evt.RoomID).Stringer("event_id", evt.ID).Logger()
	if evt.StateKey != nil {
		c.handleStateEvent(log.WithContext(ctx), evt)
		return
	}
	switch evt.Type {
	case event.EventReaction:
		c.handleReaction(evt)
	case event.EventRedaction:
		c.handleRedaction(ctx, evt)
	default:
		if isIgnoredType(evt.Type) {
			return
		}
		c.login.QueueRemoteEvent(c.remoteMessage(evt))
	}
}

// isBridgeEcho reports whether the bridge sent the event itself (see txnID). The bridge already has it.
func isBridgeEcho(evt *event.Event) bool {
	return strings.HasPrefix(evt.Unsigned.TransactionID, txnPrefix)
}

func isIgnoredType(evtType event.Type) bool {
	for _, prefix := range ignoredPrefixes {
		if strings.HasPrefix(evtType.Type, prefix) {
			return true
		}
	}
	return false
}

// remoteMessage wraps a remote timeline event as a new message or an edit.
func (c *Client) remoteMessage(evt *event.Event) bridgev2.RemoteEvent {
	meta := simplevent.EventMeta{
		Type:         bridgev2.RemoteEventMessage,
		PortalKey:    c.portalKey(evt.RoomID),
		Sender:       c.sender(evt.Sender),
		CreatePortal: true,
		Timestamp:    time.UnixMilli(evt.Timestamp),
	}
	if content, ok := evt.Content.Parsed.(*event.MessageEventContent); ok {
		if target := content.RelatesTo.GetReplaceID(); target != "" {
			meta.Type = bridgev2.RemoteEventEdit
			return &simplevent.Message[*event.Event]{
				EventMeta:       meta,
				Data:            evt,
				ID:              networkid.MessageID(evt.ID),
				TargetMessage:   networkid.MessageID(target),
				ConvertEditFunc: c.convertEditToMatrix,
			}
		}
	}
	return &simplevent.Message[*event.Event]{
		EventMeta:          meta,
		Data:               evt,
		ID:                 networkid.MessageID(evt.ID),
		TransactionID:      networkid.TransactionID(evt.Unsigned.TransactionID),
		ConvertMessageFunc: c.convertToMatrix,
	}
}

// handleDecryptError shows a message that this device cannot decrypt as a notice.
func (c *Client) handleDecryptError(evt *event.Event, err error) {
	if evt.Mautrix.EventSource&event.SourceTimeline == 0 || c.initialSync.Load() || isBridgeEcho(evt) {
		// The backfill shows the events of the first sync.
		return
	}
	c.login.Log.Warn().Err(err).Stringer("event_id", evt.ID).Msg("Failed to decrypt a remote event")
	c.login.QueueRemoteEvent(c.remoteMessage(evt))
}

func (c *Client) handleReaction(evt *event.Event) {
	content, ok := evt.Content.Parsed.(*event.ReactionEventContent)
	if !ok || content.RelatesTo.Type != event.RelAnnotation {
		return
	}
	// The emoji ID is the remote reaction event ID, so that a remote redaction can find the reaction.
	c.login.QueueRemoteEvent(&simplevent.Reaction{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReaction,
			PortalKey: c.portalKey(evt.RoomID),
			Sender:    c.sender(evt.Sender),
			Timestamp: time.UnixMilli(evt.Timestamp),
		},
		TargetMessage: networkid.MessageID(content.RelatesTo.EventID),
		EmojiID:       networkid.EmojiID(evt.ID),
		Emoji:         content.RelatesTo.Key,
	})
}

// handleRedaction removes a message or a reaction in Beeper.
func (c *Client) handleRedaction(ctx context.Context, evt *event.Event) {
	redacts := evt.Redacts
	if content, ok := evt.Content.Parsed.(*event.RedactionEventContent); ok && content.Redacts != "" {
		redacts = content.Redacts
	}
	if redacts == "" {
		return
	}
	meta := simplevent.EventMeta{
		PortalKey: c.portalKey(evt.RoomID),
		Sender:    c.sender(evt.Sender),
		Timestamp: time.UnixMilli(evt.Timestamp),
	}
	reaction, err := c.reactionByRemoteID(ctx, evt.RoomID, redacts)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to find the target of a redaction")
		return
	}
	if reaction != nil {
		meta.Type = bridgev2.RemoteEventReactionRemove
		meta.Sender = c.sender(id.UserID(reaction.SenderID))
		c.login.QueueRemoteEvent(&simplevent.Reaction{
			EventMeta:     meta,
			TargetMessage: reaction.MessageID,
			EmojiID:       reaction.EmojiID,
		})
		return
	}
	meta.Type = bridgev2.RemoteEventMessageRemove
	c.login.QueueRemoteEvent(&simplevent.MessageRemove{
		EventMeta:     meta,
		TargetMessage: networkid.MessageID(redacts),
	})
}

// reactionByRemoteID finds a bridged reaction by its remote event ID, which is the emoji ID.
func (c *Client) reactionByRemoteID(ctx context.Context, roomID id.RoomID, eventID id.EventID) (*database.Reaction, error) {
	row := c.main.br.DB.QueryRow(ctx,
		`SELECT message_id, sender_id, emoji_id FROM reaction WHERE bridge_id=$1 AND room_id=$2 AND room_receiver=$3 AND emoji_id=$4`,
		c.main.br.ID, roomID, c.login.ID, eventID)
	var reaction database.Reaction
	err := row.Scan(&reaction.MessageID, &reaction.SenderID, &reaction.EmojiID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &reaction, nil
}

func (c *Client) handleStateEvent(ctx context.Context, evt *event.Event) {
	meta := simplevent.EventMeta{
		Type:      bridgev2.RemoteEventChatInfoChange,
		PortalKey: c.portalKey(evt.RoomID),
		Sender:    c.sender(evt.Sender),
		Timestamp: time.UnixMilli(evt.Timestamp),
	}
	change := &bridgev2.ChatInfoChange{ChatInfo: &bridgev2.ChatInfo{}}
	switch evt.Type {
	case event.StateRoomName:
		change.ChatInfo.Name = ptr.Ptr(evt.Content.AsRoomName().Name)
	case event.StateTopic:
		change.ChatInfo.Topic = ptr.Ptr(evt.Content.AsTopic().Topic)
	case event.StateRoomAvatar:
		change.ChatInfo.Avatar = c.makeAvatar(evt.Content.AsRoomAvatar().URL)
	case event.StateMember:
		userID := id.UserID(evt.GetStateKey())
		member := evt.Content.AsMember()
		if userID == c.userID && member.Membership == event.MembershipJoin {
			// The user joined on another device, or accepted an invite: make the portal and backfill it.
			c.queueChatResync(evt.RoomID, true, time.UnixMilli(evt.Timestamp))
			return
		}
		change.ChatInfo = nil
		change.MemberChanges = &bridgev2.ChatMemberList{MemberMap: bridgev2.ChatMemberMap{}}
		change.MemberChanges.MemberMap.Set(bridgev2.ChatMember{
			EventSender:  c.sender(userID),
			Membership:   member.Membership,
			UserInfo:     c.userInfoFromMember(userID, member),
			MemberSender: c.sender(evt.Sender),
		})
	default:
		return
	}
	c.login.QueueRemoteEvent(&simplevent.ChatInfoChange{EventMeta: meta, ChatInfoChange: change})
}

func (c *Client) handleEphemeral(ctx context.Context, evt *event.Event) {
	switch evt.Type {
	case event.EphemeralEventReceipt:
		content, ok := evt.Content.Parsed.(*event.ReceiptEventContent)
		if !ok {
			return
		}
		for eventID, receipts := range *content {
			for userID, receipt := range receipts[event.ReceiptTypeRead] {
				c.login.QueueRemoteEvent(&simplevent.Receipt{
					EventMeta: simplevent.EventMeta{
						Type:      bridgev2.RemoteEventReadReceipt,
						PortalKey: c.portalKey(evt.RoomID),
						Sender:    c.sender(userID),
						Timestamp: receipt.Timestamp,
					},
					LastTarget: networkid.MessageID(eventID),
					Targets:    []networkid.MessageID{networkid.MessageID(eventID)},
					ReadUpTo:   receipt.Timestamp,
				})
			}
		}
	case event.EphemeralEventTyping:
		content, ok := evt.Content.Parsed.(*event.TypingEventContent)
		if !ok {
			return
		}
		c.handleTyping(evt.RoomID, content.UserIDs)
	}
}

// handleTyping sends a typing start for each new typing user and a typing stop for each user that stopped.
func (c *Client) handleTyping(roomID id.RoomID, userIDs []id.UserID) {
	c.typingLock.Lock()
	prev := c.typing[roomID]
	now := make(map[id.UserID]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID != c.userID {
			now[userID] = struct{}{}
		}
	}
	c.typing[roomID] = now
	c.typingLock.Unlock()

	send := func(userID id.UserID, timeout time.Duration) {
		c.login.QueueRemoteEvent(&simplevent.Typing{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventTyping,
				PortalKey: c.portalKey(roomID),
				Sender:    c.sender(userID),
			},
			Timeout: timeout,
			Type:    bridgev2.TypingTypeText,
		})
	}
	for userID := range now {
		send(userID, typingTimeout)
	}
	for userID := range prev {
		if _, ok := now[userID]; !ok {
			send(userID, 0)
		}
	}
}
