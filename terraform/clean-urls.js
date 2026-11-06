// CloudFront Function (cloudfront-js-2.0), viewer-request, for the images
// distribution. It gives the static pages clean URLs and touches nothing else.
//
//   /gallery, /help   -> 301 to the same path with a trailing slash
//   anything ending / -> the same path + "index.html" ("/" -> "/index.html")
//
// Image objects are stored as "<guildID>/<ksuid>": no extension and no trailing
// slash, so neither rule can match them. Add a name to PAGES to add a page.
var PAGES = ['gallery', 'help'];

function handler(event) {
  var request = event.request;
  var uri = request.uri;

  if (uri.charAt(uri.length - 1) === '/') {
    request.uri = uri + 'index.html';
    return request;
  }

  if (PAGES.indexOf(uri.slice(1)) !== -1) {
    var location = uri + '/';
    var qs = [];
    for (var k in request.querystring) {
      var q = request.querystring[k];
      qs.push(q.multiValue ? q.multiValue.map(function (m) { return k + '=' + m.value; }).join('&') : k + '=' + q.value);
    }
    if (qs.length) {
      location += '?' + qs.join('&');
    }
    return {
      statusCode: 301,
      statusDescription: 'Moved Permanently',
      headers: { location: { value: location } },
    };
  }

  return request;
}

if (typeof module !== 'undefined') {
  module.exports = { handler: handler };
}
