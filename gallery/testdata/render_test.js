// Runs the gallery's renderer against hostile input with a minimal fake DOM.
// Exits non-zero (and prints why) on the first failed assertion. Run by
// site_test.go; needs only node.
'use strict';
const path = require('path');
const assert = require('assert');

const api = require(path.resolve(process.argv[2]));

// A fake DOM that makes it impossible to smuggle markup: it has no innerHTML
// at all (setting one would just be an inert property, which we detect), and
// records every element created.
class Node {
  constructor(tag) {
    this.tagName = tag;
    this.className = '';
    this.attrs = {};
    this.children = [];
    this._text = '';
  }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  appendChild(c) { this.children.push(c); return c; }
  removeChild(c) { this.children.splice(this.children.indexOf(c), 1); return c; }
  get firstChild() { return this.children[0] || null; }
  set textContent(v) { this._text = String(v); }
  get textContent() { return this._text; }
  walk(fn) { fn(this); this.children.forEach((c) => c.walk(fn)); }
}
const created = [];
const doc = { createElement(tag) { const n = new Node(tag); created.push(n); return n; } };

const NOW = Date.parse('2026-11-05T12:00:00Z');
const hostile = [
  '<script>alert(1)</script>',
  '<img src=x onerror=alert(1)>',
  '"><svg/onload=alert(1)>',
  "'; alert(1); //",
  'javascript:alert(1)',
  '&lt;b&gt;',
];

// 1. Hostile prompts only ever land in textContent / the alt attribute.
const grid = new Node('ul');
const items = hostile.map((p, i) => ({
  id: 'id' + i, key: 'guild1/ksuid' + i, created: '2026-11-05T11:00:00Z', prompt: p, kind: i % 2 ? 'edit' : 'generate',
}));
assert.strictEqual(api.render(doc, grid, { items }, NOW), hostile.length);

const allowedTags = new Set(['ul', 'li', 'a', 'img', 'p', 'span']);
created.forEach((n) => assert(allowedTags.has(n.tagName), 'unexpected element ' + n.tagName));

grid.children.forEach((li, i) => {
  const texts = [];
  li.walk((n) => { if (n._text) texts.push(n._text); });
  assert(texts.includes(hostile[i]), 'prompt not rendered as text: ' + hostile[i]);
  li.walk((n) => {
    // Attributes must never carry prompt text except alt, which is inert.
    for (const [k, v] of Object.entries(n.attrs)) {
      if (k === 'alt') continue;
      assert(!v.includes(hostile[i]), k + ' carries prompt text');
    }
    assert.strictEqual(n.innerHTML, undefined, 'innerHTML was set');
    assert.strictEqual(n.outerHTML, undefined, 'outerHTML was set');
  });
});

// 2. Hostile keys never become URLs; those entries are dropped.
for (const key of ['javascript:alert(1)', '//evil.example/x', '../../etc/passwd', 'a/b/c', 'a b/c', 'guild/<x>', '', null, 7, undefined]) {
  assert.strictEqual(api.imageURL(key), null, 'accepted key ' + JSON.stringify(key));
}
assert.strictEqual(api.imageURL('1091532074495787049/2qN0aZxkPn8ogl3o'), '/1091532074495787049/2qN0aZxkPn8ogl3o');
assert.strictEqual(api.imageURL('private-chat/abc'), '/private-chat/abc'); // eligibility is the bot's job

const g2 = new Node('ul');
assert.strictEqual(api.render(doc, g2, { items: [null, 5, 'x', { key: 'javascript:1', prompt: 'p' }, { key: 'g/k', prompt: 'ok' }] }, NOW), 1);

// 3. Malformed manifests render nothing, without throwing.
for (const m of [null, undefined, {}, { items: 'nope' }, { items: {} }, 'x']) {
  assert.strictEqual(api.render(doc, new Node('ul'), m, NOW), 0);
}

// 4. Rendering again replaces rather than appends.
api.render(doc, g2, { items: [{ key: 'g/k', prompt: 'again' }] }, NOW);
assert.strictEqual(g2.children.length, 1);

// 5. The edit badge appears only for edits, with a fixed label.
const kinds = new Node('ul');
api.render(doc, kinds, { items: [{ key: 'g/a', prompt: 'x', kind: 'edit' }, { key: 'g/b', prompt: 'y', kind: '<b>edit</b>' }] }, NOW);
const badges = kinds.children.map((li) => { let b = null; li.walk((n) => { if (n.className === 'badge') b = n; }); return b && b.textContent; });
assert.deepStrictEqual(badges, ['redrawn', null]);

// 6. Relative times.
const t = (s) => api.relativeTime(new Date(NOW - s * 1000).toISOString(), NOW);
assert.strictEqual(t(5), 'just now');
assert.strictEqual(t(60), '1 minute ago');
assert.strictEqual(t(2 * 3600), '2 hours ago');
assert.strictEqual(t(3 * 86400), '3 days ago');
assert.strictEqual(api.relativeTime('garbage', NOW), '');

console.log('ok');
