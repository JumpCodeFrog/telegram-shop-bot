package bot

// onSupport shows support information as an inline message edit.
func (b *Bot) onSupport(chatID int64, msgID int, lang string) {
kb := StyledKeyboard{
{Btn(b.t(lang, "btn_back"), "back:catalog"), Btn(b.t(lang, "btn_menu"), "back:menu")},
}
b.sendOrEditStyled(chatID, msgID, b.t(lang, "support_welcome"), "HTML", kb)
}

func (b *Bot) onPaySupport(chatID int64, msgID int, lang string) {
kb := StyledKeyboard{
{Btn(b.t(lang, "btn_terms"), "terms"), Btn(b.t(lang, "btn_back"), "back:orders")},
{Btn(b.t(lang, "btn_menu"), "back:menu")},
}
b.sendOrEditStyled(chatID, msgID, b.t(lang, "paysupport_welcome"), "HTML", kb)
}

func (b *Bot) onTerms(chatID int64, msgID int, lang string) {
kb := StyledKeyboard{
{Btn(b.t(lang, "btn_paysupport"), "paysupport"), Btn(b.t(lang, "btn_back"), "back:catalog")},
{Btn(b.t(lang, "btn_menu"), "back:menu")},
}
b.sendOrEditStyled(chatID, msgID, b.t(lang, "terms_text"), "HTML", kb)
}
