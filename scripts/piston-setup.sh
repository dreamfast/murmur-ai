#!/bin/sh
# Install languages into Piston via its HTTP API.
# Runs as a sidecar container after Piston is healthy.

set -e

PISTON_URL="${PISTON_URL:-http://piston:2000}"

install_lang() {
	lang="$1"
	version="$2"
	echo "Installing $lang $version..."
	STATUS=$(node -e "
const http = require('http');
const data = JSON.stringify({language: '$lang', version: '$version'});
const req = http.request('$PISTON_URL/api/v2/packages', {method: 'POST', headers: {'Content-Type': 'application/json', 'Content-Length': data.length}}, r => {
  let d = '';
  r.on('data', c => d += c);
  r.on('end', () => { console.log(r.statusCode); if (r.statusCode !== 200) console.error(d); });
});
req.on('error', e => { console.error(e.message); process.exit(1); });
req.write(data);
req.end();
" 2>&1)
	echo "  $lang $version -> $STATUS"
}

echo "Waiting for Piston API..."
until node -e "const http = require('http'); http.get('$PISTON_URL/api/v2/runtimes', r => process.exit(r.statusCode === 200 ? 0 : 1)).on('error', () => process.exit(1))" 2>/dev/null; do
	sleep 2
done
echo "Piston API is ready."

# These are the latest versions available in Piston's package repo.
install_lang python 3.9.4
install_lang node 20.11.1
install_lang typescript 5.0.3
install_lang go 1.16.2
install_lang rust 1.68.2
install_lang bash 5.2.0
install_lang gcc 10.2.0
install_lang ruby 3.0.1
install_lang php 8.2.3
install_lang lua 5.4.4
install_lang java 15.0.2

echo "All languages installed."
