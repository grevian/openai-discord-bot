/*
 * Danbot's gallery. Loads latest.json and draws one card per drawing.
 *
 * Prompts are typed by strangers, so they are only ever put on the page with
 * textContent or setAttribute: never innerHTML, insertAdjacentHTML or
 * document.write. gallery_test.go enforces that, and runs the renderer against
 * hostile input with node.
 */
(function (root) {
  'use strict';

  var MANIFEST_URL = 'latest.json';

  // Image keys look like "<guildID>/<ksuid>". Anything else is not ours, and is
  // dropped rather than turned into a URL.
  var KEY_RE = /^[0-9A-Za-z_-]+\/[0-9A-Za-z_-]+$/;

  function imageURL(key) {
    return typeof key === 'string' && KEY_RE.test(key) ? '/' + key : null;
  }

  function relativeTime(iso, now) {
    var then = Date.parse(iso);
    if (isNaN(then)) { return ''; }
    var secs = Math.max(0, Math.round((now - then) / 1000));
    if (secs < 60) { return 'just now'; }
    var units = [['day', 86400], ['hour', 3600], ['minute', 60]];
    for (var i = 0; i < units.length; i++) {
      var n = Math.floor(secs / units[i][1]);
      if (n >= 1) { return n + ' ' + units[i][0] + (n === 1 ? '' : 's') + ' ago'; }
    }
    return '';
  }

  function el(doc, tag, className) {
    var node = doc.createElement(tag);
    if (className) { node.className = className; }
    return node;
  }

  // buildCard returns an <li> for entry, or null if the entry is unusable.
  function buildCard(doc, entry, now) {
    if (!entry || typeof entry !== 'object') { return null; }
    var src = imageURL(entry.key);
    if (!src) { return null; }
    var prompt = typeof entry.prompt === 'string' ? entry.prompt : '';

    var li = el(doc, 'li', 'card');

    var link = el(doc, 'a', 'pic');
    link.setAttribute('href', src);
    link.setAttribute('target', '_blank');
    link.setAttribute('rel', 'noopener noreferrer');

    var img = el(doc, 'img');
    img.setAttribute('src', src);
    img.setAttribute('width', '1024');
    img.setAttribute('height', '1024');
    img.setAttribute('loading', 'lazy');
    img.setAttribute('decoding', 'async');
    img.setAttribute('alt', prompt ? 'A crayon drawing of: ' + prompt : 'A crayon drawing');
    link.appendChild(img);
    li.appendChild(link);

    var caption = el(doc, 'p', 'caption');
    var promptNode = el(doc, 'span', 'prompt');
    promptNode.textContent = prompt;
    caption.appendChild(promptNode);
    li.appendChild(caption);

    var meta = el(doc, 'p', 'meta');
    if (entry.kind === 'edit') {
      var badge = el(doc, 'span', 'badge');
      badge.textContent = 'redrawn';
      meta.appendChild(badge);
    }
    var when = el(doc, 'span', 'when');
    when.textContent = relativeTime(entry.created, now);
    meta.appendChild(when);
    li.appendChild(meta);

    return li;
  }

  // render fills grid from manifest and returns the number of cards drawn.
  function render(doc, grid, manifest, now) {
    while (grid.firstChild) { grid.removeChild(grid.firstChild); }
    var items = manifest && Array.isArray(manifest.items) ? manifest.items : [];
    var drawn = 0;
    for (var i = 0; i < items.length; i++) {
      var card = buildCard(doc, items[i], now);
      if (card) {
        grid.appendChild(card);
        drawn++;
      }
    }
    return drawn;
  }

  function setStatus(node, text, isError) {
    node.textContent = text;
    node.className = isError ? 'status error' : 'status';
    node.hidden = !text;
  }

  function start(win) {
    var doc = win.document;
    var grid = doc.getElementById('grid');
    var status = doc.getElementById('status');

    win.fetch(MANIFEST_URL, { credentials: 'omit' })
      .then(function (resp) {
        if (!resp.ok) { throw new Error('HTTP ' + resp.status); }
        return resp.json();
      })
      .then(function (manifest) {
        var drawn = render(doc, grid, manifest, Date.now());
        if (drawn === 0) {
          setStatus(status, "There's nothing here yet. Typical.", false);
        } else {
          setStatus(status, '', false);
        }
      })
      .catch(function () {
        setStatus(status, "The pictures didn't load. I'm not surprised.", true);
      });
  }

  var api = { imageURL: imageURL, relativeTime: relativeTime, buildCard: buildCard, render: render };

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = api;
  } else if (root.document) {
    start(root);
  }
})(typeof window !== 'undefined' ? window : this);
