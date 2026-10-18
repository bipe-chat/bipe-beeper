package connector

import (
	"context"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

// Client is the Matrix client of one login on the remote homeserver.
// It does nothing until the QR sign-in (bipe#422) and the sync (bipe#424) exist.
type Client struct {
	login *bridgev2.UserLogin
}

var _ bridgev2.NetworkAPI = (*Client)(nil)

func (c *Client) Connect(ctx context.Context) {}

func (c *Client) Disconnect() {}

func (c *Client) IsLoggedIn() bool {
	return false
}

func (c *Client) LogoutRemote(ctx context.Context) {}

func (c *Client) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return networkid.UserID(c.login.ID) == userID
}

func (c *Client) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	return nil, bridgev2.ErrNotLoggedIn
}

func (c *Client) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	return nil, bridgev2.ErrNotLoggedIn
}

func (c *Client) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	return &event.RoomFeatures{}
}

func (c *Client) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	return nil, bridgev2.ErrNotLoggedIn
}
