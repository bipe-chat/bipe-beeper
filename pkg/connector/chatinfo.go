package connector

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// loadDirectRooms reads m.direct, the list of DM rooms of the remote account.
func (c *Client) loadDirectRooms(ctx context.Context) {
	var direct event.DirectChatsEventContent
	err := c.cli.GetAccountData(ctx, event.AccountDataDirectChats.Type, &direct)
	if err != nil && !errors.Is(err, mautrix.MNotFound) {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get m.direct")
		return
	}
	c.setDirectRooms(direct)
}

func (c *Client) setDirectRooms(direct event.DirectChatsEventContent) {
	rooms := make(map[id.RoomID]id.UserID)
	for userID, roomIDs := range direct {
		for _, roomID := range roomIDs {
			rooms[roomID] = userID
		}
	}
	c.dmLock.Lock()
	c.dmRooms = rooms
	c.dmLock.Unlock()
}

func (c *Client) directUser(roomID id.RoomID) id.UserID {
	c.dmLock.RLock()
	defer c.dmLock.RUnlock()
	return c.dmRooms[roomID]
}

// GetChatInfo reads the full state of a joined room.
func (c *Client) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	if err := c.requireLogin(); err != nil {
		return nil, err
	}
	roomID := roomIDOf(portal)
	state, err := c.cli.State(ctx, roomID)
	if err != nil {
		if errors.Is(err, mautrix.MForbidden) {
			// The user is not in the room yet. An invite gives its chat info from the invite state.
			return nil, nil
		}
		return nil, err
	}
	var events []*event.Event
	for _, byKey := range state {
		for _, evt := range byKey {
			events = append(events, evt)
		}
	}
	return c.chatInfoFromState(ctx, roomID, events, false), nil
}

// chatInfoFromState makes the chat info from room state events.
// For an invite, the events are the stripped invite state.
func (c *Client) chatInfoFromState(ctx context.Context, roomID id.RoomID, events []*event.Event, invite bool) *bridgev2.ChatInfo {
	info := &bridgev2.ChatInfo{
		Members: &bridgev2.ChatMemberList{
			IsFull:    !invite,
			MemberMap: make(bridgev2.ChatMemberMap),
		},
		CanBackfill: !invite,
	}
	var name, topic string
	var avatarURL id.ContentURIString
	var hasAvatar, ownDirect bool
	var inviter id.UserID
	for _, evt := range events {
		if evt.StateKey == nil {
			continue
		}
		_ = evt.Content.ParseRaw(evt.Type)
		switch evt.Type {
		case event.StateRoomName:
			name = evt.Content.AsRoomName().Name
		case event.StateTopic:
			topic = evt.Content.AsTopic().Topic
		case event.StateRoomAvatar:
			avatarURL = evt.Content.AsRoomAvatar().URL
			hasAvatar = true
		case event.StateMember:
			member := evt.Content.AsMember()
			userID := id.UserID(*evt.StateKey)
			switch member.Membership {
			case event.MembershipJoin, event.MembershipInvite:
			default:
				continue
			}
			if userID == c.userID {
				ownDirect = member.IsDirect
				if member.Membership == event.MembershipInvite {
					inviter = evt.Sender
				}
			}
			info.Members.MemberMap.Set(bridgev2.ChatMember{
				EventSender: c.sender(userID),
				Membership:  member.Membership,
				UserInfo:    c.userInfoFromMember(userID, member),
			})
		}
	}
	if inviter != "" {
		if _, ok := info.Members.MemberMap[networkid.UserID(inviter)]; !ok {
			info.Members.MemberMap.Set(bridgev2.ChatMember{EventSender: c.sender(inviter), Membership: event.MembershipJoin})
		}
	}
	info.Members.TotalMemberCount = len(info.Members.MemberMap)

	otherUser := c.directUser(roomID)
	if otherUser == "" && (ownDirect || (name == "" && len(info.Members.MemberMap) == 2)) {
		for userID := range info.Members.MemberMap {
			if id.UserID(userID) != c.userID {
				otherUser = id.UserID(userID)
			}
		}
	}
	if otherUser != "" && len(info.Members.MemberMap) <= 2 {
		info.Type = ptr.Ptr(database.RoomTypeDM)
		info.Members.OtherUserID = networkid.UserID(otherUser)
	} else {
		info.Type = ptr.Ptr(database.RoomTypeDefault)
	}
	if name != "" || *info.Type != database.RoomTypeDM {
		info.Name = &name
	}
	if !invite || topic != "" {
		info.Topic = &topic
	}
	if hasAvatar || (!invite && *info.Type != database.RoomTypeDM) {
		info.Avatar = c.makeAvatar(avatarURL)
	}
	if invite {
		// Invites stay requests until the user accepts them. The bridge never joins for the user.
		info.MessageRequest = ptr.Ptr(true)
	} else {
		info.MessageRequest = ptr.Ptr(false)
	}
	return info
}

// makeAvatar gives a bridge avatar that downloads the remote file when the bridge needs it.
func (c *Client) makeAvatar(uri id.ContentURIString) *bridgev2.Avatar {
	if uri == "" {
		return &bridgev2.Avatar{Remove: true}
	}
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(uri),
		Get: func(ctx context.Context) ([]byte, error) {
			parsed, err := uri.Parse()
			if err != nil {
				return nil, err
			}
			return c.cli.DownloadBytes(ctx, parsed)
		},
	}
}

func (c *Client) userInfoFromMember(userID id.UserID, member *event.MemberEventContent) *bridgev2.UserInfo {
	if userID == c.userID {
		return nil
	}
	name := member.Displayname
	if name == "" {
		name = userID.Localpart()
	}
	return &bridgev2.UserInfo{
		Name:        &name,
		Avatar:      c.makeAvatar(member.AvatarURL),
		Identifiers: []string{"matrix:" + userID.String()},
	}
}

// GetUserInfo reads the global profile of a remote user.
func (c *Client) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	if err := c.requireLogin(); err != nil {
		return nil, err
	}
	userID := id.UserID(ghost.ID)
	profile, err := c.cli.GetProfile(ctx, userID)
	if err != nil {
		if errors.Is(err, mautrix.MNotFound) || errors.Is(err, mautrix.MForbidden) {
			return c.userInfoFromMember(userID, &event.MemberEventContent{}), nil
		}
		return nil, err
	}
	return c.userInfoFromMember(userID, &event.MemberEventContent{
		Displayname: profile.DisplayName,
		AvatarURL:   profile.AvatarURL.CUString(),
	}), nil
}

// parseDirect parses the content of an m.direct event.
func parseDirect(raw json.RawMessage) event.DirectChatsEventContent {
	var direct event.DirectChatsEventContent
	_ = json.Unmarshal(raw, &direct)
	return direct
}
