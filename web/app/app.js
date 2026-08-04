/* Telegram Mini App shop client. ES5 + fetch, no build step.
   Screens: catalog (categories -> products), product card, cart/checkout.
   All strings come from GET /api/i18n; errors from the API are i18n keys. */
(function () {
  'use strict';

  var tg = window.Telegram && window.Telegram.WebApp;
  var initData = tg ? tg.initData : '';
  var dict = {};
  var cartCount = 0;
  // Navigation stack of {render: fn} so the back button always works.
  var navStack = [];

  var screenEl = document.getElementById('screen');
  var titleEl = document.getElementById('title');
  var backBtn = document.getElementById('back-btn');
  var cartBtn = document.getElementById('cart-btn');
  var cartBadge = document.getElementById('cart-badge');

  // ---- i18n ----------------------------------------------------------------

  function t(key) {
    return dict[key] || key;
  }

  // tf('%d items', 3) — sequential %d/%s substitution, mirrors Go's Sprintf use.
  function tf(key) {
    var args = Array.prototype.slice.call(arguments, 1);
    var i = 0;
    return t(key).replace(/%[ds]/g, function (m) {
      return i < args.length ? String(args[i++]) : m;
    });
  }

  // ---- API -----------------------------------------------------------------

  function authHeaders() {
    return { 'Authorization': 'tma ' + initData };
  }

  function api(method, path, body) {
    var opts = { method: method, headers: authHeaders() };
    if (body) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    return fetch(path, opts).then(function (resp) {
      return resp.json().then(function (data) {
        if (!resp.ok) {
          throw new Error(data && data.error ? data.error : 'webapp_err_internal');
        }
        return data;
      });
    });
  }

  function showError(err) {
    var key = err && err.message ? err.message : 'webapp_err_internal';
    if (tg && tg.showAlert) {
      tg.showAlert(t(key));
    } else {
      alert(t(key));
    }
  }

  // The photo endpoint requires the tma header, so <img src> cannot load it
  // directly — fetch as a blob instead. Public http(s) URLs load as-is.
  function loadImage(img, src) {
    if (!src) { img.className += ' hidden'; return; }
    if (src.indexOf('/api/') !== 0) { img.src = src; return; }
    fetch(src, { headers: authHeaders() }).then(function (resp) {
      if (!resp.ok) { throw new Error('photo'); }
      return resp.blob();
    }).then(function (blob) {
      img.src = URL.createObjectURL(blob);
    }).catch(function () { img.className += ' hidden'; });
  }

  // ---- rendering helpers -----------------------------------------------------

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) { node.className = className; }
    if (text !== undefined && text !== null) { node.textContent = text; }
    return node;
  }

  function clearScreen() {
    while (screenEl.firstChild) { screenEl.removeChild(screenEl.firstChild); }
  }

  function setTitle(text) {
    titleEl.textContent = text;
  }

  function push(render) {
    navStack.push(render);
    backBtn.className = navStack.length > 1 ? 'icon-btn' : 'icon-btn hidden';
    render();
  }

  function pop() {
    if (navStack.length <= 1) { return; }
    navStack.pop();
    backBtn.className = navStack.length > 1 ? 'icon-btn' : 'icon-btn hidden';
    navStack[navStack.length - 1]();
  }

  function updateCartBadge(count) {
    cartCount = count;
    cartBadge.textContent = String(count);
    cartBadge.className = count > 0 ? '' : 'hidden';
  }

  function countItems(cart) {
    var n = 0;
    for (var i = 0; i < cart.items.length; i++) { n += cart.items[i].quantity; }
    return n;
  }

  function stars(n) {
    return n + ' \u2b50';
  }

  function usd(n) {
    return '$' + n.toFixed(2);
  }

  function loading() {
    clearScreen();
    screenEl.appendChild(el('div', 'loading', t('webapp_loading')));
  }

  // ---- screen: catalog (categories) -----------------------------------------

  function renderCatalog() {
    setTitle(t('webapp_catalog'));
    loading();
    api('GET', '/api/catalog').then(function (data) {
      clearScreen();
      var list = el('div', 'list');
      for (var i = 0; i < data.categories.length; i++) {
        (function (cat) {
          var row = el('button', 'row', (cat.emoji ? cat.emoji + ' ' : '') + cat.name);
          row.type = 'button';
          row.onclick = function () { push(function () { renderProducts(cat); }); };
          list.appendChild(row);
        })(data.categories[i]);
      }
      if (!data.categories.length) {
        list.appendChild(el('div', 'empty', t('webapp_empty')));
      }
      screenEl.appendChild(list);
    }).catch(showError);
  }

  // ---- screen: product list --------------------------------------------------

  function renderProducts(cat, page) {
    page = page || 1;
    setTitle((cat.emoji ? cat.emoji + ' ' : '') + cat.name);
    loading();
    api('GET', '/api/products?category=' + cat.id + '&page=' + page).then(function (data) {
      clearScreen();
      var list = el('div', 'list');
      for (var i = 0; i < data.products.length; i++) {
        (function (p) {
          var card = el('button', 'card');
          card.type = 'button';
          var img = el('img', 'thumb');
          loadImage(img, p.photo);
          card.appendChild(img);
          var info = el('div', 'card-info');
          info.appendChild(el('div', 'card-name', p.name));
          info.appendChild(el('div', 'card-price', usd(p.price_usd) + ' / ' + stars(p.price_stars)));
          card.appendChild(info);
          card.onclick = function () { push(function () { renderProduct(p.id); }); };
          list.appendChild(card);
        })(data.products[i]);
      }
      if (!data.products.length) {
        list.appendChild(el('div', 'empty', t('webapp_empty')));
      }
      screenEl.appendChild(list);

      var pages = Math.ceil(data.total / data.per_page);
      if (pages > 1) {
        var pager = el('div', 'pager');
        var prev = el('button', 'btn secondary', '\u2039');
        prev.type = 'button';
        prev.disabled = page <= 1;
        prev.onclick = function () { renderProducts(cat, page - 1); };
        var next = el('button', 'btn secondary', '\u203a');
        next.type = 'button';
        next.disabled = page >= pages;
        next.onclick = function () { renderProducts(cat, page + 1); };
        pager.appendChild(prev);
        pager.appendChild(el('span', 'pager-label', page + ' / ' + pages));
        pager.appendChild(next);
        screenEl.appendChild(pager);
      }
    }).catch(showError);
  }

  // ---- screen: product card ---------------------------------------------------

  function renderProduct(id) {
    loading();
    api('GET', '/api/products/' + id).then(function (data) {
      var p = data.product;
      setTitle(p.name);
      clearScreen();

      if (data.photos.length) {
        var gallery = el('div', 'gallery');
        for (var i = 0; i < data.photos.length; i++) {
          var img = el('img', 'photo');
          loadImage(img, data.photos[i]);
          gallery.appendChild(img);
        }
        screenEl.appendChild(gallery);
      }

      screenEl.appendChild(el('h2', 'product-name', p.name));
      screenEl.appendChild(el('div', 'product-price', usd(p.price_usd) + ' / ' + stars(p.price_stars)));
      if (data.rating_count > 0) {
        screenEl.appendChild(el('div', 'product-rating',
          '\u2605 ' + data.rating_avg.toFixed(1) + ' \u00b7 ' + tf('webapp_reviews', data.rating_count)));
      }
      if (p.sub_period_days) {
        screenEl.appendChild(el('div', 'product-sub', tf('webapp_subscription', p.sub_period_days)));
      }
      if (p.description) {
        screenEl.appendChild(el('p', 'product-desc', p.description));
      }
      screenEl.appendChild(el('div', 'product-stock', tf('webapp_stock', p.stock)));

      var add = el('button', 'btn primary', t('webapp_add_to_cart'));
      add.type = 'button';
      add.onclick = function () {
        add.disabled = true;
        api('POST', '/api/cart', { product_id: p.id, delta: 1 }).then(function (cart) {
          updateCartBadge(countItems(cart));
          add.disabled = false;
          if (tg && tg.HapticFeedback) { tg.HapticFeedback.notificationOccurred('success'); }
        }).catch(function (err) {
          add.disabled = false;
          showError(err);
        });
      };
      screenEl.appendChild(add);
    }).catch(showError);
  }

  // ---- screen: cart + checkout -------------------------------------------------

  function renderCart() {
    setTitle(t('webapp_cart'));
    loading();
    api('GET', '/api/cart').then(function (cart) {
      clearScreen();
      updateCartBadge(countItems(cart));

      if (!cart.items.length) {
        screenEl.appendChild(el('div', 'empty', t('webapp_cart_empty')));
        return;
      }

      var list = el('div', 'list');
      for (var i = 0; i < cart.items.length; i++) {
        (function (item) {
          var row = el('div', 'cart-row');
          var info = el('div', 'card-info');
          info.appendChild(el('div', 'card-name', item.name));
          info.appendChild(el('div', 'card-price', usd(item.price_usd) + ' \u00d7 ' + item.quantity));
          row.appendChild(info);

          var controls = el('div', 'qty-controls');
          var minus = el('button', 'icon-btn', '\u2212');
          minus.type = 'button';
          minus.onclick = function () { changeQty(item.product_id, -1); };
          var qty = el('span', 'qty', String(item.quantity));
          var plus = el('button', 'icon-btn', '+');
          plus.type = 'button';
          plus.onclick = function () { changeQty(item.product_id, 1); };
          var del = el('button', 'icon-btn danger', '\u00d7');
          del.type = 'button';
          del.onclick = function () { removeItem(item.product_id); };
          controls.appendChild(minus);
          controls.appendChild(qty);
          controls.appendChild(plus);
          controls.appendChild(del);
          row.appendChild(controls);
          list.appendChild(row);
        })(cart.items[i]);
      }
      screenEl.appendChild(list);

      screenEl.appendChild(el('div', 'cart-total',
        t('webapp_total') + ': ' + usd(cart.total_usd) + ' / ' + stars(cart.total_stars)));

      var promo = el('input', 'input');
      promo.type = 'text';
      promo.placeholder = t('webapp_promo_placeholder');
      screenEl.appendChild(promo);

      var payStars = el('button', 'btn primary', t('webapp_pay_stars'));
      payStars.type = 'button';
      payStars.onclick = function () { checkout('stars', promo.value, payStars); };
      screenEl.appendChild(payStars);

      var payCrypto = el('button', 'btn secondary', t('webapp_pay_crypto'));
      payCrypto.type = 'button';
      payCrypto.onclick = function () { checkout('crypto', promo.value, payCrypto); };
      screenEl.appendChild(payCrypto);
    }).catch(showError);
  }

  function changeQty(productID, delta) {
    api('POST', '/api/cart', { product_id: productID, delta: delta })
      .then(function () { renderCart(); })
      .catch(showError);
  }

  function removeItem(productID) {
    api('DELETE', '/api/cart?product_id=' + productID)
      .then(function () { renderCart(); })
      .catch(showError);
  }

  function checkout(method, promo, btn) {
    btn.disabled = true;
    var body = { method: method };
    if (promo) { body.promo = promo; }
    api('POST', '/api/checkout', body).then(function (data) {
      btn.disabled = false;
      if (method === 'stars' && tg && tg.openInvoice) {
        tg.openInvoice(data.invoice_link, function (status) {
          if (status === 'paid') { renderCart(); }
        });
      } else if (tg && tg.openLink) {
        tg.openLink(data.invoice_link);
      } else {
        window.open(data.invoice_link, '_blank');
      }
    }).catch(function (err) {
      btn.disabled = false;
      showError(err);
    });
  }

  // ---- boot ---------------------------------------------------------------------

  function applyTheme() {
    if (!tg) { return; }
    var params = tg.themeParams || {};
    var root = document.documentElement.style;
    if (params.bg_color) { root.setProperty('--bg', params.bg_color); }
    if (params.text_color) { root.setProperty('--text', params.text_color); }
    if (params.hint_color) { root.setProperty('--hint', params.hint_color); }
    if (params.button_color) { root.setProperty('--button', params.button_color); }
    if (params.button_text_color) { root.setProperty('--button-text', params.button_text_color); }
    if (params.secondary_bg_color) { root.setProperty('--secondary-bg', params.secondary_bg_color); }
    if (params.link_color) { root.setProperty('--link', params.link_color); }
  }

  function boot() {
    if (tg) {
      tg.ready();
      tg.expand();
      applyTheme();
      tg.onEvent('themeChanged', applyTheme);
    }

    backBtn.onclick = pop;
    cartBtn.onclick = function () { push(renderCart); };

    var lang = '';
    if (tg && tg.initDataUnsafe && tg.initDataUnsafe.user) {
      lang = tg.initDataUnsafe.user.language_code || '';
    }

    fetch('/api/i18n?lang=' + encodeURIComponent(lang)).then(function (resp) {
      return resp.json();
    }).then(function (data) {
      dict = data || {};
    }).catch(function () {
      dict = {};
    }).then(function () {
      setTitle(t('webapp_title'));
      push(renderCatalog);
      // Prime the cart badge in the background.
      api('GET', '/api/cart').then(function (cart) {
        updateCartBadge(countItems(cart));
      }).catch(function () {});
    });
  }

  boot();
})();
