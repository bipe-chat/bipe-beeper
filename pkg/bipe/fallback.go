package bipe

// UnknownEventText replaces a Bipe event that the bridge cannot show in Beeper.
// The Bipe-only fallbacks (bipe-chat/bipe#425) replace it for each known type.
const UnknownEventText = "Mensagem do Bipe. Abra o app para ver."

// UndecryptableText replaces a message that this bridge device has no key for.
const UndecryptableText = "Não deu para ler esta mensagem aqui. Abra o Bipe para ver."
