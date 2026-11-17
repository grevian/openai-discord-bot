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

  // contextLines returns the entry's condensed-prompt directives: only
  // non-empty strings, and none at all for entries that predate them.
  function contextLines(entry) {
    var out = [];
    if (entry && Array.isArray(entry.context)) {
      for (var i = 0; i < entry.context.length; i++) {
        if (typeof entry.context[i] === 'string' && entry.context[i]) { out.push(entry.context[i]); }
      }
    }
    return out;
  }

  // buildHint returns the debug tooltip: a focusable marker that shows the
  // condensed prompt on hover, keyboard focus or tap. Null with nothing to show.
  function buildHint(doc, entry) {
    var lines = contextLines(entry);
    if (!lines.length) { return null; }
    var hint = el(doc, 'span', 'hint');
    hint.setAttribute('tabindex', '0');
    hint.setAttribute('role', 'note');
    hint.setAttribute('aria-label', 'Debug: condensed prompt');
    hint.textContent = '\u24d8';
    var tip = el(doc, 'span', 'tip');
    tip.setAttribute('role', 'tooltip');
    var title = el(doc, 'span', 'tip-title');
    title.textContent = 'condensed prompt (debug)';
    tip.appendChild(title);
    for (var i = 0; i < lines.length; i++) {
      var line = el(doc, 'span', 'tip-line');
      line.textContent = lines[i];
      tip.appendChild(line);
    }
    hint.appendChild(tip);
    return hint;
  }

  // buildPic returns the linked drawing for entry, or null if its key is unusable.
  function buildPic(doc, entry, prompt) {
    var src = imageURL(entry.key);
    if (!src) { return null; }
    var link = el(doc, 'a', 'pic');
    link.setAttribute('href', src);
    link.setAttribute('target', '_blank');
    link.setAttribute('rel', 'noopener noreferrer');
    link.appendChild(buildImg(doc, src, prompt ? 'A crayon drawing of: ' + prompt : 'A crayon drawing'));
    return link;
  }

  function buildImg(doc, src, alt) {
    var img = el(doc, 'img');
    img.setAttribute('src', src);
    img.setAttribute('width', '1024');
    img.setAttribute('height', '1024');
    img.setAttribute('loading', 'lazy');
    img.setAttribute('decoding', 'async');
    img.setAttribute('alt', alt);
    return img;
  }

  // appendDetails adds the caption (prompt and debug tooltip) and the meta line.
  function appendDetails(doc, parent, entry, prompt, now) {
    var caption = el(doc, 'p', 'caption');
    var promptNode = el(doc, 'span', 'prompt');
    promptNode.textContent = prompt;
    caption.appendChild(promptNode);
    var hint = buildHint(doc, entry);
    if (hint) { caption.appendChild(hint); }
    parent.appendChild(caption);

    var meta = el(doc, 'p', 'meta');
    if (entry.kind === 'edit') {
      var badge = el(doc, 'span', 'badge');
      badge.textContent = 'redrawn';
      meta.appendChild(badge);
    }
    var when = el(doc, 'span', 'when');
    when.textContent = relativeTime(entry.created, now);
    meta.appendChild(when);
    parent.appendChild(meta);
  }

  function promptOf(entry) { return typeof entry.prompt === 'string' ? entry.prompt : ''; }

  // buildCard returns an <li> for entry, or null if the entry is unusable.
  function buildCard(doc, entry, now) {
    if (!entry || typeof entry !== 'object') { return null; }
    var prompt = promptOf(entry);
    var pic = buildPic(doc, entry, prompt);
    if (!pic) { return null; }
    var li = el(doc, 'li', 'card');
    li.appendChild(pic);
    appendDetails(doc, li, entry, prompt, now);
    return li;
  }

  // groupStacks groups manifest items into redraw stacks. An item belongs to
  // the stack of the oldest ancestor the manifest still holds (following
  // "parent"), so a whole edit chain, including branches, is one stack even
  // when its original has dropped out of the manifest. Items without a usable
  // parent are stacks of one. Each stack is newest first, and stacks are in the
  // order of their newest member, so the gallery stays newest first. Unusable
  // entries are dropped.
  function groupStacks(items) {
    var usable = [];
    var byId = {};
    for (var i = 0; i < items.length; i++) {
      var it = items[i];
      if (it && typeof it === 'object' && imageURL(it.key)) {
        usable.push(it);
        if (typeof it.id === 'string' && it.id && !(it.id in byId)) { byId[it.id] = it; }
      }
    }
    function rootOf(it) {
      var seen = {};
      var cur = it;
      while (typeof cur.parent === 'string' && cur.parent && byId[cur.parent]) {
        if (seen[cur.id]) {
          // A cycle (corrupt data): every member must agree on one root.
          var ids = Object.keys(seen).sort();
          return byId[ids[0]];
        }
        seen[cur.id] = true;
        cur = byId[cur.parent];
      }
      return cur;
    }
    var stacks = [];
    var at = {};
    for (var j = 0; j < usable.length; j++) {
      var root = rootOf(usable[j]);
      var gid = typeof root.id === 'string' && root.id && byId[root.id] === root ? 'id:' + root.id : null;
      if (gid === null || !(gid in at)) {
        if (gid !== null) { at[gid] = stacks.length; }
        stacks.push([usable[j]]);
      } else {
        stacks[at[gid]].push(usable[j]);
      }
    }
    return stacks;
  }

  // buildStack returns an <li> for two or more related drawings. Collapsed it
  // shows the newest on top with the older ones peeking out behind it (they
  // fan out on hover); the toggle, or a tap on the pile, lists every drawing
  // oldest first, the original then each redraw.
  function buildStack(doc, stack, now) {
    var newest = stack[0];
    var prompt = promptOf(newest);
    var li = el(doc, 'li', 'card stack');

    var toggle = el(doc, 'button', 'stack-toggle');
    toggle.setAttribute('type', 'button');
    toggle.setAttribute('aria-expanded', 'false');
    toggle.setAttribute('aria-label', 'Show all ' + stack.length + ' drawings in this stack');
    var pile = el(doc, 'span', 'pile');
    for (var i = Math.min(stack.length, 3) - 1; i >= 0; i--) {
      var layer = el(doc, 'span', 'layer layer' + i);
      layer.appendChild(buildImg(doc, imageURL(stack[i].key), i === 0 ? 'A crayon drawing of: ' + prompt : ''));
      pile.appendChild(layer);
    }
    toggle.appendChild(pile);
    var count = el(doc, 'span', 'stack-count');
    count.textContent = stack.length + ' drawings';
    toggle.appendChild(count);
    li.appendChild(toggle);

    var front = el(doc, 'div', 'stack-front');
    appendDetails(doc, front, newest, prompt, now);
    li.appendChild(front);

    var list = el(doc, 'ol', 'members');
    for (var k = stack.length - 1; k >= 0; k--) {
      var m = buildCard(doc, stack[k], now);
      m.className = 'member';
      list.appendChild(m);
    }
    li.appendChild(list);

    toggle.addEventListener('click', function () {
      var open = li.className.indexOf(' open') < 0;
      li.className = open ? 'card stack open' : 'card stack';
      toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
    });
    return li;
  }

  // render fills grid from manifest and returns the number of cards drawn
  // (a stack is one card).
  function render(doc, grid, manifest, now) {
    while (grid.firstChild) { grid.removeChild(grid.firstChild); }
    var items = manifest && Array.isArray(manifest.items) ? manifest.items : [];
    var stacks = groupStacks(items);
    for (var i = 0; i < stacks.length; i++) {
      grid.appendChild(stacks[i].length > 1 ? buildStack(doc, stacks[i], now) : buildCard(doc, stacks[i][0], now));
    }
    return stacks.length;
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

  var api = { imageURL: imageURL, relativeTime: relativeTime, buildCard: buildCard, groupStacks: groupStacks, render: render };

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = api;
  } else if (root.document) {
    start(root);
  }
})(typeof window !== 'undefined' ? window : this);
