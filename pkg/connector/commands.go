package connector

import (
	"maunium.net/go/mautrix/bridgev2/commands"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
)

const (
	cmdVerifyConfirm = "confirmar"
	cmdVerifyReject  = "recusar"
)

func (c *Connector) registerCommands() {
	proc, ok := c.br.Commands.(*commands.Processor)
	if !ok {
		return
	}
	proc.AddHandlers(
		&commands.FullHandler{
			Name: cmdVerifyConfirm,
			Help: commands.HelpMeta{Section: commands.HelpSectionAuth, Description: "Confirm that the verification emojis match the phone."},
			Func: func(ce *commands.Event) { answerVerification(ce, true) },
		},
		&commands.FullHandler{
			Name: cmdVerifyReject,
			Help: commands.HelpMeta{Section: commands.HelpSectionAuth, Description: "Say that the verification emojis differ from the phone."},
			Func: func(ce *commands.Event) { answerVerification(ce, false) },
		},
	)
}

func answerVerification(ce *commands.Event, confirm bool) {
	for _, login := range ce.User.GetUserLogins() {
		client, ok := login.Client.(*Client)
		if ok && client.verification != nil && client.verification.answer(ce.Ctx, confirm) {
			return
		}
	}
	ce.Reply(bipe.VerifyNothingOpen)
}
