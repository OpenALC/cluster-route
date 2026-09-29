// 从 web/public/logo.svg 生成应用图标: 各尺寸 PNG + 多尺寸 ICO
// 用法: cd web && node scripts/gen-icons.mjs  (需先 npm i -D sharp)
import { readFileSync, writeFileSync, mkdirSync } from 'fs';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const svg = readFileSync(join(root, 'web', 'public', 'logo.svg'));
const outDir = join(root, 'build');
mkdirSync(outDir, { recursive: true });

const { default: sharp } = await import('sharp');

const sizes = [512, 256, 128, 64, 48, 32, 24, 16];
const pngs = [];
for (const size of sizes) {
  const buf = await sharp(svg, { density: 384 }).resize(size, size).png().toBuffer();
  pngs.push({ size, buf });
  writeFileSync(join(outDir, `icon-${size}.png`), buf);
}

// 打包 ICO(PNG 条目格式, Vista+ 支持)
function packIco(entries) {
  const header = Buffer.alloc(6);
  header.writeUInt16LE(0, 0);
  header.writeUInt16LE(1, 2);
  header.writeUInt16LE(entries.length, 4);
  const dir = Buffer.alloc(16 * entries.length);
  let offset = 6 + 16 * entries.length;
  const datas = [];
  entries.forEach((e, i) => {
    const d = dir.subarray(i * 16, (i + 1) * 16);
    d.writeUInt8(e.size >= 256 ? 0 : e.size, 0);
    d.writeUInt8(e.size >= 256 ? 0 : e.size, 1);
    d.writeUInt8(0, 2);
    d.writeUInt8(0, 3);
    d.writeUInt16LE(1, 4);
    d.writeUInt16LE(32, 6);
    d.writeUInt32LE(e.buf.length, 8);
    d.writeUInt32LE(offset, 12);
    offset += e.buf.length;
    datas.push(e.buf);
  });
  return Buffer.concat([header, dir, ...datas]);
}

writeFileSync(join(outDir, 'icon.ico'), packIco(pngs));
console.log('生成完成: build/icon.ico +', sizes.join('/'), 'PNG');
