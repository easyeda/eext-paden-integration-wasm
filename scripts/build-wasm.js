/**
 * Cross-platform Go WASM build script.
 */

const { execSync } = require('node:child_process');
const path = require('node:path');
const process = require('node:process');

const repoRoot = path.join(__dirname, '..');
const goServiceDir = path.join(repoRoot, 'go-service');
const outFile = path.join(repoRoot, 'dist', 'paden.wasm');

const isDev = process.argv.includes('--dev');

let cmd = 'go build';
if (!isDev) {
	cmd += ' -ldflags="-s -w"';
}
cmd += ` -o ${JSON.stringify(outFile)} main_wasm.go`;

console.log(`[build-wasm] ${cmd}`);
execSync(cmd, {
	cwd: goServiceDir,
	env: {
		...process.env,
		GOOS: 'js',
		GOARCH: 'wasm',
	},
	stdio: 'inherit',
});
console.log('[build-wasm] done');
