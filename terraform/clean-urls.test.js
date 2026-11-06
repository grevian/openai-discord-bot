// Run with: node terraform/clean-urls.test.js
'use strict';
const assert = require('assert');
const { handler } = require('./clean-urls.js');

const run = (uri, querystring) => handler({ request: { uri, querystring: querystring || {}, headers: {} } });

// Directory paths get index.html.
for (const [uri, want] of [['/', '/index.html'], ['/gallery/', '/gallery/index.html'], ['/help/', '/help/index.html']]) {
  assert.strictEqual(run(uri).uri, want, uri);
}

// Page names without the slash redirect, keeping the query string.
const r = run('/gallery');
assert.strictEqual(r.statusCode, 301);
assert.strictEqual(r.headers.location.value, '/gallery/');
assert.strictEqual(run('/help', { a: { value: '1' } }).headers.location.value, '/help/?a=1');

// Everything else, above all image objects (<guildID>/<ksuid>), is untouched.
for (const uri of [
  '/123456789012345678/2abcDEFghiJKLmnoPQRstuVWX',
  '/gallery/index.html', '/gallery/help.html', '/gallery/latest.json', '/gallery/items/abc.json',
  '/index.html', '/home.css', '/help/index.html', '/galleryx', '/helper', '/robots.txt',
]) {
  const out = run(uri);
  assert.strictEqual(out.uri, uri, uri);
  assert.strictEqual(out.statusCode, undefined, uri);
}
console.log('ok');
