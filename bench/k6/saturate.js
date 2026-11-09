// zipgo bench — the most a server answers: a fixed number of clients that
// each send their next request as soon as the last one is answered (a closed
// loop), so throughput climbs until something saturates. The ladder in
// scenarios.js says what a rate of arrivals costs; this says the ceiling.
//
// Env: URL (the file to fetch), VUS (concurrent clients), DURATION, RESOLVE
// (an IP the URL's host resolves to), NEW_CONN=1 (a new TLS connection per
// request: the handshake ceiling), ACCEPT_ENCODING, SUMMARY.
import http from 'k6/http';
import { check } from 'k6';

const URL = __ENV.URL;
const m = /^https?:\/\/([^/:]+)/.exec(URL || '');
const hosts = {};
if (__ENV.RESOLVE && m) hosts[m[1]] = __ENV.RESOLVE;

export const options = {
  hosts,
  discardResponseBodies: true,
  noConnectionReuse: __ENV.NEW_CONN === '1',
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  scenarios: {
    load: { executor: 'constant-vus', vus: Number(__ENV.VUS || 64), duration: __ENV.DURATION || '20s' },
  },
};

const headers = { 'Accept-Encoding': __ENV.ACCEPT_ENCODING || 'gzip, deflate, br, zstd', 'User-Agent': 'zipgo-bench (k6)' };

export default function () {
  const r = http.get(URL, { headers });
  check(r, { '200': (x) => x.status === 200 });
}

export function handleSummary(data) {
  return { [__ENV.SUMMARY || '/out/summary.json']: JSON.stringify(data) };
}
