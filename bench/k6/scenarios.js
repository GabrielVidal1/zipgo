// zipgo bench — the k6 side. One run = one scenario at one arrival rate for
// one duration; bench/run.sh steps the rate up and reads the summary this
// writes (handleSummary) to find where the server stops keeping up.
//
// Env:
//   SCENARIO        static | firstvisit | handshake | api-read | api-write | mixed
//   RATE            arrivals per second (a request for static/handshake/api-*,
//                   a visitor for firstvisit/mixed)
//   DURATION        e.g. 30s
//   SITE            https://bench.example.com/   the site a visitor loads
//   API             https://pb.bench.example.com a PocketBase (api-* and mixed)
//   RESOLVE         an IP every SITE/API host resolves to (test a box directly)
//   STATIC_PATH     the small file of the "static" scenario (default /vite.svg)
//   BENCH_KEY       the X-Bench-Key header api-write sends
//   ACCEPT_ENCODING what the client accepts (default: what Chrome sends)
//   SUMMARY         where to write the JSON summary (default /out/summary.json)
import http from 'k6/http';
import { check, sleep } from 'k6';
import { parseHTML } from 'k6/html';

const SCENARIO = __ENV.SCENARIO || 'firstvisit';
const RATE = Number(__ENV.RATE || 10);
const DURATION = __ENV.DURATION || '30s';
const SITE = __ENV.SITE || '';
const API = (__ENV.API || '').replace(/\/$/, '');
const RESOLVE = __ENV.RESOLVE || '';
const STATIC_PATH = __ENV.STATIC_PATH || '/vite.svg';
const BENCH_KEY = __ENV.BENCH_KEY || '';
const ENCODING = __ENV.ACCEPT_ENCODING || 'gzip, deflate, br, zstd';
const SUMMARY = __ENV.SUMMARY || '/out/summary.json';

function hostOf(u) {
  const m = /^https?:\/\/([^/:]+)/.exec(u || '');
  return m ? m[1] : '';
}
function originOf(u) {
  const m = /^(https?:\/\/[^/]+)/.exec(u || '');
  return m ? m[1] : '';
}

const hosts = {};
if (RESOLVE) {
  for (const u of [SITE, API]) if (hostOf(u)) hosts[hostOf(u)] = RESOLVE;
}

// How long one arrival keeps a VU busy, roughly: enough VUs for the rate,
// without starting thousands for nothing. k6 counts the arrivals it could not
// start for lack of a VU as dropped_iterations — a sign the server is slow.
const busy = { static: 0.2, handshake: 0.3, 'api-read': 0.3, 'api-write': 0.5, firstvisit: 1.5, mixed: 4 }[SCENARIO] || 1;
const preVUs = Math.max(5, Math.ceil(RATE * busy));

export const options = {
  hosts,
  discardResponseBodies: true,
  // A first visit is a new visitor: a new connection (and TLS handshake) per
  // iteration, reused within it like a browser does.
  noVUConnectionReuse: SCENARIO === 'firstvisit' || SCENARIO === 'mixed',
  // The handshake scenario: a new connection for every request.
  noConnectionReuse: SCENARIO === 'handshake',
  batchPerHost: 6, // a browser's connections per host over HTTP/1.1
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  scenarios: {
    load: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: preVUs,
      maxVUs: Math.min(4000, preVUs * 8),
      gracefulStop: '10s',
    },
  },
};

const headers = { 'Accept-Encoding': ENCODING, 'User-Agent': 'zipgo-bench (k6)' };

// The page's own assets, read once: what a browser fetches on a first visit
// (scripts, stylesheets, preloads, icons, same-origin images in the HTML).
export function setup() {
  if (!SITE) return { assets: [] };
  const res = http.get(SITE, { headers, responseType: 'text' });
  if (res.status !== 200) throw new Error(`setup: GET ${SITE} → ${res.status}`);
  const doc = parseHTML(res.body);
  const origin = originOf(SITE);
  const seen = {};
  const assets = [];
  const add = (ref) => {
    if (!ref || ref.startsWith('data:') || ref.startsWith('#')) return;
    let u = ref;
    if (ref.startsWith('//')) u = 'https:' + ref;
    else if (ref.startsWith('/')) u = origin + ref;
    else if (!/^https?:/.test(ref)) u = SITE.replace(/[^/]*$/, '') + ref;
    if (originOf(u) !== origin || seen[u]) return;
    seen[u] = true;
    assets.push(u);
  };
  doc.find('script[src]').each((_, el) => add(el.attributes().src.value));
  doc.find('link[href]').each((_, el) => {
    const rel = (el.attributes().rel || { value: '' }).value;
    if (/stylesheet|modulepreload|preload|icon|manifest/.test(rel)) add(el.attributes().href.value);
  });
  doc.find('img[src]').each((_, el) => add(el.attributes().src.value));
  return { assets };
}

function firstVisit(data) {
  const page = http.get(SITE, { headers, tags: { kind: 'html' } });
  check(page, { 'page 200': (r) => r.status === 200 });
  if (data.assets.length) {
    const rs = http.batch(data.assets.map((u) => ['GET', u, null, { headers, tags: { kind: 'asset' } }]));
    check(rs, { 'assets 200': (all) => all.every((r) => r.status === 200) });
  }
}

function apiRead() {
  const page = 1 + Math.floor(Math.random() * 50);
  const r = http.get(`${API}/api/collections/items/records?page=${page}&perPage=20&sort=-n`, {
    headers,
    tags: { kind: 'api-read' },
  });
  check(r, { 'read 200': (x) => x.status === 200 });
}

function apiWrite() {
  const body = JSON.stringify({ title: `bench ${__VU}-${__ITER}`, body: 'k6 '.repeat(40), n: __ITER });
  const r = http.post(`${API}/api/collections/items/records`, body, {
    headers: Object.assign({ 'Content-Type': 'application/json', 'X-Bench-Key': BENCH_KEY }, headers),
    tags: { kind: 'api-write' },
  });
  check(r, { 'write 200': (x) => x.status === 200 });
}

export default function (data) {
  switch (SCENARIO) {
    case 'static':
    case 'handshake': {
      const r = http.get(originOf(SITE) + STATIC_PATH, { headers, tags: { kind: 'static' } });
      check(r, { 'static 200': (x) => x.status === 200 });
      break;
    }
    case 'firstvisit':
      firstVisit(data);
      break;
    case 'api-read':
      apiRead();
      break;
    case 'api-write':
      apiWrite();
      break;
    case 'mixed':
      // A visitor: the page, then three API reads while they look at it.
      firstVisit(data);
      for (let i = 0; i < 3; i++) {
        sleep(1);
        apiRead();
      }
      break;
    default:
      throw new Error(`unknown SCENARIO ${SCENARIO}`);
  }
}

export function handleSummary(data) {
  return { [SUMMARY]: JSON.stringify(data) };
}
