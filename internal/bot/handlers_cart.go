package bot

import (
"context"
"fmt"
"strconv"

tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleCart displays the user's cart with quantity controls and totals.
func (b *Bot) handleCart(msg *tgbotapi.Message) {
b.sendCart(msg.Chat.ID, msg.From.ID, 0, msg.From.LanguageCode)
}

// sendCart sends the cart view. If msgID > 0, it edits the existing message.
func (b *Bot) sendCart(chatID, userID int64, msgID int, lang string) {
ctx := context.Background()
view, err := b.cart.Get(ctx, userID)
if err != nil {
b.logger.Error("get cart", "error", err)
b.sendOrEditStyled(chatID, msgID, b.t(lang, "error_load_cart"), "", nil)
return
}

if len(view.Items) == 0 {
kb := StyledKeyboard{
{Btn(b.t(lang, "btn_back"), "back:catalog"), Btn(b.t(lang, "btn_menu"), "back:menu")},
}
b.sendOrEditStyled(chatID, msgID, b.t(lang, "cart_empty_text"), "", kb)
return
}

kb := make(StyledKeyboard, 0, len(view.Items)*2+3)
for _, item := range view.Items {
pid := strconv.FormatInt(item.Product.ID, 10)
kb = append(kb,
[]StyledButton{{Text: "\U0001f6cd " + item.Product.Name, CallbackData: "noop"}},
[]StyledButton{
Btn("➖", "cart:minus:"+pid),
Btn(fmt.Sprintf("  %d шт.  ", item.Quantity), "noop"),
Btn("➕", "cart:plus:"+pid),
b.styledBtn(BtnKeyCartRemove, "🗑 Убрать", "cart:del:"+pid, StyleDanger),
},
)
}
kb = append(kb,
[]StyledButton{b.styledBtn(BtnKeyCartCheckout, b.t(lang, "btn_checkout"), "cart:checkout", StyleSuccess)},
[]StyledButton{Btn(b.t(lang, "btn_back"), "back:catalog"), Btn(b.t(lang, "btn_menu"), "back:menu")},
)

b.sendOrEditStyled(chatID, msgID, b.formatCartText(lang, view), "HTML", kb)
}

func (b *Bot) onCartAdd(cbID string, chatID, userID int64, msgID int, data, lang string) {
prodID, err := parseIDFromCallback(data, "cart:add:")
if err != nil {
b.logger.Error("parse cart:add callback", "error", err)
b.ack(cbID)
return
}

ctx := context.Background()
if err := b.cart.Add(ctx, userID, prodID); err != nil {
b.logger.Error("add to cart", "error", err)
b.alert(cbID, b.t(lang, "error_add_cart"))
return
}

b.toast(cbID, b.t(lang, "cart_item_added"))
b.refreshProductKeyboard(chatID, userID, msgID, prodID, lang)
}

func (b *Bot) onProductQuantityChange(cbID string, chatID, userID int64, msgID int, data, prefix string, delta int, lang string) {
prodID, err := parseIDFromCallback(data, prefix)
if err != nil {
b.logger.Error("parse product quantity callback", "prefix", prefix, "error", err)
b.ack(cbID)
return
}

ctx := context.Background()
if err := b.cart.ChangeQuantity(ctx, userID, prodID, delta); err != nil {
b.logger.Error("change quantity from product card", "product_id", prodID, "delta", delta, "error", err)
b.alert(cbID, b.t(lang, "error_short"))
return
}

b.ack(cbID)
b.refreshProductKeyboard(chatID, userID, msgID, prodID, lang)
}

func (b *Bot) onCartPlus(chatID, userID int64, msgID int, data, lang string) {
prodID, err := parseIDFromCallback(data, "cart:plus:")
if err != nil {
b.logger.Error("parse cart:plus callback", "error", err)
return
}

ctx := context.Background()
if err := b.cart.ChangeQuantity(ctx, userID, prodID, 1); err != nil {
b.logger.Error("cart plus", "error", err)
b.sendOrEditStyled(chatID, 0, b.t(lang, "error_short"), "", nil)
return
}
b.sendCart(chatID, userID, msgID, lang)
}

func (b *Bot) onCartMinus(chatID, userID int64, msgID int, data, lang string) {
prodID, err := parseIDFromCallback(data, "cart:minus:")
if err != nil {
b.logger.Error("parse cart:minus callback", "error", err)
return
}

ctx := context.Background()
view, err := b.cart.Get(ctx, userID)
if err != nil {
b.logger.Error("get cart for minus", "error", err)
b.sendOrEditStyled(chatID, 0, b.t(lang, "error_short"), "", nil)
return
}

for _, item := range view.Items {
if item.Product.ID == prodID {
if item.Quantity <= 1 {
if err := b.cart.Remove(ctx, userID, prodID); err != nil {
b.logger.Error("cart remove on minus", "error", err)
b.sendOrEditStyled(chatID, 0, b.t(lang, "error_short"), "", nil)
return
}
} else {
if err := b.cart.ChangeQuantity(ctx, userID, prodID, -1); err != nil {
b.logger.Error("cart minus", "error", err)
b.sendOrEditStyled(chatID, 0, b.t(lang, "error_short"), "", nil)
return
}
}
break
}
}
b.sendCart(chatID, userID, msgID, lang)
}

func (b *Bot) onCartDel(chatID, userID int64, msgID int, data, lang string) {
prodID, err := parseIDFromCallback(data, "cart:del:")
if err != nil {
b.logger.Error("parse cart:del callback", "error", err)
return
}

ctx := context.Background()
if err := b.cart.Remove(ctx, userID, prodID); err != nil {
b.logger.Error("cart del", "error", err)
b.sendOrEditStyled(chatID, 0, b.t(lang, "error_remove_cart"), "", nil)
return
}
b.sendCart(chatID, userID, msgID, lang)
}

func (b *Bot) onCartCheckout(chatID, userID int64, msgID int, lang string) {
ctx := context.Background()
view, err := b.cart.Get(ctx, userID)
if err != nil {
b.logger.Error("get cart for checkout", "error", err)
return
}

if len(view.Items) == 0 {
b.sendOrEditStyled(chatID, msgID, b.t(lang, "cart_empty_text"), "", nil)
return
}

kb := StyledKeyboard{
{Btn(b.t(lang, "btn_enter_promo"), "promo:enter")},
{b.styledBtn(BtnKeyCartCheckout, b.t(lang, "btn_confirm_order"), "order:confirm", StyleSuccess)},
{Btn(b.t(lang, "btn_back_to_cart"), "back:cart"), Btn(b.t(lang, "btn_menu"), "back:menu")},
}

b.sendOrEditStyled(chatID, msgID, b.formatCheckoutText(lang, view), "HTML", kb)
}
