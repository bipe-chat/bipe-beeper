package qrlogin

// CheckCode is the two bytes that both ends of a secure channel derive. The
// device that scanned the QR code shows it as a two-digit number, and the user
// types the number on the new device. A match proves that no third party sits
// in the channel.
type CheckCode [2]byte

// digitAllowLeadingZero is the MSC4108 (ECIES) form: 00 to 99.
func (c CheckCode) digitAllowLeadingZero() uint8 {
	return (c[0]%10)*10 + c[1]%10
}

// digitNoLeadingZero is the MSC4388 (HPKE) form: 10 to 99.
func (c CheckCode) digitNoLeadingZero() uint8 {
	return (c[0]%9+1)*10 + c[1]%10
}
