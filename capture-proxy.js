// Capture proxy: receives the gateway's outbound request, records its headers,
// then forwards to the real OpenCode Zen upstream so inference still works.
const http = require('http');
const https = require('https');
const fs = require('fs');
const path = require('path');

const PORT = 9090;
const LOG = path.join(__dirname, 'capture-headers.jsonl');
const UPSTREAM_HOST = 'opencode.ai';
const PREFIX = '/zen';

fs.writeFileSync(LOG, '');

function record(entry) {
  fs.appendFileSync(LOG, JSON.stringify(entry) + '\n');
}

const server = http.createServer((req, res) => {
  const chunks = [];
  req.on('data', (c) => chunks.push(c));
  req.on('end', () => {
    const body = Buffer.concat(chunks);
    const started = Date.now();

    const headers = Object.assign({}, req.headers);
    headers.host = UPSTREAM_HOST;
    headers['content-length'] = body.length;

    const proxied = https.request(
      {
        host: UPSTREAM_HOST,
        path: PREFIX + req.url,
        method: req.method,
        headers,
      },
      (up) => {
        record({
          time: new Date().toISOString(),
          method: req.method,
          path: req.url,
          upstreamPath: PREFIX + req.url,
          upstreamStatus: up.statusCode,
          durationMs: Date.now() - started,
          bodyBytes: body.length,
          headers: req.headers,
        });
        res.writeHead(up.statusCode, up.headers);
        up.pipe(res);
      }
    );

    proxied.on('error', (e) => {
      record({
        time: new Date().toISOString(),
        method: req.method,
        path: req.url,
        upstreamStatus: 0,
        error: e.message,
        durationMs: Date.now() - started,
        bodyBytes: body.length,
        headers: req.headers,
      });
      res.writeHead(502, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ error: e.message }));
    });

    proxied.end(body);
  });
});

server.listen(PORT, '0.0.0.0', () => console.log('capture proxy listening on ' + PORT));
